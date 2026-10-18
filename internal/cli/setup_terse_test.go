package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/entireio/entire-brain/internal/setupui"
	"github.com/entireio/entire-brain/internal/tui"
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

// TestDefaultModeStillHasATerminalForConsent is the guard for the worst bug in
// this change set.
//
// The first version suppressed the phase rows by handing newProgress
// io.Discard. io.Discard is not an *os.File, so caps collapsed to PlainCaps,
// the sink reported tty=false, and setupui.Consent took its `case !req.TTY`
// branch — returning "no terminal to ask" WITHOUT EVER PROMPTING. The consent
// gate, which is the entire reason setup asks before it spends, silently
// stopped existing in the default mode, and rebinding the prompt writer did
// nothing because no prompt was ever written.
//
// Capability detection must therefore come from the REAL stream even when the
// rows are thrown away. This asserts the sink's view of the terminal does not
// depend on whether rows are being kept.
func TestDefaultModeStillHasATerminalForConsent(t *testing.T) {
	t.Parallel()
	ttyCaps := tui.Caps{TTY: true, Color: true, Unicode: true, Width: 100}
	render := tui.NewRendererWith(mustTheme(t), ttyCaps)

	for _, quiet := range []bool{false, true} {
		progress := newProgressWithRenderer(&bytes.Buffer{}, "setup", render, newLiveTerminal())
		sink := newSetupSink(progress, quiet)
		if !sink.tty {
			t.Fatalf("quiet=%v: the sink lost the terminal; consent will refuse to prompt", quiet)
		}
		if got := sink.Renderer().Caps().Width; got != ttyCaps.Width {
			t.Errorf("quiet=%v: width came out %d, want the terminal's %d", quiet, got, ttyCaps.Width)
		}
		if !sink.Renderer().Caps().Unicode {
			t.Errorf("quiet=%v: unicode was lost, so the short form renders ASCII on a UTF-8 terminal", quiet)
		}
	}
}

func mustTheme(t *testing.T) tui.Theme {
	t.Helper()
	theme, _ := tui.ThemeByName("default")
	return theme
}

