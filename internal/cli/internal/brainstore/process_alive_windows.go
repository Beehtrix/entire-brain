//go:build windows

package brainstore

import (
	"os"
)

// ProcessAlive reports whether a recorded backfill pid is still running.
// os.FindProcess opens a handle on Windows, so an error means the process is
// gone.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.FindProcess(pid)
	return err == nil
}
