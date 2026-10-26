package workspace

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
)

// StoreDeps binds the filesystem policies shared with the CLI. All hooks are
// supplied per invocation; the store owns no copies of mutable CLI state.
type StoreDeps struct {
	DataDir              func(base.EntireEnv) (string, error)
	RejectRootPath       func(string, string) error
	RejectPathComponents func(string, string) error
	AcquireLock          func(string) (io.Closer, error)
	AtomicWrite          func(string, []byte, fs.FileMode) error
}

// StoreConfig holds the invocation environment and filesystem dependencies.
// Constructing it does not resolve paths or access the filesystem.
type StoreConfig struct {
	Env               base.EntireEnv
	Deps              StoreDeps
	LegacyRepoDirName string
	LockDirName       string
}

const (
	ManifestName     = "workspace.json"
	ManifestLockName = "workspace-manifest.lock"
	readmeName       = "README.md"
	SchemaVersion    = 1
)

// DirOrEmpty resolves a workspace directory for use in a MESSAGE only:
// the caller is already reporting an error, so a second one would just hide the
// first.
func (s StoreConfig) DirOrEmpty(name string) string {
	dir, err := s.Dir(name)
	if err != nil {
		return ""
	}
	return dir
}

// AddRepoLocked is the ONLY way a repo enters a workspace manifest.
// Membership is a read-modify-write of a shared file, and the common case —
// running `entire-brain setup` in two repos at once — had both readers load the
// same manifest and both write their own single-member version back, so one
// registration vanished with no error anywhere. The lock lives beside the
// manifest and follows the same discipline as the brain locks: reject symlinked
// path components first, then hold the lock across load AND write.
func (s StoreConfig) AddRepoLocked(workspaceName string, repo Repo) (AddResult, error) {
	var result AddResult
	if err := ValidateName(workspaceName); err != nil {
		return result, err
	}
	if err := ValidateRepoKey(repo.RepoKey); err != nil {
		return result, err
	}
	lock, err := s.AcquireManifestLock(workspaceName)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Close() }()

	manifest, err := s.LoadManifest(workspaceName)
	if err != nil {
		if !os.IsNotExist(err) {
			// A manifest that exists but cannot be read blocks registration for
			// EVERY repo in the workspace, permanently, and the underlying error
			// ("invalid character ...") names neither the file nor a way out.
			// Recreating it silently would be worse — it would drop every other
			// member — so say exactly which file to look at.
			return result, fmt.Errorf("%w\nworkspace manifest %s is unusable; fix or delete it, then re-run (deleting it drops the other members, who re-register on their next setup)",
				err, filepath.Join(s.DirOrEmpty(workspaceName), ManifestName))
		}
		manifest = Manifest{SchemaVersion: SchemaVersion, Name: workspaceName}
		result.Created = true
	}
	replaced := false
	for i := range manifest.Repos {
		if manifest.Repos[i].RepoKey == repo.RepoKey {
			result.Already = true
			result.PreviousPathHint = manifest.Repos[i].LocalPathHint
			// Keep the freshest local path hint; the member itself is unchanged.
			// A caller that carries no display name — `setup`'s registration,
			// which re-runs every time — must not erase the one a human set with
			// `workspace add --name`.
			if strings.TrimSpace(repo.Name) == "" {
				repo.Name = manifest.Repos[i].Name
			}
			manifest.Repos[i] = repo
			replaced = true
			break
		}
	}
	if !replaced {
		manifest.Repos = append(manifest.Repos, repo)
	}
	sortRepos(manifest.Repos)
	if err := s.WriteManifest(manifest); err != nil {
		return result, err
	}
	return result, nil
}

func (s StoreConfig) AcquireManifestLock(workspaceName string) (io.Closer, error) {
	dir, err := s.Dir(workspaceName)
	if err != nil {
		return nil, err
	}
	workspaceRoot := filepath.Dir(dir)
	if err := s.Deps.RejectPathComponents(workspaceRoot, s.LockDirName); err != nil {
		return nil, err
	}
	// Keep membership locks outside the workspace directory so whole-workspace
	// deletion can hold the same lock without deleting an open lock file.
	return s.Deps.AcquireLock(filepath.Join(workspaceRoot, s.LockDirName, workspaceName+"-"+ManifestLockName))
}

