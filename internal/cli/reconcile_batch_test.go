package cli

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func stagedTestFact(text string, paths []string, status string, at time.Time) factRecord {
	return factRecord{
		ID:        factRecordID(text, paths),
		Text:      text,
		Paths:     paths,
		Status:    status,
		Branch:    "main",
		Origin:    factOriginDistilled,
		CreatedAt: at,
		UpdatedAt: at,
	}
}

// A candidate resembling nothing stored or staged is new by construction, so
// the pass must promote it without spending an agent call.
func TestStagedReconcilePromotesUnflaggedWithoutAgent(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	facts := []factRecord{
		stagedTestFact("the daemon label derives from the daemon name", []string{"architecture.data.flow"}, factStatusActive, now.Add(-time.Hour)),
		stagedTestFact("tests run with count one to defeat the build cache", []string{"workflow.testing.rules"}, factStatusCandidate, now),
	}
	calls := 0
	run := func(context.Context, string, []string, []byte, time.Duration) (string, error) {
		calls++
		return "", nil
	}
	updated, proposals, stats, warnings := reconcileStagedCandidates(context.Background(), run, nil, facts, ".", time.Minute, 2, 0.75, false, now)
	if calls != 0 {
		t.Fatalf("an unflagged candidate must not cost an agent call, got %d", calls)
	}
	if stats.Candidates != 1 || stats.Promoted != 1 || stats.Clusters != 0 {
		t.Fatalf("stats: %+v", stats)
	}
	if len(proposals) != 0 || len(warnings) != 0 {
		t.Fatalf("unexpected proposals %v or warnings %v", proposals, warnings)
	}
	active := 0
	for _, f := range updated {
		if f.Status == factStatusCandidate {
			t.Fatalf("candidate left staged after the pass: %q", f.Text)
		}
		if f.Status == factStatusActive {
			active++
		}
	}
	if active != 2 {
		t.Fatalf("want 2 active facts, got %d", active)
	}
}

// Two same-run near-duplicates with nothing stored must still be adjudicated:
// the earliest is new by election and serves as EXISTING for the rest, so the
// agent can consolidate them instead of both being silently stored.
func TestStagedReconcileClustersSameRunNearDuplicates(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	first := stagedTestFact("indent all source files with tabs", []string{"preferences.coding.style"}, factStatusCandidate, now.Add(-time.Minute))
	second := stagedTestFact("use tabs to indent source files", []string{"preferences.coding.style"}, factStatusCandidate, now)
	var sawExisting string
	run := func(_ context.Context, _ string, _ []string, input []byte, _ time.Duration) (string, error) {
		sawExisting = string(input)
		return "1 merge 1 0.95", nil
	}
	updated, proposals, stats, _ := reconcileStagedCandidates(context.Background(), run, nil, []factRecord{first, second}, ".", time.Minute, 2, 0.75, false, now)
	if stats.Clusters != 1 || stats.AgentCalls != 1 || stats.Promoted != 0 {
		t.Fatalf("stats: %+v", stats)
	}
	if !strings.Contains(sawExisting, first.Text) {
		t.Fatalf("the elected head must be in EXISTING so the agent can merge into it:\n%s", sawExisting)
	}
	if len(proposals) != 0 {
		t.Fatalf("a confident merge must apply, got proposals %v", proposals)
	}
	var texts []string
	for _, f := range updated {
		if f.Status == factStatusActive {
			texts = append(texts, f.Text)
		}
	}
	if len(texts) != 1 || texts[0] != first.Text {
		t.Fatalf("near-duplicates must consolidate into the earliest phrasing, got %v", texts)
	}
}

// A candidate close to a stored active fact goes to the agent, and a confident
// supersede ruling applies exactly as the inline design applied it.
func TestStagedReconcileSupersedesStoredFact(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	stored := stagedTestFact("indent all source files with tabs", []string{"preferences.coding.style"}, factStatusActive, now.Add(-time.Hour))
	candidate := stagedTestFact("indent all source files with spaces", []string{"preferences.coding.style"}, factStatusCandidate, now)
	run := func(context.Context, string, []string, []byte, time.Duration) (string, error) {
		return "1 supersede 1 0.95", nil
	}
	updated, _, stats, _ := reconcileStagedCandidates(context.Background(), run, nil, []factRecord{stored, candidate}, ".", time.Minute, 2, 0.75, false, now)
	if stats.AgentCalls != 1 {
		t.Fatalf("stats: %+v", stats)
	}
	byID := map[string]factRecord{}
	for _, f := range updated {
		byID[f.ID] = f
	}
	if byID[stored.ID].Status != factStatusSuperseded {
		t.Fatalf("stored fact must be superseded, got %q", byID[stored.ID].Status)
	}
	if byID[candidate.ID].Status != factStatusActive {
		t.Fatalf("candidate must be the active replacement, got %q", byID[candidate.ID].Status)
	}
}

