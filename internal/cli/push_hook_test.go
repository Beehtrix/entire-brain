package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// hooksDirRunner mimics the HARDENED runner: rev-parse answers normally, and
// the core.hooksPath values always end with the command-line pin, optionally
// preceded by a file-sourced value like the one husky writes.
type hooksDirRunner struct {
	gitDir        string
	fileHooksPath string
}

func (r hooksDirRunner) Run(_ context.Context, _, _ string, args ...string) ([]byte, []byte, error) {
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "--git-common-dir"):
		return []byte(r.gitDir + "\n"), nil, nil
	case strings.Contains(joined, "core.hooksPath"):
		out := ""
		if r.fileHooksPath != "" {
			out += r.fileHooksPath + "\n"
		}
		out += gitHooksDisabledPath + "\n"
		return []byte(out), nil, nil
	default:
		return nil, nil, nil
	}
}

const testHostHook = "#!/bin/sh\n# Entire session tracking hook\nentire hooks git pre-push \"$1\"\n"

func pushHookHarness(t *testing.T) (Options, string, string) {
	t.Helper()
	repoDir := t.TempDir()
	gitDir := filepath.Join(repoDir, ".git")
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := Options{Version: "test", Runner: hooksDirRunner{gitDir: gitDir}}
	return opts, repoDir, hooksDir
}

// The hardened runner pins core.hooksPath to the disabled sentinel on every
// git call, so the resolver must never trust `--git-path hooks` — this is the
// regression Bugbot caught: install resolved the sentinel and silently
// no-opped everywhere.
func TestGitHooksDirIgnoresTheHardenPin(t *testing.T) {
	opts, repoDir, hooksDir := pushHookHarness(t)
	dir, err := gitHooksDir(context.Background(), opts, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if dir != hooksDir {
		t.Fatalf("pinned-only origins must resolve to the default hooks dir, got %q", dir)
	}
}

func TestGitHooksDirHonorsFileConfiguredHooksPath(t *testing.T) {
	repoDir := t.TempDir()
	gitDir := filepath.Join(repoDir, ".git")
	opts := Options{Version: "test", Runner: hooksDirRunner{gitDir: gitDir, fileHooksPath: ".husky"}}
	dir, err := gitHooksDir(context.Background(), opts, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(repoDir, ".husky") {
		t.Fatalf("relative core.hooksPath must resolve against the worktree, got %q", dir)
	}
}

// Real git, real ExecRunner, real hardening: the one test that cannot be
// blinded by a stubbed runner.
func TestInstallPushHookAgainstRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repoDir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	opts := Options{Version: "test", Runner: ExecRunner{}}

	state, path, err := installPushHook(context.Background(), opts, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if state != pushHookInstalled {
		t.Fatalf("state=%v", state)
	}
	resolvedRepo, _ := filepath.EvalSymlinks(repoDir)
	resolvedPath, _ := filepath.EvalSymlinks(filepath.Dir(path))
	if resolvedPath != filepath.Join(resolvedRepo, ".git", "hooks") {
		t.Fatalf("hook must land in the real hooks dir, got %q", path)
	}
	if !strings.Contains(readHook(t, path), pushHookMarker) {
		t.Fatal("hook content missing marker")
	}
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
	if !strings.Contains(content, "# Chain:") {
		t.Fatalf("chain line is unconditional ([ -x ]-guarded): %q", content)
	}
	if _, err := os.Stat(path + pushHookBackupSuffix); !os.IsNotExist(err) {
		t.Fatalf("empty slot must not create a backup: %v", err)
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
	primary := filepath.Join(hooksDir, "pre-push")
	if err := os.WriteFile(primary, []byte(testHostHook), 0o755); err != nil {
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
	if got := readHook(t, primary+pushHookBackupSuffix); got != testHostHook {
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
	primary := filepath.Join(hooksDir, "pre-push")
	if err := os.WriteFile(primary, []byte(testHostHook), 0o755); err != nil {
		t.Fatal(err)
	}
	moved := primary + ".pre-entire"
	ours := pushHookContent("/managed/entire-brain")
	if err := os.WriteFile(moved, []byte(ours), 0o755); err != nil {
		t.Fatal(err)
	}

	state, path, err := installPushHook(context.Background(), opts, repoDir)
	if err != nil || state != pushHookChainedIn || path != moved {
		t.Fatalf("state=%v path=%v err=%v", state, path, err)
	}
	if got := readHook(t, primary); got != testHostHook {
		t.Fatalf("primary must stay the host's: %q", got)
	}
}

func TestUninstallPushHookRestoresBackup(t *testing.T) {
	opts, repoDir, hooksDir := pushHookHarness(t)
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

func TestRefreshPushHookLeavesUninstalledRepoUntouched(t *testing.T) {
	// A bare setup after a disconnect (or on a never-connected repo) must not
	// resurrect or freshly install the hook — install belongs to connect.
	opts, repoDir, hooksDir := pushHookHarness(t)
	primary := filepath.Join(hooksDir, "pre-push")
	if err := os.WriteFile(primary, []byte(testHostHook), 0o755); err != nil {
		t.Fatal(err)
	}

	state, _, err := refreshPushHook(context.Background(), opts, repoDir)
	if err != nil || state != pushHookAbsent {
		t.Fatalf("state=%v err=%v", state, err)
	}
	if got := readHook(t, primary); got != testHostHook {
		t.Fatalf("refresh must not touch a repo without our hook: %q", got)
	}
}

func TestRefreshPushHookUpdatesInstalledHook(t *testing.T) {
	opts, repoDir, hooksDir := pushHookHarness(t)
	primary := filepath.Join(hooksDir, "pre-push")
	stale := "#!/bin/sh\n" + pushHookMarker + "\n'/old/path' memory notify --event push &\n"
	if err := os.WriteFile(primary, []byte(stale), 0o755); err != nil {
		t.Fatal(err)
	}

	state, _, err := refreshPushHook(context.Background(), opts, repoDir)
	if err != nil || state != pushHookInstalled {
		t.Fatalf("state=%v err=%v", state, err)
	}
	if got := readHook(t, primary); strings.Contains(got, "/old/path") {
		t.Fatalf("refresh must rewrite the stale plugin path: %q", got)
	}
}
