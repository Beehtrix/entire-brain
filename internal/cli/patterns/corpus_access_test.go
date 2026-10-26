package patterns_test

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
	"github.com/entireio/entire-brain/internal/cli/internal/sqlitedriver"
	patterncore "github.com/entireio/entire-brain/internal/cli/patterns"
)

func TestPatternCorpusReadSnapshotNeverMigratesLiveDatabaseAndCleansUp(t *testing.T) {
	brainDir := t.TempDir()
	path, err := base.PrepareBrainRelativeSQLiteFile(brainDir, patterncore.CorpusPath)
	if err != nil {
		t.Fatal(err)
	}
	live, err := sql.Open(sqlitedriver.Name, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := live.Exec(`CREATE TABLE sentinel (value TEXT NOT NULL)`); err != nil {
		live.Close()
		t.Fatal(err)
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	reader, err := patterncore.OpenCorpusReadDB(brainDir)
	if err != nil {
		t.Fatal(err)
	}
	var seq int
	var name, snapshotPath string
	if err := reader.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &snapshotPath); err != nil {
		reader.Close()
		t.Fatal(err)
	}
	if filepath.Clean(snapshotPath) == filepath.Clean(path) {
		reader.Close()
		t.Fatalf("reader opened live corpus path %s", path)
	}
	snapshotDir := filepath.Dir(snapshotPath)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("second close should be harmless: %v", err)
	}
	if _, err := os.Stat(snapshotDir); !os.IsNotExist(err) {
		t.Fatalf("private corpus snapshot leaked at %s: %v", snapshotDir, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("read-only corpus open modified the live database")
	}

	check, err := sql.Open(sqlitedriver.Name, brainstore.SQLiteReadOnlyDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var patternTables int
	if err := check.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='patterns'`).Scan(&patternTables); err != nil {
		t.Fatal(err)
	}
	if patternTables != 0 {
		t.Fatal("read-only corpus open migrated the live database")
	}
}
