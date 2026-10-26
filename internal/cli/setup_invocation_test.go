package cli

import (
	"strings"
	"testing"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
)

// TestSetupPrintsTheCommandTheReaderTyped: the brain ships as an external
// command of the Entire CLI, so a reader who types `entire brain setup` was
// answered with a Next block spelled `entire-brain overview` — and on a managed
// plugin install that spelling does not resolve in their shell at all.
//
// Reverting setup_render.go's Next block to string literals still compiles, and
// fails here.
func TestSetupPrintsTheCommandTheReaderTyped(t *testing.T) {
	t.Setenv(base.EnvCLIVersion, "1.2.3")
	f := newSetupTestFixture(t)
	printed := runSetupForTest(t, f, defaultSetupOptions(), &recordedSetup{})

	for _, want := range []string{
		base.SetupPluginCommand + " overview",
		base.SetupPluginCommand + ` brief "<task>"`,
		base.SetupPluginCommand + " status",
	} {
		if !strings.Contains(printed, want) {
			t.Fatalf("a reader who typed `%s setup` must be answered in that spelling; %q missing from:\n%s", base.SetupPluginCommand, want, printed)
		}
	}
	if strings.Contains(printed, base.SetupBrainBinaryName+" overview") {
		t.Fatalf("setup must not hand a plugin reader a command their shell cannot resolve:\n%s", printed)
	}
}

// TestSetupKeepsTheStandaloneSpellingWithoutTheHostSignal is the other half:
// a reader who ran this binary directly may not have the Entire CLI at all, so
// the standalone name stays the honest answer.
func TestSetupKeepsTheStandaloneSpellingWithoutTheHostSignal(t *testing.T) {
	t.Setenv(base.EnvCLIVersion, "")
	f := newSetupTestFixture(t)
	printed := runSetupForTest(t, f, defaultSetupOptions(), &recordedSetup{})

	if !strings.Contains(printed, base.SetupBrainBinaryName+" overview") {
		t.Fatalf("with no host signal setup must print its own name:\n%s", printed)
	}
	if strings.Contains(printed, base.SetupPluginCommand+" overview") {
		t.Fatalf("setup must not invent a host CLI the reader may not have:\n%s", printed)
	}
}

// TestSetupHintsFollowTheReadersSpelling sweeps the hint strings, which are the
// other place setup names a command to run and which were spelled with the
// standalone name unconditionally.
func TestSetupHintsFollowTheReadersSpelling(t *testing.T) {
	t.Parallel()
	for name, hint := range map[string]func(string) string{
		"repo key mismatch": setupRepoKeyMismatchHint,
		"dirty worktree":    setupDirtyWorktreeHint,
		"no sessions":       setupNoSessionsHint,
	} {
		text := hint(base.SetupPluginCommand)
		if strings.Contains(text, base.SetupBrainBinaryName+" ") {
			t.Fatalf("the %s hint must not hard-code the standalone spelling: %s", name, text)
		}
		if !strings.Contains(text, base.SetupPluginCommand+" ") {
			t.Fatalf("the %s hint must name a command the reader can actually run: %s", name, text)
		}
	}
}

// TestRepoKeyMismatchHintNoLongerBlamesTheRepo: the old hint told the reader to
// add a git remote. It fired on repositories that already had one, and no
// remote would have reconciled the two derivations anyway — that class is now
// fixed at ingest, so the advice must not linger.
func TestRepoKeyMismatchHintNoLongerBlamesTheRepo(t *testing.T) {
	t.Parallel()
	hint := setupRepoKeyMismatchHint(base.SetupBrainBinaryName)
	for _, dead := range []string{"no git remote", "git remote add origin", "upgrade the entire CLI"} {
		if strings.Contains(hint, dead) {
			t.Fatalf("obsolete advice %q still in the repo-key hint: %s", dead, hint)
		}
	}
}
