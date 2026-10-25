package cli

import (
	"io"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/workspace"
)

// workspaceStore binds storage policy per invocation without resolving paths.
// The store uses the same locks, symlink checks, and atomic writer as the CLI.
func workspaceStore(env EntireEnv) workspace.StoreConfig {
	return workspace.StoreConfig{
		Env:               env,
		LegacyRepoDirName: repoStoreDirName,
		LockDirName:       brainLockDirName,
		Deps: workspace.StoreDeps{
			DataDir: func(env base.EntireEnv) (string, error) {
				dirs, err := resolvePluginDirs(env)
				return dirs.Data, err
			},
			RejectRootPath:       rejectBrainRootPathSymlinks,
			RejectPathComponents: base.RejectExistingSymlinkPathComponents,
			AcquireLock: func(path string) (io.Closer, error) {
				lock, err := acquireFileLock(path, "workspace_manifest_locked", brainWriteLockTimeout)
				if err != nil {
					return nil, err
				}
				return lock, nil
			},
			AtomicWrite: writeFileAtomic,
		},
	}
}
