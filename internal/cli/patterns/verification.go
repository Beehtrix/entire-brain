package patterns

import (
	"github.com/entireio/entire-brain/internal/cli/internal/base"
)

// VerifyStats reports proposal and verification outcomes.
type VerifyStats struct {
	Considered int
	Verified   int
	Cached     int
	Failed     int
}

// AllowedVerdicts is shared by pattern verifiers and proposal validators.
var AllowedVerdicts = map[string]bool{
	"accepted": true, "rejected": true, "needs_split": true, "low_confidence": true,
}

// ProposalSampleFingerprint hashes the exact evidence payload an agent proposal
// phase sends to the agent, so the cache invalidates whenever the evidence
// CONTENT changes — not only when ids change. The payload already carries each
// item's content fields (excerpts, outcomes, commands, fact text, corroboration),
// so hashing it captures all of them. Marshaled maps have deterministic
// (key-sorted) output, so the fingerprint is stable for unchanged evidence.
func ProposalSampleFingerprint(payload []byte) string {
	return "sha256:" + base.HexSHA(string(payload))
}
