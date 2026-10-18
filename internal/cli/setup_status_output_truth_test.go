package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/entireio/entire-brain/internal/setupui"
	"io"
	"strings"
	"testing"

	"github.com/entireio/entire-brain/internal/tui"
)

// setup_status_output_truth_test.go pins four things a FIRST-TIME user reads on
// screen and was misled by: the inner refresh's lines leaking into setup's own
// (UX-24), a readiness claim a brain with no index and no baseline cannot back
// (UX-25), a status row that named failures without saying why (UX-26), and two
// contradictory remedies for one dirty worktree (UX-27).

// --- UX-24: the nested refresh is run, not read -----------------------------

// TestSetupInstantSubcommandAlwaysDiscardsItsOutput: only --json used to
// discard the nested deterministic refresh, so a human first run printed
// setup's renderer AND the inner refresh's on top of it -- its own consent
// text, its own component names, its own daemon identity.
//
// Pointing either writer back at a real stream still compiles, and fails here.
func TestSetupInstantSubcommandAlwaysDiscardsItsOutput(t *testing.T) {
	t.Parallel()
	sub, captured := newSetupInstantCommand(context.Background())
	// Writer IDENTITY for stdout: a buffer this test owns is not wired to
	// anything the sub-command could reach, so it would pass whatever the
	// writer was. io.Discard is the contract, and pointing it at a real stream
	// still compiles.
	if got := sub.OutOrStdout(); got != io.Discard {
		t.Fatalf("the nested refresh must write stdout to io.Discard, got %T", got)
	}
	// stderr is CAPTURED, not discarded: the same stream carries real warnings
	// (a coordinator that could not launch, a skipped entity index), and
	// discarding it lost them silently. It must reach the buffer the caller
	// reads, and must NOT reach a real stream.
	if captured == nil {
		t.Fatal("the nested refresh's stderr must be captured so its warnings can be re-emitted")
	}
	if got := sub.ErrOrStderr(); got != io.Writer(captured) {
		t.Fatalf("the nested refresh's stderr must be the captured buffer, got %T", got)
	}
	sub.ErrOrStderr().Write([]byte("warning: memory coordinator: worker launch failed\n"))
	if got := setupWarningsFrom(captured); len(got) != 1 || got[0] != "memory coordinator: worker launch failed" {
		t.Fatalf("a warning on the nested stderr must be recoverable, got %q", got)
	}
	if got := setupWarningsFrom(captured); len(got) == 0 {
		t.Fatal("non-vacuity: the extractor matched nothing")
	}
	if sub.Context() == nil {
		t.Fatal("the sub-command must carry the setup context")
	}
}

// TestSetupAndStatusNeverPrintTheHashedDaemonID: a first run printed
// "background watcher installed (io.entire.brain-watch.1a2b3c4d)" and `status`
// printed "running (io.entire.brain-watch.1a2b3c4d)". The hash identifies
// nothing a reader can open, choose or pass to a command. It stays in --json;
// it does not belong in prose. The unit PATH, printed once before the install,
// is the identity a person can use.
func TestSetupAndStatusNeverPrintTheHashedDaemonID(t *testing.T) {
	f := newSetupTestFixture(t)
	rec := &recordedSetup{}
	printed := runSetupForTest(t, f, defaultSetupOptions(), rec)
	label := rec.state.Label
	if label == "" {
		t.Fatal("fixture installed no labelled daemon")
	}
	for _, line := range strings.Split(printed, "\n") {
		if strings.Contains(line, label) && !strings.Contains(line, rec.state.UnitPath) {
			t.Fatalf("setup surfaced the hashed daemon id outside the unit path: %q", line)
		}
	}
	// The same claim from the other command a user is told to run.
	report := statusFixtureReport(t, 0)
	report.Onboarding.Daemon = daemonState{Manager: daemonManagerLaunchd, Label: label, Installed: true, Current: true, Running: true}
	if got := renderStatusShortPlain(t, report); strings.Contains(got, label) {
		t.Fatalf("status surfaced the hashed daemon id:\n%s", got)
	}
}

