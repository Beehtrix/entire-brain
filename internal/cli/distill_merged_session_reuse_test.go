package cli

import (
	"net/url"
	"testing"
)

// TestMergedSessionReusesChunksAcrossBranches pins COR-2177.
//
// The distill cache is keyed branch/session, and session export deliberately
// copies a merged feature-branch session onto the default branch. Every copy
// was therefore a cache miss with zero chunk reuse, so the same transcript
// went back to the agent once per branch it appeared on. Measured on
// agent-insights: 69 "sessions" were 41 unique, 19 on more than one branch,
// and --dry-run reported 209 of 209 chunks uncached.
func TestMergedSessionReusesChunksAcrossBranches(t *testing.T) {
	t.Parallel()
	const session = "7239b99a"
	cache := distillCache{Chunks: map[string][]string{
		distillSessionCacheKey("alex/trail-xray-spec", session): {"h1", "h2", "h3"},
		distillSessionCacheKey("main", session):                 {"h3", "h4"},
		distillSessionCacheKey("main", "some-other-session"):    {"zz"},
	}}

	got := cache.chunkSetForSession(session)
	for _, want := range []string{"h1", "h2", "h3", "h4"} {
		if !got[want] {
			t.Errorf("chunk %q was distilled on some branch and must not be re-sent; got %v", want, got)
		}
	}
	// The guard that matters most: another session's hashes must NOT leak in.
	// A chunk wrongly treated as distilled is a chunk whose facts never get
	// written, which is worse than paying for it twice.
	if got["zz"] {
		t.Fatalf("a different session's chunk leaked into the reuse set: %v", got)
	}
	if n := len(got); n != 4 {
		t.Fatalf("expected exactly the 4 hashes for this session, got %d: %v", n, got)
	}
}

// TestChunkReuseMatchesTheSessionSegmentExactly is the negative control for
// the matcher. A substring match would let these collide.
func TestChunkReuseMatchesTheSessionSegmentExactly(t *testing.T) {
	t.Parallel()
	cache := distillCache{Chunks: map[string][]string{
		distillSessionCacheKey("main", "abc"):    {"short"},
		distillSessionCacheKey("main", "abcdef"): {"long"},
		// A branch NAMED like the session must not match either.
		distillSessionCacheKey("abc", "other"): {"branchnamed"},
	}}
	got := cache.chunkSetForSession("abc")
	if !got["short"] {
		t.Error("the exact session's chunk is missing")
	}
	for _, leak := range []string{"long", "branchnamed"} {
		if got[leak] {
			t.Errorf("%q leaked in; the matcher is not segment-exact: %v", leak, got)
		}
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 hash, got %v", got)
	}
}

// TestChunkReuseHandlesEscapedBranchNames: branches carry slashes, and the key
// escapes them, so the session must still be the second segment.
func TestChunkReuseHandlesEscapedBranchNames(t *testing.T) {
	t.Parallel()
	const session = "s1"
	key := distillSessionCacheKey("feature/deep/nested", session)
	if parts := len(url.PathEscape("feature/deep/nested")); parts == 0 {
		t.Fatal("escaping produced nothing")
	}
	cache := distillCache{Chunks: map[string][]string{key: {"h"}}}
	if got := cache.chunkSetForSession(session); !got["h"] {
		t.Fatalf("a slashed branch name broke the segment match; key=%q got=%v", key, got)
	}
}

// An empty session id must reuse nothing rather than everything.
func TestChunkReuseRefusesAnEmptySessionID(t *testing.T) {
	t.Parallel()
	cache := distillCache{Chunks: map[string][]string{
		distillSessionCacheKey("main", "s1"): {"h"},
	}}
	if got := cache.chunkSetForSession(""); len(got) != 0 {
		t.Fatalf("an empty session id must match nothing, got %v", got)
	}
}

// TestDistillChunksAlreadyDoneUnionsBothSources covers the DECISION the
// distill loop actually calls, not just the helper underneath it.
//
// This exists because the first version of this guard tested
// chunkSetForSession directly, and stayed green when the call site was
// changed to ignore it — the same shape as the bug being fixed.
func TestDistillChunksAlreadyDoneUnionsBothSources(t *testing.T) {
	t.Parallel()
	const session = "s-merged"
	ses := exportSession{SessionID: session}
	cache := distillCache{Chunks: map[string][]string{
		distillSessionCacheKey("feature/x", session): {"from-other-branch"},
		distillSessionCacheKey("main", session):      {"from-this-branch"},
	}}

	done := distillChunksAlreadyDone(cache, "main", ses, nil)
	for _, want := range []string{"from-other-branch", "from-this-branch"} {
		if !done[want] {
			t.Errorf("%q was already distilled and must not be re-sent; got %v", want, done)
		}
	}

	// And the negative: a session nothing has distilled pays in full.
	fresh := distillChunksAlreadyDone(cache, "main", exportSession{SessionID: "never-seen"}, nil)
	if len(fresh) != 0 {
		t.Fatalf("an undistilled session must reuse nothing, got %v", fresh)
	}
}
