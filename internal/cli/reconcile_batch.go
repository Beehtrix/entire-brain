package cli

import (
	"context"
	"sort"
	"sync"
	"time"
)

// The staged-candidate reconcile pass (COR-2184).
//
// Extraction stores its facts with factStatusCandidate and never consults the
// agent; this pass, at the end of the same distill invocation, adjudicates
// every staged candidate on the branch in similarity clusters instead of one
// agent call per chunk. Candidates with no statement lexically close enough
// for merge/supersede to be plausible promote straight to active; the rest
// cluster, and each cluster gets batched agent calls that see all of its
// near-duplicates together. Clusters are disjoint by construction, so their
// agent calls run concurrently; every ruling is applied afterwards in one
// deterministic order, so a --force replay reconstructs the identical store.
//
// The pass operates on ALL candidate-status facts, not just this run's: a run
// that dies after extraction leaves its candidates staged (and therefore
// unpublished — every consumer selects active), and the next invocation
// reconciles them before anything new. Nothing is re-extracted and nothing
// unreconciled ever publishes.

// reconcileFlagThreshold is the factmerge.TextOverlap score at or above which
// a candidate is close enough to another statement that the agent must rule.
// Calibrated against a recorded real-agent run (COR-2184, 2026-10-20): every
// hand-verified genuine merge/supersede pair scored >= 0.24 and every
// hallucinated pairing < 0.20, so this threshold kept 100% of genuine rulings
// while blocking most of the judge's destructive mistakes. Candidates below it
// are promoted as new without an agent call — the safe direction: a missed
// merge is a reviewable duplicate, never a lost fact.
//
// Deliberately NOT part of the distill cache salt: it only gates how NEW
// candidates are adjudicated; sessions already distilled were reconciled under
// whatever rules held then, and their facts are final either way.
const reconcileFlagThreshold = 0.20

// reconcileBatchStats is what the pass reports up to the distill summary, so
// the staging flow is visible rather than looking like missing facts.
type reconcileBatchStats struct {
	Candidates int // staged candidates adjudicated (includes resumed leftovers)
	Promoted   int // auto-promoted to active with no agent call
	Clusters   int // similarity clusters sent to the agent
	AgentCalls int // actual agent round-trips (waves across all clusters)
}

