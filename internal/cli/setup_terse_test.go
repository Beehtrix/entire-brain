package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestSetupDefaultIsTheShortForm pins the default. The phase view reports what
// the machine did and is still available behind --verbose; what a person gets
// by default is the four-line answer.
func TestSetupDefaultIsTheShortForm(t *testing.T) {
	f := newSetupTestFixture(t)
	rec := &recordedSetup{}
	opts := defaultSetupOptions()
	opts.noDaemon = true
	opts.noBackfill = true

	out := runSetupTerseForTest(t, f, opts, rec)

	if !strings.Contains(out, "Brain turns this repo's history") {
		t.Fatalf("the short form opens by saying what Brain is for:\n%s", out)
	}
	if !strings.Contains(out, "Ready. Try:") {
		t.Fatalf("the short form ends with what to type:\n%s", out)
	}
	// The phase view's vocabulary must be absent, or this is not the short form.
	for _, leaked := range []string{"Detect", "instant core", "FAILED", "Workspace"} {
		if strings.Contains(out, leaked) {
			t.Errorf("phase-view text %q leaked into the default output:\n%s", leaked, out)
		}
	}
	if n := len(strings.Split(strings.TrimSpace(out), "\n")); n > 10 {
		t.Errorf("default output is %d lines; the point is that it is short:\n%s", n, out)
	}
}

// TestSetupVerboseStillReportsPhases keeps the escape hatch honest: --verbose
// has to bring back the thing the default hides, or hiding it is a loss.
func TestSetupVerboseStillReportsPhases(t *testing.T) {
	f := newSetupTestFixture(t)
	rec := &recordedSetup{}
	opts := defaultSetupOptions()
	opts.noDaemon = true
	opts.noBackfill = true

	out := runSetupForTest(t, f, opts, rec) // forces verbose

	if !strings.Contains(out, "instant core") {
		t.Fatalf("--verbose must still report the phases:\n%s", out)
	}
}

// TestSetupSpendDisclosureSurvivesTheShortForm is the one that matters.
//
// The cost line and the consent prompt are NOT progress rows: suppressing the
// phase view must not suppress them, or setup spends the reader's money with
// nothing on screen to object to. This asserts the disclosure is present at
// the instant the child is spawned, in the DEFAULT mode.
func TestSetupSpendDisclosureSurvivesTheShortForm(t *testing.T) {
	f := newSetupTestFixture(t, "s1")
	rec := &recordedSetup{agent: "codex"}
	steps := rec.steps(f)

	out := &bytes.Buffer{}
	var atSpawn string
	inner := steps.spawnBackfill
	steps.spawnBackfill = func(ctx context.Context, plan setupBackfillPlan) (int, error) {
		atSpawn = out.String()
		return inner(ctx, plan)
	}

	opts := defaultSetupOptions()
	opts.yes = true // consent by flag, so there is no prompt to carry the cost
	opts.noDaemon = true
	opts.verbose = false // the default, which discards the phase rows

	if err := runSetup(context.Background(), setupTestCommand(t, out, opts), f.opts, opts, f.repoDir, steps); err != nil {
		t.Fatalf("runSetup: %v", err)
	}
	if !strings.Contains(atSpawn, "SPEND TOKENS") {
		t.Fatalf("the reader was not told about the spend before it started:\n%s", atSpawn)
	}
	if !strings.Contains(atSpawn, "--no-backfill") {
		t.Fatalf("the spend disclosure must name the way out:\n%s", atSpawn)
	}
}
