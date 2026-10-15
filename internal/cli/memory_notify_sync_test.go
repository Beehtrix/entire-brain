package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

type gitlessRunner struct{}

func (gitlessRunner) Run(context.Context, string, string, ...string) ([]byte, []byte, error) {
	return nil, nil, errors.New("no git here")
}

func captureNotifyHostedSync(t *testing.T) *[]string {
	t.Helper()
	original := notifyHostedFactsSync
	t.Cleanup(func() { notifyHostedFactsSync = original })
	var branches []string
	notifyHostedFactsSync = func(_ context.Context, _ io.Writer, _ Options, _ string, _ repoStorage, branch string) error {
		branches = append(branches, branch)
		return nil
	}
	return &branches
}

func notifySyncHarness(t *testing.T, now *time.Time) (Options, repoStorage) {
	t.Helper()
	brainDir := t.TempDir()
	if err := os.MkdirAll(brainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stubNotifyCheckpointWork(t)
	opts := Options{Version: "test", Now: func() time.Time { return *now }}
	return opts, repoStorage{Key: "test/repo", BrainDir: brainDir}
}

// stubNotifyCheckpointWork replaces the export and distill seams with no-ops
// and returns the distilled session ids for assertions.
func stubNotifyCheckpointWork(t *testing.T) *[]string {
	t.Helper()
	originalExport := notifyCheckpointExport
	originalDistill := notifyCheckpointDistill
	t.Cleanup(func() {
		notifyCheckpointExport = originalExport
		notifyCheckpointDistill = originalDistill
	})
	notifyCheckpointExport = func(context.Context, io.Writer, Options, string) error { return nil }
	var sessions []string
	notifyCheckpointDistill = func(_ context.Context, _ io.Writer, _ Options, _ string, sessionID string) error {
		sessions = append(sessions, sessionID)
		return nil
	}
	return &sessions
}

func TestCheckpointHintSyncsBranchAndDefault(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	opts, storage := notifySyncHarness(t, &now)
	calls := captureNotifyHostedSync(t)

	ran := hostedSyncOnCheckpointHint(context.Background(), io.Discard, opts, t.TempDir(), storage, "s1", "feat-x")
	if !ran {
		t.Fatal("first checkpoint hint must sync")
	}
	if len(*calls) != 2 || (*calls)[0] != "feat-x" || (*calls)[1] != distillDefaultBranch {
		t.Fatalf("expected [feat-x %s], got %v", distillDefaultBranch, *calls)
	}
}

func TestCheckpointHintSyncsDefaultBranchOnce(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	opts, storage := notifySyncHarness(t, &now)
	calls := captureNotifyHostedSync(t)

	if !hostedSyncOnCheckpointHint(context.Background(), io.Discard, opts, t.TempDir(), storage, "s1", distillDefaultBranch) {
		t.Fatal("hint on the default branch must sync")
	}
	if len(*calls) != 1 || (*calls)[0] != distillDefaultBranch {
		t.Fatalf("expected one %s sync, got %v", distillDefaultBranch, *calls)
	}
}

func TestCheckpointHintSyncThrottles(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	opts, storage := notifySyncHarness(t, &now)
	calls := captureNotifyHostedSync(t)
	repoDir := t.TempDir()

	if !hostedSyncOnCheckpointHint(context.Background(), io.Discard, opts, repoDir, storage, "s1", "feat-x") {
		t.Fatal("first hint must sync")
	}
	now = now.Add(memoryNotifyHostedSyncMinInterval - time.Second)
	if hostedSyncOnCheckpointHint(context.Background(), io.Discard, opts, repoDir, storage, "s1", "feat-x") {
		t.Fatal("hint inside the interval must be throttled")
	}
	if len(*calls) != 2 {
		t.Fatalf("throttled hint must not sync, got %d calls", len(*calls))
	}
	now = now.Add(2 * time.Second)
	if !hostedSyncOnCheckpointHint(context.Background(), io.Discard, opts, repoDir, storage, "s1", "feat-x") {
		t.Fatal("hint after the interval must sync again")
	}
	if len(*calls) != 4 {
		t.Fatalf("expected 4 calls after the interval, got %d", len(*calls))
	}
}

func TestCheckpointHintSyncEmptyBranchFallsBackToDefault(t *testing.T) {
	// No git repository behind repoDir, so branch resolution fails and the
	// sync covers the default branch only — never an empty branch name.
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	opts, storage := notifySyncHarness(t, &now)
	opts.Runner = gitlessRunner{}
	calls := captureNotifyHostedSync(t)

	if !hostedSyncOnCheckpointHint(context.Background(), io.Discard, opts, t.TempDir(), storage, "s1", "") {
		t.Fatal("hint without a branch must still sync")
	}
	if len(*calls) != 1 || (*calls)[0] != distillDefaultBranch {
		t.Fatalf("expected one %s sync, got %v", distillDefaultBranch, *calls)
	}
}

func TestNotifyCommandSyncsOnCheckpointEventOnly(t *testing.T) {
	opts, _ := memoryAdminCommandFixture(t)
	originalLaunch := memoryWorkerLaunch
	memoryWorkerLaunch = func(string) error { return nil }
	t.Cleanup(func() { memoryWorkerLaunch = originalLaunch })
	calls := captureNotifyHostedSync(t)
	distilled := stubNotifyCheckpointWork(t)

	if _, err := execute(t, newMemoryNotifyCommand(opts), "--event", "session_start", "--session", "s1"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("session_start must not sync, got %v", *calls)
	}
	if _, err := execute(t, newMemoryNotifyCommand(opts), "--event", "checkpoint", "--session", "s1", "--branch", "feat-x"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[0] != "feat-x" {
		t.Fatalf("checkpoint must sync the hinted branch then default, got %v", *calls)
	}
	if len(*distilled) != 1 || (*distilled)[0] != "s1" {
		t.Fatalf("checkpoint must distill the hinted session, got %v", *distilled)
	}
}
