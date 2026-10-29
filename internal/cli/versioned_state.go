package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
)

// removeCheckedMemoryStateFile removes only the same regular file identity
// that was inspected and accepted by validate. Brain writers serialize on the
// Brain lock; the identity checks also keep unsafe aliases from turning an
// authorized cleanup into an unlink of unclassified state.
func removeCheckedMemoryStateFile(brainDir, rel, label string, maxBytes int64, validate func([]byte) error) error {
	return removeCheckedMemoryStateFileValidated(brainDir, rel, label, func(before os.FileInfo) (bool, error) {
		data, present, err := brainstore.ReadMemoryStateFileExpected(brainDir, rel, label, maxBytes, before)
		if err != nil || !present {
			return present, err
		}
		return true, validate(data)
	})
}

func removeCheckedMemoryStateFileValidated(brainDir, rel, label string, validate func(os.FileInfo) (bool, error)) error {
	clean, err := base.CleanBrainRelativePath(rel)
	if err != nil {
		return fmt.Errorf("%s: %s has an unsafe path: %w", brainstore.StateUnsafeCode, label, err)
	}
	path := filepath.Join(brainDir, clean)
	before, err := brainstore.MemoryStateLstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("%s: inspect %s: %w", brainstore.StateCorruptCode, label, err)
	}
	present, err := validate(before)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	after, err := brainstore.MemoryStateLstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("%s: re-inspect %s: %w", brainstore.StateCorruptCode, label, err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return fmt.Errorf("%s: %s changed before removal", brainstore.StateUnsafeCode, label)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
