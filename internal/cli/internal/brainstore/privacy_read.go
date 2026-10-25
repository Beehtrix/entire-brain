package brainstore

import (
	"errors"
	"fmt"
	clibase "github.com/entireio/entire-brain/internal/cli/internal/base"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const privacyInventoryMaxEntries = 10_000

// PrivacyArtifactInfo is the common fail-closed leaf check used by privacy
// inventory and verification. In particular, it lstat's before any open so a
// FIFO/device/socket can never make a checked privacy read block indefinitely.
func PrivacyArtifactInfo(brainDir, rel, label string) (os.FileInfo, bool, error) {
	clean, err := clibase.CleanBrainRelativePath(filepath.ToSlash(strings.TrimSpace(rel)))
	if err != nil {
		return nil, false, fmt.Errorf("%s: %s has an unsafe path %q: %w", StateUnsafeCode, label, rel, err)
	}
	if err := clibase.RejectExistingSymlinkPathComponents(brainDir, clean); err != nil {
		return nil, false, fmt.Errorf("%s: inspect %s %s: %w", StateUnsafeCode, label, filepath.ToSlash(clean), err)
	}
	info, err := PrivacyLstat(filepath.Join(brainDir, clean))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%s: inspect %s %s: %w", StateCorruptCode, label, filepath.ToSlash(clean), err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s: %s is not a regular file: %s", StateUnsafeCode, label, filepath.ToSlash(clean))
	}
	return info, true, nil
}

// ReadPrivacyArtifact reads a known brain-relative regular file through an
// injectable, bounded reader. Missing is reported separately; every other
// stat/read/type failure is an error rather than an empty privacy state.
func ReadPrivacyArtifact(brainDir, rel, label string, maxBytes int64) ([]byte, bool, error) {
	_, present, err := PrivacyArtifactInfo(brainDir, rel, label)
	if err != nil || !present {
		return nil, present, err
	}
	clean, _ := clibase.CleanBrainRelativePath(filepath.ToSlash(strings.TrimSpace(rel)))
	path := filepath.Join(brainDir, clean)
	// O_NONBLOCK on Unix prevents an attacker-controlled regular-to-FIFO swap
	// between the lstat above and this open from hanging a privacy gate. It is a
	// no-op on Windows and has no effect on regular-file reads.
	f, err := PrivacyOpen(path, os.O_RDONLY|clibase.FileLockOpenFlags()|clibase.MemoryStateReadOpenFlags(), 0)
	if err != nil {
		return nil, true, fmt.Errorf("%s: open %s %s: %w", StateCorruptCode, label, filepath.ToSlash(clean), err)
	}
	defer f.Close()
	if err := clibase.RejectOpenFileAlias(path, f, label); err != nil {
		return nil, true, fmt.Errorf("%s: validate opened %s %s: %w", StateUnsafeCode, label, filepath.ToSlash(clean), err)
	}
	data, err := PrivacyReadAll(f, maxBytes, path)
	if err != nil {
		return nil, true, fmt.Errorf("%s: read %s %s: %w", StateCorruptCode, label, filepath.ToSlash(clean), err)
	}
	return data, true, nil
}

func ReadPrivacyDirectory(brainDir, rel, label string) ([]os.DirEntry, bool, error) {
	clean, err := clibase.CleanBrainRelativePath(filepath.ToSlash(strings.TrimSpace(rel)))
	if err != nil {
		return nil, false, fmt.Errorf("%s: %s has an unsafe path %q: %w", StateUnsafeCode, label, rel, err)
	}
	if err := clibase.RejectExistingSymlinkPathComponents(brainDir, clean); err != nil {
		return nil, false, fmt.Errorf("%s: inspect %s %s: %w", StateUnsafeCode, label, filepath.ToSlash(clean), err)
	}
	info, err := PrivacyLstat(filepath.Join(brainDir, clean))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%s: inspect %s %s: %w", StateCorruptCode, label, filepath.ToSlash(clean), err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, false, fmt.Errorf("%s: %s is not a directory: %s", StateUnsafeCode, label, filepath.ToSlash(clean))
	}
	path := filepath.Join(brainDir, clean)
	dir, err := PrivacyOpen(path, os.O_RDONLY|clibase.FileLockOpenFlags()|clibase.MemoryStateReadOpenFlags(), 0)
	if err != nil {
		return nil, true, fmt.Errorf("%s: open %s %s: %w", StateCorruptCode, label, filepath.ToSlash(clean), err)
	}
	defer dir.Close()
	pathInfo, err := PrivacyLstat(path)
	if err != nil {
		return nil, true, fmt.Errorf("%s: validate opened %s %s: %w", StateCorruptCode, label, filepath.ToSlash(clean), err)
	}
	dirInfo, err := dir.Stat()
	if err != nil {
		return nil, true, fmt.Errorf("%s: validate opened %s %s: %w", StateCorruptCode, label, filepath.ToSlash(clean), err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(pathInfo, dirInfo) || !dirInfo.IsDir() {
		return nil, true, fmt.Errorf("%s: %s changed or became unsafe while opening: %s", StateUnsafeCode, label, filepath.ToSlash(clean))
	}
	entries, err := PrivacyReadDir(dir, privacyInventoryMaxEntries+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, true, fmt.Errorf("%s: read %s %s: %w", StateCorruptCode, label, filepath.ToSlash(clean), err)
	}
	if len(entries) > privacyInventoryMaxEntries {
		return nil, true, fmt.Errorf("%s: %s %s exceeds %d entries", StateCorruptCode, label, filepath.ToSlash(clean), privacyInventoryMaxEntries)
	}
	return entries, true, nil
}

var (
	PrivacyLstat   = os.Lstat
	PrivacyOpen    = os.OpenFile
	PrivacyReadAll = clibase.SafeReadAll
	PrivacyReadDir = func(dir *os.File, n int) ([]os.DirEntry, error) { return dir.ReadDir(n) }
)
