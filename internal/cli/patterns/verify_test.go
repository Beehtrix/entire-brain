package patterns_test

import (
	"testing"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/patterntest"
	patterncore "github.com/entireio/entire-brain/internal/cli/patterns"
)

// TestRefreshDoesNotInvokeVerifier proves the deterministic build never produces
// a verdict — the agent path is reachable only through the explicit verify
// surface (refresh/watch/brief/query/MCP must stay token-free).
func TestRefreshDoesNotInvokeVerifier(t *testing.T) {
	db := patterntest.PromotableCorpus(t, time.Now())
	if err := patterncore.BuildCandidates(db, "gh/acme/cli", time.Now()); err != nil {
		t.Fatal(err)
	}
	var withVerdict int
	db.QueryRow(`SELECT COUNT(*) FROM dossiers WHERE verdict IS NOT NULL`).Scan(&withVerdict)
	if withVerdict != 0 {
		t.Errorf("deterministic build set %d verdict(s); the verifier must only run via explicit `patterns verify`", withVerdict)
	}
}
