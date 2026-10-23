package cli

import (
	"fmt"
	"strings"
	"testing"
)

// An installed Graph that predates the read-only store contract is a designed
// optional state (docs/graph-owned-store.md): refresh forgives it, every other
// brain source builds, and the only remedy is the Graph upgrade. These tests
// pin the two reports of the one defect that motivated them: setup recorded
// the state as "failed", and the status verdict then paired "semantic failed"
// with the dirty-tree remedy — a command that cannot build a store the
// installed Graph does not produce.

func TestGraphStoreUnsupportedIsSkippedNotFailed(t *testing.T) {
	t.Parallel()
	for name, err := range map[string]error{
		"store command absent": errGraphStoreUnsupported,
		"contract older":       fmt.Errorf("probe: %w (Graph reports store contract 0, this build requires 1)", errGraphStoreContractOlder),
	} {
		component := newSetupComponent(brainComponentSemantic, err, "entire-brain")
		if component.State != "skipped" {
			t.Errorf("%s: recorded %q, want \"skipped\" — a designed optional state is not a failure", name, component.State)
		}
		if !strings.Contains(component.Hint, "entire plugin install graph") {
			t.Errorf("%s: hint does not name the one remedy (the Graph upgrade): %q", name, component.Hint)
		}
		// status re-derives the state from the PERSISTED detail, where the
		// error value is gone; if the matcher cannot, the verdict falls back
		// to the dirty-tree remedy this state exists to outrank.
		if !setupIsGraphStoreUnsupported(component.Detail) {
			t.Errorf("%s: setupIsGraphStoreUnsupported does not match the recorded detail %q", name, component.Detail)
		}
	}

	// The other direction is NOT optional: a store contract newer than this
	// build means entire-brain is behind, and recording that as a skip would
	// silence a brain that needs upgrading.
	newer := fmt.Errorf("probe: %w (Graph reports store contract 2, this build supports 1)", errGraphStoreContractNewer)
	component := newSetupComponent(brainComponentSemantic, newer, "entire-brain")
	if component.State != "failed" {
		t.Errorf("a newer store contract recorded %q, want \"failed\" — this build is the one that is behind", component.State)
	}
	if setupIsGraphStoreUnsupported(component.Detail) {
		t.Errorf("the upgrade-Graph matcher must not claim a detail whose remedy is upgrading entire-brain: %q", component.Detail)
	}
}

func TestGraphStoreUnsupportedOutranksTheDirtyTreeRemedy(t *testing.T) {
	t.Parallel()
	health := statusHealth{SemanticMissing: true, SemanticStoreUnsupported: true, DirtyWorktree: true}
	got := health.nextStepMessage("entire-brain")
	if got != setupGraphStoreUnsupportedHint("entire-brain") {
		t.Errorf("verdict remedy:\n got  %s\n want %s", got, setupGraphStoreUnsupportedHint("entire-brain"))
	}
	if strings.Contains(got, "commit or stash") {
		t.Errorf("committing cannot build a store the installed Graph does not produce: %s", got)
	}
}

// A skipped source answers queries exactly as well as a failed one: not at
// all. It must not count as evidence the brain is queryable, and the phase
// clause must name it among the missing sources when nothing else can answer.
func TestASkippedSourceIsNotEvidenceOfQueryability(t *testing.T) {
	t.Parallel()
	skippedSemantic := newSetupComponent(brainComponentSemantic, errGraphStoreUnsupported, "entire-brain")
	failedSeed := setupComponent{Name: brainComponentSeed, State: "failed", Detail: "refused"}
	failedHistory := setupComponent{Name: brainComponentHistory, State: "failed", Detail: "refused"}
	if setupBrainQueryable([]setupComponent{skippedSemantic, failedSeed, failedHistory}) {
		t.Fatal("every content source is failed or skipped; a `setup && query` script would proceed against nothing")
	}
	builtSeed := setupComponent{Name: brainComponentSeed, State: "ok"}
	if !setupBrainQueryable([]setupComponent{skippedSemantic, builtSeed}) {
		t.Fatal("a built seed still answers; a skipped semantic must not fail the brain")
	}
	if labels := setupUnqueryableCore([]setupComponent{skippedSemantic, failedSeed}); len(labels) != 2 {
		t.Fatalf("the phase clause must name both missing retrieval-core sources, got %v", labels)
	}
	if labels := setupUnqueryableCore([]setupComponent{skippedSemantic, builtSeed}); labels != nil {
		t.Fatalf("a built seed keeps the brain queryable, got %v", labels)
	}
}

// The live `graph store` descriptor status fetches carries no GeneratedAt, so
// the timestamp-based clear would read its zero time as "older than the
// record" and pin a recorded skip forever. Its presence is itself current
// proof the store answers — after the hinted Graph upgrade, the row clears.
func TestASkipClearsWhenTheLiveStoreAnswers(t *testing.T) {
	t.Parallel()
	recorded := newSetupComponent(brainComponentSemantic, errGraphStoreUnsupported, "entire-brain")
	manifest := &exportManifest{Sources: &brainSources{Semantic: &semanticSourceManifest{}}}
	components := instantPhaseComponents(manifest, setupInstantRecord{
		UpdatedAt:  setupTestNow,
		Components: []setupComponent{recorded},
	}, nil)
	for _, component := range components {
		if component.Name == brainComponentSemantic && component.State != "built" {
			t.Fatalf("a live store descriptor is current proof the store answers; the skip must clear: %+v", component)
		}
	}
}

// The status row keeps a skipped component's reason and hint. Presence-based
// reporting alone would render "missing" and drop both, leaving the glyph with
// nothing to explain it.
func TestStatusKeepsASkippedComponentsReasonAndHint(t *testing.T) {
	t.Parallel()
	recorded := newSetupComponent(brainComponentSemantic, errGraphStoreUnsupported, "entire-brain")
	components := instantPhaseComponents(nil, setupInstantRecord{
		UpdatedAt:  setupTestNow,
		Components: []setupComponent{recorded},
	}, nil)
	for _, component := range components {
		if component.Name != brainComponentSemantic {
			continue
		}
		if component.State != "skipped" || component.Detail == "" || component.Hint == "" {
			t.Fatalf("status dropped the skip's reason or remedy: %+v", component)
		}
		return
	}
	t.Fatal("status reported no semantic component at all")
}

// Both recorded shapes set the flag: new records say "skipped", and a record
// written by an older build still says "failed" for the same condition.
func TestBuildStatusHealthDerivesStoreUnsupportedFromThePersistedDetail(t *testing.T) {
	t.Parallel()
	for name, state := range map[string]string{"skipped record": "skipped", "legacy failed record": "failed"} {
		report := brainStatusReport{
			Live: brainLiveState{Dirty: true},
			Onboarding: &brainStatusOnboarding{Components: []brainStatusComponent{
				{Name: brainComponentSemantic, State: state, Detail: errGraphStoreUnsupported.Error()},
			}},
		}
		health := buildStatusHealth(report)
		if !health.SemanticStoreUnsupported {
			t.Errorf("%s: SemanticStoreUnsupported not derived from the component detail", name)
		}
		if state == "skipped" && len(health.Failed) != 0 {
			t.Errorf("%s: a skipped component counted into Failed: %v", name, health.Failed)
		}
	}
}
