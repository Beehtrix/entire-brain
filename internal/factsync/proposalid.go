package factsync

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/entireio/entire-brain/factmerge"
)

// proposalIDPrefix is the fixed lead-in of every derived proposal id, and
// proposalIDHexLen the number of hex digits that follow it.
const (
	proposalIDPrefix = "prop-"
	proposalIDHexLen = 16
)

// ProposalID derives the stable id of a proposal from the fields that identify the
// conflict it represents: the action, both fact ids, the branch, and the member who
// raised it. It is deterministic and collision-resistant, so the same conflict has the
// same id at every member and across re-reads of a queue. The local review queue's
// deduplication keys on it (internal/cli's dedupeProposals).
func ProposalID(p factmerge.Proposal) string {
	h := sha256.New()
	for _, field := range []string{p.Action, p.CandidateID, p.TargetID, p.Branch, p.ProposedBy} {
		h.Write([]byte(field))
		h.Write([]byte{0})
	}
	return proposalIDPrefix + hex.EncodeToString(h.Sum(nil))[:proposalIDHexLen]
}