// One component, one name, in whatever shape the build line takes.
//
// The original asserted a per-component TICK ROW. #401 removed that row on
// purpose -- its point is that the nine component names were rendered three
// times in one run and nobody acted on any of them, so the healthy case is a
// COUNT and only failures are named. The naming rule survives the change and
// is what this now checks: every component this line does name, it names with
// setupComponentLabel, the same spelling every other line uses.
func TestSetupSummaryCallsEachComponentByOneName(t *testing.T) {
	t.Parallel()
	report := setupRenderFixtureReport()
	built, failed := partitionSetupComponents(report.Instant.Components)
	line := setupComponentCountSummary(built, failed)

	if len(failed) == 0 {
		t.Fatal("fixture has no failing component, so this guard would check nothing")
	}
	for _, component := range failed {
		want := setupComponentLabel(component.Name)
		if !strings.Contains(line, want) {
			t.Errorf("the build line does not name failed component %q as %q: %q", component.Name, want, line)
		}
		if component.Name != want && strings.Contains(line, component.Name) {
			t.Errorf("the build line uses the raw id %q as well as the label %q: %q", component.Name, want, line)
		}
	}
	// A component that BUILT is counted, never listed: that is the change.
	for _, component := range built {
		if strings.Contains(line, setupComponentLabel(component.Name)) {
			t.Errorf("a healthy component is named instead of counted: %q", line)
		}
	}
}

// --- UX-25: do not claim queryability this brain does not have --------------

// unqueryableReport is a brain whose code index and baseline BOTH failed: one
// component survived, so setup exits 0 and the old summary said "Brain ready
// (degraded)" over a brain that cannot answer anything about this repository.
func unqueryableReport() setupReport {
	report := setupRenderFixtureReport()
	report.Instant.Components = []setupComponent{
		{Name: brainComponentSessions, State: "ok"},
		{Name: brainComponentSeed, State: "failed", Detail: "dirty_worktree: refusing to seed uncommitted content"},
		{Name: brainComponentSemantic, State: "failed", Detail: "dirty_worktree: refusing to index uncommitted content"},
	}
	return report
}

// A brain that cannot answer must not be called ready.
//
// The verdict moved: #401 settles it on the Ready phase, through
// setupui.Summary.Verdict, rather than in a closing block of its own. The
// rule did not move, so this asks the verdict directly instead of grepping a
// rendered page for the word.
func TestSetupSummaryDoesNotCallAnUnqueryableBrainReady(t *testing.T) {
	t.Parallel()
	report := unqueryableReport()
	if setupBrainQueryable(report.Instant.Components) {
		t.Fatal("fixture is queryable, so this guard would pass on anything")
	}
	theme, _ := tui.ThemeByName("default")
	rend := setupui.NewRendererWidth(tui.NewRendererWith(theme, tui.PlainCaps()), 200)
	verdict := setupui.Summary{RepoPath: report.Repo, BrainPath: report.BrainPath, Queryable: false}.Verdict(rend)

	if strings.Contains(strings.ToLower(verdict), "ready") {
		t.Fatalf("the verdict calls an unqueryable brain ready: %q", verdict)
	}
	// And it says what is missing, not merely that something is.
	built, failed := partitionSetupComponents(report.Instant.Components)
	line := setupComponentCountSummary(built, failed)
	for _, component := range failed {
		if !strings.Contains(line, setupComponentLabel(component.Name)) {
			t.Errorf("the build line does not name %q among the failures: %q", component.Name, line)
		}
	}
}

