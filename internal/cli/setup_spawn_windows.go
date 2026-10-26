//go:build windows

package cli

import (
	"syscall"
)

// detachedSysProcAttr has no session concept to use on Windows; the child is
// already detached from the console because its stdio is redirected and the
// parent releases the process handle.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// noFollowOpenFlag has no Windows equivalent in the syscall package; symlink
// creation there requires a privilege the attack this guards against does not
// have by default.
const noFollowOpenFlag = 0
