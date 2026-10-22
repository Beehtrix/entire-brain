package setupui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-brain/internal/tui"
)

func terseRender(t *testing.T, rep TerseReport) string {
	t.Helper()
	var buf bytes.Buffer
	RenderTerse(&buf, rendererFor(tui.Caps{TTY: true, Color: false, Unicode: true, Width: 160}, 160), rep)
	return buf.String()
}

// bodyLines is what a person actually sees: blank lines removed, so a test
// asserting "four lines" is asserting four pieces of information.
func bodyLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	return lines
}

// The shapes below are the agreed output. They are asserted as whole lines
// rather than substrings: a substring check passes when the line also contains
// a paragraph of provider error text, which is the defect this file exists to
// prevent.
func TestTerseShapes(t *testing.T) {
	t.Parallel()
	base := TerseReport{Elapsed: 4 * time.Second, Queryable: true, BrainCmd: "entire brain"}

	for _, tc := range []struct {
		name  string
		rep   TerseReport
		lines []string
	}{
		{
			name: "accepted the prompt",
			rep: func() TerseReport {
				r := base
				r.Sessions, r.Distill, r.Watcher = 274, DistillStarted, WatcherInstalled
				return r
			}(),
			lines: []string{
				"Brain turns this repo's history and past AI sessions into memories your agents recall while they work.",
				"✓ Brain built — queryable now (4s)",
				"✓ Distilling 274 sessions in the background (watch: entire brain status)",
				"✓ Watcher installed — keeps the brain fresh; starts at login",
			},
		},
		{
			name: "declined the prompt",
			rep: func() TerseReport {
				r := base
				r.Sessions, r.Distill, r.Watcher = 274, DistillDeclined, WatcherInstalled
				return r
			}(),
			lines: []string{
				"Brain turns this repo's history and past AI sessions into memories your agents recall while they work.",
				"✓ Brain built — queryable now (4s)",
				"− Distillation skipped — run `entire brain setup` again anytime",
				"✓ Watcher installed — keeps the brain fresh; starts at login",
			},
		},
		{
			name: "no sessions yet",
			rep: func() TerseReport {
				r := base
				r.Elapsed, r.Distill, r.Watcher = 2*time.Second, DistillNoSessions, WatcherInstalled
				return r
			}(),
			lines: []string{
				"Brain turns this repo's history and past AI sessions into memories your agents recall while they work.",
				"✓ Brain built — queryable now (2s)",
				"✓ No past sessions to distill — memories accrue as you work",
				"✓ Watcher installed — keeps the brain fresh; starts at login",
			},
		},
		{
			name: "sessions but no agent CLI",
			rep: func() TerseReport {
				r := base
				r.Sessions, r.Distill, r.Watcher = 274, DistillNoAgent, WatcherInstalled
				return r
			}(),
			lines: []string{
				"Brain turns this repo's history and past AI sessions into memories your agents recall while they work.",
				"✓ Brain built — queryable now (4s)",
				"− 274 sessions found, but no agent CLI to distill them — install claude or codex, then re-run `entire brain setup`",
				"✓ Watcher installed — keeps the brain fresh; starts at login",
			},
		},
		{
			name: "re-run with nothing to do",
			rep: TerseReport{
				Elapsed: time.Second, Queryable: true, UpToDate: true,
				Distilled: 274, Watcher: WatcherAlreadyRunning, BrainCmd: "entire brain",
			},
			lines: []string{
				"✓ Brain up to date — 274 sessions distilled, watcher running (1s)",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := bodyLines(terseRender(t, tc.rep))
			if len(got) != len(tc.lines) {
				t.Fatalf("want %d lines, got %d:\n%s", len(tc.lines), len(got), strings.Join(got, "\n"))
			}
			for i := range tc.lines {
				if got[i] != tc.lines[i] {
					t.Errorf("line %d:\n want: %s\n  got: %s", i+1, tc.lines[i], got[i])
				}
			}
		})
	}
}

// A degraded build is still a working brain and must not read as a failure.
func TestTerseDegradedStaysReadyAndNamesTheRemedy(t *testing.T) {
	t.Parallel()
	out := terseRender(t, TerseReport{
		Elapsed: 700 * time.Millisecond, Queryable: true,
		Unavailable: []string{"semantic search"}, Remedy: "run `entire plugin install graph`",
		Distill: DistillNoSessions, Watcher: WatcherInstalled, BrainCmd: "entire brain",
	})
	if !strings.Contains(out, "Brain built") {
		t.Fatalf("a degraded build is still built:\n%s", out)
	}
	if !strings.Contains(out, "semantic search unavailable — run `entire plugin install graph`") {
		t.Fatalf("degraded line must name the capability and the remedy:\n%s", out)
	}
	if strings.Contains(out, "Ready.") {
		t.Fatalf("the summary IS the output; no trailing hint after the status lines:\n%s", out)
	}
}

