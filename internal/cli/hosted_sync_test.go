package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/entireio/entire-brain/factmerge"
	"github.com/entireio/entire-brain/internal/factsync"
)

// hostedFake is an in-memory brainstore speaking the per-fact wire:
// POST …/brain/facts:upsert (identity-verified NDJSON batch, counts reply)
// and GET …/brain/facts (insert-ordered keyset feed, index page tokens,
// empty page terminal). Upserted records join the feed with the caller's
// server-stamped attribution, like the real store.
type hostedFake struct {
	mu           sync.Mutex
	items        []factsync.FeedItem
	index        map[string]int
	requests     int
	upsertCalls  int
	feedTokens   []string
	denyPush     bool // upsert → 403, the push-grant-denied shape
	rejectTokens int  // while >0, any request with a pageToken → 400 once each
	callerBy     string
	callerHandle string
}

func (f *hostedFake) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func (f *hostedFake) upsertCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.upsertCalls
}

func (f *hostedFake) feedTokensSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.feedTokens...)
}

func (f *hostedFake) has(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.index[id]
	return ok
}

// seed plants a feed item as if another member had upserted it.
func (f *hostedFake) seed(rec factmerge.Record, trailID, createdBy, handle string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.put(factsync.FeedItem{Record: rec, TrailID: trailID, CreatedBy: createdBy, CreatedByHandle: handle, UpdatedAt: "2026-10-07T12:00:00.000Z"})
}

// setTrail rewrites a seeded item's trail id in place (simulates the merge
// stamp arriving later); feed position is unchanged.
func (f *hostedFake) setTrail(id, trailID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i, ok := f.index[id]; ok {
		f.items[i].TrailID = trailID
	}
}

func (f *hostedFake) put(item factsync.FeedItem) {
	if f.index == nil {
		f.index = make(map[string]int)
	}
	if i, ok := f.index[item.Record.ID]; ok {
		// Like the real store: a re-upsert updates the record but keeps the
		// server-owned envelope — first-writer attribution and the trail id
		// stamped at merge survive later upserts of the same fact — and status
		// advances one way (a stale active copy never revives a retirement).
		if f.items[i].Record.Status != factmerge.StatusActive && item.Record.Status == factmerge.StatusActive {
			item.Record.Status = f.items[i].Record.Status
			item.Record.SupersededBy = f.items[i].Record.SupersededBy
		}
		f.items[i].Record = item.Record
		return
	}
	f.index[item.Record.ID] = len(f.items)
	f.items = append(f.items, item)
}

