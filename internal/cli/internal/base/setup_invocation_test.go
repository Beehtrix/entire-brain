package base

import (
	"testing"
)

// TestSetupCommandPrefixIgnoresAnEmptyMarker: exporting NAME= to clear a
// variable is how a reader turns a signal off, so a present-but-empty
// ENTIRE_CLI_VERSION is not a plugin launch.
func TestSetupCommandPrefixIgnoresAnEmptyMarker(t *testing.T) {
	t.Parallel()
	empty := func(string) (string, bool) { return "  ", true }
	if got := SetupCommandPrefix(empty); got != SetupBrainBinaryName {
		t.Fatalf("a blank marker is not a signal: got %q, want %q", got, SetupBrainBinaryName)
	}
	set := func(string) (string, bool) { return "1.2.3", true }
	if got := SetupCommandPrefix(set); got != SetupPluginCommand {
		t.Fatalf("the host's unconditional marker is the signal: got %q, want %q", got, SetupPluginCommand)
	}
	if got := SetupCommandPrefix(nil); got != SetupBrainBinaryName {
		t.Fatalf("no lookup means no signal: got %q", got)
	}
}

// TestBothSpellingsAreTheSameWidth: the summary and Next blocks are
// column-aligned, and one of these strings is substituted into every one of
// them. A future rename of the plugin verb must fail loudly here rather than
// silently clipping a column.
func TestBothSpellingsAreTheSameWidth(t *testing.T) {
	t.Parallel()
	if len(SetupBrainBinaryName) != len(SetupPluginCommand) {
		t.Fatalf("the two spellings must stay the same width or every width budget in setup's output shifts: %q (%d) vs %q (%d)",
			SetupBrainBinaryName, len(SetupBrainBinaryName), SetupPluginCommand, len(SetupPluginCommand))
	}
}
