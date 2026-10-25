package base

// EntireEnv captures the environment variables supplied by the parent Entire
// CLI when this binary is dispatched as an external command.
type EntireEnv struct {
	CLIVersion      string
	RepoRoot        string
	PluginConfigDir string
	PluginDataDir   string
	PluginStateDir  string
	PluginCacheDir  string
}

type RepoStorage struct {
	Key      string
	BrainDir string
	HeadPath string
}
