# Per-Fact Push + Reachability-Filtered Pull (COR-2126) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the branch-head CAS sync in `hostedFactsSyncAndPull` with per-fact batch push and a repo-wide delta-feed pull filtered locally by git reachability, then validate teammate distribution with a fresh clone against the live cell.

**Architecture:** The plugin pushes its branch store as NDJSON batches to brainstore's idempotent `facts:upsert` and pulls the repo-wide feed into a local repo-level cache (cursor + records). The branch store is rebuilt as: filter output over the whole cache (trail-merged backlink, anchor-commit reachability from HEAD, own-member facts, anchor-less repo pool) plus this machine's facts that are not in the cache. No CAS, no proposals, no `lineage.json`.

**Tech Stack:** Go stdlib only. Reuses `internal/factsync`'s hardened HTTP transport (`newRequest`/`do`/redaction), `factmerge` wire types, and the existing brain write lock / branch-store helpers.

**Spec:** Linear COR-2126 ticket description + the canonical COR-2110 v3 design comment (both quoted in this plan's companion notes). Server contract verified against brainstore `origin/main` `internal/httpapi/factsupsert.go` + `factsfeed.go`; gateway publishes brainstore's **bare** `/repos/{repo_id}/brain/*` paths on the cell API origin (entire-api `internal/gateway/config/topology.go`, `PathPrefix: ""`).

## Global Constraints

- Never-fails discipline: every gated/soft failure in the sync path is one stderr line and a `nil` return; the error return is for programming errors only.
- Gate ladder order fixed: kill switch → repo binding present/opt-out → `apiurl.Validate` → `mintHostedToken`. Unchanged from today.
- Pull-only degradation: a failed/denied push must not prevent the pull, and must not write the per-branch synced marker (`writeHostedFactsBinding`) — the blind-spot note keys off its absence.
- `SanitizeForEgress` before any record leaves; `factmerge.VerifyIdentities` on local facts before push (server rejects a whole batch on one bad record).
- Wire paths carry the gateway's `/api/v1` prefix like every other route: `POST {base}/api/v1/repos/{id}/brain/facts:upsert`, `GET {base}/api/v1/repos/{id}/brain/facts` (live-verified during Task 6; the bare path 404s). Repo id validated via `repoid.Validate` before any request exists.
- Server caps: 5000 records/batch, 32 MiB body. Client chunks at 1000 records.
- Feed paging: `pageSize` ≤ 100 server-side; every non-empty page returns `nextPageToken`; only an empty page is terminal. Invalid `pageToken` is a 400 → client resets cursor + cache and refetches.
- Lifecycle stays one-way: pull may move a local fact active → superseded/retracted, never revive one.
- No new dependencies. Existing tests must stay green.

## Review Focus

1. Server bumps its cursor epoch → stored pageToken rejected (400): pull must reset cursor AND cache, refetch from start, and converge — never an error loop. (Task 4 test)
2. Anchor commit absent from local git (squash merge, gc, shallow clone): reachability check returns false quietly — no stderr spam per fact; the fact still arrives when `trailId` is set. (Task 3 test)
3. A teammate's retraction in the feed retires the local active copy; a local retraction is never revived by an active feed copy. (Task 4 test)
4. Recycled branch dir: a cached teammate fact that fails the filter is REMOVED from the branch store, while this machine's unsynced local fact survives the rebuild. (Task 4 test)
5. Empty branch store (fresh clone): push is skipped without a server call (server 400s an empty body), pull still runs and fills the store. (Task 4 test)

---

### Task 1: Per-fact wire client in `internal/factsync`

**Files:**
- Create: `internal/factsync/perfact.go`
- Test: `internal/factsync/perfact_test.go`

**Interfaces:**
- Consumes: existing `HTTPServer` (`newRequest`, `do`, `detail`), `repoid.Validate`, `factmerge.Record`.
- Produces (used by Tasks 2 & 4):

```go
type UpsertCounts struct{ Added, Changed, Unchanged int }

type FeedItem struct {
    Record          factmerge.Record `json:"record"`
    TrailID         string           `json:"trailId"`
    CreatedBy       string           `json:"createdBy"`
    CreatedByHandle string           `json:"createdByHandle"`
    UpdatedAt       string           `json:"updatedAt"`
}

type FeedPage struct {
    Items         []FeedItem `json:"items"`
    NextPageToken *string    `json:"nextPageToken"`
}

var ErrInvalidPageToken = errors.New("factsync: hosted feed rejected the page token")

func (h *HTTPServer) UpsertFacts(ctx context.Context, repoID string, ndjson []byte) (UpsertCounts, error)
func (h *HTTPServer) FactsFeedPage(ctx context.Context, repoID, pageToken string, pageSize int) (FeedPage, error)
```

- [ ] **Step 1: Write failing tests** in `perfact_test.go` (httptest server):
  - `TestUpsertFactsWire`: asserts method POST, path `/repos/r1/brain/facts:upsert` (no `/api/v1`), `Authorization: Bearer tok`, body passthrough; returns `{"added":2,"changed":1,"unchanged":3}`; asserts decoded counts.
  - `TestUpsertFactsRejectsBadRepoID`: repoID `"a/b"` → error before any request (serve handler fails the test if hit).
  - `TestUpsertFactsNon2xx`: 403 with JSON error body → error containing status, token redacted.
  - `TestFactsFeedPageWire`: GET `/repos/r1/brain/facts?pageSize=100&pageToken=abc`; returns one item + `nextPageToken`; asserts decode including `record` (snake_case inner), `trailId`, `createdBy`, `createdByHandle`.
  - `TestFactsFeedPageInvalidToken`: 400 with non-empty pageToken → `errors.Is(err, ErrInvalidPageToken)`; 400 with empty pageToken → plain error.
- [ ] **Step 2: Run** `go test ./internal/factsync/ -run 'Upsert|FactsFeed' -v` — expect FAIL (undefined symbols).
- [ ] **Step 3: Implement `perfact.go`.** Path builder: validate with `repoid.Validate`, concatenate `"/repos/" + repoID + "/brain"` (same raw-segment rationale as `repoBasePath`; do not reuse it — these routes are bare). `UpsertFacts` sets `Content-Type: application/x-ndjson` after `newRequest`. `FactsFeedPage` builds the query with `url.Values`; map 400+non-empty-token → `ErrInvalidPageToken` wrapped with `h.detail(resp)`.
- [ ] **Step 4: Run** the same tests — expect PASS. Also `go build ./...`.
- [ ] **Step 5: Commit** `feat(factsync): per-fact upsert and delta-feed client for the brainstore wire`

### Task 2: Repo-level feed cache + cursor store

**Files:**
- Create: `internal/cli/hosted_feed_cache.go`
- Test: `internal/cli/hosted_feed_cache_test.go`

**Interfaces:**
- Consumes: `resolvePluginDirs(env)` / `rejectBrainRootPathSymlinks` (mirror `gitmetaDirForKey`, `facts_sync_cmd.go`), `factsync.FeedItem`.
- Produces (used by Task 4):

```go
type hostedFeedState struct {
    RepoID  string `json:"repo_id"`
    BaseURL string `json:"base_url"`
    Cursor  string `json:"cursor"`
}

func hostedFeedDirForKey(env EntireEnv, key string) (string, error) // <cache>/brain/<key>/hostedfeed
func loadHostedFeedCache(dir string) (hostedFeedState, map[string]factsync.FeedItem, error)
func writeHostedFeedCache(dir string, state hostedFeedState, items map[string]factsync.FeedItem) error
```

Layout: `state.json` (the struct above) and `records.ndjson` (one `factsync.FeedItem` JSON per line, keyed by `Record.ID`, last line wins on load). The cache is rebuildable staging data — it lives under the plugin CACHE tree like `gitmetaDirForKey`, with the same reserved-`workspace`-segment and symlink guards.

- [ ] **Step 1: Write failing tests**: round-trip (write then load equals input), missing dir → zero state + empty map + nil error, corrupt line in `records.ndjson` → that line skipped, rest loaded (cache is rebuildable; never fail the sync for it), reserved key segment rejected by `hostedFeedDirForKey`.
- [ ] **Step 2: Run** `go test ./internal/cli/ -run HostedFeedCache -v` — expect FAIL.
- [ ] **Step 3: Implement.** Write = `os.MkdirAll` + write temp file + `os.Rename` for each of the two files (callers hold the brain write lock; rename keeps a crashed writer from truncating).
- [ ] **Step 4: Run tests** — expect PASS.
- [ ] **Step 5: Commit** `feat(cli): repo-level hosted feed cache with resume cursor`

### Task 3: Pull filter — reachability, member, trail backlink, repo pool

**Files:**
- Create: `internal/cli/hosted_pull_filter.go`
- Test: `internal/cli/hosted_pull_filter_test.go`

**Interfaces:**
- Consumes: `CommandRunner` (`runner.Run(ctx, dir, "git", ...)`), `factsync.FeedItem`.
- Produces (used by Task 4):

```go
func newCommitReachability(ctx context.Context, runner CommandRunner, repoDir string) func(commit string) bool
func keepHostedFact(item factsync.FeedItem, memberAccountID string, reachable func(string) bool) bool
func jwtSubClaim(token string) string
```

Keep rules, in order (first match wins):
1. `item.TrailID != ""` — stamped only at trail merge; covers squash merges whose anchor commits never reach main (COR-2110 amendment 2).
2. Any anchor with `Commit != ""` where `reachable(commit)` is true.
3. `memberAccountID != "" && item.CreatedBy == memberAccountID` — the member's own facts follow them to every machine.
4. No anchor carries a `SessionID` or a `Commit` → repo pool, everyone keeps it.

`newCommitReachability` memoizes per commit; one `git merge-base --is-ancestor <commit> HEAD` each, `err != nil` (including unknown commit) → false, silently. `jwtSubClaim` base64url-decodes the JWT's middle segment and returns the `sub` claim, `""` on any failure — the token was minted by our own host CLI and the claim is used only for local filtering, never authorization.

- [ ] **Step 1: Write failing tests**: one test per rule plus: fact with unreachable commit AND foreign creator AND a session anchor → dropped; unknown commit treated as unreachable without error; memoization (fake runner counts invocations; same commit twice → one git call); `jwtSubClaim` happy path + garbage input → `""`.
- [ ] **Step 2: Run** `go test ./internal/cli/ -run 'HostedPullFilter|JwtSub|CommitReachability' -v` — expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run tests** — expect PASS.
- [ ] **Step 5: Commit** `feat(cli): reachability/member/trail/pool pull filter for hosted facts`

### Task 4: Swap `hostedFactsSyncAndPull` internals

**Files:**
- Modify: `internal/cli/hosted_sync.go` (full rewrite below the gates)
- Modify: `internal/cli/hosted_sync_test.go` (fake speaks the per-fact wire)

**Interfaces:**
- Consumes: Tasks 1–3, `loadFacts`/`writeFacts`/`upsertFact`/`indexOfFact`, `withBrainWriteLock`, `updateFactSourceManifestLocked`, `factsync.SanitizeForEgress`, `factmerge.VerifyIdentities`/`WriteNDJSON`.
- Produces: `hostedFactsSyncAndPull(ctx, errW, opts, repoDir, storage, branch) (bool, error)` — SIGNATURE UNCHANGED (callers: `watch.go`, `hook_cmd.go`, `connect_cmd.go`, `memory_notify_sync.go` seam). `pullOnlyHostedFacts`, `mergeHostedFactsIntoLocal`, and the `factsync.Sync`/`CurrentWithAuthors`/`persistFactsSyncProposals` usage in this file are deleted (per-fact storage keeps conflicting facts side by side; proposals leave the sync path). The old `factsync` Sync/Current/Advance stay — the manual `facts sync` command still uses them until its own deprecation.

New flow after the existing gates (constants `hostedUpsertChunk = 1000`, `hostedFeedPageSize = 100`, `maxHostedFeedPages = 200`):

1. **Push:** `loadFacts(brainDir, branch)` → `SanitizeForEgress` → `VerifyIdentities` → chunks of `hostedUpsertChunk` → `WriteNDJSON` → `UpsertFacts`. Zero facts → skip the call entirely. Any failure: one stderr line "publish failed (local brain unaffected)", remember pushFailed, continue to pull.
2. **Pull (whole thing under `withBrainWriteLock`):** resolve `hostedFeedDirForKey(opts.Env, storage.Key)`; load cache; if `state.RepoID != binding.RepoID || state.BaseURL != baseURL` → start from empty state+items. Loop `FactsFeedPage(pageSize=hostedFeedPageSize)` from `state.Cursor`: merge each page's items into the map by `Record.ID`, follow `NextPageToken`, stop on empty page or `maxHostedFeedPages` (ponytail: a feed larger than 20k records per sync finishes on the next tick — cursor persists). On `ErrInvalidPageToken` for the stored cursor: reset cursor AND items once, restart from `""`. Persist cache (state cursor = last non-empty page's token).
3. **Filter + rebuild branch store (same lock):** `member := jwtSubClaim(token)`; `reachable := newCommitReachability(...)`; `kept := items passing keepHostedFact`, each stamped `Record.Author = CreatedByHandle` (server attribution is the only source — overwrite whatever the wire carried). Rebuild:
   - `existing := loadFacts(brainDir, branch)`
   - upsert every kept record (`upsertFact`), then apply the one-way status move exactly as the old `mergeHostedFactsIntoLocal` did (non-active kept status + active local → copy status/SupersededBy, stamp UpdatedAt).
   - removal pass: drop any existing record whose ID is in the cache map but NOT in kept — it is server-known and filter-rejected (recycle/rename decontamination). Records absent from the cache are this machine's own unsynced facts and always survive.
   - `writeFacts` + `updateFactSourceManifestLocked`; a manifest failure reports beside the successful pull.
