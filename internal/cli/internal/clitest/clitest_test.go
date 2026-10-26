package clitest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinXDGRoots(t *testing.T) {
	var root string
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
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
}

func TestUseHostXDGDataHomeIsScoped(t *testing.T) {
	original := os.Getenv("XDG_DATA_HOME")
	t.Run("restores host data root", func(t *testing.T) {
		UseHostXDGDataHome(t)
		if got := os.Getenv("XDG_DATA_HOME"); got != hostXDGDataHome {
			t.Fatalf("data root = %q, want host root %q", got, hostXDGDataHome)
		}
	})
	if got := os.Getenv("XDG_DATA_HOME"); got != original {
		t.Fatalf("host override leaked: got %q, want %q", got, original)
	}
}