// One core source down is still a usable brain, and must still read as ready.
//
// Same move as the test above: the verdict is the Ready phase's now.
func TestSetupSummaryStillSaysReadyWhenOnlyOneCoreComponentFailed(t *testing.T) {
	t.Parallel()
	for _, down := range []string{brainComponentSemantic, brainComponentSeed} {
		report := setupRenderFixtureReport()
		for i := range report.Instant.Components {
			if report.Instant.Components[i].Name == down {
				report.Instant.Components[i].State = "failed"
			}
		}
		if !setupBrainQueryable(report.Instant.Components) {
			t.Fatalf("%s down should leave the brain queryable; fixture says otherwise", down)
		}
		theme, _ := tui.ThemeByName("default")
		rend := setupui.NewRendererWidth(tui.NewRendererWith(theme, tui.PlainCaps()), 200)
		verdict := setupui.Summary{RepoPath: report.Repo, BrainPath: report.BrainPath, Queryable: true}.Verdict(rend)
		if !strings.Contains(strings.ToLower(verdict), "queryable") && !strings.Contains(strings.ToLower(verdict), "ready") {
			t.Errorf("with only %s down the verdict should still read as usable: %q", down, verdict)
		}
	}
}

// TestSetupSummaryUnchangedWhenNoComponentsWereReported keeps the pre-component
// contract: a phase that reports nothing built fine, and must not be accused.
func TestSetupSummaryUnchangedWhenNoComponentsWereReported(t *testing.T) {
	t.Parallel()
	report := setupRenderFixtureReport()
	report.Instant = setupPhase{State: "ok"}
	got := renderSetupSummaryPlain(t, report, nil)
	if strings.Contains(got, "not queryable") {
		t.Fatalf("no components reported is not evidence of failure:\n%s", got)
	}
}

