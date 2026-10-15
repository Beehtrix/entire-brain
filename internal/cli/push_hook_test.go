package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type hooksDirRunner struct{ dir string }

func (r hooksDirRunner) Run(_ context.Context, _, name string, args ...string) ([]byte, []byte, error) {
	return []byte(r.dir + "\n"), nil, nil
}

func pushHookHarness(t *testing.T) (Options, string, string) {
	t.Helper()
	repoDir := t.TempDir()
	hooksDir := filepath.Join(repoDir, ".git", "hooks")
	opts := Options{Version: "test", Runner: hooksDirRunner{dir: hooksDir}}
	return opts, repoDir, hooksDir
}

func readHook(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInstallPushHookIntoEmptySlot(t *testing.T) {
	opts, repoDir, hooksDir := pushHookHarness(t)

	state, path, err := installPushHook(context.Background(), opts, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if state != pushHookInstalled || path != filepath.Join(hooksDir, "pre-push") {
		t.Fatalf("state=%v path=%v", state, path)
	}
	content := readHook(t, path)
	if !strings.Contains(content, pushHookMarker) || !strings.Contains(content, "--event push") {
		t.Fatalf("hook content missing marker or event: %q", content)
	}
	if strings.Contains(content, "# Chain:") {
		t.Fatalf("empty slot must not chain: %q", content)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("hook must be executable: %v %v", info, err)
	}

	// Idempotent rerun.
	state, _, err = installPushHook(context.Background(), opts, repoDir)
	if err != nil || state != pushHookUnchanged {
		t.Fatalf("rerun state=%v err=%v", state, err)
	}
}

func TestInstallPushHookChainsExistingHook(t *testing.T) {
	opts, repoDir, hooksDir := pushHookHarness(t)
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(hooksDir, "pre-push")
	hostHook := "#!/bin/sh\n# Entire session tracking hook\nentire hooks git pre-push \"$1\"\n"
	if err := os.WriteFile(primary, []byte(hostHook), 0o755); err != nil {
		t.Fatal(err)
	}

	state, _, err := installPushHook(context.Background(), opts, repoDir)
	if err != nil || state != pushHookInstalled {
		t.Fatalf("state=%v err=%v", state, err)
	}
	content := readHook(t, primary)
	if !strings.Contains(content, pushHookMarker) || !strings.Contains(content, "# Chain:") {
		t.Fatalf("primary must be ours with a chain: %q", content)
	}
	if got := readHook(t, primary+pushHookBackupSuffix); got != hostHook {
		t.Fatalf("backup must hold the original hook verbatim, got %q", got)
	}
	if !strings.Contains(content, `exec "$backup" "$@"`) {
		t.Fatalf("chain must exec the backup with original args: %q", content)
	}
}

func TestInstallPushHookLeavesHostRewrapAlone(t *testing.T) {
	// The host CLI re-claimed the primary slot and moved our hook to ITS
	// backup name; our line still runs via its chain. Install must not touch
	// the primary — re-claiming would ping-pong with the host's reconcile.
	opts, repoDir, hooksDir := pushHookHarness(t)
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(hooksDir, "pre-push")
	hostHook := "#!/bin/sh\n# Entire session tracking hook\nentire hooks git pre-push \"$1\"\n"
	if err := os.WriteFile(primary, []byte(hostHook), 0o755); err != nil {
		t.Fatal(err)
	}
	moved := primary + ".pre-entire"
	ours := pushHookContent("/managed/entire-brain", true)
	if err := os.WriteFile(moved, []byte(ours), 0o755); err != nil {
		t.Fatal(err)
	}

	state, path, err := installPushHook(context.Background(), opts, repoDir)
	if err != nil || state != pushHookChainedIn || path != moved {
		t.Fatalf("state=%v path=%v err=%v", state, path, err)
	}
	if got := readHook(t, primary); got != hostHook {
		t.Fatalf("primary must stay the host's: %q", got)
	}
}

func TestUninstallPushHookRestoresBackup(t *testing.T) {
	opts, repoDir, hooksDir := pushHookHarness(t)
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(hooksDir, "pre-push")
	original := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(primary, []byte(original), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := installPushHook(context.Background(), opts, repoDir); err != nil {
		t.Fatal(err)
	}

	if err := uninstallPushHook(context.Background(), opts, repoDir); err != nil {
		t.Fatal(err)
	}
	if got := readHook(t, primary); got != original {
		t.Fatalf("uninstall must restore the original hook, got %q", got)
	}
	if _, err := os.Stat(primary + pushHookBackupSuffix); !os.IsNotExist(err) {
		t.Fatalf("backup must be gone after restore: %v", err)
	}

	// Idempotent on a clean slate too.
	if err := uninstallPushHook(context.Background(), opts, repoDir); err != nil {
		t.Fatal(err)
	}
}
