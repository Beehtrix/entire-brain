//go:build !windows

package brainstore

import (
	"os"
	"syscall"
)

// ProcessAlive reports whether a recorded backfill pid is still running.
// Signal 0 performs the permission/existence check without delivering anything.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
