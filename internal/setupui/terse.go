package setupui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/entireio/entire-brain/internal/tui"
)

// terse.go is the DEFAULT shape of `entire brain setup`.
//
// The phase view in this package reports what the machine did: eight component
// names, absolute paths, provider error text, internal identifiers. That is the
// right report when something is wrong and the wrong one the other 95% of the
// time. A first run printed seventeen lines, two of them raw subprocess error
// dumps and one of them an internal ticket id, to say "it worked".
//
// The short form answers the three questions a person actually has, in order:
// is it usable, what happens to my past sessions, and will it stay fresh. One
// line each, then one line telling them what to type. Everything else moves
// behind `--verbose` and `doctor`, which exist for it.
//
// This is a pure function over a value: every state below is reachable in a
// test without a brain, a repo, or a terminal.

// DistillOutcome is what happened to the repo's past sessions.
type DistillOutcome int

const (
	// DistillNoSessions is a repo with nothing recorded yet. Not a problem,
	// and must not read like one: memories accrue as the person works.
	DistillNoSessions DistillOutcome = iota
	// DistillStarted means the backfill is running in the background.
	DistillStarted
	// DistillDeclined means the person said no at the prompt.
	DistillDeclined
	// DistillSkippedFlag means a flag decided, so no prompt was shown.
	DistillSkippedFlag
	// DistillNoAgent means sessions exist but nothing can read them.
	DistillNoAgent
	// DistillAlreadyDone means a previous run already distilled them.
	DistillAlreadyDone
)

// WatcherOutcome is what happened to the background watcher.
type WatcherOutcome int

const (
	// WatcherInstalled is the normal case.
	WatcherInstalled WatcherOutcome = iota
	// WatcherAlreadyRunning is a re-run finding a healthy watcher.
	WatcherAlreadyRunning
	// WatcherSkippedFlag is --no-daemon.
	WatcherSkippedFlag
	// WatcherUnavailable is a platform or permission problem. It carries a
	// reason, because this is the one watcher state a person must act on.
	WatcherUnavailable
)

// TerseReport is everything the short form needs and nothing it does not.
type TerseReport struct {
	// Elapsed is how long the instant phase took.
	Elapsed time.Duration
	// Queryable reports whether the brain can answer questions now. A build
	// can be degraded and still queryable, which is the common case and must
	// not be reported as failure.
	Queryable bool
	// Unavailable names capabilities that did NOT build, in words a person
	// recognises ("semantic search"), never component ids. Empty when the
	// build was complete.
	Unavailable []string
	// Remedy is the single command that fixes Unavailable, when one exists.
	Remedy string

	Sessions  int
	Distilled int
	Agent     string
	Distill   DistillOutcome

	Watcher       WatcherOutcome
	WatcherReason string

	// UpToDate collapses the whole report to one line: a re-run that found
	// nothing to do. Repeating the full report teaches the reader to stop
	// reading it.
	UpToDate bool

	// BrainCmd is the command name to put in the "try this" line.
	BrainCmd string
}

// RenderTerse writes the short form.
func RenderTerse(w io.Writer, r *Renderer, rep TerseReport) {
	brainCmd := strings.TrimSpace(rep.BrainCmd)
	if brainCmd == "" {
		brainCmd = "entire brain"
	}
	ok := r.MarkGlyph(tui.MarkDone)
	skip := r.MarkGlyph(tui.MarkSkipped)

	fmt.Fprintln(w)

	if rep.UpToDate {
		fmt.Fprintf(w, "  %s %s\n\n", ok, rep.upToDateLine())
		fmt.Fprintln(w, "Ready.")
		return
	}

	// The opening sentence is for the person who has just installed this and
	// does not yet know what it is for. It says what they GET, not what the
	// program is about to do.
	for _, line := range r.Wrap("Brain turns this repo's history and past AI sessions into memories your agents recall while they work.") {
		fmt.Fprintf(w, "  %s\n", line)
	}
	fmt.Fprintln(w)

	built := fmt.Sprintf("Brain built — queryable now (%s)", roundTerse(rep.Elapsed))
	if !rep.Queryable {
		built = "Brain could not be built"
	}
	fmt.Fprintf(w, "  %s %s\n", markFor(r, rep.Queryable), built)

	// A degraded build gets ONE line naming the capability in plain words and
	// the command that restores it -- not the provider's error text, which is
	// what `doctor` is for.
	if len(rep.Unavailable) > 0 {
		line := fmt.Sprintf("%s unavailable", strings.Join(rep.Unavailable, " and "))
		if rep.Remedy != "" {
			line += " — " + rep.Remedy
		}
		fmt.Fprintf(w, "  %s %s\n", skip, line)
	}

	if line, mark := rep.distillLine(brainCmd); line != "" {
		fmt.Fprintf(w, "  %s %s\n", r.MarkGlyph(mark), line)
	}
	if line, mark := rep.watcherLine(); line != "" {
		fmt.Fprintf(w, "  %s %s\n", r.MarkGlyph(mark), line)
	}

	fmt.Fprintln(w)
	if rep.Queryable {
		fmt.Fprintf(w, "Ready. Try: %s brief \"<your task>\"\n", brainCmd)
		return
	}
	fmt.Fprintf(w, "Run `%s doctor` for what went wrong.\n", brainCmd)
}

