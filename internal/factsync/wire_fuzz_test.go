package factsync

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// Once the fact-set is shared (P1), every byte HTTPServer decodes was produced
// by a remote server: the upsert counts and the delta-feed pages. These
// harnesses point the real client at a hostile server and assert the only
// outcomes are "a value" or "an error" — never a panic, never a hang.
//
//	go test ./internal/factsync -run xxx -fuzz FuzzName

const fuzzOrigin = "https://fuzz.invalid"

// fuzzTransport answers every request in-process from a fixed status and body.
//
// A real httptest server per execution — or even one shared server — is
// socket-bound: the client abandons a connection whenever a fuzzed body does not
// decode cleanly to EOF, so every request costs a fresh TCP connection. That
// exhausts the machine's ephemeral ports within seconds (tens of thousands of
// TIME_WAIT sockets), collapses the exec rate to double digits, and breaks every
// other test on the box. A RoundTripper drives the identical client code — status
// mapping and JSON decode — with no sockets at all.
type fuzzTransport struct {
	status int
	body   []byte
}

func (t fuzzTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	return &http.Response{
		StatusCode:    t.status,
		Status:        fmt.Sprintf("%d %s", t.status, http.StatusText(t.status)),
		Proto:         "HTTP/1.1",
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(t.body)),
		ContentLength: int64(len(t.body)),
		Request:       req,
	}, nil
}

func fuzzClient(status int, body []byte) *http.Client {
	return &http.Client{Transport: fuzzTransport{status: status, body: body}}
}

// FuzzHTTPServerResponses drives every HTTPServer path against an arbitrary
// status line and body: the hostile server controls the counts reply, the feed
// items (including nested wire records), and the page token.
func FuzzHTTPServerResponses(f *testing.F) {
	f.Add(200, []byte(`{"added":1,"changed":0,"unchanged":2}`))
	f.Add(200, []byte(`{"items":[{"record":{"id":"fact:aa","text":"t"},"createdByHandle":"x"}],"nextPageToken":"5"}`))
	f.Add(200, []byte(`{"items":[{}],"nextPageToken":""}`))
	f.Add(400, []byte(`{"title":"Bad Request"}`))
	f.Add(503, []byte(`{`))
	f.Add(200, []byte(`{"items":`))

	f.Fuzz(func(t *testing.T, status int, body []byte) {
		if status < 100 || status > 599 {
			return // net/http refuses to write a non-status code
		}
		h := &HTTPServer{BaseURL: fuzzOrigin, Token: "t", Client: fuzzClient(status, body)}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, _ = h.UpsertFacts(ctx, "repo", []byte("x"))
		_, _ = h.FactsFeedPage(ctx, "repo", "", 100)
		_, _ = h.FactsFeedPage(ctx, "repo", "tok", 1)
	})
}

func TestFuzzOriginReachesFeedDecoder(t *testing.T) {
	h := &HTTPServer{BaseURL: fuzzOrigin, Client: fuzzClient(200, []byte(`{"items":[{"record":{"id":"fact:aa"}}],"nextPageToken":"decoded-marker"}`))}
	page, err := h.FactsFeedPage(context.Background(), "repo", "", 100)
	if err != nil || len(page.Items) != 1 || page.Items[0].Record.ID != "fact:aa" ||
		page.NextPageToken == nil || *page.NextPageToken != "decoded-marker" {
		t.Fatalf("fuzz harness did not decode seed: %+v %v", page, err)
	}
}
