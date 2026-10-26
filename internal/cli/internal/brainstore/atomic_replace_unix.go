//go:build !windows

package brainstore

import (
	"os"
	"path/filepath"
)

// AtomicReplaceBeforeParentSync is a deterministic durability-failure seam.
// Production leaves it as a no-op; Unix tests use it to prove callers handle
// the rename-succeeded/sync-failed outcome rather than assuming no mutation.
var AtomicReplaceBeforeParentSync = func(string) error { return nil }

func replaceFileAtomic(tmpName, path string) error {
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if err := AtomicReplaceBeforeParentSync(path); err != nil {
		return &AtomicReplaceDurabilityError{Path: path, Err: err}
	}
	if err := SyncParentDir(path); err != nil {
		return &AtomicReplaceDurabilityError{Path: path, Err: err}
	}
	return nil
}

func SyncParentDir(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// SyncPinnedDirectory persists directory-entry operations through the exact
// descriptor-rooted directory used for publication, even if its pathname is
// concurrently replaced.
func SyncPinnedDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
