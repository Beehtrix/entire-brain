package factsync

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// flakyDialTransport fails the first failFirst round trips with the exact
// error shape net/http produces for a LOCAL dial failure (ephemeral port /
// connection table exhaustion under host load), then delegates to a real
// transport. It is what a momentarily overloaded host looks like from the
// client's side: nothing about the remote server is wrong.
type flakyDialTransport struct {
	mu        sync.Mutex
	failFirst int
	calls     int
	real      http.RoundTripper
}

func (f *flakyDialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if n <= f.failFirst {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: cannot assign requested address")}
	}
	return f.real.RoundTrip(req)
}

func (f *flakyDialTransport) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// nonTransientDialTransport always fails with a dial error that is NOT the
// local-exhaustion signature (e.g. a real "connection refused" from a server
// that is actually down), so do() must return it on the very first attempt.
type nonTransientDialTransport struct {
	calls int
}

func (f *nonTransientDialTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls++
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
}

// countingUpsertServer answers the per-fact upsert and counts how many POSTs
// actually reached it.
func countingUpsertServer(t *testing.T) (*httptest.Server, *int, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.Method == http.MethodPost {
			posts++
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"added": 1, "changed": 0, "unchanged": 0})
	}))
	t.Cleanup(srv.Close)
	return srv, &posts, &mu
}

// TestHTTPServerGivesUpAfterTheRetryBudget proves do() is bounded: one MORE
// transient failure than the budget must still surface as an error rather
// than retrying forever or hanging.
func TestHTTPServerGivesUpAfterTheRetryBudget(t *testing.T) {
	srv, _, _ := countingUpsertServer(t)
	flaky := &flakyDialTransport{failFirst: transientDialRetries + 1, real: http.DefaultTransport}
	h := &HTTPServer{BaseURL: srv.URL, Token: "test-token", Client: &http.Client{Transport: flaky}}

	if _, err := h.FactsFeedPage(context.Background(), "repo", "", 100); err == nil {
		t.Fatal("FactsFeedPage succeeded despite exhausting the retry budget")
	}
	if got, want := flaky.callCount(), transientDialRetries+1; got != want {
		t.Fatalf("round trips attempted = %d, want exactly %d (the retry budget, no more)", got, want)
	}
}

// TestHTTPServerRetriesTransientDialFailureOnPOSTWithoutDoublingTheMutation
// proves the retry is safe for a mutating call: a dial failure never reaches
// the peer, so the server must observe the upsert exactly once even though
// the client redialed (GetBody rearms the request body on each attempt).
func TestHTTPServerRetriesTransientDialFailureOnPOSTWithoutDoublingTheMutation(t *testing.T) {
	srv, posts, mu := countingUpsertServer(t)
	flaky := &flakyDialTransport{failFirst: transientDialRetries, real: http.DefaultTransport}
	h := &HTTPServer{BaseURL: srv.URL, Token: "test-token", Client: &http.Client{Transport: flaky}}

	counts, err := h.UpsertFacts(context.Background(), "repo", []byte("fact:a\n"))
	if err != nil || counts.Added != 1 {
		t.Fatalf("UpsertFacts through transient dial failures = %+v, %v", counts, err)
	}
	if got, want := flaky.callCount(), transientDialRetries+1; got != want {
		t.Fatalf("round trips attempted = %d, want %d (the failures plus the one that reached the server)", got, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if *posts != 1 {
		t.Fatalf("server observed %d POSTs, want exactly 1 (the failed dials never reached it)", *posts)
	}
}
