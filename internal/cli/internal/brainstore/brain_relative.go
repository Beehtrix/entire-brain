package brainstore

import (
	"fmt"
	"os"
	"path/filepath"

	clibase "github.com/entireio/entire-brain/internal/cli/internal/base"
)

// WriteBrainRelativeFileAtomic writes a brain-relative file through the shared
// path-safety checks (no symlinked brain root, no symlinked path components)
// and the atomic writer, creating parent directories as needed.
func WriteBrainRelativeFileAtomic(brainDir, rel string, data []byte, perm os.FileMode) error {
	if err := clibase.RejectSymlinkedBrainRoot(brainDir); err != nil {
		return err
	}
	clean, err := clibase.CleanBrainRelativePath(rel)
	if err != nil {
		return err
	}
	dir := filepath.Dir(clean)
	if dir != "." {
		if err := clibase.RejectExistingSymlinkPathComponents(brainDir, dir); err != nil {
			return err
		}
	}
	abs := filepath.Join(brainDir, clean)
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return err
	}
	if err := clibase.RejectExistingSymlinkPathComponents(brainDir, clean); err != nil {
		return err
	}
	return WriteFileAtomic(abs, data, perm)
}

// RemoveBrainRelativeFile removes a brain-relative file through the same
// path-safety checks as the atomic writer. A missing file is not an error.
func RemoveBrainRelativeFile(brainDir, rel string) error {
	if err := clibase.RejectSymlinkedBrainRoot(brainDir); err != nil {
		return err
	}
	clean, err := clibase.CleanBrainRelativePath(rel)
	if err != nil {
		return err
	}
	if err := clibase.RejectExistingSymlinkPathComponents(brainDir, clean); err != nil {
		return err
	}
	err = os.Remove(filepath.Join(brainDir, clean))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RemoveBrainRelativeFileExpected removes a file only when it is still the
// exact inode/file identity observed by a descriptor-bound inventory. Privacy
// cleanup uses this after validating content so a raced replacement is never
// unlinked under the authority of the original file.
func RemoveBrainRelativeFileExpected(brainDir, rel string, expected os.FileInfo) error {
	if expected == nil {
		return fmt.Errorf("memory_state_unsafe: missing expected file identity for %s", rel)
	}
	if err := clibase.RejectSymlinkedBrainRoot(brainDir); err != nil {
		return err
	}
	clean, err := clibase.CleanBrainRelativePath(rel)
	if err != nil {
		return err
	}
	if err := clibase.RejectExistingSymlinkPathComponents(brainDir, clean); err != nil {
		return err
	}
	path := filepath.Join(brainDir, clean)
	current, err := MemoryStateLstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("memory_state_unsafe: expected file disappeared before removal: %s", filepath.ToSlash(clean))
		}
		return err
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(expected, current) {
		return fmt.Errorf("memory_state_unsafe: file changed before removal: %s", filepath.ToSlash(clean))
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return nil
}
