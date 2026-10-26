package patterns

import (
	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
)

// DerivedReadGate prevents the tombstone-first cleanup window from exposing
// corpus-derived content. A valid tombstone does not permanently disable
// patterns: once the authoritative full verification is clean, reads resume
// normally. verifyClean is the caller's full derived-projection verifier; it
// runs only while tombstones exist.
func DerivedReadGate(brainDir string, verifyClean func(string) (bool, error)) (bool, error) {
	// The corpus is optional, but when present its main database and WAL must be
	// ordinary Brain-owned files even when there are no tombstones. Without this
	// unconditional check, best-effort pattern loaders could hide an unsafe alias
	// as an empty result on status/get/skill surfaces.
	if err := ValidateCorpusReadSafety(brainDir); err != nil {
		return false, err
	}
	stones, _, err := brainstore.LoadSessionTombstonesChecked(brainDir)
	if err != nil {
		return false, err
	}
	if len(stones.Excluded) == 0 {
		return true, nil
	}
	return verifyClean(brainDir)
}
