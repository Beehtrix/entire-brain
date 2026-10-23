package cli

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// This descriptor is resolved for each operation, never persisted. Graph owns
// the filename, schema, cache identity and lifecycle. Brain opens only its
// versioned views, read-only; no graph rows are copied into Brain storage.
type graphStoreDescriptor struct {
	BrainDir         string         `json:"brain_dir,omitempty"`
	ContractVersion  int            `json:"contract_version"`
	Path             string         `json:"path"`
	EntryDigest      string         `json:"entry_digest"`
	Commit           string         `json:"commit"`
	Tree             string         `json:"tree"`
	Header           semanticHeader `json:"header"`
	Files            int            `json:"files"`
	Symbols          int            `json:"symbols"`
	Relations        int            `json:"relations"`
	Externals        int            `json:"externals"`
	StoreUnavailable bool           `json:"store_unavailable"`
	Detail           string         `json:"detail"`
}

const graphStoreHandlePrefix = "graph-store-v1:"

func fetchGraphStore(ctx context.Context, opts Options, repoDir, binary, profile string) (graphStoreDescriptor, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if binary == "" {
		binary = "entire"
	}
	if profile == "" {
		profile = "full"
	}
	args := []string{"graph", "store", "--repo", repoDir, "--profile", profile}
	ignores, err := semanticProviderIgnoreFiles(repoDir)
	if err != nil {
		return graphStoreDescriptor{}, err
	}
	for _, ignore := range ignores {
		args = append(args, "--ignore-file", ignore)
	}
	ctx, cancel := context.WithTimeout(ctx, graphTaskContextTimeout)
	defer cancel()
	stdout, stderr, err := opts.Runner.Run(ctx, repoDir, binary, args...)
	if err != nil {
		detail := strings.ToLower(string(stderr) + string(stdout))
		if strings.Contains(detail, "unknown command") || strings.Contains(detail, "unknown subcommand") {
			return graphStoreDescriptor{}, errGraphStoreUnsupported
		}
		return graphStoreDescriptor{}, fmt.Errorf("Graph read-only store unavailable: %w: %s", err, firstLine(strings.TrimSpace(string(stderr))))
	}
	var d graphStoreDescriptor
	if err := json.Unmarshal(stdout, &d); err != nil {
		return d, fmt.Errorf("decode Graph store descriptor: %w", err)
	}
	if d.StoreUnavailable {
		return d, fmt.Errorf("%w: %s; run %s", errGraphStoreUnavailable, d.Detail, brainInvocation(os.LookupEnv, "refresh index"))
	}
	if d.ContractVersion != 1 {
		return d, errGraphStoreUnsupported
	}
	if !filepath.IsAbs(d.Path) || d.EntryDigest == "" || d.Commit == "" || d.Tree == "" {
		return d, errors.New("Graph returned an unsupported or incomplete store descriptor")
	}
	return d, nil
}

var errGraphStoreUnsupported = errors.New("installed Graph lacks the read-only store interface; upgrade Graph to enable code inspection")

var errGraphStoreUnavailable = errors.New("Graph store unavailable")

func graphStoreSource(d graphStoreDescriptor, brainDir string) *semanticSourceManifest {
	d.BrainDir = brainDir
	encoded, _ := json.Marshal(struct {
		Path        string `json:"path"`
		EntryDigest string `json:"entry_digest"`
		BrainDir    string `json:"brain_dir"`
	}{d.Path, d.EntryDigest, brainDir})
	h := d.Header
	return &semanticSourceManifest{Provider: "entire-graph", ProviderVersion: h.ProviderVersion, IdentityRevision: h.IdentityRevision, SchemaVersion: h.SchemaVersion, Commit: d.Commit, Tree: d.Tree, Profile: h.Profile, Symbols: d.Symbols, Relations: d.Relations, Externals: d.Externals, Files: d.Files, NoEgressVerified: true, StoreOwner: "graph", StorePath: graphStoreHandlePrefix + base64.RawURLEncoding.EncodeToString(encoded), graphStore: &d, Languages: h.Languages, LanguageTiers: h.LanguageTiers, Capabilities: h.Capabilities, Warnings: h.Warnings, PartialFailures: h.PartialFailures, Completeness: h.Completeness, Stats: h.Stats, ProfileLimits: h.ProfileLimits, RelationSet: h.RelationSet, CompletenessLevel: completenessLevelFromStats(h.Stats), Trust: trustForCompleteness(completenessLevelFromStats(h.Stats))}

}

