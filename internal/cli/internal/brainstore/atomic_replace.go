package brainstore

import (
	"fmt"
)

// AtomicReplaceDurabilityError means the destination rename is already live,
// but its parent-directory durability barrier failed. Callers must not assume
// the old destination survived merely because the atomic writer returned an
// error; transactional callers can verify/repair the live bytes and retry the
// directory sync.
type AtomicReplaceDurabilityError struct {
	Path string
	Err  error
}

func (e *AtomicReplaceDurabilityError) Error() string {
	return fmt.Sprintf("replacement of %s is live but not durably synced: %v", e.Path, e.Err)
}

func (e *AtomicReplaceDurabilityError) Unwrap() error { return e.Err }