// TestTerseDistillOutcomeReadsThePhaseNotTheCounts pins the second bug: the
// mapper branched on report.Facts.Running and .Agent, neither of which
// factsBackfillStatusForBrain ever sets. Every run with pending sessions
// therefore reported "no agent CLI to distill them" — immediately after the
// same command had printed "agent codex".
func TestTerseDistillOutcomeReadsThePhaseNotTheCounts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		report setupReport
		want   setupui.DistillOutcome
	}{
		{
			name: "spawned",
			report: setupReport{
				Facts:    factsBackfillStatus{Sessions: 274},
				Backfill: setupPhase{State: "ok"},
			},
			want: setupui.DistillStarted,
		},
		{
			name: "attempted and broke",
			report: setupReport{
				Facts:    factsBackfillStatus{Sessions: 1},
				Backfill: setupPhase{State: "failed", Detail: "backfill state not recorded"},
			},
			want: setupui.DistillFailed,
		},
		{
			name: "human declined",
			report: setupReport{
				Facts:    factsBackfillStatus{Sessions: 274},
				Backfill: setupPhase{State: "skipped", consentWhy: setupui.ConsentReasonDeclined},
			},
			want: setupui.DistillDeclined,
		},
		{
			name: "genuinely no agent",
			report: setupReport{
				Facts:    factsBackfillStatus{Sessions: 274},
				Backfill: setupPhase{State: "skipped", consentWhy: setupui.ConsentReasonNoAgent},
			},
			want: setupui.DistillNoAgent,
		},
		{
			name: "no sessions",
			report: setupReport{
				Backfill: setupPhase{State: "skipped", consentWhy: setupui.ConsentReasonNoSessions},
			},
			want: setupui.DistillNoSessions,
		},
		{
			name: "--no-backfill",
			report: setupReport{
				Facts:    factsBackfillStatus{Sessions: 274},
				Backfill: setupPhase{State: "skipped", consentWhy: setupui.ConsentReasonNoBackfill},
			},
			want: setupui.DistillSkippedFlag,
		},
		{
			name: "skipped before consent, everything already distilled",
			report: setupReport{
				Facts:    factsBackfillStatus{Sessions: 274, Distilled: 274},
				Backfill: setupPhase{State: "skipped", Detail: "all sessions already distilled"},
			},
			want: setupui.DistillAlreadyDone,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := terseDistillOutcome(tc.report); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// The specific misdirection, named: a run that used an agent must never tell
// the reader to install one.
func TestSpawnedBackfillIsNeverReportedAsMissingAgent(t *testing.T) {
	t.Parallel()
	report := setupReport{
		Facts:    factsBackfillStatus{Sessions: 274},
		Backfill: setupPhase{State: "ok", Detail: "pid 123, 274 of 274 pending session(s) queued"},
	}
	if got := terseDistillOutcome(report); got == setupui.DistillNoAgent {
		t.Fatal("a backfill that SPAWNED reported as having no agent CLI")
	}
}

// TestProgressWriterIsOnlySilencedByJSON guards the decision that broke.
//
// The sink derives tty from the progress reporter's writer, and
// setupui.Consent refuses to prompt without a tty. So silencing the default
// short form by passing io.Discard here does not just hide rows — it tells
// the whole run there is no terminal, and the consent gate stops asking
// before it spends. Rows are suppressed at the sink; this writer is not the
// lever for that.
func TestProgressWriterIsOnlySilencedByJSON(t *testing.T) {
	t.Parallel()
	real := &bytes.Buffer{}

	if got := setupProgressWriter(real, false); got != io.Writer(real) {
		t.Fatal("default mode must keep the real writer, or terminal detection dies and consent never prompts")
	}
	if got := setupProgressWriter(real, true); got != io.Discard {
		t.Fatalf("--json must silence the reporter to keep stdout parseable, got %T", got)
	}
}

// TestConnectOfferSurvivesTheShortForm is the merge guard for COR-2112.
//
// Hosted sync is opt-in and `connect` is the only way in, so main put it in
// setup's closing Next block. The short form replaced that block as the
// DEFAULT output, which would have retired the only announcement the feature
// has without anyone deciding to. These three assertions are the whole
// contract: offered when nothing is bound, silent once something is, and
// silent again when the person has said no.
func TestConnectOfferSurvivesTheShortForm(t *testing.T) {
	t.Parallel()
	brainDir := t.TempDir()

	if !setupOffersConnect(brainDir) {
		t.Fatal("a repo with no binding has never been told `connect` exists; it must be offered")
	}

	// The rendered line, not just the flag: a bool nobody prints is not a
	// feature.
	out := renderTerseFor(t, setupui.TerseReport{Queryable: true, BrainCmd: "entire brain", OfferConnect: true})
	if !strings.Contains(out, "entire brain connect") {
		t.Fatalf("the short form must name the command:\n%s", out)
	}
	if off := renderTerseFor(t, setupui.TerseReport{Queryable: true, BrainCmd: "entire brain"}); strings.Contains(off, "connect") {
		t.Fatalf("a bound repo must not be nagged:\n%s", off)
	}

	// And the wiring between the two. Asserting the predicate and the
	// renderer separately leaves the line that joins them untested, which is
	// exactly how this got lost in the merge in the first place.
	report := setupReport{BrainPath: brainDir}
	if !terseReportFor(report, nil, true, defaultSetupOptions(), "entire brain").OfferConnect {
		t.Fatal("terseReportFor must carry the offer, not just be able to compute it")
	}

	if err := writeHostedRepoBinding(brainDir, hostedRepoBinding{RepoID: "repo1", BaseURL: "https://cell.api.example.com"}); err != nil {
		t.Fatal(err)
	}
	if setupOffersConnect(brainDir) {
		t.Fatal("a repo that is already connected must not be told to connect")
	}

	if _, err := setHostedOptOut(brainDir); err != nil {
		t.Fatal(err)
	}
	if setupOffersConnect(brainDir) {
		t.Fatal("an explicit opt-out is a decision, not a prompt to re-ask")
	}
}

// TestUpToDateRerunDoesNotRepeatTheConnectOffer keeps the discovery a
// discovery. The one-line re-run is the output a person sees most often, and
// a sales line on every single run is the noise this whole PR removes.
func TestUpToDateRerunDoesNotRepeatTheConnectOffer(t *testing.T) {
	t.Parallel()
	out := renderTerseFor(t, setupui.TerseReport{
		Queryable:    true,
		UpToDate:     true,
		BrainCmd:     "entire brain",
		OfferConnect: true,
	})
	if strings.Contains(out, "connect") {
		t.Fatalf("an up-to-date re-run must stay one line:\n%s", out)
	}
}

// renderTerseFor renders the short form into a string.
func renderTerseFor(t *testing.T, rep setupui.TerseReport) string {
	t.Helper()
	var buf bytes.Buffer
	theme, _ := tui.ThemeByName("default")
	setupui.RenderTerse(&buf, setupui.NewRendererWidth(tui.NewRendererWith(theme, tui.PlainCaps()), 200), rep)
	return buf.String()
}

// TestVerboseNextBlockStillNamesConnect holds the surface main actually
// changed. The short form is the default, but `--verbose` is what a person
// reaches for when setup did something they did not expect, and the Next
// block there is where `connect` was announced before this PR existed.
func TestVerboseNextBlockStillNamesConnect(t *testing.T) {
	t.Parallel()
	named := func(steps []setupui.NextStep) bool {
		for _, step := range steps {
			if strings.Contains(step.Command, "connect") {
				return true
			}
		}
		return false
	}
	if !named(setupNextSteps("entire brain", false, 0, true)) {
		t.Fatal("an unconnected repo must be offered `connect` in the Next block")
	}
	if named(setupNextSteps("entire brain", false, 0, false)) {
		t.Fatal("a connected or opted-out repo must not be offered it")
	}
}

// TestSetupStillRefreshesThePushHook is the second merge guard.
//
// main added the push-hook refresh as a flat block after the watcher; this PR
// replaced that whole region with phases, so the refresh had to move or be
// lost. Nothing on main asserted setup called it at all -- the block was only
// ever progress text -- which is precisely why it was one `--ours` away from
// disappearing. The AGENTS phase is where it lives now, and `--verbose` is
// where its line shows.
func TestSetupStillRefreshesThePushHook(t *testing.T) {
	f := newSetupTestFixture(t)
	rec := &recordedSetup{}
	opts := defaultSetupOptions()
	opts.noDaemon = true
	opts.noBackfill = true

	out := runSetupForTest(t, f, opts, rec) // forces verbose

	if !strings.Contains(out, "push hook") {
		t.Fatalf("setup must still report what it did about the push hook:\n%s", out)
	}
	// And not in the default output: it is a detail, and details are what
	// --verbose is for.
	if terse := runSetupTerseForTest(t, newSetupTestFixture(t), opts, &recordedSetup{}); strings.Contains(terse, "push hook") {
		t.Fatalf("the short form must not carry hook plumbing:\n%s", terse)
	}
}
