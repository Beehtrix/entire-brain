//go:build unix

package cli

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO at a hook path must fail the read, not block it: os.ReadFile on a
// pipe with no writer hangs before the first byte.
func TestInstallPushHookRefusesFIFOHook(t *testing.T) {
	opts, repoDir, hooksDir := pushHookHarness(t)
	primary := filepath.Join(hooksDir, "pre-push")
	if err := syscall.Mkfifo(primary, 0o755); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := installPushHook(context.Background(), opts, repoDir)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("install must refuse a FIFO in the hook slot")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("install blocked on the FIFO")
	}
}
