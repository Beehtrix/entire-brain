package cli

import (
	"testing"
	"time"
)

// These production bounds are vars only so oversized-fixture and
// lock-contention tests can lower them without materializing the full-size
// condition under the race detector. This pin catches an accidental edit to
// a default and, in most test orders, an override that leaked past its
// cleanup.
func TestOverridableProductionBoundsKeepTheirDefaults(t *testing.T) {
	if semanticIndexLockTimeout != 10*time.Second {
		t.Errorf("semanticIndexLockTimeout = %v, want 10s", semanticIndexLockTimeout)
	}
	if defaultMaxReadBytes != 64<<20 {
		t.Errorf("defaultMaxReadBytes = %d, want 64 MiB", defaultMaxReadBytes)
	}
	if maxManifestBytes != 16<<20 {
		t.Errorf("maxManifestBytes = %d, want 16 MiB", maxManifestBytes)
	}
	if privacyVectorVerifyMaxRows != 2_000_000 {
		t.Errorf("privacyVectorVerifyMaxRows = %d, want 2_000_000", privacyVectorVerifyMaxRows)
	}
	if distillAgentWaitDelay != 5*time.Second {
		t.Errorf("distillAgentWaitDelay = %v, want 5s", distillAgentWaitDelay)
	}
	if memoryProjectionClaimsPerPass != 64 {
		t.Errorf("memoryProjectionClaimsPerPass = %d, want 64", memoryProjectionClaimsPerPass)
	}
}
