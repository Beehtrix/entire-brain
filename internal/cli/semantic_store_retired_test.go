package cli

import (
	"errors"
	"testing"
)

// A persisted source without the live graph handle is the pre-0.2.0 layout;
// since COR-2217 Brain has no reader for it, only a remedy.
func TestDeclaredStoreWithoutGraphHandleIsRetired(t *testing.T) {
	src := &semanticSourceManifest{
		SnapshotPath:   "semantic/snapshots/x/snapshot.ndjson",
		GenerationPath: "semantic/generations/x",
		StorePath:      "semantic/generations/x/semantic.sqlite",
	}
	_, err := validateSemanticDeclaredStore(t.TempDir(), src)
	if err == nil || !errors.Is(err, errSemanticStoreRetired) {
		t.Fatalf("want errSemanticStoreRetired, got %v", err)
	}
}
