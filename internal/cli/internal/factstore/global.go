package factstore

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/entireio/entire-brain/factmerge"
	"github.com/entireio/entire-brain/internal/cli/internal/base"
)

const (
	// globalStoreDirName sits beside "repos" in the plugin data directory, so a
	// global brain is visible next to the per-repo ones rather than buried
	// inside one of them.
	globalStoreDirName = "global"

	// GlobalBranch is the branch component of the global store's filename.
	// Global facts are not on a branch; this is the storage layer's requirement,
	// not a claim about the fact. Display never takes a fact's scope from this
	// value — see globalFactIDs — because a repository may legitimately have a
	// branch called "global" and a fact on it is not a global fact.
	GlobalBranch = "global"
)

// GlobalBrainDir returns the global store's directory, creating nothing. The
// caller creates it on first write, so merely reading never materialises a
// directory for somebody who has no global memories.
func GlobalBrainDir(env base.EntireEnv) (string, error) {
	dirs, err := base.ResolvePluginDirs(env)
	if err != nil {
		return "", err
	}
	return filepath.Join(dirs.Data, globalStoreDirName), nil
}

// LoadGlobal reads the global store. A store that does not exist yet is
// not an error: it is the state of every brain before the first global fact,
// and recall must not fail because of it.
func LoadGlobal(env base.EntireEnv) ([]factmerge.Record, error) {
	brainDir, err := GlobalBrainDir(env)
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Stat(brainDir); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, nil
		}
		return nil, statErr
	}
	return Load(brainDir, GlobalBranch)
}

// ResolveGlobalTarget is the global counterpart of the per-repo facts-target
// resolution, for commands operating on the global store directly.
func ResolveGlobalTarget(env base.EntireEnv) (brainDir, branch string, err error) {
	brainDir, err = GlobalBrainDir(env)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(brainDir, 0o700); err != nil {
		return "", "", fmt.Errorf("create global brain: %w", err)
	}
	return brainDir, GlobalBranch, nil
}
