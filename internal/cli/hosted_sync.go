package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/entireio/entire-brain/factmerge"
	"github.com/entireio/entire-brain/internal/apiurl"
	"github.com/entireio/entire-brain/internal/factsync"
)

// hostEntireBinary is the host CLI the plugin shells out to; PATH comes from
// the caller's environment (the daemon plist bakes PATH at install time).
const hostEntireBinary = "entire"

const (
	// hostedUpsertChunk stays well under brainstore's 5000-record batch cap
	// and its 32 MiB body cap.
	hostedUpsertChunk = 1000
	// hostedFeedPageSize is brainstore's page-size ceiling.
	hostedFeedPageSize = 100
	// maxHostedFeedPages bounds one pull so the never-fails path cannot spin
	// on a pathological feed. ponytail: a delta larger than 20k records
	// finishes on the next trigger — the cursor persists either way.
	maxHostedFeedPages = 200
)

// hostedFactsSyncAndPull closes the hosted-memory loop for one branch on the
// per-fact wire (COR-2126): batch-upsert this machine's branch store to
// brainstore, pull the repo-wide delta feed into the repo-level cache, and
// rebuild the branch store as the pull filter's output plus this machine's
// own facts the feed has not seen. No CAS, no branch heads, no proposals —
// per-fact storage keeps conflicting facts side by side and resolution stays
// deferred to the async pipeline.
//
// It is a NEVER-FAILS step: every gated or soft failure is one stderr line and
// a nil return, because its callers (daemon tick, session-end hook, connect)
// must not fail on hosted outages. The error return exists for programming
// errors only.
//
// Gate order is fixed: (1) the local-only kill switch always wins; (2) a
// recorded opt-out (`entire brain disconnect`) stops sync until `connect`
// clears it; (3) the repo binding is the consent AND the connection state —
// only `entire brain connect` (or the future onboarding flow) writes it, so an
// absent binding means local-only, silently, every run. Background triggers
// never connect a repo. (4) apiurl.Validate before any client exists. A fresh
// token is minted per call via the host CLI's own login and held only in
// memory.
//
// The bool reports whether a sync was actually ATTEMPTED (gates passed, the
// server was contacted): callers that log success must not report a gated
// no-op as a sync that ran.
func hostedFactsSyncAndPull(ctx context.Context, errW io.Writer, opts Options, repoDir string, storage repoStorage, branch string) (bool, error) {
	if brainNoEgressMode() {
		return false, nil
	}
	binding, present, err := readHostedRepoBinding(storage.BrainDir)
	if err != nil {
		// NO unreadable binding may fall through to auto-connect: the file may
		// hide a recorded opt-out (privacy) or a custom target, and neither
		// survives a rewrite. Skip loudly; the remedy is an explicit
		// `entire brain connect`.
		fmt.Fprintf(errW, "hosted sync: skipped: %v\n", err)
		return false, nil
	}
	if !present || binding.OptedOut {
		return false, nil
	}
	baseURL, err := apiurl.Validate(binding.BaseURL)
	if err != nil {
		fmt.Fprintf(errW, "hosted sync: skipped: %v\n", err)
		return false, nil
	}
	token, err := mintHostedToken(ctx, opts.Runner, repoDir, hostEntireBinary)
	if err != nil {
		fmt.Fprintf(errW, "hosted sync: skipped: %v\n", err)
		return false, nil
	}
	srv := &factsync.HTTPServer{BaseURL: baseURL, Token: token}

	pushed, counts := pushHostedFacts(ctx, errW, storage, srv, binding.RepoID, branch)
	kept, total, manifestErr, pullErr := pullHostedFacts(ctx, opts, repoDir, storage, srv, binding.RepoID, baseURL, token, branch)
	if pullErr != nil {
		fmt.Fprintf(errW, "hosted sync: branch %s pull failed (local brain unaffected): %v\n", branch, pullErr)
		return true, nil
	}
	if pushed {
		// The per-branch marker records that this branch completed a sync
		// against this target; the blind-spot note keys off its absence. It
		// needs BOTH halves: a pull-only run leaves it unwritten, and so does
		// a failed pull — the empty-store push "succeeds" without contacting
		// the server, so the pull is what proves the exchange happened.
		if err := writeHostedFactsBinding(storage.BrainDir, branch, binding.RepoID, baseURL); err != nil {
			fmt.Fprintf(errW, "hosted sync: record hosted target: %v\n", err)
		}
	}
	if manifestErr != nil {
		fmt.Fprintf(errW, "hosted sync: facts updated, but the source manifest could not be refreshed: %v\n", manifestErr)
	}
	fmt.Fprintf(errW, "hosted sync: branch %s pushed added=%d changed=%d unchanged=%d; feed facts=%d kept=%d\n",
		branch, counts.Added, counts.Changed, counts.Unchanged, total, kept)
	return true, nil
}

