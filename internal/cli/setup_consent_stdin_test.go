package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-brain/internal/setupui"
	"github.com/entireio/entire-brain/internal/tui"
)

// setup_consent_stdin_test.go holds one review finding: the consent gate
// asked whether the OUTPUT was a terminal and then read from stdin.
//
// Those are different questions, and the gap is not theoretical. A
// pty-wrapping log tool -- `script`, and several CI runners -- gives a
// terminal-looking stdout while stdin is an open stream that never reaches
// EOF. askTwice's ReadString('\n') then blocks forever: the wedged pipeline
// this gate exists to prevent, reached through the gate itself, with the
// install sitting silent and no indication why.

func TestConsentNeedsBothEndsOfTheTerminal(t *testing.T) {
	t.Parallel()
	// The rule, in all four combinations. Taking two booleans is what makes
	// this writable at all: no CI machine running these tests has a terminal
	// to supply, so the "output alone is not enough" case cannot be expressed
	// against a reader -- and the first version of this, which took one,
	// stayed green with the output check deleted.
	for _, tc := range []struct {
		out, in, want bool
	}{
		{true, true, true},
		{true, false, false}, // the hang: terminal stdout, piped stdin
		{false, true, false}, // the question is never seen
		{false, false, false},
	} {
		if got := setupCanAskAHuman(tc.out, tc.in); got != tc.want {
			t.Errorf("setupCanAskAHuman(out=%v, in=%v) = %v, want %v", tc.out, tc.in, got, tc.want)
		}
	}
}

// And what counts as an input terminal. A pipe is the shape that hangs: open,
// readable, and never EOFs on its own -- exactly how `script`-style wrapping
// reaches a run whose stdout looks interactive.
func TestOnlyARealTerminalCountsAsAnInput(t *testing.T) {
	t.Parallel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })

	if readerIsTerminal(r) {
		t.Fatal("a pipe is not a person; this is the hang")
	}
	if readerIsTerminal(&bytes.Buffer{}) {
		t.Fatal("a buffer is not a person")
	}
	if readerIsTerminal(strings.NewReader("y\n")) {
		t.Fatal("a canned answer is not consent")
	}
	if readerIsTerminal(nil) {
		t.Fatal("a nil reader must not be treated as a terminal")
	}
	// The POSITIVE case needs a real terminal, which this machine does not
	// have (/dev/ptmx is not a tty and /dev/tty is not configured). That half
	// is isatty's contract. Stating the limit beats a fixture that pretends.
}

// And the call site. The helper above can be perfectly correct and never
// consulted: the defect was that runSetupBackfill passed ui.tty, which is
// about the writer, straight into a field that decides whether to READ.
//
// This drives the real backfill path with exactly the shape that hangs --
// a terminal-looking sink and an os.Pipe stdin that never reaches EOF -- and
// fails on the clock rather than wedging the suite.
func TestTheBackfillGateDoesNotBlockOnAPipeWithATerminalStdout(t *testing.T) {
	f := newSetupTestFixture(t, "s1")
	rec := &recordedSetup{agent: "codex"}
	steps := rec.steps(f)

	theme, _ := tui.ThemeByName("default")
	render := tui.NewRendererWith(theme, tui.Caps{TTY: true, Color: false, Unicode: false, Width: 80})
	progress := newProgressWithRenderer(io.Discard, "setup", render, newLiveTerminal())
	// quiet=false so the gate runs the full prompt path; a quiet sink could
	// suppress the prompt and quietly stop exercising the read this test is about.
	ui := newSetupSink(progress, false)
	if !ui.tty {
		t.Fatal("the fixture must model a terminal stdout; without one there is no hang to reproduce")
	}

	stdin, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately left OPEN for the whole test: an EOF would make the read
	// return and hide the defect.
	t.Cleanup(func() { _ = writeEnd.Close(); _ = stdin.Close() })

	report := setupReport{Facts: factsBackfillStatus{Sessions: 1}}
	run := setupui.NewRun(ui, ui.Renderer())
	phase := run.Begin(setupui.PhaseFacts)

	done := make(chan setupPhase, 1)
	go func() {
		done <- runSetupBackfill(context.Background(), phase, ui, stdin, &bytes.Buffer{},
			f.opts, defaultSetupOptions(), steps, f.storage, f.repoDir, &report, "entire brain")
	}()

	select {
	case got := <-done:
		if got.State != "skipped" {
			t.Fatalf("with no reachable human the gate must skip, got state %q (%s)", got.State, got.Detail)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the consent gate read from a pipe that never EOFs: this is the wedged install the gate exists to prevent")
	}
}
