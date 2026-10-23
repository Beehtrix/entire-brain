package cli

import (
	"testing"

	"github.com/entireio/entire-brain/internal/factsync"
)

// distill's private review backlog and a cross-member conflict raised by
// `facts sync` can name the same action/candidate/target — the shared head holds
// this member's own facts, so the pair repeats. They are DIFFERENT rows to every
// layer that keys on factsync.ProposalID (branch and proposed_by included), so
// the queue must keep both.
func TestDedupeProposalsKeepsMemberAttribution(t *testing.T) {
	private := factProposal{Action: factActionSupersede, CandidateID: "fact:c", TargetID: "fact:t"}
	shared := factProposal{Action: factActionSupersede, CandidateID: "fact:c", TargetID: "fact:t", Branch: "main", ProposedBy: "alice@example.com"}

	got := dedupeProposals([]factProposal{private, shared})
	if len(got) != 2 {
		t.Fatalf("want both the private and the member-attributed proposal, got %+v", got)
	}
	if factsync.ProposalID(got[0]) == factsync.ProposalID(got[1]) {
		t.Fatalf("dedupe kept two entries with the same shared-queue id: %+v", got)
	}
	// A true duplicate still collapses.
	if again := dedupeProposals([]factProposal{shared, shared, private}); len(again) != 2 {
		t.Fatalf("identical entries must still collapse: %+v", again)
	}
	// Deterministic order regardless of input order.
	a := dedupeProposals([]factProposal{private, shared})
	b := dedupeProposals([]factProposal{shared, private})
	for i := range a {
		if factsync.ProposalID(a[i]) != factsync.ProposalID(b[i]) {
			t.Fatalf("dedupe order depends on input order: %+v vs %+v", a, b)
		}
	}
}
