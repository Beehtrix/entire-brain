package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestWatchSettlesTheLockBeforeRefresh pins COR-2175.
//
// The watch tick queued reconciliation, launched the memory worker detached,
// then ran a refresh needing the same brain write.lock. The worker starts
// first and holds it 11-16s; refresh's timeout is shorter, so the tick logged
// brain_locked, skipped ALL agent work including distill, and did not advance
// its cursor. 368 of 369 observed brain_locked failures followed a
// reconciliation queued in the same tick.
func TestWatchSettlesTheLockBeforeRefresh(t *testing.T) {
	dir := t.TempDir()

	// Hold the lock, as the worker would.
	unlock, err := acquireBrainWriteLockTimeout(dir, time.Second)
	if err != nil {
		t.Fatalf("seed the held lock: %v", err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		unlock()
		close(released)
	}()

	start := time.Now()
	memoryWorkerSettle(context.Background(), dir, 5*time.Second)
	// Measured BEFORE joining the releaser: taking the elapsed time after
	// waiting on it made this assertion true no matter what settle did.
	elapsed := time.Since(start)
	<-released

	if elapsed < 250*time.Millisecond {
		t.Fatalf("settle returned in %v while the lock was still held; refresh would hit brain_locked", elapsed)
	}
	// And it must actually be free afterwards, not merely waited out.
	again, err := acquireBrainWriteLockTimeout(dir, time.Second)
	if err != nil {
		t.Fatalf("the lock is still held after settle: %v", err)
	}
	again()
}

// The wait is BOUNDED: a pathological hold must not stall the tick forever.
// One observed hold ran 2m15s, so the budget has to expire rather than block.
func TestLockSettleGivesUpWithinItsBudget(t *testing.T) {
	dir := t.TempDir()
	unlock, err := acquireBrainWriteLockTimeout(dir, time.Second)
	if err != nil {
		t.Fatalf("seed the held lock: %v", err)
	}
	t.Cleanup(unlock)

	start := time.Now()
	memoryWorkerSettle(context.Background(), dir, 600*time.Millisecond)
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("settle did not respect its budget: waited %v on a lock that is never released", elapsed)
	}
}

// A cancelled tick must abandon the wait immediately.
func TestLockSettleStopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	unlock, err := acquireBrainWriteLockTimeout(dir, time.Second)
	if err != nil {
		t.Fatalf("seed the held lock: %v", err)
	}
	t.Cleanup(unlock)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	memoryWorkerSettle(ctx, dir, time.Minute)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("a cancelled tick kept waiting for %v", elapsed)
	}
}

// Only the watch trigger pays the wait: notify and the session-end hook are
// done once the work is queued, and the launch itself stays behind
// memoryWorkerLaunch, which 36 existing tests stub. Waiting on the PROCESS
// instead re-execed os.Executable() — inside a test binary, the test binary.
func TestOnlyWatchWaitsAndTheLaunchStaysBehindTheStubbedSeam(t *testing.T) {
	t.Parallel()
	if memoryTriggerWatch != "watch" {
		t.Fatalf("the watch trigger is %q; reconcileMemoryAndLaunch keys on it", memoryTriggerWatch)
	}
	// Resolved relative to THIS file, not the working directory: the Windows
	// shard runner executes precompiled test binaries from a temp directory,
	// where a bare "memory_cmd.go" does not exist. That is what made this
	// guard the only failure on shard 7.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("cannot locate this test's own file; the source guard cannot run here")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "memory_cmd.go"))
	if err != nil {
		t.Skipf("source not readable from this runner: %v", err)
	}
	// Normalised: a Windows checkout gives .go files CRLF, and the body
	// delimiter below is searched as "\n}\n". Without this the guard reported
	// a defect that was not there — twice, on shard 7 only.
	source := strings.ReplaceAll(string(data), "\r\n", "\n")
	if len(source) == 0 {
		t.Fatal("memory_cmd.go is empty; this guard would be vacuous")
	}
	if !strings.Contains(source, "if trigger == memoryTriggerWatch {") {
		t.Error("the wait is not gated on the watch trigger, so every caller now blocks")
	}
	if !strings.Contains(source, "memoryWorkerLaunch(repoDir)") {
		t.Error("the launch no longer goes through memoryWorkerLaunch, so the tests that stub it would spawn real processes")
	}
	// Scoped to the settle function's own body: os.Executable() is legitimate
	// in launchDetachedSelf, which lives in this same file.
	start := strings.Index(source, "var memoryWorkerSettle = func(")
	if start < 0 {
		t.Fatal("memoryWorkerSettle not found; this guard would be vacuous")
	}
	end := strings.Index(source[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not delimit memoryWorkerSettle's body")
	}
	body := source[start : start+end]
	if strings.Contains(body, "os.Executable()") || strings.Contains(body, "exec.Command") {
		t.Error("settle re-execs the binary; it must wait on the LOCK, not the process — inside a test binary that spawns the test binary")
	}
	if !strings.Contains(body, "acquireBrainWriteLockTimeout") {
		t.Error("settle does not wait on the brain write lock, which is the resource actually in contention")
	}
}

// TestLockSettleDoesNotWaitOutNonContentionErrors is why CI's 30-minute job
// timed out on the first version of this change.
//
// A brain root that cannot be locked for a reason other than contention will
// still fail in 90 seconds. The first implementation treated every error as
// "still held" and sat through the whole budget, and tests with unlockable
// brain dirs each paid that, pushing the suite past its cap.
//
// A symlinked root is the case: rejectSymlinkedBrainRoot refuses it outright.
// A MISSING directory is deliberately not used here — the lock creates it and
// succeeds, so it exercises nothing, which is how the first version of this
// test passed against the very bug it was meant to catch.
func TestLockSettleDoesNotWaitOutNonContentionErrors(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	if _, err := acquireBrainWriteLockTimeout(link, 100*time.Millisecond); err == nil {
		t.Skip("this platform allows locking a symlinked brain root; nothing to assert")
	}

	start := time.Now()
	memoryWorkerSettle(context.Background(), link, time.Minute)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("settle waited %v on an error that cannot resolve; only contention is worth waiting out", elapsed)
	}
}
