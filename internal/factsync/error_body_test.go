package factsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A live round-trip against entire-api showed the drift these tests close: the server
// answers a violated cap with an actionable body —
//
//	400 {"title":"Bad Request","status":400,"detail":"batch exceeds 5000 records"}
//
// — and the client threw the body away, leaving the member with "unexpected status 400
// Bad Request": no statement of what was wrong and no statement of the limit.
//
// The other half of the fix is that improving the MESSAGE must not disturb the
// MEANING. The statuses this transport reads are load-bearing control flow — a 400
// with a page token sent is the cursor-epoch signal (ErrInvalidPageToken) the pull
// loop resets on. So the surfacing tests have sentinel tests beside them, and the
// sentinel tests assert errors.Is, not the string.

// serverDetail is the sentence the hosted API returns for the cap that exposed this.
const serverDetail = "batch exceeds 5000 records"

const humaCapBody = `{"title":"Bad Request","status":400,"detail":"` + serverDetail + `"}`

// statusServer answers every request with one status and one body.
func statusServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// factsyncCalls is every method on HTTPServer that reads a status, keyed by name.
// Each returns only the error, because that is the whole of what a member sees when
// the server refuses. FactsFeedPage is exercised WITHOUT a page token here, so a 400
// is a plain refusal rather than the cursor-epoch sentinel.
var factsyncCalls = map[string]func(context.Context, *HTTPServer) error{
	"UpsertFacts": func(ctx context.Context, h *HTTPServer) error {
		_, err := h.UpsertFacts(ctx, "repo", []byte("fact:a\n"))
		return err
	},
	"FactsFeedPage": func(ctx context.Context, h *HTTPServer) error {
		_, err := h.FactsFeedPage(ctx, "repo", "", 100)
		return err
	},
}

// TestEveryFactsyncCallSurfacesTheServersObjection is the regression for the drift:
// on a 400 the member must read the server's own sentence, not just the status line.
func TestEveryFactsyncCallSurfacesTheServersObjection(t *testing.T) {
	for name, call := range factsyncCalls {
		t.Run(name, func(t *testing.T) {
			srv := statusServer(t, http.StatusBadRequest, humaCapBody)
			h := &HTTPServer{BaseURL: srv.URL, Token: "tok"}
			err := call(context.Background(), h)
			if err == nil {
				t.Fatalf("%s: a 400 produced no error", name)
			}
			if !strings.Contains(err.Error(), serverDetail) {
				t.Fatalf("%s: error = %q; want it to carry the server's own explanation %q", name, err, serverDetail)
			}
		})
	}
}

// A 5xx is read the same way — the surfacing is keyed on "not the success status",
// not on a hand-listed set of 4xx codes.
func TestFactsyncSurfacesA5xxObjection(t *testing.T) {
	for name, call := range factsyncCalls {
		t.Run(name, func(t *testing.T) {
			srv := statusServer(t, http.StatusInternalServerError, `{"title":"Internal Server Error","detail":"fact store is read-only"}`)
			h := &HTTPServer{BaseURL: srv.URL, Token: "tok"}
			err := call(context.Background(), h)
			if err == nil || !strings.Contains(err.Error(), "fact store is read-only") {
				t.Fatalf("%s: error = %v; want the server's 500 explanation", name, err)
			}
		})
	}
}

// An ingress in front of entire-api answers with an HTML page, not JSON. The member
// must get its title, and the whole error must stay short enough to read.
func TestFactsyncNonJSONErrorPageIsBoundedAndReadable(t *testing.T) {
	page := "<!DOCTYPE html>\n<html><head><title>504 Gateway Time-out</title></head><body>\n" +
		strings.Repeat("<div>x</div>\n", 200000) + "</body></html>\n"
	for name, call := range factsyncCalls {
		t.Run(name, func(t *testing.T) {
			srv := statusServer(t, http.StatusGatewayTimeout, page)
			h := &HTTPServer{BaseURL: srv.URL, Token: "tok"}
			err := call(context.Background(), h)
			if err == nil {
				t.Fatalf("%s: a 504 produced no error", name)
			}
			if !strings.Contains(err.Error(), "504 Gateway Time-out") {
				t.Fatalf("%s: error = %q; want the page title", name, err)
			}
			if len(err.Error()) > 1024 {
				t.Fatalf("%s: error is %d bytes; the HTML page was dumped at the member", name, len(err.Error()))
			}
			if strings.Contains(err.Error(), "<div>") {
				t.Fatalf("%s: error = %q; raw markup reached the member", name, err)
			}
		})
	}
}

