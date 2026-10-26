package cli

import (
	"testing"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
)

// TestPurgeEvictsDistillCacheForColonSessionID guards the trap that sits right
// next to the filename fix.
//
// Three places escape a session id for the distill cache --
// distillSessionCacheKey writes the key, purgeDistillCacheEntries matches it by
// suffix to evict, and verifySessionPrivacy matches it again to report
// leftovers -- and all three use url.PathEscape. That is CORRECT there: the
// result is a JSON map key, not a filename, so a colon is harmless. But the
// three only work because they agree, and the filename fix next door is exactly
// the kind of change someone extends into one of them "for consistency". Do
// that to one and purge silently stops evicting while verify starts failing.
//
// A colon id is the case that would diverge first, so this pins it end to end.
func TestPurgeEvictsDistillCacheForColonSessionID(t *testing.T) {
	brainDir := writePrivacyFixture(t)
	now := time.Date(2026, 8, 8, 11, 0, 0, 0, time.UTC)
	const colonID = "session:colon-cache-id"

	cache := loadDistillCache(brainDir)
	if cache.Sessions == nil {
		cache.Sessions = map[string]string{}
	}
	cache.Sessions[distillSessionCacheKey("main", colonID)] = "sha256:aaa"
	cache.Sessions[distillSessionCacheKey("main", "kept-sess")] = "sha256:bbb"
	if err := saveDistillCache(brainDir, cache); err != nil {
		t.Fatal(err)
	}

	if err := brainstore.WithBrainWriteLock(brainDir, func() error {
		plan, planErr := buildSessionPurgePlan(brainDir, colonID)
		if planErr != nil {
			return planErr
		}
		return executeSessionCleanup(brainDir, colonID, plan, now, "", true)
	}); err != nil {
		t.Fatalf("exclude with a colon id: %v", err)
	}

	after := loadDistillCache(brainDir)
	if _, ok := after.Sessions[distillSessionCacheKey("main", colonID)]; ok {
		t.Fatalf("purge did not evict the distill-cache entry for %q: %+v", colonID, after.Sessions)
	}
	if _, ok := after.Sessions[distillSessionCacheKey("main", "kept-sess")]; !ok {
		t.Fatalf("purge evicted an unrelated distill-cache entry: %+v", after.Sessions)
	}
	report, err := verifySessionPrivacy(brainDir)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Clean {
		t.Fatalf("verification must be clean after excluding a colon id: %+v", report.Findings)
	}
}