func newHostedFakeServer(t *testing.T, fake *hostedFake) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.requests++
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/brain/facts:upsert"):
			fake.upsertCalls++
			if fake.denyPush {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			records, err := factmerge.ParseNDJSON(r.Body)
			if err != nil || len(records) == 0 || factmerge.VerifyIdentities(records) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			added, changed := 0, 0
			for _, rec := range records {
				if _, ok := fake.index[rec.ID]; ok {
					changed++
				} else {
					added++
				}
				fake.put(factsync.FeedItem{Record: rec, CreatedBy: fake.callerBy, CreatedByHandle: fake.callerHandle, UpdatedAt: "2026-10-07T12:00:00.000Z"})
			}
			_ = json.NewEncoder(w).Encode(map[string]int{"added": added, "changed": changed, "unchanged": 0})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/brain/facts"):
			token := r.URL.Query().Get("pageToken")
			fake.feedTokens = append(fake.feedTokens, token)
			if token != "" && fake.rejectTokens > 0 {
				fake.rejectTokens--
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid pageToken"}`))
				return
			}
			start := 0
			if token != "" {
				n, err := strconv.Atoi(token)
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				start = n
			}
			pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
			if pageSize < 1 || pageSize > 100 {
				pageSize = 50
			}
			end := min(start+pageSize, len(fake.items))
			var page []factsync.FeedItem
			if start < end {
				page = fake.items[start:end]
			}
			resp := map[string]any{"items": page, "nextPageToken": nil}
			if len(page) > 0 {
				resp["nextPageToken"] = strconv.Itoa(end)
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func hostedSyncTestFact(text string, now time.Time) factRecord {
	return hostedSyncAnchoredFact(text, now, factmerge.Anchor{SessionID: "s"})
}

func hostedSyncAnchoredFact(text string, now time.Time, anchors ...factmerge.Anchor) factRecord {
	paths := normalizeFactPaths([]string{"architecture.sync.wire"})
	if len(paths) == 0 {
		panic("fixture taxonomy path must be valid")
	}
	return factRecord{
		ID:         factRecordID(text, paths),
		Paths:      paths,
		Text:       text,
		Branch:     "main",
		Origin:     factOriginAuthored,
		Status:     factStatusActive,
		Provenance: anchors,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// hostedSyncHarness wires a temp brain dir and cache, a binding to the fake
// server, and a runner that mints a fake token (sub "ulrich") and fails git
// calls. The last return is the fake server's base URL (the bound target).
func hostedSyncHarness(t *testing.T, fake *hostedFake, bind bool) (Options, repoStorage, string, string) {
	t.Helper()
	repoDir := t.TempDir()
	brainDir := t.TempDir()
	storage := repoStorage{Key: "test/repo", BrainDir: brainDir, HeadPath: filepath.Join(t.TempDir(), "HEAD")}
	if fake.callerBy == "" {
		fake.callerBy = "acct-caller"
	}
	if fake.callerHandle == "" {
		fake.callerHandle = "caller"
	}
	srv := newHostedFakeServer(t, fake)
	if bind {
		if err := writeHostedRepoBinding(brainDir, hostedRepoBinding{RepoID: "repo1", BaseURL: srv.URL}); err != nil {
			t.Fatal(err)
		}
	}
	runner := commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		if name == "entire" && len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
			return []byte(fakeJWT + "\n"), nil, nil
		}
		return nil, nil, errors.New("not available")
	})
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	opts := Options{Version: "test", Env: EntireEnv{RepoRoot: repoDir, PluginCacheDir: t.TempDir()}, Runner: runner, Now: func() time.Time { return now }}
	return opts, storage, repoDir, srv.URL
}

func TestHostedSyncGates(t *testing.T) {
	t.Run("NO_EGRESS wins over a present binding", func(t *testing.T) {
		fake := &hostedFake{}
		opts, storage, repoDir, _ := hostedSyncHarness(t, fake, true)
		t.Setenv("ENTIRE_BRAIN_NO_EGRESS", "1")
		var errOut bytes.Buffer
		if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
			t.Fatal(err)
		}
		if fake.requestCount() != 0 {
			t.Fatalf("NO_EGRESS must make zero requests, got %d", fake.requestCount())
		}
	})
	t.Run("no binding means silent no-op", func(t *testing.T) {
		fake := &hostedFake{}
		opts, storage, repoDir, _ := hostedSyncHarness(t, fake, false)
		var errOut bytes.Buffer
		if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
			t.Fatal(err)
		}
		if fake.requestCount() != 0 {
			t.Fatalf("unconnected repo must make zero requests, got %d", fake.requestCount())
		}
		if errOut.Len() != 0 {
			t.Fatalf("unconnected repo must be silent, got %q", errOut.String())
		}
	})
}

func TestHostedSyncTokenMintFailureIsNonFatal(t *testing.T) {
	fake := &hostedFake{}
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, true)
	opts.Runner = commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		return nil, nil, errors.New("not logged in")
	})
	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatalf("mint failure must be non-fatal, got %v", err)
	}
	if fake.requestCount() != 0 {
		t.Fatalf("mint failure must make zero requests, got %d", fake.requestCount())
	}
	if lines := strings.Count(strings.TrimRight(errOut.String(), "\n"), "\n") + 1; errOut.Len() == 0 || lines != 1 {
		t.Fatalf("expected exactly one stderr line, got %q", errOut.String())
	}
}

