package patterns

import (
	"fmt"
	"os"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
	"github.com/entireio/entire-brain/internal/cli/workspace"
)

// DerivedReadGate prevents the tombstone-first cleanup window from exposing
// corpus-derived content. A valid tombstone does not permanently disable
// patterns: once the authoritative full verification is clean, reads resume
// normally. verifyClean is the caller's full derived-projection verifier; it
// runs only while tombstones exist.
func DerivedReadGate(brainDir string, verifyClean func(string) (bool, error)) (bool, error) {
	// The corpus is optional, but when present its main database and WAL must be
	// ordinary Brain-owned files even when there are no tombstones. Without this
	// unconditional check, best-effort pattern loaders could hide an unsafe alias
	// as an empty result on status/get/skill surfaces.
	if err := ValidateCorpusReadSafety(brainDir); err != nil {
		return false, err
	}
	stones, _, err := brainstore.LoadSessionTombstonesChecked(brainDir)
	if err != nil {
		return false, err
	}
	if len(stones.Excluded) == 0 {
		return true, nil
	}
	return verifyClean(brainDir)
}

// VerifyDerivedClean is the full derived-projection verifier. A nil verifier
// fails closed: when tombstones exist and no verifier was supplied, derived
// reads are refused rather than assumed clean.
type VerifyDerivedClean func(brainDir string) (bool, error)

func gateVerifier(verify VerifyDerivedClean) func(string) (bool, error) {
	if verify != nil {
		return func(brainDir string) (bool, error) { return verify(brainDir) }
	}
	return func(string) (bool, error) {
		return false, fmt.Errorf("%s: derived-projection verifier unavailable; excluded-session cleanup cannot be proven clean", brainstore.PrivacyDirtyCode)
	}
}

// RequireDerivedRead fails unless the derived-read gate allows reads.
func RequireDerivedRead(brainDir string, verify VerifyDerivedClean) error {
	allowed, err := DerivedReadGate(brainDir, gateVerifier(verify))
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("%s: derived pattern state is unavailable until excluded-session cleanup verifies clean; run `"+base.BrainInvocation(os.LookupEnv, "privacy purge")+" <session-id>`", brainstore.PrivacyDirtyCode)
	}
	return nil
}

// CaptureWorkspaceRetrievalPrivacyPolicies captures each member Brain's checked
// tombstone identity, deduplicated by repo key.
func CaptureWorkspaceRetrievalPrivacyPolicies(env base.EntireEnv, manifest workspace.Manifest) ([]brainstore.RetrievalPrivacyPolicy, map[string]string, error) {
	policies := make([]brainstore.RetrievalPrivacyPolicy, 0, len(manifest.Repos))
	byRepo := make(map[string]string, len(manifest.Repos))
	for _, repo := range manifest.Repos {
		if _, exists := byRepo[repo.RepoKey]; exists {
			continue
		}
		brainDir, err := base.BrainDirForKey(env, repo.RepoKey)
		if err != nil {
			return nil, nil, err
		}
		policy, err := brainstore.CaptureRetrievalPrivacyPolicy(brainDir)
		if err != nil {
			return nil, nil, err
		}
		policies = append(policies, policy)
		byRepo[repo.RepoKey] = policy.Identity
	}
	return policies, byRepo, nil
}

// CaptureWorkspaceDerivedReadPolicies additionally proves every member's
// transcript-derived projections are clean before a workspace aggregate is
// read. Policy capture alone only proves the tombstone file did not change; it
// cannot make a corpus built before an already-present tombstone safe.
func CaptureWorkspaceDerivedReadPolicies(env base.EntireEnv, manifest workspace.Manifest, verify VerifyDerivedClean) ([]brainstore.RetrievalPrivacyPolicy, error) {
	policies, _, err := CaptureWorkspaceRetrievalPrivacyPolicies(env, manifest)
	if err != nil {
		return nil, err
	}
	for i := range policies {
		policy := &policies[i]
		policy.RequireDerivedClean = true
		allowed, gateErr := DerivedReadGate(policy.BrainDir, gateVerifier(verify))
		if gateErr != nil {
			return nil, gateErr
		}
		if !allowed {
			return nil, fmt.Errorf("%s: workspace member has unverified transcript-derived projections: %s", brainstore.PrivacyDirtyCode, policy.BrainDir)
		}
	}
	// Workspace pattern/theme/family rows live in their own aggregate corpus.
	// Give that Brain the same final-emission proof as every member so a corpus
	// alias or replacement after assembly cannot be collapsed into empty output.
	wsDir, err := workspace.DefaultStore(env).Dir(manifest.Name)
	if err != nil {
		return nil, err
	}
	wsPolicy, err := brainstore.CaptureRetrievalPrivacyPolicy(wsDir)
	if err != nil {
		return nil, err
	}
	wsPolicy.RequireDerivedClean = true
	if err := RequireDerivedRead(wsDir, verify); err != nil {
		return nil, err
	}
	policies = append(policies, wsPolicy)
	return policies, nil
}