func (rep TerseReport) upToDateLine() string {
	parts := []string{"Brain up to date"}
	switch {
	case rep.Distilled > 0:
		parts = append(parts, fmt.Sprintf("%s distilled", plural(rep.Distilled, "session")))
	case rep.Sessions > 0:
		parts = append(parts, fmt.Sprintf("%s indexed", plural(rep.Sessions, "session")))
	}
	line := strings.Join(parts, " — ")
	// Comma, not a third em dash: two dashes in one sentence stop separating
	// anything.
	if rep.Watcher == WatcherInstalled || rep.Watcher == WatcherAlreadyRunning {
		line += ", watcher running"
	}
	return line + fmt.Sprintf(" (%s)", roundTerse(rep.Elapsed))
}

func (rep TerseReport) distillLine(brainCmd string) (string, tui.Mark) {
	switch rep.Distill {
	case DistillNoSessions:
		return "No past sessions to distill — memories accrue as you work", tui.MarkDone
	case DistillStarted:
		return fmt.Sprintf("Distilling %s in the background (watch: %s status)",
			plural(rep.Sessions, "session"), brainCmd), tui.MarkDone
	case DistillDeclined:
		return fmt.Sprintf("Distillation skipped — run `%s setup` again anytime", brainCmd), tui.MarkSkipped
	case DistillSkippedFlag:
		return fmt.Sprintf("Distillation skipped — run `%s setup` again anytime", brainCmd), tui.MarkSkipped
	case DistillNoAgent:
		// Named as a missing TOOL, not a failure of the repo: the sessions are
		// fine and nothing is lost by fixing this later.
		return fmt.Sprintf("%s found, but no agent CLI to distill them — install claude or codex, then re-run `%s setup`",
			plural(rep.Sessions, "session"), brainCmd), tui.MarkSkipped
	case DistillAlreadyDone:
		return fmt.Sprintf("%s already distilled", plural(rep.Distilled, "session")), tui.MarkDone
	}
	return "", tui.MarkDone
}

func (rep TerseReport) watcherLine() (string, tui.Mark) {
	switch rep.Watcher {
	case WatcherInstalled:
		return "Watcher installed — keeps the brain fresh; starts at login", tui.MarkDone
	case WatcherAlreadyRunning:
		return "Watcher already running — keeps the brain fresh", tui.MarkDone
	case WatcherSkippedFlag:
		return "Watcher not installed (--no-daemon) — the brain updates only when you re-run setup", tui.MarkSkipped
	case WatcherUnavailable:
		line := "Watcher unavailable — the brain updates only when you re-run setup"
		if reason := strings.TrimSpace(rep.WatcherReason); reason != "" {
			line += " (" + reason + ")"
		}
		return line, tui.MarkSkipped
	}
	return "", tui.MarkDone
}

// markFor goes through the Renderer so the non-UTF-8 and NO_COLOR fallbacks
// are the ones the rest of this package already agreed on.
func markFor(r *Renderer, ok bool) string {
	if ok {
		return r.MarkGlyph(tui.MarkDone)
	}
	return r.MarkGlyph(tui.MarkFailed)
}

// roundTerse renders a duration the way a person would say it.
func roundTerse(d time.Duration) string {
	switch {
	case d <= 0:
		return "0s"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Round(time.Millisecond).Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%gs", d.Round(100*time.Millisecond).Seconds())
	default:
		return d.Round(time.Second).String()
	}
}