func TestHostedSyncPublishesAndPulls(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	localFact := hostedSyncTestFact("local fact A", now)
	remoteFact := hostedSyncTestFact("remote fact B", now)
	fake := &hostedFake{}
	fake.seed(remoteFact, "trail-1", "acct-teammate", "teammate")
	opts, storage, repoDir, baseURL := hostedSyncHarness(t, fake, true)
	if err := writeFacts(storage.BrainDir, "main", []factRecord{localFact}); err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}

	if !fake.has(localFact.ID) {
		t.Fatal("the local fact must be upserted to the hosted store")
	}
	local, err := loadFacts(storage.BrainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	var pulled *factRecord
	for i := range local {
		if local[i].ID == remoteFact.ID {
			pulled = &local[i]
		}
	}
	if pulled == nil {
		t.Fatalf("remote fact must be pulled into the local store, got %d records", len(local))
	}
	if pulled.Author != "teammate" {
		t.Fatalf("pulled fact must carry the server-stamped author, got %q", pulled.Author)
	}
	if err := checkHostedFactsBinding(storage.BrainDir, "main", "repo1", baseURL); err != nil {
		t.Fatalf("per-branch hosted binding must be recorded: %v", err)
	}
	if !strings.Contains(errOut.String(), "hosted sync: branch main") {
		t.Fatalf("expected a summary line, got %q", errOut.String())
	}
}

func TestHostedSyncEmptyStoreSkipsPush(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	remoteFact := hostedSyncTestFact("remote fact B", now)
	fake := &hostedFake{}
	fake.seed(remoteFact, "trail-1", "acct-teammate", "teammate")
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, true)

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	if fake.upsertCount() != 0 {
		t.Fatalf("an empty store must not post an upsert, got %d calls", fake.upsertCount())
	}
	local, err := loadFacts(storage.BrainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 1 || local[0].ID != remoteFact.ID {
		t.Fatalf("pull must still fill the store, got %+v", local)
	}
}

func TestHostedSyncFilterDropsUnreachable(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	dropped := hostedSyncAnchoredFact("foreign unmerged fact", now, factmerge.Anchor{SessionID: "s1", Commit: "deadbeef"})
	keptViaTrail := hostedSyncAnchoredFact("foreign merged fact", now, factmerge.Anchor{SessionID: "s2", Commit: "deadbeef"})
	fake := &hostedFake{}
	fake.seed(dropped, "", "acct-teammate", "teammate")
	fake.seed(keptViaTrail, "trail-7", "acct-teammate", "teammate")
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, true)

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	local, err := loadFacts(storage.BrainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, rec := range local {
		ids[rec.ID] = true
	}
	if ids[dropped.ID] {
		t.Fatal("a foreign fact with no reachable anchor and no trail must be filtered out")
	}
	if !ids[keptViaTrail.ID] {
		t.Fatal("a merged trail id marks the fact main-visible even with unreachable commits")
	}
}

func TestHostedSyncMemberFactsFollowTheAccount(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	// fakeJWT's sub is "ulrich": the member's own fact, pushed from another
	// machine, carries their account id and an unreachable commit anchor.
	ownFact := hostedSyncAnchoredFact("my fact from my laptop", now, factmerge.Anchor{SessionID: "s1", Commit: "deadbeef"})
	fake := &hostedFake{}
	fake.seed(ownFact, "", "ulrich", "ulrich")
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, true)

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	local, err := loadFacts(storage.BrainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 1 || local[0].ID != ownFact.ID {
		t.Fatalf("the member's own facts must follow their account, got %+v", local)
	}
}

func TestHostedSyncCursorResumes(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	first := hostedSyncTestFact("first remote fact", now)
	fake := &hostedFake{}
	fake.seed(first, "trail-1", "acct-teammate", "teammate")
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, true)

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	second := hostedSyncTestFact("second remote fact", now)
	fake.seed(second, "trail-1", "acct-teammate", "teammate")

	before := len(fake.feedTokensSeen())
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	for _, token := range fake.feedTokensSeen()[before:] {
		if token == "" {
			t.Fatal("the second sync must resume from the stored cursor, not replay from the start")
		}
	}
	local, err := loadFacts(storage.BrainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, rec := range local {
		ids[rec.ID] = true
	}
	if !ids[first.ID] || !ids[second.ID] {
		t.Fatalf("both feed facts must be local after the second sync, got %v", ids)
	}
}

