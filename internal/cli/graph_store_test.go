package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// The provider contract is also tested against real Graph in its repository;
// this fixture isolates Brain's lifetime, migration and read-only guarantees.
func newGraphStoreFixture(t *testing.T) (Options, string, string, graphStoreDescriptor) {
	t.Helper()
	repo := t.TempDir()
	env := semanticTestEnv(t, repo)
	runner := semanticFixtureRunner(repo, semanticFixtureSnapshot("1.0"))
	opts := Options{Version: "test", Env: env, Runner: runner, Now: func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) }}
	storage, err := repoStoragePaths(context.Background(), runner, env, repo)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "graph.sqlite")
	db, err := sql.Open(sqliteDriverName, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = initializeSemanticSQLite(db); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []semanticRecord{{ID: "render", Kind: "function", Name: "Render", QualifiedName: "demo.Render", FilePath: "main.go", StartLine: 2, EndLine: 3, Language: "Go"}, {ID: "normalize", Kind: "function", Name: "Normalize", QualifiedName: "demo.Normalize", FilePath: "main.go", StartLine: 4, EndLine: 5, Language: "Go"}} {
		if err = insertSemanticSymbol(tx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err = insertSemanticRelation(tx, semanticRecord{FromID: "render", ToID: "normalize", Type: "CALLS", Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"symbols", "relations", "files", "reverse_relations", "symbol_fts"} {
		if _, err = db.Exec("CREATE VIEW brain_" + name + "_v1 AS SELECT * FROM " + name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO meta(key,value) VALUES('entry_digest','"digest"')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	d := graphStoreDescriptor{ContractVersion: 1, Path: path, EntryDigest: "digest", Commit: "aaa111", Tree: "tree111", Files: 1, Symbols: 2, Relations: 1, Header: semanticHeader{Provider: "entire-graph", ProviderVersion: "test", SchemaVersion: "1.0", Profile: "full"}}
	data, _ := json.Marshal(d)
	runner.responses[fakeCommandKey("entire", "graph", "store", "--repo", repo, "--profile", "full")] = fakeCommandResponse{stdout: string(data)}
	runner.responses[fakeCommandKey("entire", "graph", "index", "--repo", repo, "--head", "--profile", "full", "--format", "json")] = fakeCommandResponse{stdout: `{}`}
	return opts, repo, storage.BrainDir, d
}

func TestGraphOwnedIndexHasNoBrainGraphArtifacts(t *testing.T) {
	opts, repo, brain, d := newGraphStoreFixture(t)
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runSemanticIndex(context.Background(), cmd, opts, semanticIndexOptions{}, repo); err != nil {
		t.Fatal(err)
	}
	m, err := loadBrainManifest(brain)
	if err != nil {
		t.Fatal(err)
	}
	s := m.Sources.Semantic
	if s.StoreOwner != "graph" || s.StorePath != "" || s.SnapshotPath != "" || s.GenerationPath != "" {
		t.Fatalf("persisted a duplicate or local handle: %+v", s)
	}
	for _, rel := range []string{"semantic/generations", "semantic/snapshots"} {
		if _, err := os.Stat(filepath.Join(brain, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s exists: %v", rel, err)
		}
	}
	before, _ := os.ReadFile(d.Path)
	for _, run := range []func() error{
		func() error {
			return runSemanticQuery(context.Background(), cmd, opts, semanticQueryOptions{limit: 10, json: true}, "Render")
		},
		func() error {
			return runSemanticContext(context.Background(), cmd, opts, semanticContextOptions{limit: 10, json: true}, "Render")
		},
		func() error {
			return runSemanticImpact(context.Background(), cmd, opts, semanticImpactOptions{limit: 10, depth: 2, json: true}, "Render")
		},
	} {
		out.Reset()
		if err := run(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Render") {
			t.Fatalf("missing symbol: %s", out.String())
		}
	}
	syms, err := loadSemanticSymbols(brain, graphStoreSource(d, brain), 10)
	if err != nil || len(syms) != 2 {
		t.Fatalf("dashboard Graph symbols: %v, %v", syms, err)
	}
	after, _ := os.ReadFile(d.Path)
	if !bytes.Equal(before, after) {
		t.Fatal("Brain changed Graph's store")
	}
	db, err := openSemanticReadDB(graphStoreSource(d, brain).StorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("DELETE FROM main.symbols"); err == nil {
		t.Fatal("Graph store was writable")
	}
}

func TestGraphStoreDescriptorRaceRefusesReplacement(t *testing.T) {
	_, _, brain, d := newGraphStoreFixture(t)
	d.EntryDigest = "stale"
	if db, err := openSemanticReadDB(graphStoreSource(d, brain).StorePath); err == nil {
		db.Close()
		t.Fatal("served replacement store")
	}
}

func TestGraphTraceMigrationPreservesAndDeduplicates(t *testing.T) {
	opts, _, brain, d := newGraphStoreFixture(t)
	rel := "semantic/generations/legacy/semantic.sqlite"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(brain, rel)), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(brain, rel), data, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(sqliteDriverName, filepath.Join(brain, rel))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO runtime_traces(imported_at,source_path,from_id,to_id,observed_type,matched_static_edge) VALUES('2026-10-22T00:00:00Z','trace.json','render','normalize','CALLS',1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = writeBrainSemanticSource(brain, "gh/example/repo", &semanticSourceManifest{StorePath: rel, GenerationPath: "semantic/generations/legacy"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = migrateLegacyGraphTraces(brain); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := readGraphTraces(brain)
	if err != nil || len(rows) != 1 || rows[0].FromID != "render" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	handle := graphStoreSource(d, brain).StorePath
	db, err = openSemanticReadDB(handle)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err = db.QueryRow("SELECT count(*) FROM runtime_traces").Scan(&n); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	_ = opts
}

func TestGraphAbsentBriefStillWorksWithLegacyStore(t *testing.T) {
	opts, repo, brain, _ := newGraphStoreFixture(t)
	// Even a corrupt obsolete pointer is ignored by brief; facts/history remain usable.
	if err := writeBrainSemanticSource(brain, "gh/example/repo", &semanticSourceManifest{StorePath: "missing", SnapshotPath: "missing"}); err != nil {
		t.Fatal(err)
	}
	runner := opts.Runner.(*fakeCommandRunner)
	delete(runner.responses, fakeCommandKey("entire", "graph", "store", "--repo", repo, "--profile", "full"))
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runBrainBrief(context.Background(), cmd, opts, brainBriefOptions{json: true, limit: 5}, "modify Render"); err != nil {
		t.Fatal(err)
	}
	var report brainBriefJSONReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Semantic.Context.Symbols) != 0 {
		t.Fatal("used obsolete Brain graph")
	}
}

func TestGraphTraceBundleRoundTripContainsNoCodeStore(t *testing.T) {
	opts, repo, brain, _ := newGraphStoreFixture(t)
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runSemanticIndex(context.Background(), cmd, opts, semanticIndexOptions{}, repo); err != nil {
		t.Fatal(err)
	}
	observations := []graphTraceObservation{{ImportedAt: "2026-10-22T00:00:00Z", SourcePath: graphTraceFile, FromID: "render", ToID: "normalize", ObservedType: "CALLS", Matched: 1}}
	if err := writeGraphTraces(brain, observations); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "brain.tar")
	if err := runSemanticBundleExport(context.Background(), cmd, opts, archive, false); err != nil {
		t.Fatal(err)
	}
	entries := readTestBundleEntries(t, archive)
	found := false
	for _, name := range entries {
		if name == graphTraceFile {
			found = true
		}
		if strings.HasSuffix(name, ".sqlite") || strings.Contains(name, "semantic/snapshots") {
			t.Fatalf("copied Graph into bundle: %s", name)
		}
	}
	if !found {
		t.Fatalf("lost observations: %v", entries)
	}
	// Import into a fresh Brain under the same repository identity.
	opts.Env.PluginDataDir = t.TempDir()
	if err := runSemanticBundleImport(context.Background(), cmd, opts, archive, bundleSHA256(t, archive), false); err != nil {
		t.Fatal(err)
	}
	dest, err := repoStoragePaths(context.Background(), opts.Runner, opts.Env, repo)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := readGraphTraces(dest.BrainDir)
	if err != nil || len(rows) != 1 || rows[0] != observations[0] {
		t.Fatalf("observations changed: %+v (%v)", rows, err)
	}
}

func TestGraphMissingStoreDoesNotCreateAFile(t *testing.T) {
	_, _, brain, d := newGraphStoreFixture(t)
	if err := os.Remove(d.Path); err != nil {
		t.Fatal(err)
	}
	if db, err := openSemanticReadDB(graphStoreSource(d, brain).StorePath); err == nil {
		db.Close()
		t.Fatal("opened absent provider database")
	}
	if _, err := os.Stat(d.Path); !os.IsNotExist(err) {
		t.Fatalf("read created database: %v", err)
	}
}

func TestLegacyGraphMigrationRefusesToLoseUnreadableTraces(t *testing.T) {
	_, _, brain, _ := newGraphStoreFixture(t)
	rel := "semantic/generations/legacy/semantic.sqlite"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(brain, rel)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brain, rel), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeBrainSemanticSource(brain, "gh/example/repo", &semanticSourceManifest{GenerationPath: "semantic/generations/legacy", StorePath: rel}); err != nil {
		t.Fatal(err)
	}
	if err := migrateBrainGraphStorage(brain, "gh/example/repo"); err == nil {
		t.Fatal("discarded unreadable legacy observations")
	}
	m, err := loadBrainManifest(brain)
	if err != nil || m.Sources.Semantic.StorePath != rel {
		t.Fatalf("changed legacy pointer on failed migration: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(brain, rel)); err != nil || string(data) != "corrupt" {
		t.Fatalf("deleted legacy store: %v", err)
	}
}
