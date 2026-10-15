package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// push_hook.go installs the plugin's own git pre-push hook. The host CLI's
// hook installer (entireio/cli strategy/hooks.go) stays untouched: the team
// decided plugin-specific triggering lives in the plugin, chained beside
// whatever hook already owns the slot. The hook body spawns a detached
// `entire-brain memory notify --event push`, which dispatches the throttled
// fact-store upload — so the trail shows memories together with the commits
// the push delivers.

const (
	pushHookName = "pre-push"
	// pushHookMarker identifies OUR hook file. Deliberately distinct from the
	// host CLI's marker: carrying theirs would make `entire enable` treat our
	// file as its own and overwrite it without a backup.
	pushHookMarker = "# entire-brain push hook"
	// pushHookBackupSuffix is where a pre-existing hook moves; our generated
	// chain line invokes it so it keeps running after ours.
	pushHookBackupSuffix = ".pre-entire-brain"
)

// pushHookContent renders our hook. selfPath is this plugin binary's absolute
// path, baked in because git hooks run with the user's PATH: a managed plugin
// install is not on it, and only the `entire` child re-prepends the managed
// dir — a dependency this hook deliberately avoids. The notify runs
// backgrounded with stdin/stdout/stderr detached so it can never consume the
// pre-push ref list, slow the push, or fail it.
func pushHookContent(selfPath string, chained bool) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(pushHookMarker + "\n")
	b.WriteString("# Tells the local entire-brain plugin that a push is transferring this\n")
	b.WriteString("# repo's commits, so it uploads its fact store (detached, best-effort).\n")
	fmt.Fprintf(&b, "if [ -x %s ]; then\n", shellSingleQuote(selfPath))
	fmt.Fprintf(&b, "  %s memory notify --event push </dev/null >/dev/null 2>&1 &\n", shellSingleQuote(selfPath))
	b.WriteString("fi\n")
	if chained {
		b.WriteString("# Chain: run the pre-existing hook with the original stdin and arguments;\n")
		b.WriteString("# its exit code decides the push, exactly as before.\n")
		fmt.Fprintf(&b, "backup=\"$0%s\"\n", pushHookBackupSuffix)
		b.WriteString("if [ -x \"$backup\" ]; then exec \"$backup\" \"$@\"; fi\n")
	}
	return b.String()
}

// shellSingleQuote wraps s in single quotes, escaping embedded single quotes,
// so an install path with spaces or metacharacters cannot change the script.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type pushHookState string

const (
	pushHookInstalled pushHookState = "installed" // our file written or refreshed in the primary slot
	pushHookChainedIn pushHookState = "chained"   // ours is reachable via another tool's chain; left alone
	pushHookUnchanged pushHookState = "unchanged" // primary already ours with current content
)

// installPushHook puts our pre-push hook in place. Rules, mirroring the host
// CLI's installer but for a GUEST of the slot rather than its owner:
//
//   - no hook: write ours (no chain line).
//   - primary carries OUR marker: rewrite only when the content is stale.
//   - OUR marker is reachable at the backup name (the host CLI re-wrapped us —
//     its installer backs up "foreign" hooks and chains to them): do NOTHING.
//     Re-claiming the primary would ping-pong with the host's reconcile; our
//     line still runs via its chain. We refresh the moved copy's content only.
//   - anything else in the slot (the host CLI's hook, husky, a user script,
//     a symlink): move it to the backup name and chain to it. Never chain a
//     file carrying our own marker (self-call), matching the host's guard.
//
// The backup is moved, not copied, so there is exactly one runnable copy of
// the previous hook. A pre-existing DIFFERENT backup rotates to a timestamped
// name that no longer runs, and the install reports it.
func installPushHook(ctx context.Context, opts Options, repoDir string) (pushHookState, string, error) {
	hooksDir, err := gitHooksDir(ctx, opts, repoDir)
	if err != nil {
		return "", "", err
	}
	selfPath, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("resolve plugin path: %w", err)
	}
	primary := filepath.Join(hooksDir, pushHookName)
	backup := primary + pushHookBackupSuffix

	primaryBytes, primaryErr := os.ReadFile(primary)
	primaryExists := primaryErr == nil
	if primaryErr != nil && !os.IsNotExist(primaryErr) {
		// A symlinked hook reads through to its target here; only a genuinely
		// unreadable file stops the install.
		return "", "", fmt.Errorf("read %s: %w", primary, primaryErr)
	}
	primaryContent := string(primaryBytes)

	// Host re-wrapped us: our hook lives under a backup name and the host's
	// chain runs it. Leave the primary alone; refresh our moved copy so a
	// plugin upgrade's new path still reaches a binary.
	if primaryExists && !strings.Contains(primaryContent, pushHookMarker) {
		for _, candidate := range movedPushHookCandidates(hooksDir) {
			data, readErr := os.ReadFile(candidate)
			if readErr != nil || !strings.Contains(string(data), pushHookMarker) {
				continue
			}
			refreshed := pushHookContent(selfPath, strings.Contains(string(data), "# Chain:"))
			if string(data) != refreshed {
				if err := writePushHookFile(candidate, refreshed); err != nil {
					return "", "", err
				}
			}
			return pushHookChainedIn, candidate, nil
		}
	}

	switch {
	case primaryExists && strings.Contains(primaryContent, pushHookMarker):
		chained := fileExecutableExists(backup)
		want := pushHookContent(selfPath, chained)
		if primaryContent == want {
			return pushHookUnchanged, primary, nil
		}
		if err := writePushHookFile(primary, want); err != nil {
			return "", "", err
		}
		return pushHookInstalled, primary, nil

	case primaryExists:
		if existing, err := os.ReadFile(backup); err == nil {
			if string(existing) != primaryContent {
				rotated := backup + "." + time.Now().UTC().Format("20060102T150405Z")
				if err := os.Rename(backup, rotated); err != nil {
					return "", "", fmt.Errorf("rotate stale backup: %w", err)
				}
				fmt.Fprintf(os.Stderr, "entire-brain: %s changed since it was backed up; the older copy was kept as %s and no longer runs\n", pushHookName, rotated)
			} else if err := os.Remove(backup); err != nil {
				return "", "", fmt.Errorf("replace identical backup: %w", err)
			}
		}
		if err := os.Rename(primary, backup); err != nil {
			return "", "", fmt.Errorf("back up existing %s hook: %w", pushHookName, err)
		}
		if err := writePushHookFile(primary, pushHookContent(selfPath, true)); err != nil {
			return "", "", err
		}
		return pushHookInstalled, primary, nil

	default:
		if err := writePushHookFile(primary, pushHookContent(selfPath, false)); err != nil {
			return "", "", err
		}
		return pushHookInstalled, primary, nil
	}
}