func TestHostedSyncCursorResetOnRejectedToken(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	first := hostedSyncTestFact("first remote fact", now)
	fake := &hostedFake{}
	fake.seed(first, "trail-1", "acct-teammate", "teammate")
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, true)

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	second := hostedSyncTestFact("second remote fact", now)
	fake.seed(second, "trail-1", "acct-teammate", "teammate")
	fake.mu.Lock()
	fake.rejectTokens = 1
	fake.mu.Unlock()

	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	local, err := loadFacts(storage.BrainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, rec := range local {
		ids[rec.ID] = true
	}
	if !ids[first.ID] || !ids[second.ID] {
		t.Fatalf("a rejected cursor must trigger a full refetch that converges, got %v", ids)
	}
}

func TestHostedSyncRemovesFilterRejectedCachedFacts(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	localFact := hostedSyncTestFact("this machine's unsynced fact", now)
	teammate := hostedSyncAnchoredFact("teammate fact on a recycled branch", now, factmerge.Anchor{SessionID: "s1", Commit: "c1"})
	fake := &hostedFake{denyPush: true} // keep localFact out of the hosted feed
	fake.seed(teammate, "", "acct-teammate", "teammate")
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, true)
	if err := writeFacts(storage.BrainDir, "main", []factRecord{localFact}); err != nil {
		t.Fatal(err)
	}
	reachable := true
	opts.Runner = commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		if name == "entire" && len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
			return []byte(fakeJWT + "\n"), nil, nil
		}
		if name == "git" && len(args) == 4 && args[0] == "merge-base" && args[1] == "--is-ancestor" && args[2] == "c1" && reachable {
			return nil, nil, nil
		}
		return nil, nil, errors.New("not available")
	})

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	local, err := loadFacts(storage.BrainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, rec := range local {
		ids[rec.ID] = true
	}
	if !ids[teammate.ID] || !ids[localFact.ID] {
		t.Fatalf("first sync must hold both facts, got %v", ids)
	}

	// HEAD moved (recycle/rename): the anchor commit is no longer an ancestor.
	reachable = false
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	local, err = loadFacts(storage.BrainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	ids = map[string]bool{}
	for _, rec := range local {
		ids[rec.ID] = true
	}
	if ids[teammate.ID] {
		t.Fatal("a cached fact that no longer passes the filter must leave the branch store")
	}
	if !ids[localFact.ID] {
		t.Fatal("this machine's own unsynced fact must survive the rebuild")
	}
}

func TestPullPreservesLocalStatus(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	retracted := hostedSyncTestFact("contested fact", now)
	retracted.Status = factStatusRetracted
	remoteActive := hostedSyncTestFact("contested fact", now)

	fake := &hostedFake{}
	fake.seed(remoteActive, "trail-1", "acct-teammate", "teammate")
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, true)
	if err := writeFacts(storage.BrainDir, "main", []factRecord{retracted}); err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	local, err := loadFacts(storage.BrainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range local {
		if rec.ID == retracted.ID && rec.Status != factStatusRetracted {
			t.Fatalf("local retraction must survive the pull, got status %q", rec.Status)
		}
	}
}

func TestPullRefusesForgedFeedWithoutWrite(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	localFact := hostedSyncTestFact("local fact A", now)
	forged := hostedSyncTestFact("remote fact B", now)
	forged.ID = "fact:forged-id-that-does-not-match-content"

	fake := &hostedFake{}
	fake.mu.Lock()
	fake.put(factsync.FeedItem{Record: forged, TrailID: "trail-1", CreatedBy: "acct-teammate", CreatedByHandle: "teammate"})
	fake.mu.Unlock()
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, true)
	if err := writeFacts(storage.BrainDir, "main", []factRecord{localFact}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(storage.BrainDir, filepath.FromSlash(factsFileRelPath("main"))))
	if err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatalf("sync failure must be non-fatal, got %v", err)
	}
	after, err := os.ReadFile(filepath.Join(storage.BrainDir, filepath.FromSlash(factsFileRelPath("main"))))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a forged feed must leave the local store byte-identical")
	}
	if errOut.Len() == 0 {
		t.Fatal("a refused feed must leave a stderr note")
	}
}

