package workspace

import (
	"testing"
)

func TestWorkspaceRejectsDotRepoKeys(t *testing.T) {
	for _, key := range []string{".", "..", "gh/../repo", "gh//repo"} {
		if err := ValidateRepoKey(key); err == nil {
			t.Fatalf("repo key accepted: %q", key)
		}
	}
}

func TestWorkspaceFreshnessBlocksScan(t *testing.T) {
	// Only an untrusted brain<->tree pairing (PairingUnsafe) blocks the scan. State alone does NOT —
	// a stale/broken semantic index (even State "unsafe") over a valid pairing is still scannable
	// from raw sessions, and blocking it would drop real findings.
	for _, st := range []string{"ok", "degraded", "missing-brain", "missing-semantic", "unknown", "unsafe", ""} {
		if FreshnessBlocksScan(RepoFreshness{State: st}) {
			t.Errorf("state %q without PairingUnsafe should NOT block the scan", st)
		}
	}
	if !FreshnessBlocksScan(RepoFreshness{State: "unsafe", PairingUnsafe: true}) {
		t.Error("PairingUnsafe must block the scan")
	}
}
