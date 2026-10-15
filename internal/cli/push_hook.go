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
func pushHookContent(selfPath string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(pushHookMarker + "\n")
	b.WriteString("# Tells the local entire-brain plugin that a push is transferring this\n")
	b.WriteString("# repo's commits, so it uploads its fact store (detached, best-effort).\n")
	fmt.Fprintf(&b, "if [ -x %s ]; then\n", shellSingleQuote(selfPath))
	fmt.Fprintf(&b, "  %s memory notify --event push </dev/null >/dev/null 2>&1 &\n", shellSingleQuote(selfPath))
	b.WriteString("fi\n")
	// The chain line is unconditional: [ -x ] makes it a no-op when no backup
	// exists, and one content variant means install never has to re-derive
	// whether a hook was chained. $0-relative, so a copy the host CLI moves
	// aside stops chaining instead of double-running the host's hook.
	b.WriteString("# Chain: run the pre-existing hook with the original stdin and arguments;\n")
	b.WriteString("# its exit code decides the push, exactly as before.\n")
	fmt.Fprintf(&b, "backup=\"$0%s\"\n", pushHookBackupSuffix)
	b.WriteString("if [ -x \"$backup\" ]; then exec \"$backup\" \"$@\"; fi\n")
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
	hooksDir, inRepo, err := gitHooksDir(ctx, opts, repoDir)
	if err != nil {
		return "", "", err
	}
	if !inRepo {
		// A shared core.hooksPath (a global ~/.githooks, say) serves EVERY
		// repository at once: a hook written there fires for repos that never
		// connected, and a later uninstall on one repo would strip it for all
		// of them. Managing per-repo state in a shared directory is wrong in
		// both directions, so refuse; uploads ride the daemon tick instead.
		return pushHookSharedDir, hooksDir, nil
	}
	selfPath, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("resolve plugin path: %w", err)
	}
	primary := filepath.Join(hooksDir, pushHookName)
	backup := primary + pushHookBackupSuffix

	primaryBytes, primaryErr := readHookFile(primary)
	primaryExists := primaryErr == nil
	if primaryErr != nil && !os.IsNotExist(primaryErr) {
		// A symlinked hook reads through to its target here; only a genuinely
		// unreadable file (or a special file) stops the install.
		return "", "", fmt.Errorf("read %s: %w", primary, primaryErr)
	}
	primaryContent := string(primaryBytes)

	switch {
	case primaryExists && strings.Contains(primaryContent, pushHookMarker):
		want := pushHookContent(selfPath)
		if primaryContent == want {
			return pushHookUnchanged, primary, nil
		}
		if err := writePushHookFile(primary, want); err != nil {
			return "", "", err
		}
		return pushHookInstalled, primary, nil

	case primaryExists:
		// Host re-wrapped us: our hook lives under a backup name and the
		// host's chain runs it. Leave the primary alone; refresh our moved
		// copy so a plugin upgrade's new path still reaches a binary.
		for _, candidate := range movedPushHookCandidates(hooksDir) {
			data, readErr := readHookFile(candidate)
			if readErr != nil || !strings.Contains(string(data), pushHookMarker) {
				continue
			}
			if refreshed := pushHookContent(selfPath); string(data) != refreshed {
				if err := writePushHookFile(candidate, refreshed); err != nil {
					return "", "", err
				}
			}
			return pushHookChainedIn, candidate, nil
		}
		if existing, err := readHookFile(backup); err == nil {
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
		if err := writePushHookFile(primary, pushHookContent(selfPath)); err != nil {
			// Put the original hook back: leaving the slot empty would silently
			// disable the host's pre-push (privacy filtering rides on it).
			if rbErr := os.Rename(backup, primary); rbErr != nil {
				return "", "", fmt.Errorf("write %s hook: %w; original hook stranded at %s: %v", pushHookName, err, backup, rbErr)
			}
			return "", "", err
		}
		return pushHookInstalled, primary, nil

	default:
		if err := writePushHookFile(primary, pushHookContent(selfPath)); err != nil {
			return "", "", err
		}
		return pushHookInstalled, primary, nil
	}
}

// pushHookAbsent reports that no hook of ours exists anywhere; refresh-only
// callers (setup) leave the repo untouched in that case.
const pushHookAbsent pushHookState = "absent"

// pushHookSharedDir reports that core.hooksPath points outside this repo —
// a directory shared across repositories — where per-repo hook management
// must not write or delete anything.
const pushHookSharedDir pushHookState = "shared-hooks-dir"

