package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
)

// planTranscriptCacheSweep proves each live cached transcript can be recovered
// locally before the export switches storage modes. A missing/promisor-only
// source must never turn migration into loss of the only readable copy.
// Unlisted files are stale exports. Exclusion is reversible, so excluded
// sessions need the same recovery proof as every other legacy cached session.
func planTranscriptCacheSweep(ctx context.Context, runner CommandRunner, brainDir, repoDir string) (*historySessionInventory, error) {
	if _, err := os.Lstat(brainDir); os.IsNotExist(err) {
		return nil, nil
	}
	inventory, err := collectHistorySessionInventoryMode(ctx, brainDir, true)
	if errors.Is(err, errHistorySessionsRootMissing) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = inventory.Close()
		}
	}()
	manifest, err := loadBrainManifest(brainDir)
	if err != nil {
		return nil, err
	}
	sessions := historySessionByTranscriptPath(manifest)
	if _, err := loadSessionReadGuard(brainDir, manifest); err != nil {
		return nil, err
	}
	store := newTranscriptStore(runner, brainDir)
	// Persistent brains are shared by worktrees; the previous exporter's
	// recorded worktree may have been removed since the cache was written.
	store.repoDir = repoDir
	var retained map[string]bool
	for _, file := range inventory.files {
		_, live := sessions[file.Rel]
		if checkpointBackedSessions(manifest) || !live {
			continue
		}
		location, snapshot, err := store.lookupLocation(ctx, file.Rel)
		if err != nil {
			return nil, fmt.Errorf("retain transcript cache: %w", err)
		}
		data, err := readSnapshotTranscriptFromSource(ctx, runner, snapshot, location.sourceKey, location.sourcePath)
		if err != nil {
			return nil, fmt.Errorf("retain transcript cache for %s: %w", file.Rel, err)
		}
		cached, _, err := readHistorySessionInventoryFile(ctx, inventory, file)
		if err != nil {
			return nil, err
		}
		switch {
		case bytes.HasPrefix(data, cached):
			// The checkpoint holds every cached byte (equal, or the
			// append-only transcript ran ahead of the last disk export), so
			// the file is recoverable and safe to sweep.
		case bytes.HasPrefix(cached, data):
			// The disk export ran ahead of the last checkpoint. Keep the
			// file out of the sweep instead of blocking every future export;
			// the next checkpoint reconciles it.
			fmt.Fprintf(os.Stderr, "warning: retain transcript cache: cached transcript is newer than its checkpoint for %s; keeping the cached copy\n", file.Rel)
			if retained == nil {
				retained = make(map[string]bool)
			}
			retained[file.Rel] = true
		default:
			return nil, fmt.Errorf("retain transcript cache: checkpoint bytes differ from %s", file.Rel)
		}
	}
	if len(retained) > 0 {
		kept := inventory.files[:0]
		for _, file := range inventory.files {
			if !retained[file.Rel] {
				kept = append(kept, file)
			}
		}
		inventory.files = kept
	}
	success = true
	return inventory, nil
}

// sweepTranscriptCache removes only the descriptor-bound files validated by the
// migration preflight. The caller holds the brain write lock and has published
// the checkpoint-backed manifest. Directories are removed only when empty.
func sweepTranscriptCache(ctx context.Context, inventory *historySessionInventory) error {
	if inventory == nil {
		return nil
	}
	current, err := collectHistorySessionInventoryMode(ctx, inventory.brainDir, true)
	if err != nil {
		return err
	}
	unchanged := inventory.sameMembership(current)
	_ = current.Close()
	if !unchanged {
		return fmt.Errorf("%s: transcript cache changed before migration sweep", memoryErrSourceStale)
	}
	if err := inventory.validateAll(ctx); err != nil {
		return err
	}
	for _, file := range inventory.files {
		if err := inventory.validateFileMembership(ctx, file); err != nil {
			return err
		}
		if err := inventory.root.Remove(file.Rel); err != nil {
			return err
		}
	}
	for i := len(inventory.entries) - 1; i >= 0; i-- {
		if inventory.entries[i].Mode.IsDir() {
			// A nonempty directory may contain non-transcript artifacts.
			_ = inventory.root.Remove(inventory.entries[i].Rel)
		}
	}
	return nil
}
