//go:build !windows

package base

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func FileLockOpenFlags() int {
	return unix.O_NOFOLLOW
}

func RejectOpenFileHardlink(path string, _ *os.File, info os.FileInfo, label string) error {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
		return fmt.Errorf("%s %w: %s", label, ErrFileHardlinked, path)
	}
	return nil
}
