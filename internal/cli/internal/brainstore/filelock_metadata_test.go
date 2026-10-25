package brainstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A pid and a timestamp have been stamped on every lock acquisition since the
// package existed and nothing ever read them, so a contended brain said only
// which file it could not lock -- the one thing the caller already knew -- and
// withheld the only thing that identifies the culprit.
func TestLockTimeoutNamesTheProcessHoldingTheLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "write.lock")
	held, err := AcquireFileLock(path, "brain_locked", time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer func() { _ = held.Close() }()

	// A second acquisition from anywhere -- another goroutine here, another
	// process in production -- must be told who is in the way.
	done := make(chan error, 1)
	go func() {
		_, blockedErr := AcquireFileLock(path, "brain_locked", 20*time.Millisecond)
		done <- blockedErr
	}()
	blocked := <-done
	if !errors.Is(blocked, ErrFileLockTimeout) {
		t.Fatalf("expected a lock timeout, got %v", blocked)
	}
	if !strings.Contains(blocked.Error(), fmt.Sprintf("pid %d", os.Getpid())) {
		t.Fatalf("the timeout must name the holder; got %q", blocked.Error())
	}
	if !strings.Contains(blocked.Error(), path) {
		t.Fatalf("the timeout must still name the lock; got %q", blocked.Error())
	}
}

// The regression #259 caught on Windows shard 3, pinned as a property rather
// than a message: the owner metadata must be readable BY ANOTHER READER WHILE
// THE LOCK IS HELD, which is the only moment it is worth reading.
//
// Windows byte-range locks are mandatory, so while a holder exists a read that
// overlaps the locked byte fails with ERROR_LOCK_VIOLATION. Keeping the
// metadata at offset 0 of the lock file therefore made it unreadable on exactly
// the platform and exactly the instant that mattered, while POSIX's advisory
// flock let every other shard pass. This runs on every platform -- a skip on
// the one platform where the behaviour differs is how the defect hid in the
// first place.
func TestLockOwnerMetadataIsReadableWhileTheLockIsHeld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "write.lock")
	held, err := AcquireFileLock(path, "brain_locked", time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer func() { _ = held.Close() }()

	if got := describeFileLockHolder(path); !strings.Contains(got, fmt.Sprintf("pid %d", os.Getpid())) {
		t.Fatalf("the holder must be readable while the lock is held; got %q", got)
	}
	// And it must not be INSIDE the locked file -- that is the arrangement that
	// cannot be read on Windows. Stat rather than read: stat inspects metadata
	// and never touches the locked byte range.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat lock: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("the lock file must stay empty; owner metadata belongs in the sidecar, found %d bytes", info.Size())
	}
	if _, err := os.Stat(path + FileLockOwnerSuffix); err != nil {
		t.Fatalf("the owner sidecar must exist beside the lock: %v", err)
	}
}

// The message must never be worse than the one it replaces: metadata naming a
// process that is gone describes an innocent bystander, because flock is
// released by the kernel the moment its holder dies.
func TestLockHolderDescriptionIgnoresUnusableMetadata(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cases := map[string]string{
		"dead holder": "pid 2147483646\ncreated_at=2020-01-01T00:00:00Z\n",
		"no pid":      "created_at=2020-01-01T00:00:00Z\n",
		"garbage":     "\x00\x01\x02not a lock file at all",
		"empty":       "",
	}
	for name, contents := range cases {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".lock")
		if err := os.WriteFile(path+FileLockOwnerSuffix, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := describeFileLockHolder(path); got != "" {
			t.Fatalf("%s must describe no holder, got %q", name, got)
		}
	}
	if got := describeFileLockHolder(filepath.Join(dir, "absent.lock")); got != "" {
		t.Fatalf("a missing lock file must describe no holder, got %q", got)
	}
}