// reconcileStagedCandidates adjudicates the branch's candidate-status facts
// and returns the updated fact set, queued proposals, stats, and warnings.
// A branch with no candidates is returned unchanged at zero cost.
func reconcileStagedCandidates(ctx context.Context, run distillAgentRunner, agentArgs []string, facts []factRecord, repoDir string, timeout time.Duration, concurrency int, confidenceThreshold float64, newestFirst bool, now time.Time) ([]factRecord, []factProposal, reconcileBatchStats, []string) {
	var stats reconcileBatchStats
	rest := make([]factRecord, 0, len(facts))
	var candidates []factRecord
	for _, f := range facts {
		if f.Status == factStatusCandidate {
			// Adjudicated as if freshly extracted: the staged row leaves the
			// store and re-enters through ApplyActions, exactly like the
			// pre-staging flow where candidates were never stored first.
			f.Status = factStatusActive
			candidates = append(candidates, f)
			continue
		}
		rest = append(rest, f)
	}
	stats.Candidates = len(candidates)
	if len(candidates) == 0 {
		return facts, nil, stats, nil
	}
	// Chronological, then by id: the order the serial design adjudicated in,
	// and what makes a --force replay deterministic.
	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].UpdatedAt.Equal(candidates[j].UpdatedAt) {
			return candidates[i].UpdatedAt.Before(candidates[j].UpdatedAt)
		}
		return candidates[i].ID < candidates[j].ID
	})

	flagged, clusters, anchored := clusterFlaggedCandidates(candidates, rest)

	var warnings []string
	applyOne := func(active []factRecord, action factAction, proposals *[]factProposal) []factRecord {
		threshold := confidenceThreshold
		if newestFirst && action.Kind == factActionSupersede {
			// Extraction order is not evidence that a candidate is newer. In
			// reverse-order backfill, preserve both facts for review.
			threshold = 2 // above every valid agent confidence
			warnings = append(warnings, "newest-first supersession requires review; both facts retained (run facts review)")
		}
		if action.Kind == factActionMerge || action.Kind == factActionSupersede {
			// Low lexical overlap is insufficient evidence for automatically
			// discarding a candidate or replacing its target. Preserve both and
			// ask for review, even when the agent reports high confidence.
			// This is a conservative guard, not a semantic correctness test.
			if ti := indexOfFact(active, action.TargetID); ti >= 0 &&
				factTextOverlap(action.Candidate.Text, active[ti].Text) < reconcileFlagThreshold {
				threshold = 2
				warnings = append(warnings, "reconcile: "+action.Kind+" pairs lexically unrelated facts; queued for review instead of applying")
			}
		}
		updated, pending := applyFactActions(active, []factAction{action}, threshold, now)
		*proposals = append(*proposals, pending...)
		return updated
	}

	var proposals []factProposal
	// Unflagged candidates first: nothing stored or staged resembles them, so
	// they are new by the same reasoning as the no-facts-at-paths shortcut.
	for i, c := range candidates {
		if flagged[i] {
			continue
		}
		stats.Promoted++
		rest = applyOne(rest, factAction{Kind: factActionNew, Confidence: 1.0, Candidate: c}, &proposals)
	}

	// Each cluster's agent calls run concurrently (clusters are disjoint);
	// rulings are collected and applied afterwards in cluster order, so the
	// result is independent of completion order.
	type clusterResult struct {
		elected  *factRecord
		actions  []factAction
		warnings []string
		calls    int
	}
	results := make([]clusterResult, len(clusters))
	limit := concurrency
	if limit < 1 {
		limit = 1
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for ci, cluster := range clusters {
		wg.Add(1)
		go func(ci int, cluster []int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			members := make([]factRecord, len(cluster))
			for k, idx := range cluster {
				members[k] = candidates[idx]
			}
			var res clusterResult
			remaining := members
			if !anchored[ci] {
				// A candidate-only cluster: the earliest member is new by
				// election — nothing before it exists to merge into — and
				// holding it out gives the cluster a non-empty EXISTING list so
				// same-run near-duplicates are still adjudicated rather than
				// silently both stored. A cluster anchored to a stored fact
				// adjudicates every member, the stored fact is the EXISTING.
				res.elected = &members[0]
				remaining = members[1:]
			}
			for len(remaining) > 0 {
				wave := remaining
				if len(wave) > maxReconcileExisting {
					wave = wave[:maxReconcileExisting]
				}
				remaining = remaining[len(wave):]
				// EXISTING for this wave: the branch's stored facts plus every
				// cluster member not in the wave — the elected head and any
				// other waves — so the agent can merge within the run.
				// ponytail: later waves do not see earlier waves' RULINGS, only
				// their texts; a merge chain across waves degrades to a
				// reviewable duplicate, never a lost fact. Upgrade path: feed
				// applied outcomes into subsequent waves' active list.
				activeArg := make([]factRecord, 0, len(rest)+len(members)-len(wave))
				activeArg = append(activeArg, rest...)
				inWave := make(map[string]struct{}, len(wave))
				for _, w := range wave {
					inWave[w.ID] = struct{}{}
				}
				for _, m := range members {
					if _, ok := inWave[m.ID]; !ok {
						activeArg = append(activeArg, m)
					}
				}
				called := false
				counting := func(ctx context.Context, dir string, args []string, input []byte, timeout time.Duration) (string, error) {
					called = true
					return run(ctx, dir, args, input, timeout)
				}
				actions, warns := reconcileChunkCandidates(ctx, counting, agentArgs, wave, activeArg, repoDir, timeout)
				if called {
					res.calls++
				}
				res.actions = append(res.actions, actions...)
				res.warnings = append(res.warnings, warns...)
			}
			results[ci] = res
		}(ci, cluster)
	}
	wg.Wait()

	for _, res := range results {
		stats.Clusters++
		stats.AgentCalls += res.calls
		warnings = append(warnings, res.warnings...)
		if res.elected != nil {
			rest = applyOne(rest, factAction{Kind: factActionNew, Confidence: 1.0, Candidate: *res.elected}, &proposals)
		}
		// New rulings land before merges/supersedes so a ruling that targets a
		// same-cluster candidate finds it already active (ApplyActions degrades
		// an unknown target to new, which would store the duplicate instead).
		for _, action := range res.actions {
			if action.Kind == factActionNew {
				rest = applyOne(rest, action, &proposals)
			}
		}
		for _, action := range res.actions {
			if action.Kind != factActionNew {
				rest = applyOne(rest, action, &proposals)
			}
		}
	}
	return rest, proposals, stats, warnings
}

