package entityindex

import (
	"errors"
	"strings"
	"testing"

	"github.com/entireio/entire-brain/internal/providerdetail"
)

// TestIdentityErrorDoesNotRepeatTheHostsOwnRemedy pins the CALL SITE, not the
// helper.
//
// The defect class the review found is one component deciding for itself what
// the other decides centrally, so a test of providerdetail alone cannot see
// it: delete the call here and the shared helper is still perfectly correct
// and perfectly unused.
func TestIdentityErrorDoesNotRepeatTheHostsOwnRemedy(t *testing.T) {
	t.Parallel()
	bare := `unknown command "graph" for "entire"`

	got := identityError("entire", errors.New(bare)).Error()
	if !strings.Contains(got, providerdetail.InstallGraphCommand) {
		t.Fatalf("a bare missing-provider failure must still name the install command:\n%s", got)
	}
	if !strings.Contains(got, "read graph identity revision") {
		t.Fatalf("the prefix callers and the doctor match on is gone:\n%s", got)
	}

	// A host whose remedy survives into the SUMMARY. This is the case that
	// separates "goes through the shared decision" from "appends
	// unconditionally and happens to be right": with a multi-line detail the
	// summary cuts the host's line, so an unconditional append also yields
	// exactly one copy and proves nothing.
	oneLine := `unknown command "graph"; run ` + "`" + providerdetail.InstallGraphCommand + "`"
	kept := identityError("entire", errors.New(oneLine)).Error()
	if n := strings.Count(kept, providerdetail.InstallGraphCommand); n != 1 {
		t.Fatalf("the message already names the remedy; it appears %d times:\n%s", n, kept)
	}

	// And the multi-line case, where the summary drops the host's copy and
	// ours must therefore appear.
	multi := bare + "\n\nDid you mean to run `" + providerdetail.InstallGraphCommand + "`?"
	summarised := identityError("entire", errors.New(multi)).Error()
	if n := strings.Count(summarised, providerdetail.InstallGraphCommand); n != 1 {
		t.Fatalf("the summary dropped the host's copy, so exactly one of ours is owed; got %d:\n%s", n, summarised)
	}
}
