package factsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/entireio/entire-brain/internal/repoid"
)

// The per-fact transport interpolates the repo id into the request target by
// concatenation, then newRequest attaches the member's bearer token — and, on
// the upsert path, the member's facts. url.PathEscape was standing in for a
// check it cannot perform: "." and ".." are RFC 3986 unreserved, so PathEscape
// leaves them untouched and
//
//	/api/v1/repos/../brain/facts
//
// reaches the wire, where any dot-segment-normalizing hop collapses it to
// /api/v1/brain/facts — a route the caller never asked for.
//
// Every test below asserts the RESULTING REQUEST (its path, or that no request was
// made at all). None of them asserts the escaping function against itself, which is
// the tautology that let this survive the first fix.

// hostileRepoIDs is the shared set: each one either adds a path segment, walks off
// the /repos/ prefix, or would be silently rewritten on its way out.
var hostileRepoIDs = map[string]string{
	"empty":           "",
	"blank":           "   ",
	"dot":             ".",
	"dotdot":          "..",
	"dotdot padded":   " .. ",
	"triple dot":      "...",
	"slash":           "a/b",
	"leading slash":   "/admin",
	"backslash":       `a\b`,
	"encoded slash":   "..%2f..",
	"query":           "x?admin=1",
	"fragment":        "x#frag",
	"space":           "repo 1",
	"traversal chain": "../../admin",
}

// repoIDCountingServer answers every request with body and records how many arrived.
func repoIDCountingServer(t *testing.T, body string) (*httptest.Server, *int) {
	t.Helper()
	var sent int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &sent
}

// repoIDPathRecordingServer records the escaped path and raw query of the last request.
func repoIDPathRecordingServer(t *testing.T, body string, gotPath, gotQuery *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotPath, *gotQuery = r.URL.EscapedPath(), r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestFactsyncRepoIDMustBeOneSafeSegment covers EVERY repo-id-bearing endpoint on
// HTTPServer at once: a hostile id must be refused before the request exists, so the
// bearer token and (for the upsert) the fact batch never leave the process.
func TestFactsyncRepoIDMustBeOneSafeSegment(t *testing.T) {
	calls := map[string]func(context.Context, *HTTPServer, string) error{
		"UpsertFacts": func(ctx context.Context, h *HTTPServer, id string) error {
			_, err := h.UpsertFacts(ctx, id, []byte("{}\n"))
			return err
		},
		"FactsFeedPage": func(ctx context.Context, h *HTTPServer, id string) error {
			_, err := h.FactsFeedPage(ctx, id, "", 100)
			return err
		},
	}
	for method, call := range calls {
		for name, repoID := range hostileRepoIDs {
			t.Run(method+"/"+name, func(t *testing.T) {
				srv, sent := repoIDCountingServer(t, `{"items":[],"added":0}`)
				h := &HTTPServer{BaseURL: srv.URL, Token: "secret"}
				err := call(context.Background(), h, repoID)
				if err == nil {
					t.Fatalf("%s accepted repo id %q; want a refusal before any request", method, repoID)
				}
				if !errors.Is(err, repoid.ErrInvalid) {
					t.Errorf("%s repo id %q: error = %v; want it to wrap repoid.ErrInvalid", method, repoID, err)
				}
				if *sent != 0 {
					t.Errorf("%s repo id %q: %d request(s) reached the server; want the bearer token to never leave the process", method, repoID, *sent)
				}
			})
		}
	}
}

// TestFactsFeedLandsOnTheIntendedPath pins the whole target of the feed read:
// the id occupies exactly one segment, the paging stays in the query, and the
// path is already in normal form so no hop can move it.
func TestFactsFeedLandsOnTheIntendedPath(t *testing.T) {
	const repoID = "01JABCDEFGHJKMNPQRSTVWXYZ0"
	var gotPath, gotQuery string
	srv := repoIDPathRecordingServer(t, `{"items":[],"nextPageToken":null}`, &gotPath, &gotQuery)

	h := &HTTPServer{BaseURL: srv.URL}
	if _, err := h.FactsFeedPage(context.Background(), repoID, "tok/1", 25); err != nil {
		t.Fatalf("FactsFeedPage: %v", err)
	}
	assertSegmentPath(t, gotPath, "/api/v1/repos/"+repoID+"/brain/facts")
	if gotQuery != "pageSize=25&pageToken=tok%2F1" {
		t.Errorf("query = %q, want the paging escaped into the query", gotQuery)
	}
}

// TestUpsertLandsOnTheIntendedPath does the same for the write — the request
// that carries the member's facts.
func TestUpsertLandsOnTheIntendedPath(t *testing.T) {
	const repoID = "repo_1~x"
	var gotPath, gotQuery string
	srv := repoIDPathRecordingServer(t, `{"added":1,"changed":0,"unchanged":0}`, &gotPath, &gotQuery)

	h := &HTTPServer{BaseURL: srv.URL}
	if _, err := h.UpsertFacts(context.Background(), repoID, []byte("{}\n")); err != nil {
		t.Fatalf("UpsertFacts: %v", err)
	}
	assertSegmentPath(t, gotPath, "/api/v1/repos/"+repoID+"/brain/facts")
	if gotQuery != "" {
		t.Errorf("query = %q, want empty", gotQuery)
	}
}

// TestFactsyncRepoIDCannotReachAnotherRoute is the end-to-end consequence, served by
// a real http.ServeMux — which performs the dot-segment cleanup a bare handler does
// not. Without the segment rule, repo id ".." lands on the sibling /api/v1/brain/
// route with the member's bearer token attached; with it, nothing is sent at all.
func TestFactsyncRepoIDCannotReachAnotherRoute(t *testing.T) {
	reached := make(chan string, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/", func(w http.ResponseWriter, r *http.Request) {
		reached <- "repos:" + r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items":[]}`)
	})
	mux.HandleFunc("/api/v1/brain/", func(w http.ResponseWriter, r *http.Request) {
		reached <- "elsewhere:" + r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items":[]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	h := &HTTPServer{BaseURL: srv.URL, Token: "secret"}
	if _, err := h.FactsFeedPage(context.Background(), "..", "", 100); err == nil {
		t.Fatal(`repo id ".." was accepted by FactsFeedPage`)
	}
	if _, err := h.UpsertFacts(context.Background(), "..", []byte("{}\n")); err == nil {
		t.Fatal(`repo id ".." was accepted by UpsertFacts`)
	}
	close(reached)
	for hit := range reached {
		if strings.HasPrefix(hit, "elsewhere:") {
			t.Fatalf(`repo id ".." traversed out of the repos prefix and reached %s`, hit)
		}
		t.Fatalf(`repo id ".." produced a request at all: %s`, hit)
	}
}

// assertSegmentPath checks the exact target AND that it is in normal form, which is
// the property that makes it un-rewritable by any hop between here and the handler.
func assertSegmentPath(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("request path = %q, want %q", got, want)
	}
	if cleaned := path.Clean(got); cleaned != got {
		t.Errorf("path %q is not in normal form: a normalizing hop rewrites it to %q", got, cleaned)
	}
}
