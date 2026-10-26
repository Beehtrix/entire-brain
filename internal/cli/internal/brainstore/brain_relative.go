package brainstore

import (
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