// refreshPushHook updates an ALREADY-INSTALLED hook (ours in the primary slot
// or moved aside by the host CLI) and never installs a fresh one. Setup owns
// the upgrade beat — the baked selfPath goes stale when the plugin binary
// moves, and the hook's [ -x ] guard turns that into a silent no-op — but
// fresh installs belong to connect: a bare `setup` after a disconnect must
// not resurrect the hook, and setup on a never-connected repo must not move
// other tools' hooks around for a feature that is gated off.
func refreshPushHook(ctx context.Context, opts Options, repoDir string) (pushHookState, string, error) {
	hooksDir, inRepo, err := gitHooksDir(ctx, opts, repoDir)
	if err != nil {
		return "", "", err
	}
	if !inRepo {
		return pushHookSharedDir, hooksDir, nil
	}
	primary := filepath.Join(hooksDir, pushHookName)
	ours := false
	if data, readErr := readHookFile(primary); readErr == nil && strings.Contains(string(data), pushHookMarker) {
		ours = true
	} else {
		for _, candidate := range movedPushHookCandidates(hooksDir) {
			if data, readErr := readHookFile(candidate); readErr == nil && strings.Contains(string(data), pushHookMarker) {
				ours = true
				break
			}
		}
	}
	if !ours {
		return pushHookAbsent, primary, nil
	}
	return installPushHook(ctx, opts, repoDir)
}

// uninstallPushHook removes our hook. When we hold the primary slot, the
// backup (if any) moves back; when the host re-wrapped us, our moved copy is
// replaced by whatever IT had chained to, so the host's chain line keeps
// resolving. Missing pieces are fine: uninstall is idempotent.
func uninstallPushHook(ctx context.Context, opts Options, repoDir string) error {
	hooksDir, inRepo, err := gitHooksDir(ctx, opts, repoDir)
	if err != nil {
		return err
	}
	if !inRepo {
		// Install refuses shared dirs, so there is nothing of ours to remove —
		// and removing anything there would act on every repo sharing the dir.
		return nil
	}
	primary := filepath.Join(hooksDir, pushHookName)
	backup := primary + pushHookBackupSuffix

	if data, err := readHookFile(primary); err == nil && strings.Contains(string(data), pushHookMarker) {
		if _, err := os.Stat(backup); err == nil {
			// Remove first: on Windows, rename cannot replace an existing file.
			if err := os.Remove(primary); err != nil {
				return err
			}
			return os.Rename(backup, primary)
		}
		return os.Remove(primary)
	}
	for _, candidate := range movedPushHookCandidates(hooksDir) {
		data, err := readHookFile(candidate)
		if err != nil || !strings.Contains(string(data), pushHookMarker) {
			continue
		}
		// Host re-wrapped us: the live chain is host-primary → our moved copy;
		// our old backup (the host hook we once wrapped) is a DORMANT stale
		// copy — renaming it into the candidate slot would put a second host
		// pre-push back into the live chain and run it twice. Delete both; the
		// host's chain call is [ -x ]-guarded, so a missing chain target is a
		// clean no-op.
		if err := os.Remove(candidate); err != nil {
			return err
		}
		if _, statErr := os.Stat(backup); statErr == nil {
			return os.Remove(backup)
		}
		return nil
	}
	return nil
}

// readHookFile reads a hook path after confirming it resolves to a regular
// file (Stat follows symlinks, so a symlinked hook still reads through).
// os.ReadFile alone would block forever on a FIFO or device sitting at a
// repo-controlled hook path.
func readHookFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.ReadFile(path)
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

// writePushHookFile is atomic (temp-and-rename via writeFileAtomic) so a
// symlink sitting at the hook path is replaced rather than written through,
// a crash never leaves a half-written executable hook, and the dot-prefixed
// random temp name can never collide with a concurrent installer or match
// movedPushHookCandidates' glob.
func writePushHookFile(path, content string) error {
	return writeFileAtomic(path, []byte(content), 0o755) //nolint:gosec // git hooks must be executable
}

// gitHooksDir is realGitHooksDir (git_harden.go — the resolver lives beside
// the pin it reads around) plus creation: installers need the directory to
// exist.
func gitHooksDir(ctx context.Context, opts Options, repoDir string) (string, bool, error) {
	dir, inRepo, err := realGitHooksDir(ctx, opts.Runner, repoDir)
	if err != nil {
		return "", false, err
	}
	if !inRepo {
		// Never create a shared hooks directory on a repo's behalf.
		return dir, false, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // hooks dirs are 0755 by convention
		return "", false, err
	}
	return dir, true, nil
}
