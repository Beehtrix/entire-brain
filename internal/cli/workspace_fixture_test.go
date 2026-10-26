package cli

// twoRepoWorkspace and seedPromotableMember are deliberate copies of the
// fixtures in internal/cli/patterns/workspace_semantic_integration_test.go:
// the root-command integration tests here exercise the same seeded workspace
// through the full CLI, and an in-package patterns test cannot be imported.

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/internal/patterntest"
	"github.com/entireio/entire-brain/internal/cli/internal/sqlitedriver"
	patterncore "github.com/entireio/entire-brain/internal/cli/patterns"
	"github.com/entireio/entire-brain/internal/cli/workspace"
)

// seedPromotableMember builds a member corpus whose (intentSig, heads) task is
// promotable (12 corroborated successes + noise), so it surfaces as a member
// task candidate for the workspace judgment layer.
func seedPromotableMember(t *testing.T, brainDir, repoKey, intentSig string, heads ...string) {
	t.Helper()
	path, err := base.PrepareBrainRelativeSQLiteFile(brainDir, patterncore.CorpusPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(sqlitedriver.Name, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range patterncore.CorpusSchema {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	now := time.Now()
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("%s:ok%d", intentSig, i)
		patterntest.InsertCorpusEpisode(t, db, id, intentSig, "success", len(heads), now)
		patterntest.InsertCorpusShape(t, db, id, heads...)
		db.Exec(`INSERT INTO episode_facts (episode_id, fact_id, kind, paths, weight) VALUES (?,?,?,?,2)`,
			id, "fact:"+intentSig, "architecture", "ops/x.go")
	}
	for i := 0; i < 120; i++ {
		id := fmt.Sprintf("%s:noise%d", intentSig, i)
		patterntest.InsertCorpusEpisode(t, db, id, "other:thing", "neutral", 2, now)
		patterntest.InsertCorpusShape(t, db, id, "ls", "cat")
	}
	if err := patterncore.BuildCandidates(db, repoKey, now); err != nil {
		t.Fatal(err)
	}
}

func twoRepoWorkspace(t *testing.T, a, aIntent string, aHeads []string, b, bIntent string, bHeads []string) (EntireEnv, workspace.Manifest) {
	t.Helper()
	env := EntireEnv{PluginDataDir: t.TempDir()}
	aDir, _ := base.BrainDirForKey(env, a)
	bDir, _ := base.BrainDirForKey(env, b)
	seedPromotableMember(t, aDir, a, aIntent, aHeads...)
	seedPromotableMember(t, bDir, b, bIntent, bHeads...)
	manifest := workspace.Manifest{Name: "plat", Repos: []workspace.Repo{{RepoKey: a}, {RepoKey: b}}}
	if err := workspace.DefaultStore(env).WriteManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := buildWorkspacePatternCorpus(env, manifest, time.Now()); err != nil {
		t.Fatal(err)
	}
	return env, manifest
}