// pushHostedFacts batch-upserts the branch store. Idempotent server-side, so
// every trigger can fire it without coordination. Any failure degrades to
// pull-only: one stderr line, pushed=false, and the caller skips the synced
// marker so the blind-spot note keeps firing.
func pushHostedFacts(ctx context.Context, errW io.Writer, storage repoStorage, srv *factsync.HTTPServer, repoID, branch string) (bool, factsync.UpsertCounts) {
	facts, err := loadFacts(storage.BrainDir, branch)
	if err != nil {
		fmt.Fprintf(errW, "hosted sync: branch %s publish failed (local brain unaffected): %v; pull-only\n", branch, err)
		return false, factsync.UpsertCounts{}
	}
	if len(facts) == 0 {
		// Nothing to publish counts as a successful (empty) publish; the
		// server rejects an empty body, so don't send one.
		return true, factsync.UpsertCounts{}
	}
	// Redact local-only provenance coordinates before these facts leave, and
	// verify this member's own facts BEFORE publishing them — the server
	// rejects a whole batch for one malformed record, so the check runs here,
	// per record: one corrupted line must not hold the rest of the store
	// hostage. Unverifiable records stay home with a note; everything else
	// uploads.
	egress := factsync.SanitizeForEgress(facts)
	publishable := egress[:0]
	skipped := 0
	for _, rec := range egress {
		if err := factmerge.VerifyIdentity(rec); err != nil {
			skipped++
			continue
		}
		publishable = append(publishable, rec)
	}
	egress = publishable
	if skipped > 0 {
		fmt.Fprintf(errW, "hosted sync: branch %s: skipped %d unpublishable record(s) whose content id does not re-derive (kept locally, not uploaded)\n", branch, skipped)
	}
	if len(egress) == 0 {
		// Everything was unpublishable: nothing was shared, so this is a
		// failed publish, not a vacuous success — the branch must not be
		// marked as synced.
		fmt.Fprintf(errW, "hosted sync: branch %s publish failed (local brain unaffected): no publishable records; pull-only\n", branch)
		return false, factsync.UpsertCounts{}
	}
	var total factsync.UpsertCounts
	for start := 0; start < len(egress); start += hostedUpsertChunk {
		end := min(start+hostedUpsertChunk, len(egress))
		var buf bytes.Buffer
		if err := factmerge.WriteNDJSON(&buf, egress[start:end]); err != nil {
			fmt.Fprintf(errW, "hosted sync: branch %s publish failed (local brain unaffected): %v; pull-only\n", branch, err)
			return false, factsync.UpsertCounts{}
		}
		counts, err := srv.UpsertFacts(ctx, repoID, buf.Bytes())
		if err != nil {
			fmt.Fprintf(errW, "hosted sync: branch %s publish failed (local brain unaffected): %v; pull-only\n", branch, err)
			return false, factsync.UpsertCounts{}
		}
		total.Added += counts.Added
		total.Changed += counts.Changed
		total.Unchanged += counts.Unchanged
	}
	return true, total
}