// TestSetupExitsZeroButSaysWhatIsUnavailable: the words are the point.
//
// The NAME is now half wrong and is kept so the history is findable. The exit
// code is no longer unconditionally zero: the stack settled that it follows
// queryability, and with both the seed baseline and the semantic index down
// there is nothing for a query to read. What this test is actually for -- that
// the run says WHICH sources are unavailable rather than only that something
// is -- is unchanged, and is asserted below against the error and the output
// together.
func TestSetupExitsZeroButSaysWhatIsUnavailable(t *testing.T) {
	f := newSetupTestFixture(t, "s1")
	rec := &recordedSetup{instantComponents: []setupComponent{
		{Name: brainComponentSessions, State: "ok"},
		{Name: brainComponentSeed, State: "failed", Detail: "dirty_worktree: refusing to seed uncommitted content"},
		{Name: brainComponentSemantic, State: "failed", Detail: "dirty_worktree: refusing to index uncommitted content"},
	}}
	opts := defaultSetupOptions()
	opts.noDaemon = true
	opts.noBackfill = true

	out := &bytes.Buffer{}
	err := runSetup(context.Background(), setupTestCommand(t, out, opts), f.opts, opts, f.repoDir, rec.steps(f))
	if err == nil {
		t.Fatal("with no index and no baseline a query has nothing to read; the exit code follows queryability")
	}
	// The refusal names the sources, not just the state.
	for _, want := range []string{"seed baseline", "semantic index"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	printed := out.String()
	if strings.Contains(printed, "the brain is queryable now") {
		t.Fatalf("setup claimed queryability with no index and no baseline:\n%s", printed)
	}

	// The full phrasing lives on the BUILD line, which is the verbose report:
	// #401 made the short form the default, and it names missing capabilities
	// in a reader's words ("semantic search") rather than component ids. Both
	// are right for their surface, so this asks the surface that carries the
	// sentence it is about.
	verbose := opts
	verbose.verbose = true
	vout := &bytes.Buffer{}
	rec2 := &recordedSetup{instantComponents: rec.instantComponents}
	f2 := newSetupTestFixture(t, "s1")
	if err := runSetup(context.Background(), setupTestCommand(t, vout, verbose), f2.opts, verbose, f2.repoDir, rec2.steps(f2)); err == nil {
		t.Fatal("the verbose run must refuse for the same reason")
	}
	if !strings.Contains(vout.String(), "no semantic index and no seed baseline") {
		t.Fatalf("the build line must name what is unavailable:\n%s", vout)
	}
}

// --- UX-26: status holds the reason, so status prints it --------------------

func renderStatusShortPlain(t *testing.T, report brainStatusReport) string {
	t.Helper()
	out := &bytes.Buffer{}
	theme, _ := tui.ThemeByName("default")
	renderBrainStatusShort(out, tui.NewRendererWith(theme, tui.PlainCaps()), report, setupBrainBinaryName)
	return out.String()
}

func TestStatusPrintsWhyEachComponentFailed(t *testing.T) {
	t.Parallel()
	report := statusFixtureReport(t, 0)
	report.Onboarding.Components = []brainStatusComponent{
		{Name: "sessions", State: "built"},
		{Name: "semantic", State: "failed", Detail: "repo key mismatch: snapshot repo_key gh/other/repo does not match current repo", Hint: "rebuild it with `entire-brain refresh index --force`"},
		{Name: "history", State: "missing"},
	}
	got := renderStatusShortPlain(t, report)
	if !strings.Contains(got, "snapshot repo_key gh/other/repo does not match current repo") {
		t.Fatalf("status holds the reason and must print it:\n%s", got)
	}
	if !strings.Contains(got, "hint: rebuild it with `entire-brain refresh index --force`") {
		t.Fatalf("status holds the hint and must print it:\n%s", got)
	}
	// Over-application guard: a component that merely was never built has no
	// reason to report, and must not grow a line.
	if strings.Contains(got, "history:") {
		t.Fatalf("a missing component is not a failure report:\n%s", got)
	}
}

// TestStatusSaysSoWhenAFailureHasNoRecordedReason: silence under a failed
// component would read as "nothing to explain".
func TestStatusSaysSoWhenAFailureHasNoRecordedReason(t *testing.T) {
	t.Parallel()
	report := statusFixtureReport(t, 0)
	report.Onboarding.Components = []brainStatusComponent{{Name: "entities", State: "failed"}}
	got := renderStatusShortPlain(t, report)
	if !strings.Contains(got, "entities: no reason recorded") {
		t.Fatalf("a reasonless failure must say so:\n%s", got)
	}
}

// --- UX-27: one remedy for one dirty worktree -------------------------------

// dirtySeedRetrievalStatus runs the REAL producer, so a remedy put back into
// the freshness axis is caught here rather than in a hand-written fixture.
func dirtySeedRetrievalStatus(t *testing.T) *brainStatusRetrieval {
	t.Helper()
	repoDir := t.TempDir()
	runner := &fakeCommandRunner{responses: map[string]fakeCommandResponse{}}
	live := brainLiveState{Branch: "main", Head: "58b0c54e", Dirty: true}
	manifest := &exportManifest{Sources: &brainSources{
		Seed: &seedSourceManifest{Commit: "58b0c54e", WorktreeMode: "commit", GeneratedAt: setupTestNow},
	}}
	// audit=false is the lightweight status path, which is the surface this
	// test is about; the audit path is doctor's.
	return buildBrainRetrievalStatus(context.Background(), runner, t.TempDir(), repoDir, manifest, live, false)
}

func TestDirtyWorktreeCauseCarriesNoRemedyOfItsOwn(t *testing.T) {
	t.Parallel()
	status := dirtySeedRetrievalStatus(t)
	axis := status.Freshness.Axes["seed"]
	if axis.State != "dirty-unindexed" {
		t.Fatalf("fixture did not reach the dirty-unindexed axis: %+v", axis)
	}
	if strings.Contains(axis.Detail, "--worktree") || strings.Contains(axis.Detail, "refresh") {
		t.Fatalf("the cause must not offer a remedy; setupDirtyWorktreeHint owns it: %q", axis.Detail)
	}
	if !strings.Contains(axis.Detail, "committed HEAD") {
		t.Fatalf("the cause must still say what is wrong: %q", axis.Detail)
	}
}

func TestDirtyWorktreeGetsExactlyOneRemedy(t *testing.T) {
	t.Parallel()
	report := statusFixtureReport(t, 0)
	report.Live.Dirty = true
	report.Semantic.Freshness = &staleReport{Severity: "ok", Axes: map[string]staleAxis{"store": {State: "ok"}}}
	// ONLY the seed axis. The producer's fixture also raises docs=missing, and
	// the two details together overrun statusCauseWidth -- which silently
	// truncated the very text this test is about, so the first version of it
	// could not fail. The axis itself still comes from the producer, so a
	// remedy put back into it is still caught here.
	seed := dirtySeedRetrievalStatus(t).Freshness.Axes["seed"]
	report.Retrieval = &brainStatusRetrieval{Freshness: &staleReport{
		Severity: "degraded",
		Axes:     map[string]staleAxis{"seed": seed},
	}}

	got := renderStatusShortPlain(t, report)
	remedy := setupDirtyWorktreeHint(setupBrainBinaryName)
	if !strings.Contains(got, remedy) {
		t.Fatalf("the verdict must carry the one remedy resolver's answer:\n%s", got)
	}
	// The resolver names --worktree exactly once. A second mention in the cause
	// underneath is the contradiction this guards against.
	if want, got2 := strings.Count(remedy, "--worktree"), strings.Count(got, "--worktree"); got2 != want {
		t.Fatalf("a dirty tree must be answered once, found %d mentions of --worktree (resolver has %d):\n%s", got2, want, got)
	}
	if strings.Contains(got, "refresh with --worktree to include current changes") {
		t.Fatalf("the cause is still offering its own, contradictory remedy:\n%s", got)
	}
	// Non-vacuity: the cause line must actually be on screen, or the count
	// above is counting nothing.
	if !strings.Contains(got, "seed=dirty-unindexed") || !strings.Contains(got, seed.Detail) {
		t.Fatalf("the cause never reached the output, so nothing above was tested:\n%s", got)
	}
}

// TestStatusStatesTheDirtyRemedyExactlyOnce is UX-26 and UX-27 together. A
// dirty worktree refuses BOTH seed and semantic, so printing each failed
// component's hint put the same sentence on screen twice -- and the verdict
// underneath printed it a third time. Saying what failed and why must not
// become saying one remedy three times.
func TestStatusStatesTheDirtyRemedyExactlyOnce(t *testing.T) {
	t.Parallel()
	remedy := setupDirtyWorktreeHint(setupBrainBinaryName)
	report := statusFixtureReport(t, 0)
	report.Live.Dirty = true
	report.Semantic.Freshness = &staleReport{Severity: "ok", Axes: map[string]staleAxis{"store": {State: "ok"}}}
	seed := dirtySeedRetrievalStatus(t).Freshness.Axes["seed"]
	report.Retrieval = &brainStatusRetrieval{Freshness: &staleReport{
		Severity: "degraded",
		Axes:     map[string]staleAxis{"seed": seed},
	}}
	report.Onboarding.Components = []brainStatusComponent{
		{Name: "seed", State: "failed", Detail: "dirty_worktree: refusing to seed uncommitted content without --worktree", Hint: remedy},
		{Name: "semantic", State: "failed", Detail: "dirty_worktree: refusing to index uncommitted content without --worktree", Hint: remedy},
	}

	got := renderStatusShortPlain(t, report)
	if n := strings.Count(got, remedy); n != 1 {
		t.Fatalf("one remedy, one appearance; found %d:\n%s", n, got)
	}
	// Non-vacuity: both failures are still reported, with their own reasons.
	for _, want := range []string{"refusing to seed uncommitted content", "refusing to index uncommitted content"} {
		if !strings.Contains(got, want) {
			t.Fatalf("deduping the remedy must not drop a failure reason (%q):\n%s", want, got)
		}
	}
}

// TestDirtyWorktreeRemedyNamesBothWaysOut: the resolver is the only place the
// reader is told what to do, so it has to name both -- the one that re-runs
// setup on a clean tree and the one that indexes the tree as it is.
func TestDirtyWorktreeRemedyNamesBothWaysOut(t *testing.T) {
	t.Parallel()
	remedy := setupDirtyWorktreeHint(setupBrainBinaryName)
	for _, want := range []string{"commit or stash", setupBrainBinaryName + " setup", "refresh --worktree", "entire enable"} {
		if !strings.Contains(remedy, want) {
			t.Fatalf("the single remedy must name %q: %s", want, remedy)
		}
	}
}

// Moving the remedy out of axis.Detail (so the short status would not print it
// twice) left `status --verbose` showing the cause with no way out: it renders
// Detail directly and never re-pairs the hint. Raised as a finding on this PR,
// and correct.
func TestVerboseAxesStillNameTheWayOut(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	// The remedy now travels ON the axis, so the fixture carries it the way
	// a producer does. Rendering it from the state alone is what left the
	// JSON report -- which is what an agent and every script read -- holding
	// a cause with no next step.
	renderFreshnessAxes(&buf, map[string]staleAxis{
		"seed": {
			State:  dirtyUnindexedAxisState,
			Detail: "seed and docs are based on committed HEAD",
			Remedy: setupDirtyWorktreeHint("entire brain"),
		},
	})
	line := buf.String()

	if !strings.Contains(line, "dirty-unindexed") {
		t.Fatalf("fixture did not render the axis under test: %q", line)
	}
	for _, want := range []string{"--worktree", "commit or stash"} {
		if !strings.Contains(line, want) {
			t.Errorf("the verbose axis states a cause with no remedy; missing %q in: %s", want, line)
		}
	}
}

// A healthy axis must not gain a dirty-worktree remedy it has no use for.
func TestACleanAxisGetsNoDirtyRemedy(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	renderFreshnessAxes(&buf, map[string]staleAxis{"seed": {State: "current"}})
	if strings.Contains(buf.String(), "--worktree") {
		t.Fatalf("a clean axis was given the dirty-worktree remedy: %s", buf.String())
	}
}

// A dirty working tree fails BOTH seed and semantic — the common first run this
// release's own docs describe — and each failed component carries the identical
// hint, so the reader was told the same thing twice within six lines.
// status_render.go was given a dedup guard in this change; setup's own summary
// was missed. Raised as a finding on this PR, and correct.
func TestTheSetupSummaryStatesOneRemedyOnce(t *testing.T) {
	t.Parallel()
	hint := setupDirtyWorktreeHint("entire brain")
	report := setupReport{
		Instant: setupPhase{Components: []setupComponent{
			{Name: "seed", State: "failed", Detail: "dirty_worktree: refusing to seed", Hint: hint},
			{Name: "semantic", State: "failed", Detail: "dirty_worktree: refusing to index", Hint: hint},
		}},
	}
	out := renderSetupSummaryPlain(t, report, &setupTimings{})

	if !strings.Contains(out, "refusing to seed") || !strings.Contains(out, "refusing to index") {
		t.Fatalf("both failures must still be named, so this guard is not vacuous:\n%s", out)
	}
	if n := strings.Count(out, "hint: "); n != 1 {
		t.Fatalf("one remedy, one appearance; printed %d times:\n%s", n, out)
	}
}

// Two DIFFERENT remedies must both still be shown — dedup is not suppression.
func TestTwoDifferentRemediesAreBothShown(t *testing.T) {
	t.Parallel()
	report := setupReport{
		Instant: setupPhase{Components: []setupComponent{
			{Name: "seed", State: "failed", Detail: "a", Hint: "first remedy"},
			{Name: "semantic", State: "failed", Detail: "b", Hint: "second remedy"},
		}},
	}
	out := renderSetupSummaryPlain(t, report, &setupTimings{})
	if n := strings.Count(out, "hint: "); n != 2 {
		t.Fatalf("distinct remedies must both appear, got %d:\n%s", n, out)
	}
}

// A Go error is routinely multi-line: errors.Join concatenates its causes with
// a newline, and the memory path joins a deterministic failure with a run
// failure before it reaches a `warning: %v` line. Keeping only the lines that
// carry the prefix dropped every cause after the first — the same information
// loss this re-emission exists to repair, one layer down. Raised as a finding
// on this PR, and correct.
func TestAMultiLineWarningKeepsItsCauses(t *testing.T) {
	t.Parallel()

	got := setupWarningsFrom(bytes.NewBufferString(
		"warning: memory coordinator could not launch: exec failed\npermission denied\nno such file\n"))
	if len(got) != 1 {
		t.Fatalf("expected one warning carrying its causes, got %d: %q", len(got), got)
	}
	for _, cause := range []string{"exec failed", "permission denied", "no such file"} {
		if !strings.Contains(got[0], cause) {
			t.Errorf("the re-emitted warning lost a cause %q: %s", cause, got[0])
		}
	}

	// A continuation ends at a blank line, so unrelated stderr written after a
	// warning is not swept into it.
	sep := setupWarningsFrom(bytes.NewBufferString(
		"warning: first: a\nb\n\nunrelated progress noise\nwarning: second\n"))
	if len(sep) != 2 {
		t.Fatalf("expected two warnings, got %d: %q", len(sep), sep)
	}
	if strings.Contains(sep[0], "unrelated progress noise") {
		t.Errorf("stderr after a blank line was swept into the warning: %s", sep[0])
	}
	if sep[1] != "second" {
		t.Errorf("the second warning was altered: %q", sep[1])
	}
}

// TestEveryDirtyUnindexedAxisShipsItsRemedy closes the gap the renderer-only
// fix left: an agent reading `status --json` got the diagnosis and no way
// out, because only the text surface re-attached the hint.
//
// It walks the PRODUCERS, so adding a third dirty-unindexed axis without a
// remedy fails here rather than silently reaching the JSON consumers.
func TestEveryDirtyUnindexedAxisShipsItsRemedy(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"semantic.go", "agent_surface.go"} {
		src := readGoSourceForTest(t, file)
		rest := src
		for {
			i := strings.Index(rest, "State:  dirtyUnindexedAxisState")
			if i < 0 {
				i = strings.Index(rest, "State:   dirtyUnindexedAxisState")
			}
			if i < 0 {
				break
			}
			// The literal ends at the first line that closes it.
			end := strings.Index(rest[i:], "\n\t\t\t}")
			if end < 0 {
				end = strings.Index(rest[i:], "\n\t\t}")
			}
			if end < 0 {
				end = len(rest) - i
			}
			literal := rest[i : i+end]
			if !strings.Contains(literal, "Remedy:") {
				t.Errorf("%s: a dirty-unindexed axis ships with no remedy, so `status --json` states a cause and no next step:\n%s", file, literal)
			}
			rest = rest[i+end:]
		}
	}
}