func TestPullServerAttributionBeatsBlobAuthor(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	forgedAuthor := hostedSyncTestFact("remote fact with forged author", now)
	forgedAuthor.Author = "mallory"
	fake := &hostedFake{}
	fake.seed(forgedAuthor, "trail-1", "acct-teammate", "teammate")
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, true)

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	local, err := loadFacts(storage.BrainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range local {
		if rec.ID == forgedAuthor.ID && rec.Author != "teammate" {
			t.Fatalf("the server-stamped attribution must beat a blob-carried author, got %q", rec.Author)
		}
	}
}

// TestHostedSyncNeverConnectsWithoutBinding pins the default-off model: the
// ONLY ways a repo starts syncing are `entire brain connect` and the (future)
// onboarding flow, both of which write the binding in the foreground. Even
// with placement resolvable and a token available, a background trigger with
// no binding must stay local, write nothing, and say nothing.
func TestHostedSyncNeverConnectsWithoutBinding(t *testing.T) {
	fake := &hostedFake{}
	opts, storage, repoDir, baseURL := hostedSyncHarness(t, fake, false)
	opts.Runner = commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		if name == "entire" && len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			return []byte(`{"repoId":"repo1","apiUrl":"` + baseURL + `"}` + "\n"), nil, nil
		}
		if name == "entire" && len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
			return []byte(fakeJWT + "\n"), nil, nil
		}
		return nil, nil, errors.New("not available")
	})

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	if fake.requestCount() != 0 {
		t.Fatalf("an unbound repo must make zero hosted requests, got %d", fake.requestCount())
	}
	if _, present, _ := readHostedRepoBinding(storage.BrainDir); present {
		t.Fatal("a background trigger must never write the binding")
	}
	if errOut.Len() != 0 {
		t.Fatalf("staying local is the default, not an event: %q", errOut.String())
	}
}

// TestHostedSyncSkipsCorruptBindingWithoutReconnect pins the privacy rule
// that killed the old corrupt-binding self-heal: an unreadable binding may
// hide a recorded opt-out (`entire brain disconnect`), so NO read error may
// fall through to auto-connect. Skip loudly and leave the file alone; the
// remedy is an explicit `entire brain connect`.
func TestHostedSyncSkipsCorruptBindingWithoutReconnect(t *testing.T) {
	fake := &hostedFake{}
	opts, storage, repoDir, baseURL := hostedSyncHarness(t, fake, false)
	opts.Runner = commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		if name == "entire" && len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			return []byte(`{"repoId":"repo1","apiUrl":"` + baseURL + `"}` + "\n"), nil, nil
		}
		if name == "entire" && len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
			return []byte(fakeJWT + "\n"), nil, nil
		}
		return nil, nil, errors.New("not available")
	})
	bindingPath := filepath.Join(storage.BrainDir, hostedRepoBindingFile)
	if err := os.WriteFile(bindingPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	if fake.requestCount() != 0 {
		t.Fatalf("a corrupt binding must not sync anywhere, got %d requests", fake.requestCount())
	}
	data, err := os.ReadFile(bindingPath)
	if err != nil || string(data) != "{not json" {
		t.Fatalf("a corrupt binding must survive untouched, got %q err=%v", data, err)
	}
	if !strings.Contains(errOut.String(), "skipped") {
		t.Fatalf("skip must be reported: %q", errOut.String())
	}
}