// An agent failure must degrade to new — both facts live, nothing lost — the
// same fallback the inline design had.
func TestStagedReconcileAgentFailureKeepsBoth(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	stored := stagedTestFact("indent all source files with tabs", []string{"preferences.coding.style"}, factStatusActive, now.Add(-time.Hour))
	candidate := stagedTestFact("indent all source files with spaces", []string{"preferences.coding.style"}, factStatusCandidate, now)
	run := func(context.Context, string, []string, []byte, time.Duration) (string, error) {
		return "", errors.New("agent down")
	}
	updated, _, _, warnings := reconcileStagedCandidates(context.Background(), run, nil, []factRecord{stored, candidate}, ".", time.Minute, 2, 0.75, false, now)
	active := 0
	for _, f := range updated {
		if f.Status == factStatusActive {
			active++
		}
	}
	if active != 2 {
		t.Fatalf("agent failure must keep both facts active, got %d", active)
	}
	if len(warnings) == 0 {
		t.Fatal("the degradation must be disclosed")
	}
}

// In newest-first order, extraction order is not evidence of recency: a
// supersede ruling queues for review instead of applying.
func TestStagedReconcileNewestFirstSupersedeQueued(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	stored := stagedTestFact("indent all source files with tabs", []string{"preferences.coding.style"}, factStatusActive, now.Add(-time.Hour))
	candidate := stagedTestFact("indent all source files with spaces", []string{"preferences.coding.style"}, factStatusCandidate, now)
	run := func(context.Context, string, []string, []byte, time.Duration) (string, error) {
		return "1 supersede 1 0.95", nil
	}
	updated, proposals, _, warnings := reconcileStagedCandidates(context.Background(), run, nil, []factRecord{stored, candidate}, ".", time.Minute, 2, 0.75, true, now)
	if len(proposals) != 1 {
		t.Fatalf("newest-first supersede must queue for review, got %v", proposals)
	}
	active := 0
	for _, f := range updated {
		if f.Status == factStatusActive {
			active++
		}
	}
	if active != 2 {
		t.Fatalf("both facts must stay active pending review, got %d", active)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "newest-first supersession requires review") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the review queueing must be disclosed: %v", warnings)
	}
}

// Candidates left staged by a crashed run are reconciled by the next pass even
// when it extracted nothing new — the resume rule that makes staging safe.
func TestStagedReconcileResumesLeftovers(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	leftover := stagedTestFact("exports read checkpoint blobs through one batch child", []string{"architecture.data.flow"}, factStatusCandidate, now.Add(-24*time.Hour))
	run := func(context.Context, string, []string, []byte, time.Duration) (string, error) {
		t.Fatal("nothing resembles the leftover; no agent call expected")
		return "", nil
	}
	updated, _, stats, _ := reconcileStagedCandidates(context.Background(), run, nil, []factRecord{leftover}, ".", time.Minute, 2, 0.75, false, now)
	if stats.Candidates != 1 || stats.Promoted != 1 {
		t.Fatalf("stats: %+v", stats)
	}
	if updated[0].Status != factStatusActive {
		t.Fatalf("leftover must promote, got %q", updated[0].Status)
	}
}

// Concurrent cluster calls must not make the outcome depend on completion
// order: identical inputs produce identical stores, run after run.
func TestStagedReconcileIsDeterministic(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	var facts []factRecord
	for i := 0; i < 6; i++ {
		facts = append(facts, stagedTestFact(
			fmt.Sprintf("cluster alpha duplicate statement number %d about tabs", i),
			[]string{"preferences.coding.style"}, factStatusCandidate, now.Add(time.Duration(i)*time.Second)))
		facts = append(facts, stagedTestFact(
			fmt.Sprintf("cluster beta duplicate statement number %d about exports", i),
			[]string{"architecture.data.flow"}, factStatusCandidate, now.Add(time.Duration(i)*time.Second)))
	}
	run := func(_ context.Context, _ string, _ []string, input []byte, _ time.Duration) (string, error) {
		// Deterministic on input: merge every candidate into existing 1.
		n := 0
		for _, line := range strings.Split(string(input), "\n") {
			if line == "" || line == "CANDIDATES" {
				continue
			}
			if line == "EXISTING" {
				break
			}
			n++
		}
		var out strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&out, "%d merge 1 0.9\n", i)
		}
		return out.String(), nil
	}
	first, _, _, _ := reconcileStagedCandidates(context.Background(), run, nil, append([]factRecord(nil), facts...), ".", time.Minute, 4, 0.75, false, now)
	second, _, _, _ := reconcileStagedCandidates(context.Background(), run, nil, append([]factRecord(nil), facts...), ".", time.Minute, 4, 0.75, false, now)
	sortFactRecords(first)
	sortFactRecords(second)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same input, different stores: concurrent clusters leaked completion order")
	}
}