func loadGraphManifest(ctx context.Context, opts Options, repoDir, brainDir string) (*exportManifest, error) {
	manifest, err := loadBrainManifest(brainDir)
	if err != nil {
		return nil, err
	}
	if manifest.Sources == nil {
		manifest.Sources = &brainSources{}
	}
	profile := graphStoreProfile(manifest)
	d, err := fetchGraphStore(ctx, opts, repoDir, "", profile)
	if err != nil {
		return nil, err
	}
	manifest.Sources.Semantic = graphStoreSource(d, brainDir)
	return manifest, nil
}

func graphStoreProfile(manifest *exportManifest) string {
	if manifest != nil && manifest.Sources != nil && manifest.Sources.Semantic != nil && manifest.Sources.Semantic.Profile != "" {
		return manifest.Sources.Semantic.Profile
	}
	return "full"
}

// openSemanticReadDB opens the provider's versioned projection. TEMP VIEWs
// are connection-local aliases, not a second store. A single connection keeps
// those aliases visible throughout the operation. mode=ro protects Graph even
// if a future caller accidentally attempts a write.
func openSemanticReadDB(handle string) (*sql.DB, error) {
	if !strings.HasPrefix(handle, graphStoreHandlePrefix) {
		return sql.Open(sqliteDriverName, sqliteReadOnlyDSN(handle))
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(handle, graphStoreHandlePrefix))
	if err != nil {
		return nil, err
	}
	var d graphStoreDescriptor
	if err = json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	db, err := sql.Open(sqliteDriverName, sqliteReadOnlyDSN(d.Path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*sql.DB, error) { db.Close(); return nil, err }
	var digest string
	if err = db.QueryRow("SELECT value FROM meta WHERE key='entry_digest'").Scan(&digest); err != nil {
		return fail(err)
	}
	var actual string
	if err = json.Unmarshal([]byte(digest), &actual); err != nil || actual != d.EntryDigest {
		return fail(errors.New("Graph store changed after resolution; retry the operation"))
	}
	for _, table := range []string{"symbols", "relations", "files", "reverse_relations", "symbol_fts"} {
		if _, err = db.Exec("CREATE TEMP VIEW " + table + " AS SELECT * FROM main.brain_" + table + "_v1"); err != nil {
			return fail(err)
		}
	}
	if err = loadGraphTraceTable(db, d.BrainDir); err != nil {
		return fail(err)
	}
	return db, nil
}

func runGraphOwnedIndex(ctx context.Context, cmd *cobra.Command, opts Options, indexOpts semanticIndexOptions, target string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if indexOpts.worktree {
		return errors.New("Graph owns the committed-tree index; use entire graph query without --head to inspect uncommitted code")
	}
	repoDir, local, err := resolveLocalTargetRepoDir(ctx, opts.Runner, target)
	if err != nil {
		return err
	}
	if !local {
		return fmt.Errorf("index requires a local repository: %s", target)
	}
	if err = enforceIndexContainment(indexOpts.containRoot, repoDir); err != nil {
		return err
	}
	if _, err := gitScalar(ctx, opts.Runner, repoDir, "rev-parse", "HEAD"); err != nil {
		return nameDegenerateRepoFailure(ctx, opts.Runner, repoDir, fmt.Errorf("resolve HEAD for Graph index: %w", err))
	}
	storage, err := repoStoragePaths(ctx, opts.Runner, opts.Env, repoDir)
	if err != nil {
		return err
	}
	if indexOpts.outputExplicit {
		storage.BrainDir, err = filepath.Abs(indexOpts.outputDir)
		if err != nil {
			return err
		}
	}
	// Retire the duplicate even on installations without Graph. Authored
	// observations are durable; code indexes are reproducible provider data.
	if err := migrateBrainGraphStorage(storage.BrainDir, storage.Key); err != nil {
		return err
	}
	binary := indexOpts.graphBinary
	if binary == "" {
		binary = indexOpts.semBinary
	}
	if binary == "" {
		binary = "entire"
	}
	if indexOpts.skipGraph || indexOpts.skipSem {
		return semanticProviderAbsentError{err: errors.New("Graph was explicitly disabled")}
	}
	doctor, warnings := runSemanticDoctor(ctx, opts.Runner, repoDir, binary)
	if !doctor.NoEgress {
		return semanticDoctorFailureError(binary, warnings)
	}
	profile := indexOpts.profile
	if profile == "" {
		profile = "full"
	}
	args := []string{"graph", "index", "--repo", repoDir, "--head", "--profile", profile, "--format", "json"}
	if indexOpts.force {
		args = append(args, "--force")
	}
	ignores, err := semanticProviderIgnoreFiles(repoDir)
	if err != nil {
		return err
	}
	for _, ignore := range ignores {
		args = append(args, "--ignore-file", ignore)
	}
	runner, ok := opts.Runner.(EnvironmentCommandRunner)
	if !ok {
		return errors.New("Graph indexing requires a runner supporting per-command environment")
	}
	if _, probeErr := fetchGraphStore(ctx, opts, repoDir, binary, profile); errors.Is(probeErr, errGraphStoreUnsupported) {
		return probeErr
	}
	timeout := indexOpts.timeout
	if timeout <= 0 {
		timeout = semanticSnapshotTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	indexOpts.reportPhase("building Graph-owned store")
	stdout, stderr, err := runner.RunWithEnv(ctx, repoDir, map[string]string{"ENTIRE_GRAPH_SEARCH_STORE": "1"}, binary, args...)
	if err != nil {
		return fmt.Errorf("Graph index: %w: %s", err, firstLine(strings.TrimSpace(string(stderr))))
	}
	d, err := fetchGraphStore(ctx, opts, repoDir, binary, profile)
	if err != nil {
		return err
	}
	unlock, err := acquireSemanticIndexLock(storage.BrainDir)
	if err != nil {
		return err
	}
	defer unlock()
	// Preserve authored traces before removing any legacy graph artifacts.
	if err = migrateLegacyGraphTraces(storage.BrainDir); err != nil {
		return err
	}
	source := graphStoreSource(d, storage.BrainDir)
	source.StorePath = ""
	source.GeneratedAt = opts.Now().UTC()
	source.Profile = profile
	source.GraphRepoRoot = repoDir
	if err = writeBrainSemanticSource(storage.BrainDir, storage.Key, source); err != nil {
		return err
	}
	removed, err := removeLegacyGraphArtifacts(storage.BrainDir)
	if err != nil {
		return err
	}
	if removed {
		fmt.Fprintln(os.Stderr, "brain: retired the duplicate semantic index; Graph owns code indexing now")
	}

	var indexed struct {
		CacheHit bool `json:"index_cache_hit"`
	}
	_ = json.Unmarshal(stdout, &indexed)
	label := "Graph-owned index"
	if indexed.CacheHit {
		label += " already current"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s: %d files, %d symbols, %d relations\n", label, d.Files, d.Symbols, d.Relations)
	return nil
}

// removeLegacyGraphArtifacts deletes the pre-Graph-ownership semantic store
// directories, if present, and reports whether it actually deleted anything --
// the one signal callers need to announce a real, one-time destructive
// removal rather than a routine no-op on an already-migrated brain.
func removeLegacyGraphArtifacts(brainDir string) (bool, error) {
	removed := false
	for _, rel := range []string{"semantic/generations", "semantic/snapshots"} {
		if err := rejectExistingSymlinkPathComponents(brainDir, rel); err != nil {
			return removed, err
		}
		path := filepath.Join(brainDir, rel)
		if _, err := os.Lstat(path); err == nil {
			removed = true
		} else if !os.IsNotExist(err) {
			return removed, err
		}
		if err := os.RemoveAll(path); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func migrateBrainGraphStorage(brainDir, repoKey string) error {
	unlock, err := acquireSemanticIndexLock(brainDir)
	if err != nil {
		return err
	}
	defer unlock()
	manifest, err := loadBrainManifest(brainDir)
	if err != nil {
		return err
	}
	if manifest.Sources == nil || manifest.Sources.Semantic == nil {
		return nil
	}
	if err = migrateLegacyGraphTraces(brainDir); err != nil {
		return err
	}
	if manifest.Sources.Semantic.StoreOwner != "graph" {
		source := &semanticSourceManifest{StoreOwner: "graph", Provider: "entire-graph", Profile: manifest.Sources.Semantic.Profile}
		if err = writeBrainSemanticSource(brainDir, repoKey, source); err != nil {
			return err
		}
	}
	removed, err := removeLegacyGraphArtifacts(brainDir)
	if err != nil {
		return err
	}
	if removed {
		fmt.Fprintln(os.Stderr, "brain: retired the duplicate semantic index; Graph owns code indexing now")
	}
	return nil
}
