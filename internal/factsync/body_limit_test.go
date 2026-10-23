package factsync

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/entireio/entire-brain/internal/httpx"
)

// Every per-fact 200 used to be read with
//
//	json.NewDecoder(resp.Body).Decode(&out)
//
// which streams the PARSE but allocates the VALUES in full, so a peer answering
// a well-formed JSON object with one multi-gigabyte string field allocates
// gigabytes on the member's machine — an OOM on a laptop, and the workspace
// daemon is long-lived. The request timeout does not help: it bounds time, and
// a LAN peer delivers tens of gigabytes inside five minutes. The ceiling must
// REFUSE rather than truncate, so a cut-short feed page can never be mistaken
// for a complete one.

// oversizedBody streams a well-formed JSON object whose one string field is padded
// past the limit, so the refusal is about SIZE and not about malformed JSON.
func oversizedBody(t *testing.T, prefix string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := io.WriteString(w, prefix); err != nil {
			return
		}
		chunk := strings.Repeat("QUJD", 1024) // 4 KiB of valid base64
		for written := 0; int64(written) <= httpx.MaxJSONBodyBytes; written += len(chunk) {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
		_, _ = io.WriteString(w, `"}`)
	}
}

func withSmallBodyLimit(t *testing.T, limit int64) {
	t.Helper()
	prev := httpx.MaxJSONBodyBytes
	httpx.MaxJSONBodyBytes = limit
	t.Cleanup(func() { httpx.MaxJSONBodyBytes = prev })
}

func TestFactsFeedRefusesAnOversizedPage(t *testing.T) {
	withSmallBodyLimit(t, 64<<10)
	srv := httptest.NewServer(oversizedBody(t, `{"items":[],"nextPageToken":"`))
	defer srv.Close()

	h := &HTTPServer{BaseURL: srv.URL, Token: "tok"}
	page, err := h.FactsFeedPage(t.Context(), "01HZZPROBE0000000000000000", "", 100)
	if err == nil {
		t.Fatalf("FactsFeedPage accepted an oversized page (%d items); it must refuse", len(page.Items))
	}
	if !errors.Is(err, httpx.ErrBodyTooLarge) {
		t.Errorf("FactsFeedPage failed with %v; want it to wrap httpx.ErrBodyTooLarge so the cause is distinguishable", err)
	}
}

func TestUpsertFactsRefusesAnOversizedResponse(t *testing.T) {
	withSmallBodyLimit(t, 64<<10)
	srv := httptest.NewServer(oversizedBody(t, `{"added":1,"detail":"`))
	defer srv.Close()

	h := &HTTPServer{BaseURL: srv.URL, Token: "tok"}
	counts, err := h.UpsertFacts(t.Context(), "01HZZPROBE0000000000000000", []byte("{}\n"))
	if err == nil {
		t.Fatalf("UpsertFacts accepted an oversized response (%+v)", counts)
	}
	if !errors.Is(err, httpx.ErrBodyTooLarge) {
		t.Errorf("UpsertFacts failed with %v; want httpx.ErrBodyTooLarge", err)
	}
}

// A body AT the ceiling is still served: the bound exists to stop the absurd, not to
// shrink the real payload, and a refusal that also rejected legitimate responses
// would have traded one failure for another.
func TestFactsFeedAcceptsABodyWithinTheLimit(t *testing.T) {
	withSmallBodyLimit(t, 1<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// ~300 KiB page token, comfortably inside 1 MiB.
		_, _ = io.WriteString(w, `{"items":[],"nextPageToken":"`+strings.Repeat("QUJD", 76800)+`"}`)
	}))
	defer srv.Close()

	h := &HTTPServer{BaseURL: srv.URL, Token: "tok"}
	page, err := h.FactsFeedPage(t.Context(), "01HZZPROBE0000000000000000", "", 100)
	if err != nil {
		t.Fatalf("FactsFeedPage refused a body inside the limit: %v", err)
	}
	if page.NextPageToken == nil || len(*page.NextPageToken) == 0 {
		t.Fatalf("FactsFeedPage lost a valid page token")
	}
}
