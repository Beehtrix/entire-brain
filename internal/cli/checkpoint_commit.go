package cli

import (
	"context"
	"strings"
	"time"

	"github.com/entireio/entire-brain/internal/cli/entities"
	"github.com/entireio/entire-brain/internal/cli/internal/base"
)

// checkpoint_commit.go resolves a checkpoint id to the commit carrying its
// Entire-Checkpoint trailer, so distilled facts can anchor to the commit the
// work actually landed in — never HEAD, which during a background distill of
// an old session is whatever happens to be checked out (COR-2126).
//
// One checkpoint id can sit on several commits: cherry-picks and rebase copies
// carry the trailer verbatim. The honest answer is the newest commit ON THE
// SESSION'S BRANCH (the same discipline as entities_provenance.go); none
// on-branch resolves to blank — absent is honest, wrong is not.

// checkpointCommitCandidate is one commit carrying a checkpoint's trailer.
type checkpointCommitCandidate struct {
	commit string
	at     time.Time
}

// checkpointCommitResolver is built once per distill run from one git log
// pass and is read-only afterwards. A nil resolver resolves everything to
// blank, so construction failure degrades to the pre-COR-2126 anchoring.
type checkpointCommitResolver struct {
	byCheckpoint map[string][]checkpointCommitCandidate
	reachable    entities.BranchReachability
}

// newCheckpointCommitResolver scans every commit carrying a checkpoint
// trailer, the same walk buildCheckpointAuthorIndex does. It builds its own
// memoized reachability lookup; the entity-provenance resolver keeps a
// separate one (ponytail: one duplicate rev-list per branch per distill run —
// sharing them means threading a lookup through 7 call sites for a walk that
// is noise next to distill's agent calls).
func newCheckpointCommitResolver(ctx context.Context, opts Options, repoDir string) *checkpointCommitResolver {
	stdout, _, err := runCheckpointGitWithPolicy(ctx, opts.Runner, repoDir, brainNoEgressMode(),
		"log", "--all", "--format=%H%x00%cI%x00%B%x1e", "--grep", base.CheckpointTrailerKey+":")
	if err != nil {
		return nil
	}
	byCheckpoint := checkpointCommitsFromGitLog(stdout)
	if len(byCheckpoint) == 0 {
		return nil
	}
	return &checkpointCommitResolver{
		byCheckpoint: byCheckpoint,
		reachable:    entities.BranchReachabilityLookup(ctx, entityConfig(opts), repoDir),
	}
}

func checkpointCommitsFromGitLog(data []byte) map[string][]checkpointCommitCandidate {
	byCheckpoint := make(map[string][]checkpointCommitCandidate)
	for _, record := range strings.Split(string(data), gitLogRecordSeparator) {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, gitLogFieldSeparator, 3)
		if len(parts) != 3 {
			continue
		}
		commit := strings.TrimSpace(parts[0])
		committedAt, _ := time.Parse(time.RFC3339, strings.TrimSpace(parts[1])) //nolint:errcheck // Timestamp orders copies; zero is acceptable.
		checkpointIDs := base.CheckpointIDsFromCommitMessageText(parts[2])
		if commit == "" || len(checkpointIDs) == 0 {
			continue
		}
		for _, checkpointID := range checkpointIDs {
			byCheckpoint[checkpointID] = append(byCheckpoint[checkpointID], checkpointCommitCandidate{commit: commit, at: committedAt})
		}
	}
	return byCheckpoint
}

// commitFor resolves a checkpoint to the newest on-branch commit carrying its
// trailer, or "" when the checkpoint is unknown or none of its commits are on
// the branch. Unknown reachability is not a refusal (provenanceCommitOnBranch).
func (r *checkpointCommitResolver) commitFor(checkpointID, branch string) string {
	if r == nil {
		return ""
	}
	best := ""
	var bestAt time.Time
	for _, candidate := range r.byCheckpoint[strings.TrimSpace(checkpointID)] {
		if !entities.ProvenanceCommitOnBranch(r.reachable, branch, candidate.commit) {
			continue
		}
		if best == "" || candidate.at.After(bestAt) {
			best, bestAt = candidate.commit, candidate.at
		}
	}
	return best
}
