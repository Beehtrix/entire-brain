package patterntest

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/internal/sqlitedriver"
	patterncore "github.com/entireio/entire-brain/internal/cli/patterns"
)

func FreshCorpusDB(t *testing.T) *sql.DB {
	t.Helper()
	path, err := base.PrepareBrainRelativeSQLiteFile(t.TempDir(), patterncore.CorpusPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(sqlitedriver.Name, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range patterncore.CorpusSchema {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func InsertCorpusEpisode(t *testing.T, db *sql.DB, id, intentSig, outcome string, nCmds int, now time.Time) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO episodes
		(id, episode_key, repo_key, session_id, turn_ord, source_path, start_line, end_line, intent_sig, n_cmds, outcome, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, id, "gh/acme/cli", id, 0, "sessions/main/x.jsonl", 1, 5, intentSig, nCmds, outcome, now.UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
}

// InsertCorpusShape records a command shape for an episode: one gram joining the
// heads with GramSep (matching production) and the per-command episode_commands
// rows that feed loadCommandDF / gramSpecificity.
func InsertCorpusShape(t *testing.T, db *sql.DB, episodeID string, heads ...string) {
	t.Helper()
	gram := strings.Join(heads, patterncore.GramSep)
	if _, err := db.Exec(`INSERT INTO grams (episode_id, n, gram) VALUES (?,?,?)`, episodeID, len(heads), gram); err != nil {
		t.Fatal(err)
	}
	for i, h := range heads {
		if _, err := db.Exec(`INSERT INTO episode_commands (episode_id, ord, head, raw_redacted, line, failed) VALUES (?,?,?,?,?,0)`,
			episodeID, i, h, h, i+1); err != nil {
			t.Fatal(err)
		}
	}
}

func TaskStrengthFor(t *testing.T, db *sql.DB, intentSig string) (float64, bool) {
	t.Helper()
	var s float64
	err := db.QueryRow(`SELECT strength FROM patterns WHERE type='task' AND intent_sig=?`, intentSig).Scan(&s)
	if err == sql.ErrNoRows {
		return 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return s, true
}

type corpusRowQueryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func CorpusCount(t *testing.T, db corpusRowQueryer, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// PromotableCorpus builds a corpus with one promotable, corroborated task
// (deploy:release via "mise build ▷ mise deploy", weight>=2 fact-linked) plus
// generic noise so the deploy shape is specific. Returns the db.
func PromotableCorpus(t *testing.T, now time.Time) *sql.DB {
	t.Helper()
	db := FreshCorpusDB(t)
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("episode:d%d", i)
		InsertCorpusEpisode(t, db, id, "deploy:release", "success", 2, now)
		InsertCorpusShape(t, db, id, "mise build", "mise deploy")
		if _, err := db.Exec(`INSERT INTO episode_facts (episode_id, fact_id, kind, paths, weight) VALUES (?,?,?,?,2)`,
			id, "fact:deploy", "architecture", "ops/deploy.go"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("episode:u%d", i)
		InsertCorpusEpisode(t, db, id, "other:thing", "neutral", 2, now)
		InsertCorpusShape(t, db, id, "ls", "cat")
	}
	return db
}

func DossierFP(t *testing.T, db *sql.DB) string {
	t.Helper()
	_, fp := OnePromotableDossier(t, db)
	return fp
}

func OnePromotableDossier(t *testing.T, db *sql.DB) (string, string) {
	t.Helper()
	var pid, fp string
	if err := db.QueryRow(`SELECT pattern_id, fingerprint FROM dossiers ORDER BY pattern_id LIMIT 1`).Scan(&pid, &fp); err != nil {
		t.Fatal(err)
	}
	return pid, fp
}