// uninstallPushHook removes our hook. When we hold the primary slot, the
// backup (if any) moves back; when the host re-wrapped us, our moved copy is
// replaced by whatever IT had chained to, so the host's chain line keeps
// resolving. Missing pieces are fine: uninstall is idempotent.
func uninstallPushHook(ctx context.Context, opts Options, repoDir string) error {
	hooksDir, err := gitHooksDir(ctx, opts, repoDir)
	if err != nil {
		return err
	}
	primary := filepath.Join(hooksDir, pushHookName)
	backup := primary + pushHookBackupSuffix

	if data, err := os.ReadFile(primary); err == nil && strings.Contains(string(data), pushHookMarker) {
		if _, err := os.Stat(backup); err == nil {
			return os.Rename(backup, primary)
		}
		return os.Remove(primary)
	}
	for _, candidate := range movedPushHookCandidates(hooksDir) {
		data, err := os.ReadFile(candidate)
		if err != nil || !strings.Contains(string(data), pushHookMarker) {
			continue
		}
		if _, err := os.Stat(backup); err == nil {
			return os.Rename(backup, candidate)
		}
		return os.Remove(candidate)
	}
	return nil
}

// movedPushHookCandidates lists where another installer may have moved our
// hook: every pre-push sibling except the primary and our own backup slot.
func movedPushHookCandidates(hooksDir string) []string {
	matches, _ := filepath.Glob(filepath.Join(hooksDir, pushHookName+".*"))
	var out []string
	for _, m := range matches {
		if m == filepath.Join(hooksDir, pushHookName)+pushHookBackupSuffix {
			continue
		}
		out = append(out, m)
	}
	return out
}

func fileExecutableExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// writePushHookFile is temp-and-rename so a symlink sitting at the hook path
// is replaced rather than written through, and a crash never leaves a
// half-written executable hook.
func writePushHookFile(path, content string) error {
	tmp := path + ".entire-brain.tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o755); err != nil { //nolint:gosec // git hooks must be executable
		return err
	}
	return os.Rename(tmp, path)
}

// gitHooksDir resolves the repo's REAL hooks directory. It cannot ask
// `rev-parse --git-path hooks`: the hardened runner pins core.hooksPath to a
// disabled sentinel on every git invocation (git_harden.go), so that answer is
// always the sentinel — installing there fails by construction (/dev/null is a
// character device). Instead, recover the user's effective core.hooksPath from
// `git config --show-origin --get-all`: the pinned entry is labeled
// `command line:`, every file-sourced entry keeps its origin, and git lists
// them in ascending precedence — so the LAST non-command-line entry is the
// setting git would honor without the pin. No entry means the default,
// <git-common-dir>/hooks (the common dir so linked worktrees share one hooks
// directory, exactly as git does).
func gitHooksDir(ctx context.Context, opts Options, repoDir string) (string, error) {
	commonOut, err := gitScalar(ctx, opts.Runner, repoDir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("resolve git dir: %w", err)
	}
	commonDir := strings.TrimSpace(commonOut)
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(repoDir, commonDir)
	}

	dir := filepath.Join(commonDir, "hooks")
	// The pin guarantees at least one entry, so a failure here is a real error,
	// not "key unset".
	originsOut, err := gitScalar(ctx, opts.Runner, repoDir, "config", "--show-origin", "--get-all", "core.hooksPath")
	if err != nil {
		return "", fmt.Errorf("resolve core.hooksPath: %w", err)
	}
	for _, line := range strings.Split(originsOut, "\n") {
		origin, value, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || strings.HasPrefix(origin, "command line") || strings.TrimSpace(value) == "" {
			continue
		}
		hooksPath := strings.TrimSpace(value)
		if filepath.IsAbs(hooksPath) {
			dir = hooksPath
		} else {
			// git resolves a relative core.hooksPath against the worktree.
			dir = filepath.Join(repoDir, hooksPath)
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // hooks dirs are 0755 by convention
		return "", err
	}
	return dir, nil
}
