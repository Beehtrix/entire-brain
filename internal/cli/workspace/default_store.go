package workspace

import (
	"io"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
)

// DefaultStore binds storage policy per invocation without resolving paths.
// The store uses the same locks, symlink checks, and atomic writer as the CLI.
func DefaultStore(env base.EntireEnv) StoreConfig {
	return StoreConfig{
		Env:               env,
		LegacyRepoDirName: base.RepoStoreDirName,
		LockDirName:       brainstore.BrainLockDirName,
		Deps: StoreDeps{
			DataDir: func(env base.EntireEnv) (string, error) {
				dirs, err := base.ResolvePluginDirs(env)
				return dirs.Data, err
			},
			RejectRootPath:       base.RejectBrainRootPathSymlinks,
			RejectPathComponents: base.RejectExistingSymlinkPathComponents,
			AcquireLock: func(path string) (io.Closer, error) {
				lock, err := brainstore.AcquireFileLock(path, "workspace_manifest_locked", brainstore.BrainWriteLockTimeout)
				if err != nil {
					return nil, err
				}
				return lock, nil
			},
			AtomicWrite: brainstore.WriteFileAtomic,
		},
	}
}
