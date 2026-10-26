//go:build windows

package base

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func FileLockOpenFlags() int {
	return 0
}

func RejectOpenFileHardlink(path string, f *os.File, _ os.FileInfo, label string) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return err
	}
	if info.NumberOfLinks > 1 {
		return fmt.Errorf("%s %w: %s", label, ErrFileHardlinked, path)
	}
	return nil
}