// TestHostedSyncSkipsUnreadableBindingWithoutRewrite bounds the self-heal:
// only a PARSE error (corrupt JSON, content unrecoverable) may fall through to
// re-resolution. Any other read failure says nothing about what the binding
// holds — rewriting it could silently replace an explicitly configured custom
// target with default placement and redirect publishes to another repo.
func TestHostedSyncSkipsUnreadableBindingWithoutRewrite(t *testing.T) {
	fake := &hostedFake{}
	opts, storage, repoDir, baseURL := hostedSyncHarness(t, fake, false)
	opts.Runner = commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		if name == "entire" && len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			return []byte(`{"repoId":"repo1","apiUrl":"` + baseURL + `"}` + "\n"), nil, nil
		}
		if name == "entire" && len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
			return []byte(fakeJWT + "\n"), nil, nil
		}
		return nil, nil, errors.New("not available")
	})
	// A directory at the binding path fails the read with a non-parse error on
	// every platform; chmod 0o000 is a read no-op on Windows.
	bindingPath := filepath.Join(storage.BrainDir, hostedRepoBindingFile)
	if err := os.Mkdir(bindingPath, 0o700); err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	if fake.requestCount() != 0 {
		t.Fatalf("an unreadable binding must not sync anywhere, got %d requests", fake.requestCount())
	}
	if info, err := os.Stat(bindingPath); err != nil || !info.IsDir() {
		t.Fatalf("unreadable binding must survive untouched: info=%v err=%v", info, err)
	}
	out := errOut.String()
	if !strings.Contains(out, "skipped") || strings.Contains(out, "re-resolving") {
		t.Fatalf("a read failure must skip without re-resolution: %q", out)
	}
}

func TestHostedSyncOptedOutSkipsSyncAndAutoConnect(t *testing.T) {
	fake := &hostedFake{}
	opts, storage, repoDir, baseURL := hostedSyncHarness(t, fake, false)
	if err := writeHostedRepoBinding(storage.BrainDir, hostedRepoBinding{RepoID: "repo1", BaseURL: baseURL, OptedOut: true}); err != nil {
		t.Fatal(err)
	}
	shellOuts := 0
	opts.Runner = commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		shellOuts++
		return nil, nil, errors.New("not available")
	})

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	if shellOuts != 0 || fake.requestCount() != 0 {
		t.Fatalf("an opted-out repo must make no shell-outs and no requests, got shellOuts=%d requests=%d", shellOuts, fake.requestCount())
	}
	if errOut.Len() != 0 {
		t.Fatalf("an opted-out repo must be silent, got %q", errOut.String())
	}
}

func TestSetHostedOptOutKeepsCoordinatesAndHealsCorruption(t *testing.T) {
	brainDir := t.TempDir()
	if err := writeHostedRepoBinding(brainDir, hostedRepoBinding{RepoID: "repo1", BaseURL: "https://cell.api.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := setHostedOptOut(brainDir); err != nil {
		t.Fatal(err)
	}
	binding, present, err := readHostedRepoBinding(brainDir)
	if err != nil || !present || !binding.OptedOut || binding.RepoID != "repo1" {
		t.Fatalf("opt-out must keep the coordinates: present=%v err=%v binding=%+v", present, err, binding)
	}

	if err := writeBrainRelativeFileAtomic(brainDir, hostedRepoBindingFile, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := setHostedOptOut(brainDir); err != nil {
		t.Fatalf("a corrupt state file must not block the opt-out, got %v", err)
	}
	binding, present, err = readHostedRepoBinding(brainDir)
	if err != nil || !present || !binding.OptedOut {
		t.Fatalf("corrupt file must be replaced by a bare opt-out: present=%v err=%v binding=%+v", present, err, binding)
	}
}

func TestHostedSyncNoEgressBlocksAutoConnect(t *testing.T) {
	fake := &hostedFake{}
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, false)
	placementCalls := 0
	opts.Runner = commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		placementCalls++
		return nil, nil, errors.New("not available")
	})
	t.Setenv("ENTIRE_BRAIN_NO_EGRESS", "1")
	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	if placementCalls != 0 || fake.requestCount() != 0 {
		t.Fatalf("NO_EGRESS must block even placement resolution, got placement=%d requests=%d", placementCalls, fake.requestCount())
	}
}

