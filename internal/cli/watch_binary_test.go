package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A long-running daemon outlives the binary it was started from; the probe is
// what lets it notice and hand itself back to the service manager (COR-2174).
func TestWatchBinaryChangeProbeDetectsReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entire-brain")
	if err := os.WriteFile(path, []byte("build A"), 0o755); err != nil {
		t.Fatal(err)
	}
	probe := watchBinaryChangeProbe(path)
	if probe == nil {
		t.Fatal("probe must exist for a readable binary")
	}
	if probe() {
		t.Fatal("unchanged binary reported as replaced")
	}

	// In-place rewrite (same inode, new mtime): the nightly `go build` case.
	if err := os.WriteFile(path, []byte("build B"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Now(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if !probe() {
		t.Fatal("rewritten binary not reported as replaced")
	}

	// Atomic replacement (new inode): the install-by-rename case.
	fresh := watchBinaryChangeProbe(path)
	replacement := filepath.Join(t.TempDir(), "next")
	if err := os.WriteFile(replacement, []byte("build C!"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if !fresh() {
		t.Fatal("renamed-over binary not reported as replaced")
	}

	// A vanished binary (mid-replace) must not trigger a restart onto nothing.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if fresh() {
		t.Fatal("a missing binary must defer the decision, not restart")
	}

	if watchBinaryChangeProbe(filepath.Join(t.TempDir(), "absent")) != nil {
		t.Fatal("an unreadable binary must disable the probe")
	}
}

// A stale binary must end the supervised loop — the one loop the installed
// service runs — so the service manager restarts it on the current build,
// instead of the old build failing every tick forever.
func TestSupervisedWatchExitsWhenBinaryReplaced(t *testing.T) {
	f := newSetupTestFixture(t)
	if _, err := recordSetupWatchPlan(f.env, daemonDefaultName, setupWatchPlanEntry{Workspace: "alpha", Interval: "5m"}); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	passes := 0
	err := supervisedWatchLoop(context.Background(), out, f.env, defaultWatchOptions(),
		func(string, watchCommandOptions, *int) error { passes++; return nil },
		nil, func() bool { return true })
	if !errors.Is(err, errWatchBinaryReplaced) {
		t.Fatalf("a replaced binary must end the loop with its sentinel, got %v", err)
	}
	if passes != 0 {
		t.Fatalf("an outdated build ran %d workspace passes before exiting", passes)
	}
	if !strings.Contains(out.String(), "binary replaced") {
		t.Fatalf("the log must say why the daemon exited: %q", out.String())
	}
}

// Registration is not liveness: a job whose spawn failed (e.g. launchd's cdhash
// pin after an ad-hoc-signed binary was rebuilt) still lists successfully, and
// reporting it as running is what hid the wedge behind "daemon ✓ running".
func TestDaemonRunningRequiresALivePID(t *testing.T) {
	home := t.TempDir()
	spec := fixedDaemonSpec()
	spec.LogPath = filepath.Join(home, "logs", "watch.log")
	plan, err := planBrainWatchDaemon("darwin", home, "", "", spec)
	if err != nil {
		t.Fatal(err)
	}
	dead := &scriptedListRunner{listOutput: "{\n\t\"LastExitStatus\" = 0;\n}\n"}
	running, detail := daemonRunning(context.Background(), dead, plan)
	if running {
		t.Fatal("a registered-but-dead job must not report as running")
	}
	if detail == "" {
		t.Fatal("the registered-but-dead state must be named, not silent")
	}
	live := &scriptedListRunner{listOutput: "{\n\t\"PID\" = 123;\n}\n"}
	if running, _ := daemonRunning(context.Background(), live, plan); !running {
		t.Fatal("a job with a live PID must report as running")
	}
}

type scriptedListRunner struct {
	listOutput string
}

func (r *scriptedListRunner) Run(_ context.Context, _, _ string, args ...string) ([]byte, []byte, error) {
	if len(args) > 0 && args[0] == "list" {
		return []byte(r.listOutput), nil, nil
	}
	return nil, nil, nil
}
