package cli

import (
	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-brain/internal/cli/internal/clitest"
)

// TestMain pins the four XDG roots at a per-run temporary directory before a
// single test runs, so no test in this package can resolve a store under the
// developer's real home. The pin lives in clitest and is shared by every
// extracted package in the internal/cli tree; see clitest.PinXDGRoots for the
// escape analysis that motivated pinning here rather than fixing call sites,
// and clitest.UseHostXDGDataHome for the one live end-to-end exception.
func TestMain(m *testing.M) {
	os.Exit(clitest.PinXDGRoots(m))
}

func TestTestMainPinsXDGRoots(t *testing.T) {
	var root string
	for _, key := range []string{base.XDGConfigHome, base.XDGDataHome, base.XDGStateHome, base.XDGCacheHome} {
		dir := os.Getenv(key)
		if !filepath.IsAbs(dir) || filepath.Base(dir) != key {
			t.Fatalf("%s = %q, want an absolute per-run root", key, dir)
		}
		if root == "" {
			root = filepath.Dir(dir)
		}
		if filepath.Dir(dir) != root || !strings.HasPrefix(filepath.Base(root), "entire-brain-cli-test-xdg-") {
			t.Fatalf("%s = %q, want a child of the shared temporary root %q", key, dir, root)
		}
	}
	dirs, err := base.ResolvePluginDirs(EntireEnv{})
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{dirs.Config, dirs.Data, dirs.State, dirs.Cache} {
		rel, err := filepath.Rel(root, dir)
		if err != nil || !filepath.IsLocal(rel) {
			t.Fatalf("default plugin directory %q escaped temporary root %q: %v", dir, root, err)
		}
	}
}
