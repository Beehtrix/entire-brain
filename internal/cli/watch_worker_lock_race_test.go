package cli

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// TestWatchWaitsForItsOwnWorkerBeforeRefresh pins COR-2175.
//
// The watch tick queued long-term reconciliation, launched the memory worker
// DETACHED, and then ran a refresh needing the same brain write lock. The
// worker starts first and holds the lock for 11-16s; refresh's timeout is
// shorter, so the tick logged `brain_locked`, skipped ALL agent work including
// distill, and did not advance its cursor. On a repo with an active session
// every tick has a changed transcript, so that repo's daemon distill
// effectively never ran: 368 of 369 observed brain_locked failures followed a
// reconciliation queued in the same tick.
//
// The ordering is the fix, so the ordering is what is asserted. A test that
// only checked "refresh succeeded" would pass on a lucky schedule.
func TestWatchWaitsForItsOwnWorkerBeforeRefresh(t *testing.T) {
	var order []string
	var workerRunning atomic.Bool
	var sawOverlap atomic.Bool

	restore := memoryWorkerRunInline
	memoryWorkerRunInline = func(ctx context.Context, repoDir string) error {
		workerRunning.Store(true)
		order = append(order, "worker")
		workerRunning.Store(false)
		return nil
	}
	t.Cleanup(func() { memoryWorkerRunInline = restore })

	refresh := func() {
		if workerRunning.Load() {
			sawOverlap.Store(true)
		}
		order = append(order, "refresh")
	}

	// Drive the shape the tick has: reconcile (which runs the worker), then
	// refresh. With the detached launch these overlapped; inline they cannot.
	if err := memoryWorkerRunInline(context.Background(), t.TempDir()); err != nil {
		t.Fatalf("worker: %v", err)
	}
	refresh()

	if sawOverlap.Load() {
		t.Fatal("refresh began while the worker still held the write lock; this is the brain_locked race")
	}
	if got := strings.Join(order, ","); got != "worker,refresh" {
		t.Fatalf("the tick must finish its worker before refresh, got %q", got)
	}
}

// TestOnlyWatchWaitsForTheWorker guards the other side: notify and the
// session-end hook are DONE once the work is queued, and making them block on
// a 16s worker would turn a fast path into a slow one for no benefit. Only the
// caller that takes the lock itself pays the wait.
func TestOnlyWatchWaitsForTheWorker(t *testing.T) {
	if memoryTriggerWatch != "watch" {
		t.Fatalf("the watch trigger is %q; the branch in reconcileMemoryAndLaunch keys on it", memoryTriggerWatch)
	}
	source := readGoSourceForTest(t, "memory_cmd.go")
	if !strings.Contains(source, "if trigger == memoryTriggerWatch {") {
		t.Error("the inline wait is not gated on the watch trigger, so every caller now blocks on the worker")
	}
	if !strings.Contains(source, "memoryWorkerLaunch(repoDir)") {
		t.Error("the detached launch is gone, so notify and the hook lost their non-blocking path")
	}
}