// pullHostedFacts fetches the repo delta feed since the stored cursor into
// the repo-level cache, filters the WHOLE cache by local visibility
// (keepHostedFact), and rebuilds the branch store from the result. Returns
// (kept, total cached) for the caller's summary line.
//
// Everything runs under the brain write lock: the cache and the branch store
// must move together, and concurrent triggers (tick, hook, push hint) must
// not interleave their rebuilds.
func pullHostedFacts(ctx context.Context, opts Options, repoDir string, storage repoStorage, srv *factsync.HTTPServer, repoID, baseURL, token, branch string) (kept, total int, manifestErr, err error) {
	feedDir, err := hostedFeedDirForKey(opts.Env, storage.Key)
	if err != nil {
		return 0, 0, nil, err
	}
	err = withBrainWriteLock(storage.BrainDir, func() error {
		state, items, err := loadHostedFeedCache(feedDir)
		if err != nil {
			return err
		}
		// A different hosted target invalidates everything: cached facts from
		// another repo (or another cell) must never rebuild this branch store.
		if state.RepoID != repoID || state.BaseURL != baseURL {
			state = hostedFeedState{RepoID: repoID, BaseURL: baseURL}
			items = make(map[string]factsync.FeedItem)
		}
		cursor := state.Cursor
		cursorReset := false
		for page := 0; page < maxHostedFeedPages; page++ {
			feedPage, err := srv.FactsFeedPage(ctx, repoID, cursor, hostedFeedPageSize)
			if errors.Is(err, factsync.ErrInvalidPageToken) && !cursorReset && cursor != "" {
				// The server's cursor epoch moved: the stored position is
				// meaningless, so refetch from the start. The cache rebuild is
				// idempotent (keyed by fact id).
				cursorReset = true
				cursor = ""
				items = make(map[string]factsync.FeedItem)
				continue
			}
			if err != nil {
				return err
			}
			if len(feedPage.Items) == 0 {
				break
			}
			for _, item := range feedPage.Items {
				if item.Record.ID != "" {
					items[item.Record.ID] = item
				}
			}
			if feedPage.NextPageToken == nil {
				break
			}
			cursor = *feedPage.NextPageToken
		}

		member := jwtSubClaim(token)
		reachable := newCommitReachability(ctx, opts.Runner, repoDir)
		keptIDs := make(map[string]bool)
		var keptRecords []factRecord
		for id, item := range items {
			if !keepHostedFact(item, member, reachable) {
				continue
			}
			rec := item.Record
			// Server attribution is the only author source: it overwrites
			// whatever the wire record carried (an old or hostile peer's value
			// must not survive).
			rec.Author = item.CreatedByHandle
			keptRecords = append(keptRecords, rec)
			keptIDs[id] = true
		}
		// Same fail-closed rule as the old head pull: a record whose content id
		// does not re-derive is a forgery, and nothing from this feed — cache
		// and cursor included — may land until the server stops serving it.
		if err := factmerge.VerifyIdentities(keptRecords); err != nil {
			return fmt.Errorf("refused untrustworthy feed: %w", err)
		}
		sort.Slice(keptRecords, func(i, j int) bool { return keptRecords[i].ID < keptRecords[j].ID })

		if err := writeHostedFeedCache(feedDir, hostedFeedState{RepoID: repoID, BaseURL: baseURL, Cursor: cursor}, items); err != nil {
			return err
		}

		existing, err := loadFacts(storage.BrainDir, branch)
		if err != nil {
			return err
		}
		for _, fact := range keptRecords {
			// upsertFact never moves a local status, so a locally retracted
			// fact stays retracted whatever the feed says. The feed is
			// authoritative for RETIREMENTS, though: lifecycle moves one way —
			// active to retired, never a revival.
			existing = upsertFact(existing, fact)
			if fact.Status == factStatusActive {
				continue
			}
			if i := indexOfFact(existing, fact.ID); i >= 0 && existing[i].Status == factStatusActive {
				existing[i].Status = fact.Status
				existing[i].SupersededBy = fact.SupersededBy
				existing[i].UpdatedAt = opts.Now().UTC()
			}
		}
		// The branch store is the filter output plus this machine's own facts
		// the feed has not seen (unsynced or push-denied). A record the feed
		// knows but the filter rejected is another branch's — or a recycled
		// name's — fact: drop it. This is what replaces rename/recycle
		// detection (lineage.json is cancelled). Three stand-downs guard
		// against evicting this machine's own knowledge when the feed's copy
		// fails the filter (first-writer attribution can belong to a teammate
		// who pushed the same statement first, carrying only THEIR anchors):
		// a transcript anchor is proof the fact was distilled here
		// (SanitizeForEgress strips it on push, so a pulled copy never
		// carries one); the local copy's OWN anchors get the same keep filter
		// (judged memberless — being in this store is not proof of
		// authorship), so a locally reachable commit or an anchor-less pool
		// fact survives; and with no member identity the filter cannot
		// recognize this member's facts at all, so removal is disabled
		// rather than evicting them wholesale.
		rebuilt := existing[:0]
		for _, rec := range existing {
			if _, inFeed := items[rec.ID]; member != "" && inFeed && !keptIDs[rec.ID] &&
				!hasLocalProvenance(rec) && !keepHostedFact(factsync.FeedItem{Record: rec}, "", reachable) {
				continue
			}
			rebuilt = append(rebuilt, rec)
		}
		if err := writeFacts(storage.BrainDir, branch, rebuilt); err != nil {
			return err
		}
		// A manifest failure reports alongside the successful pull instead of
		// masking it.
		manifestErr = updateFactSourceManifestLocked(storage.BrainDir, opts.Now().UTC())
		kept, total = len(keptRecords), len(items)
		return nil
	})
	return kept, total, manifestErr, err
}

// hostedSyncBranch resolves the branch a hosted sync step should cover: the
// repo's current branch, else the default branch.
func hostedSyncBranch(ctx context.Context, runner CommandRunner, repoDir string) string {
	if current, err := gitScalar(ctx, runner, repoDir, "branch", "--show-current"); err == nil {
		if branch := strings.TrimSpace(current); branch != "" {
			return branch
		}
	}
	return distillDefaultBranch
}