4. **Marker + log:** `writeHostedFactsBinding` only when push succeeded. One summary stderr line with pushed counts (`added/changed/unchanged` summed over chunks) and pulled/kept counts. Return `(true, nil)` whenever the server was contacted.

- [ ] **Step 1: Rewrite the test fake** (`hostedFake`) to speak the per-fact wire: `POST …/brain/facts:upsert` parses NDJSON, stores items keyed by id, counts added/changed/unchanged, `denyPush` → 403; `GET …/brain/facts` serves keyset pages of `{record, trailId, createdBy, createdByHandle, updatedAt}` ordered by insert sequence with opaque tokens (index-based is fine), empty page when drained; `rejectToken bool` → 400 once for cursor-reset coverage.
- [ ] **Step 2: Write/adapt failing scenario tests** (names indicative):
  - `TestHostedSyncPushesAndPulls`: local facts reach the fake via upsert; a teammate fact (foreign `createdBy`, anchor commit = a real commit in the test repo's history) lands in the branch store with `Author` = handle; marker written.
  - `TestHostedSyncPullOnlyWhenPushDenied`: `denyPush` → teammate facts still arrive, marker NOT written, stderr notes pull-only.
  - `TestHostedSyncEmptyStoreSkipsPush`: empty branch store → no upsert request recorded, pull still fills the store.
  - `TestHostedSyncFilterDropsUnreachable`: feed fact with unknown commit + foreign creator + session anchor → absent locally; same fact with `trailId` set → present.
  - `TestHostedSyncCursorResumes`: second sync sends the stored token; fake proves no replay from start.
  - `TestHostedSyncCursorResetOnRejectedToken`: `rejectToken` → full refetch converges, no error.
  - `TestHostedSyncRemovesFilterRejectedCachedFacts`: pre-seed branch store with a fact that is in the cache but fails the filter → removed; a local-only fact survives.
  - `TestHostedSyncRetirementOneWay`: feed copy retracted + local active → local retires; local retracted + feed active → stays retracted.
  - Keep all existing gate tests (kill switch, opt-out, absent binding, unreadable binding, bad URL, token failure) passing unchanged.
- [ ] **Step 3: Run** `go test ./internal/cli/ -run HostedSync -v` — expect FAIL.
- [ ] **Step 4: Implement the rewrite.**
- [ ] **Step 5: Run** `go test ./internal/cli/ -run HostedSync -v` — expect PASS.
- [ ] **Step 6: Repo-wide check:** `go build ./... && go test ./...` — fix any caller/test still referencing deleted helpers (grep `mergeHostedFactsIntoLocal|pullOnlyHostedFacts|CurrentWithAuthors` under `internal/cli`).
- [ ] **Step 7: Commit** `feat(cli): per-fact push + reachability-filtered pull replaces branch-head sync (COR-2126)`

### Task 5: Full verification + race + format

- [ ] **Step 1:** `go test ./... ` (full suite) — PASS.
- [ ] **Step 2:** `go test -race ./internal/cli/ ./internal/factsync/` — PASS.
- [ ] **Step 3:** Run the repo's format/CI check (user convention: `oxfmt`; if absent here, `gofmt -l .` must print nothing).
- [ ] **Step 4: Commit** anything the formatter changed.

### Task 6: Fresh-clone teammate validation (live cell)

Evidence-captured validation against the real hosted cell; this is the ticket's third deliverable. Abort any step that would need credentials we don't have; report instead of faking.

- [ ] **Step 1: Probe the deployed wire.** In `~/code/entire/entire-brain`: `entire auth token` + `entire repo view --json` → `curl -s -w '%{http_code}' -H "Authorization: Bearer $TOK" "$API/repos/$REPO_ID/brain/facts?pageSize=1"`. Expect 200 JSON `{items, nextPageToken}`. A 404/503 means the gateway/brainstore rollout hasn't reached this cell — STOP, record as a blocker in the final report; unit coverage still ships.
- [ ] **Step 2: Build the plugin** from the worktree and wire it so the `entire` host dispatches to it (or invoke the built binary's verbs directly with the repo as cwd).
- [ ] **Step 3: Seed + push from checkout A** (the main entire-brain checkout or a scratch hosted repo): author a uniquely-worded fact (`remember`), trigger the sync (`connect` or the sync verb), capture the upsert counts line.
- [ ] **Step 4: Fresh clone as "teammate":** `git clone` the same repo into a temp dir (clean brain state = fresh machine), run `connect` there, then verify: the seeded fact and existing team facts appear in the clone's branch store with author handles; facts anchored to merged main commits pass the filter; the blind-spot note clears after sync.
- [ ] **Step 5: Negative check (unit-level only):** single-account live env cannot exercise the foreign-member drop; state this explicitly in the report and point at `TestHostedSyncFilterDropsUnreachable`.
- [ ] **Step 6:** Record the transcript/evidence in the PR body and a Linear comment on COR-2126.

### Task 7: PR

- [ ] **Step 1:** Push branch `evis/cor-2126-per-fact-sync`, open PR against `entireio/entire-brain` `main` titled `Per-fact push + reachability-filtered pull (COR-2126)`; body: why (COR-2110 v3), what changed, validation evidence, the deliberate leftovers (manual `facts sync` command still on the old wire; single-store collapse is a later stage).