// clusterFlaggedCandidates reports which candidates need the agent, groups
// them into disjoint clusters, and reports per cluster whether any member was
// flagged against a STORED active fact (anchored) rather than only against
// fellow candidates. A candidate is flagged when any statement at a shared
// taxonomy path — stored active fact or fellow candidate — scores at or above
// reconcileFlagThreshold; flagged candidates sharing a near-duplicate relation
// (directly, or through a common stored fact) join one cluster so the agent
// rules on the whole family together.
func clusterFlaggedCandidates(candidates, rest []factRecord) ([]bool, [][]int, []bool) {
	flagged := make([]bool, len(candidates))
	parent := make([]int, len(candidates))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) { parent[find(a)] = find(b) }

	sharePath := func(a, b []string) bool {
		for _, p := range a {
			for _, q := range b {
				if p == q {
					return true
				}
			}
		}
		return false
	}

	// Candidate vs stored active facts: flag, and union candidates flagged
	// against the same stored fact (they will share its EXISTING row).
	activeNeighbor := make([]bool, len(candidates))
	firstByFact := map[string]int{}
	for i, c := range candidates {
		for _, f := range rest {
			if f.Status != factStatusActive || !sharePath(c.Paths, f.Paths) {
				continue
			}
			if factTextOverlap(c.Text, f.Text) < reconcileFlagThreshold {
				continue
			}
			flagged[i] = true
			activeNeighbor[i] = true
			if j, ok := firstByFact[f.ID]; ok {
				union(i, j)
			} else {
				firstByFact[f.ID] = i
			}
		}
	}
	// Candidate vs candidate.
	for i := range candidates {
		for j := i + 1; j < len(candidates); j++ {
			if !sharePath(candidates[i].Paths, candidates[j].Paths) {
				continue
			}
			if factTextOverlap(candidates[i].Text, candidates[j].Text) < reconcileFlagThreshold {
				continue
			}
			flagged[i], flagged[j] = true, true
			union(i, j)
		}
	}
	groups := map[int][]int{}
	for i, isFlagged := range flagged {
		if isFlagged {
			root := find(i)
			groups[root] = append(groups[root], i)
		}
	}
	roots := make([]int, 0, len(groups))
	for root := range groups {
		roots = append(roots, root)
	}
	// Cluster order: by earliest member, so application order is chronological
	// like the serial design's and independent of map iteration.
	sort.Slice(roots, func(a, b int) bool { return groups[roots[a]][0] < groups[roots[b]][0] })
	clusters := make([][]int, 0, len(roots))
	anchored := make([]bool, 0, len(roots))
	for _, root := range roots {
		members := groups[root]
		sort.Ints(members)
		clusters = append(clusters, members)
		hasActive := false
		for _, m := range members {
			if activeNeighbor[m] {
				hasActive = true
				break
			}
		}
		anchored = append(anchored, hasActive)
	}
	return flagged, clusters, anchored
}