func TestPullAppliesRemoteRetirement(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	localActive := hostedSyncTestFact("fact the team retired", now)
	remoteRetired := localActive
	remoteRetired.Status = factStatusRetracted
	remoteRetired.UpdatedAt = now.Add(time.Hour)

	fake := &hostedFake{}
	fake.seed(remoteRetired, "trail-1", "acct-teammate", "teammate")
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, true)
	if err := writeFacts(storage.BrainDir, "main", []factRecord{localActive}); err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	local, err := loadFacts(storage.BrainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range local {
		if rec.ID == localActive.ID && rec.Status != factStatusRetracted {
			t.Fatalf("a retirement settled in the hosted store must reach the local store, got status %q", rec.Status)
		}
	}
}

func TestPullNeverRevivesFromAnActiveFeed(t *testing.T) {
	// The inverse of TestPullAppliesRemoteRetirement: lifecycle moves one way.
	// (TestPullPreservesLocalStatus covers the full sync; this pins the pair.)
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	localRetired := hostedSyncTestFact("fact this member retired", now)
	localRetired.Status = factStatusRetracted
	remoteActive := hostedSyncTestFact("fact this member retired", now)

	fake := &hostedFake{}
	fake.seed(remoteActive, "trail-1", "acct-teammate", "teammate")
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, true)
	if err := writeFacts(storage.BrainDir, "main", []factRecord{localRetired}); err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatal(err)
	}
	local, err := loadFacts(storage.BrainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range local {
		if rec.ID == localRetired.ID && rec.Status != factStatusRetracted {
			t.Fatalf("the pull must never revive a locally retired fact, got status %q", rec.Status)
		}
	}
}

func TestHostedSyncFallsBackToPullOnlyWhenPublishDenied(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	localFact := hostedSyncTestFact("local fact A", now)
	remoteFact := hostedSyncTestFact("remote fact B", now)
	fake := &hostedFake{denyPush: true}
	fake.seed(remoteFact, "trail-1", "acct-teammate", "teammate")
	opts, storage, repoDir, baseURL := hostedSyncHarness(t, fake, true)
	if err := writeFacts(storage.BrainDir, "main", []factRecord{localFact}); err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatalf("publish denial must be non-fatal, got %v", err)
	}
	local, err := loadFacts(storage.BrainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range local {
		if rec.ID == remoteFact.ID && rec.Author == "teammate" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a denied publish must still pull teammates' facts, got %d local records", len(local))
	}
	// The branch did NOT sync: the marker stays absent so the blind-spot note
	// keeps firing until a full sync succeeds.
	if err := checkHostedFactsBinding(storage.BrainDir, "main", "repo1", baseURL); err == nil {
		t.Fatal("a pull-only run must not mark the branch as synced")
	}
	if !strings.Contains(errOut.String(), "pull-only") {
		t.Fatalf("expected a pull-only note on stderr, got %q", errOut.String())
	}
}

func TestPullOnlyRejectsAForgedFeed(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	localFact := hostedSyncTestFact("local fact A", now)
	forged := hostedSyncTestFact("remote fact B", now)
	forged.ID = "fact:forged-id-that-does-not-match-content"
	fake := &hostedFake{denyPush: true}
	fake.mu.Lock()
	fake.put(factsync.FeedItem{Record: forged, TrailID: "trail-1", CreatedBy: "acct-teammate", CreatedByHandle: "teammate"})
	fake.mu.Unlock()
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, true)
	if err := writeFacts(storage.BrainDir, "main", []factRecord{localFact}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(storage.BrainDir, filepath.FromSlash(factsFileRelPath("main"))))
	if err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	if _, err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatalf("must be non-fatal, got %v", err)
	}
	after, err := os.ReadFile(filepath.Join(storage.BrainDir, filepath.FromSlash(factsFileRelPath("main"))))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a forged feed must leave the local store byte-identical on the pull-only path too")
	}
}
