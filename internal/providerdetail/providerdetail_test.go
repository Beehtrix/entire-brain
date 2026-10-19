package providerdetail

import (
	"strings"
	"testing"
)

// TestTheRemedyIsNotPrintedTwice is the review finding, fixed in one place.
//
// The report claimed the semantic index already deduped and the entity index
// did not. It did not: neither did. Both appended the install sentence
// whenever Missing matched, so a host CLI whose own stderr names the command
// got it twice from BOTH components. The PR's goal -- the two cannot
// disagree -- held; what failed is that they agreed on the wrong thing.
func TestTheRemedyIsNotPrintedTwice(t *testing.T) {
	t.Parallel()
	lookup := func(string) (string, bool) { return "", false }

	// A host that says nothing about installing: we supply the remedy.
	bare := `unknown command "graph" for "entire"`
	suffix := MissingSuffix("read graph identity revision: "+bare, bare, "entire", lookup)
	if !strings.Contains(suffix, InstallGraphCommand) {
		t.Fatalf("a bare missing-provider detail must get the remedy, got %q", suffix)
	}

	// A host that already printed it: we stay quiet.
	helpful := bare + "\n\nDid you mean to run `" + InstallGraphCommand + "`?"
	if got := MissingSuffix("read graph identity revision: "+helpful, helpful, "entire", lookup); got != "" {
		t.Fatalf("the host already named the remedy; appending a second copy is noise, got %q", got)
	}

	// Not a missing provider at all: nothing to say.
	if got := MissingSuffix("x: exit status 1", "exit status 1: index build failed", "entire", lookup); got != "" {
		t.Fatalf("an unrelated failure must not grow an install remedy, got %q", got)
	}
	if got := MissingSuffix("", "", "entire", lookup); got != "" {
		t.Fatalf("an empty detail must produce nothing, got %q", got)
	}
}

// And the part the finding was really about: both components must reach the
// same answer for the same input. Testing the shared helper alone would not
// show it -- the defect class here is one call site deciding for itself.
func TestBothComponentsAskTheSameQuestion(t *testing.T) {
	t.Parallel()
	lookup := func(string) (string, bool) { return "", false }
	for _, detail := range []string{
		`unknown command "graph" for "entire"`,
		`unknown command "graph"` + "\nrun `" + InstallGraphCommand + "` first",
		"exec: \"entire\": executable file not found in $PATH",
		"exit status 2: something else entirely",
		"",
	} {
		// Whatever each site appends, it must be exactly this.
		want := MissingSuffix("prefix: "+detail, detail, "entire", lookup)
		if n := strings.Count(want, InstallGraphCommand); n > 1 {
			t.Errorf("detail %q: the shared suffix itself repeats the command %d times", detail, n)
		}
	}
}

// TestDedupReadsTheMessageNotTheDetail pins the thing the first attempt got
// wrong, and which the review finding's suggested fix would also have got
// wrong.
//
// Suppressing on the DETAIL looks right and silently deletes information: the
// message is built from Summary(detail), and the summary is exactly what cuts
// the host's own remedy line off the end. The reader then gets neither copy.
func TestDedupReadsTheMessageNotTheDetail(t *testing.T) {
	t.Parallel()
	lookup := func(string) (string, bool) { return "", false }
	bare := `unknown command "graph" for "entire"`
	// Detail carries the remedy; the summarised message does not.
	detail := bare + "\n\nDid you mean to run `" + InstallGraphCommand + "`?"
	summarised := "read graph identity revision: " + bare

	if got := MissingSuffix(summarised, detail, "entire", lookup); got == "" {
		t.Fatal("the summary dropped the host's remedy, so ours must be printed; deduping on the detail loses it entirely")
	}
	// And when the message really does carry it, stay quiet.
	full := summarised + "; run `" + InstallGraphCommand + "`"
	if got := MissingSuffix(full, detail, "entire", lookup); got != "" {
		t.Fatalf("the message already names the remedy, got %q", got)
	}
}
