package tui

import (
	"bytes"
	"testing"
)

// TestWarnfIsByteStableOnPipes pins the plain form: a non-terminal destination
// must get exactly the bytes the raw fmt.Fprintf it replaced would have written.
func TestWarnfIsByteStableOnPipes(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	Warnf(&buf, "%s is deprecated; use %s", "OLD", "NEW")
	if got, want := buf.String(), "warning: OLD is deprecated; use NEW\n"; got != want {
		t.Errorf("Warnf = %q, want %q", got, want)
	}
}