// SEMANTIC PRESERVATION — a 400 with a page token sent stays the cursor-epoch
// signal. The pull loop resets its cursor and cache on errors.Is(err,
// ErrInvalidPageToken); if the surfacing replaced that sentinel with a plain
// formatted error, a rotated keyset would stop looking like one and the pull
// would fail instead of refetching from the start.
func TestRejectedPageTokenStaysErrInvalidPageToken(t *testing.T) {
	for _, body := range []string{"", `{"title":"Bad Request","detail":"page token epoch rotated"}`} {
		t.Run(fmt.Sprintf("body=%d", len(body)), func(t *testing.T) {
			srv := statusServer(t, http.StatusBadRequest, body)
			h := &HTTPServer{BaseURL: srv.URL, Token: "tok"}
			_, err := h.FactsFeedPage(context.Background(), "repo", "stale-token", 100)
			if !errors.Is(err, ErrInvalidPageToken) {
				t.Fatalf("FactsFeedPage(400, token sent) = %v; want ErrInvalidPageToken", err)
			}
			if body != "" && !strings.Contains(err.Error(), "page token epoch rotated") {
				t.Fatalf("FactsFeedPage(400, token sent) = %q; the server's explanation was dropped", err)
			}
		})
	}
}

// SEMANTIC PRESERVATION, transport half — the dial-retry classifier reads a transport
// error, which exists BEFORE any response does. Reading error bodies must leave it
// untouched: a local port-table blip is still retried, and a refused connection is
// still not.
func TestErrorBodyReadingLeavesTheDialClassifierAlone(t *testing.T) {
	ctx := context.Background()
	ts := statusServer(t, http.StatusOK, `{"items":[],"nextPageToken":null}`)
	flaky := &flakyDialTransport{failFirst: transientDialRetries, real: http.DefaultTransport}
	h := &HTTPServer{BaseURL: ts.URL, Token: "tok", Client: &http.Client{Transport: flaky}}
	if _, err := h.FactsFeedPage(ctx, "repo", "", 100); err != nil {
		t.Fatalf("FactsFeedPage with transient dial failures = %v; want the retry to still absorb them", err)
	}
	if got, want := flaky.callCount(), transientDialRetries+1; got != want {
		t.Fatalf("round trips = %d, want %d", got, want)
	}

	refused := &nonTransientDialTransport{}
	h2 := &HTTPServer{BaseURL: "http://127.0.0.1:1", Token: "tok", Client: &http.Client{Transport: refused}}
	if _, err := h2.FactsFeedPage(ctx, "repo", "", 100); err == nil {
		t.Fatal("FactsFeedPage succeeded against an always-refusing transport")
	} else if refused.calls != 1 {
		t.Fatalf("round trips = %d, want exactly 1 (a real connectivity error is not retried)", refused.calls)
	}
}

// A body that only echoes the status text must not render it twice (COR-2210:
// "unexpected status 403 Forbidden: Forbidden; pull-only"). The echo is dropped;
// what the server said beyond it survives.
func TestFactsyncDoesNotRepeatTheStatusText(t *testing.T) {
	srv := statusServer(t, http.StatusForbidden, `{"title":"Forbidden","detail":"Forbidden; pull-only"}`)
	h := &HTTPServer{BaseURL: srv.URL, Token: "tok"}
	_, err := h.UpsertFacts(context.Background(), "repo", []byte("fact:a\n"))
	if err == nil {
		t.Fatal("UpsertFacts(403) produced no error")
	}
	if strings.Contains(err.Error(), "Forbidden: Forbidden") {
		t.Fatalf("UpsertFacts(403) = %q; the status text is rendered twice", err)
	}
	if !strings.Contains(err.Error(), "403 Forbidden: pull-only") {
		t.Fatalf("UpsertFacts(403) = %q; the server's extra detail was dropped", err)
	}

	pure := statusServer(t, http.StatusForbidden, `{"title":"Forbidden"}`)
	h2 := &HTTPServer{BaseURL: pure.URL, Token: "tok"}
	_, err = h2.UpsertFacts(context.Background(), "repo", []byte("fact:a\n"))
	if err == nil {
		t.Fatal("UpsertFacts(403, pure echo) produced no error")
	}
	if !strings.HasSuffix(err.Error(), "403 Forbidden") {
		t.Fatalf("UpsertFacts(403, pure echo) = %q; want the status rendered exactly once, last", err)
	}
}

// A refusal with nothing to add must not grow a dangling separator.
func TestSilentRefusalKeepsACleanMessage(t *testing.T) {
	srv := statusServer(t, http.StatusBadRequest, "")
	h := &HTTPServer{BaseURL: srv.URL, Token: "tok"}
	_, err := h.UpsertFacts(context.Background(), "repo", []byte("fact:a\n"))
	if err == nil {
		t.Fatal("UpsertFacts(400, empty body) produced no error")
	}
	if strings.HasSuffix(err.Error(), ": ") || strings.HasSuffix(err.Error(), ":") {
		t.Fatalf("UpsertFacts(400, empty body) = %q; an empty body left a dangling separator", err)
	}
}