// And the JSON side specifically: the field must survive encoding, because
// that is the surface the finding was about.
func TestTheRemedyReachesTheJSONReport(t *testing.T) {
	t.Parallel()
	axis := staleAxis{
		State:  dirtyUnindexedAxisState,
		Detail: "semantic index is based on committed HEAD",
		Remedy: setupDirtyWorktreeHint("entire brain"),
	}
	encoded, err := json.Marshal(axis)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	remedy, _ := back["remedy"].(string)
	if !strings.Contains(remedy, "--worktree") {
		t.Fatalf("an agent reading the report gets no next step; remedy=%q in %s", remedy, encoded)
	}
	// Additive: a clean axis must not grow an empty key.
	clean, err := json.Marshal(staleAxis{State: "current"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(clean), "remedy") {
		t.Fatalf("remedy must be omitempty so existing readers are unaffected: %s", clean)
	}
}

// readGoSourceForTest lives in regression_caps_propagation_test.go. This file
// had its own copy, which collided in the integration: both PRs needed to walk
// package source for a structural guard and each grew the same helper. The
// surviving one also fails on an empty file, which is the stricter contract.

func setupRenderFixtureReport() setupReport {
	return setupReport{
		SchemaVersion: 1,
		GeneratedAt:   setupTestNow,
		Repo:          "/repo",
		RepoKey:       "gh/demo-org/brain-demo",
		BrainPath:     "/brain",
		Instant: setupPhase{
			State:   "degraded",
			Seconds: 0.7,
			Components: []setupComponent{
				{Name: brainComponentSessions, State: "failed", Detail: "checkpoint_scope_incomplete", Hint: "add a git remote"},
				{Name: brainComponentSeed, State: "ok"},
				{Name: brainComponentDocs, State: "ok"},
				{Name: brainComponentSemantic, State: "ok"},
			},
		},
		Backfill:  setupPhase{State: "skipped", Detail: "--no-backfill"},
		Workspace: setupWorkspaceState{Name: "default", Registered: true},
		Daemon:    daemonState{Manager: daemonManagerUnsupported, Detail: "skipped: --no-daemon"},
		Agents: setupAgentsState{
			Installed:   true,
			Files:       []string{".entire/agent-guide.md", "AGENTS.md", "CLAUDE.md"},
			Changed:     []string{".entire/agent-guide.md", "AGENTS.md", "CLAUDE.md"},
			Hook:        "/repo/.claude/settings.json",
			HookChanged: true,
		},
		Facts: factsBackfillStatus{Sessions: 618, Distilled: 300},
	}
}

// renderSetupSummaryPlain composes what a reader now SEES for the properties
// these tests are about.
//
// renderSetupSummary is gone: #401 replaced the closing block with the Build
// and Ready phases. The two things it rendered that these guards assert --
// the component clause and the ready/not-ready verdict -- are still produced,
// by setupComponentCountSummary plus setupUnqueryableCore for the first and
// setupui.Summary.Verdict for the second. This calls those, so the assertions
// stay keyed to production code rather than to a renderer that no longer runs.
func renderSetupSummaryPlain(t *testing.T, report setupReport, timings *setupTimings) string {
	t.Helper()
	out := &bytes.Buffer{}
	theme, _ := tui.ThemeByName("default")
	rend := setupui.NewRendererWidth(tui.NewRendererWith(theme, tui.PlainCaps()), 200)

	built, failed := partitionSetupComponents(report.Instant.Components)
	queryability := "the brain is queryable now"
	if unavailable := setupUnqueryableCore(report.Instant.Components); len(unavailable) > 0 {
		queryability = "no " + strings.Join(unavailable, " and no ") + ", so queries have nothing to read for this repo yet"
	}
	fmt.Fprintf(out, "instant core ready %s %s, %s\n",
		rend.Dash(), setupComponentCountSummary(built, failed), queryability)
	for _, detail := range setupComponentFailureDetails(failed, rend.Dash(), setupBrainBinaryName) {
		fmt.Fprintln(out, detail)
	}

	queryable := setupBrainQueryable(report.Instant.Components)
	summary := setupui.Summary{
		RepoPath:  report.Repo,
		BrainPath: report.BrainPath,
		Queryable: queryable,
	}
	if queryable {
		summary.Next = setupNextSteps(setupBrainBinaryName, report.Daemon.Installed, report.Facts.Pending(), false, report.Agents.Installed)
	}
	fmt.Fprintln(out, summary.Verdict(rend))
	for _, line := range summary.Lines(rend) {
		fmt.Fprintln(out, line.Text)
	}
	return out.String()
}
