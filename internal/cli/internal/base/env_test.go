package base

import (
	"path/filepath"
	"testing"
)

func TestEnvFromOS(t *testing.T) {
	t.Setenv(EnvCLIVersion, "cli-test")
	t.Setenv(EnvRepoRoot, "/tmp/repo")
	t.Setenv(EnvPluginConfigDir, "/tmp/config")
	t.Setenv(EnvPluginDataDir, "/tmp/data")
	t.Setenv(EnvPluginStateDir, "/tmp/state")
	t.Setenv(EnvPluginCacheDir, "/tmp/cache")

	env := EnvFromOS()
	if env.CLIVersion != "cli-test" {
		t.Fatalf("CLIVersion = %q", env.CLIVersion)
	}
	if env.RepoRoot != "/tmp/repo" {
		t.Fatalf("RepoRoot = %q", env.RepoRoot)
	}
	if env.PluginConfigDir != "/tmp/config" {
		t.Fatalf("PluginConfigDir = %q", env.PluginConfigDir)
	}
	if env.PluginDataDir != "/tmp/data" {
		t.Fatalf("PluginDataDir = %q", env.PluginDataDir)
	}
	if env.PluginStateDir != "/tmp/state" {
		t.Fatalf("PluginStateDir = %q", env.PluginStateDir)
	}
	if env.PluginCacheDir != "/tmp/cache" {
		t.Fatalf("PluginCacheDir = %q", env.PluginCacheDir)
	}
}
func TestResolvePluginDirsUsesXDGDefaultsUnderEntireRoot(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv(XDGConfigHome, filepath.Join(xdg, "config"))
	t.Setenv(XDGDataHome, filepath.Join(xdg, "data"))
	t.Setenv(XDGStateHome, filepath.Join(xdg, "state"))
	t.Setenv(XDGCacheHome, filepath.Join(xdg, "cache"))

	dirs, err := ResolvePluginDirs(EntireEnv{})
	if err != nil {
		t.Fatalf("resolve plugin dirs: %v", err)
	}
	if dirs.Config != filepath.Join(xdg, "config", "entire") {
		t.Fatalf("Config = %q", dirs.Config)
	}
	// Data mirrors the host's plugin-data namespace so a standalone/dev run hits
	// the real brain store; config/state/cache stay at the bare XDG root (the host
	// leaves those env vars unset too).
	if dirs.Data != filepath.Join(xdg, "data", "entire", "plugins", "data", PluginDataName) {
		t.Fatalf("Data = %q", dirs.Data)
	}
	if dirs.State != filepath.Join(xdg, "state", "entire") {
		t.Fatalf("State = %q", dirs.State)
	}
	if dirs.Cache != filepath.Join(xdg, "cache", "entire") {
		t.Fatalf("Cache = %q", dirs.Cache)
	}
}