// MergeRefreshResult keeps the expensive multi-repo work outside the
// membership lock, then merges its results into the latest manifest. Repos
// added during refresh are preserved and simply receive freshness next pass;
// repos removed during refresh are not resurrected.
func (s StoreConfig) MergeRefreshResult(workspaceName string, freshness []RepoFreshness, refreshedAt time.Time) error {
	lock, err := s.AcquireManifestLock(workspaceName)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	manifest, err := s.LoadManifest(workspaceName)
	if err != nil {
		return err
	}
	byRepo := make(map[string]RepoFreshness, len(freshness))
	for _, state := range freshness {
		byRepo[state.RepoKey] = state
	}
	merged := make([]RepoFreshness, 0, len(manifest.Repos))
	for _, repo := range manifest.Repos {
		if state, ok := byRepo[repo.RepoKey]; ok {
			merged = append(merged, state)
		}
	}
	manifest.Freshness = merged
	manifest.RefreshedAt = refreshedAt
	return s.WriteManifest(manifest)
}

// Dir resolves a workspace's home at <data>/workspaces/<name> — a
// SIBLING of the repos/ tree, not inside it. Workspaces are not repos:
// keeping them under repos/ parked a magic "workspaces" directory in the
// middle of the repo keyspace (an unknown host slug or hand-edited
// DomainSlugs entry could legitimately claim the same segment and collide),
// and forced any future repos/-enumerator to know to skip it. Manifests
// written by older builds under repos/workspaces/<name> are migrated lazily
// on first touch (a directory rename; the payload is kilobytes).
func (s StoreConfig) Dir(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	dataDir, err := s.Deps.DataDir(s.Env)
	if err != nil {
		return "", err
	}
	workspaceRoot := filepath.Join(dataDir, base.WorkspacesDirName)
	if err := s.Deps.RejectRootPath(workspaceRoot, name); err != nil {
		return "", err
	}
	dir := filepath.Join(workspaceRoot, name)
	if err := s.migrateLegacyDir(dataDir, name, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// migrateLegacyDir moves a pre-relocation workspace
// (<data>/repos/workspaces/<name>) to its new home on first touch. A no-op
// when the legacy dir is absent or the new dir already exists (the new copy
// wins: it is the one current builds have been writing to).
func (s StoreConfig) migrateLegacyDir(dataDir, name, newDir string) error {
	legacyRoot := filepath.Join(dataDir, s.LegacyRepoDirName)
	legacy := filepath.Join(legacyRoot, base.WorkspacesDirName, name)
	info, err := os.Lstat(legacy)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		// Absent, unreadable, or a SYMLINK: never migrate. Renaming a symlink
		// would move the link itself into the new home, making every future
		// workspace write flow through an attacker-placed target.
		return nil
	}
	if s.Deps.RejectRootPath(legacyRoot, filepath.Join(base.WorkspacesDirName, name)) != nil {
		return nil // tampered legacy tree (symlinked root/components): leave it alone
	}
	if _, err := os.Stat(newDir); err == nil {
		return nil // the new copy wins: it is the one current builds write to
	}
	if err := os.MkdirAll(filepath.Dir(newDir), 0o700); err != nil {
		return err
	}
	if err := os.Rename(legacy, newDir); err != nil {
		return fmt.Errorf("migrate workspace %s to %s: %w", name, newDir, err)
	}
	return nil
}

func (s StoreConfig) LoadManifest(name string) (Manifest, error) {
	if err := ValidateName(name); err != nil {
		return Manifest{}, err
	}
	dir, err := s.Dir(name)
	if err != nil {
		return Manifest{}, err
	}
	path := filepath.Join(dir, ManifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.SchemaVersion != SchemaVersion {
		return Manifest{}, fmt.Errorf("unsupported workspace schema_version: %d", manifest.SchemaVersion)
	}
	if manifest.Name != name {
		return Manifest{}, fmt.Errorf("workspace name mismatch: %s", manifest.Name)
	}
	for _, repo := range manifest.Repos {
		if err := ValidateRepoKey(repo.RepoKey); err != nil {
			return Manifest{}, err
		}
	}
	return manifest, nil
}

func (s StoreConfig) WriteManifest(manifest Manifest) error {
	if err := ValidateName(manifest.Name); err != nil {
		return err
	}
	for _, repo := range manifest.Repos {
		if err := ValidateRepoKey(repo.RepoKey); err != nil {
			return err
		}
	}
	manifest.SchemaVersion = SchemaVersion
	sortRepos(manifest.Repos)
	dir, err := s.Dir(manifest.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := s.Deps.AtomicWrite(filepath.Join(dir, ManifestName), data, 0o600); err != nil {
		return err
	}
	return s.Deps.AtomicWrite(filepath.Join(dir, readmeName), []byte(renderReadme(manifest)), 0o600)
}
