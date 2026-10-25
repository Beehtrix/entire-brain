package base

import (
	"path/filepath"
	"testing"
)

func TestRejectExistingSymlinkPathComponentsRejectsBareDotDot(t *testing.T) {
	t.Parallel()
	// root deliberately absent — the first-write case the guard is written to
	// tolerate, and the case where the containment check short-circuits.
	root := filepath.Join(t.TempDir(), "repos")
	if err := RejectExistingSymlinkPathComponents(root, ".."); err == nil {
		t.Fatalf("guard accepted %q, which resolves to the parent of root (%s)", "..", filepath.Dir(root))
	}
}