// The one case that is genuinely a failure points at doctor instead.
func TestTerseUnbuildableDoesNotSayReady(t *testing.T) {
	t.Parallel()
	out := terseRender(t, TerseReport{Queryable: false, BrainCmd: "entire brain"})
	if strings.Contains(out, "Ready.") {
		t.Fatalf("an unbuildable brain must not claim Ready:\n%s", out)
	}
	if !strings.Contains(out, "doctor") {
		t.Fatalf("a failure must point somewhere:\n%s", out)
	}
}

// --no-daemon is a choice, so it reads as one, and it says what the cost is.
func TestTerseNoDaemonExplainsTheConsequence(t *testing.T) {
	t.Parallel()
	out := terseRender(t, TerseReport{
		Elapsed: time.Second, Queryable: true,
		Distill: DistillNoSessions, Watcher: WatcherSkippedFlag, BrainCmd: "entire brain",
	})
	if !strings.Contains(out, "updates only when you re-run setup") {
		t.Fatalf("skipping the watcher must say what it costs:\n%s", out)
	}
}

// A watcher that could not install is the one watcher state to act on, so it
// carries the reason.
func TestTerseWatcherUnavailableCarriesTheReason(t *testing.T) {
	t.Parallel()
	out := terseRender(t, TerseReport{
		Elapsed: time.Second, Queryable: true, Distill: DistillNoSessions,
		Watcher: WatcherUnavailable, WatcherReason: "launchd unavailable in this container",
		BrainCmd: "entire brain",
	})
	if !strings.Contains(out, "launchd unavailable in this container") {
		t.Fatalf("watcher failure must carry its reason:\n%s", out)
	}
}

// "1 sessions" is the tell that nobody read the output.
func TestTerseSingularSession(t *testing.T) {
	t.Parallel()
	out := terseRender(t, TerseReport{
		Elapsed: time.Second, Queryable: true, Sessions: 1,
		Distill: DistillStarted, Watcher: WatcherInstalled, BrainCmd: "entire brain",
	})
	if strings.Contains(out, "1 sessions") {
		t.Fatalf("singular must not read as plural:\n%s", out)
	}
	if !strings.Contains(out, "Distilling 1 session in the background") {
		t.Fatalf("want singular phrasing:\n%s", out)
	}
}

// Nothing in the short form may leak a filesystem path, an internal component
// id, or a ticket reference. That is the whole point of it.
func TestTerseLeaksNothingInternal(t *testing.T) {
	t.Parallel()
	out := terseRender(t, TerseReport{
		Elapsed: 4 * time.Second, Queryable: true, Sessions: 274,
		Unavailable: []string{"semantic search"}, Remedy: "run `entire plugin install graph`",
		Distill: DistillStarted, Watcher: WatcherInstalled, BrainCmd: "entire brain",
	})
	for _, banned := range []string{"/Users/", "/var/folders", "COR-", "provider_doctor_failed", "exit status"} {
		if strings.Contains(out, banned) {
			t.Errorf("short form leaked %q:\n%s", banned, out)
		}
	}
	if n := len(bodyLines(out)); n > 6 {
		t.Errorf("short form grew to %d lines; it exists to be short:\n%s", n, out)
	}
}

// A budget-capped backfill must not claim the whole pool: "Distilling 272
// sessions" about a budget of 2 was a false promise.
func TestTerseDistillLineSaysQueuedOfPool(t *testing.T) {
	t.Parallel()
	rep := TerseReport{Elapsed: time.Second, Queryable: true, BrainCmd: "entire brain",
		Sessions: 272, Queued: 2, Distill: DistillStarted, Watcher: WatcherSkippedFlag}
	out := terseRender(t, rep)
	if !strings.Contains(out, "Distilling 2 of 272 sessions in the background") {
		t.Fatalf("capped backfill misreported:\n%s", out)
	}
	rep.Queued = 272
	if out := terseRender(t, rep); !strings.Contains(out, "Distilling 272 sessions in the background") {
		t.Fatalf("uncapped backfill lost its plain count:\n%s", out)
	}
}
