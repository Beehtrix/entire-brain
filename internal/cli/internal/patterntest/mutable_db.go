package patterntest

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
	"github.com/entireio/entire-brain/internal/cli/internal/sqlitedriver"
	patterncore "github.com/entireio/entire-brain/internal/cli/patterns"
)

// OpenCorpusMutableDB is test-only setup access. Production mutations
// always clone into private staging and publish through a pinned os.Root.
func OpenCorpusMutableDB(brainDir string) (*sql.DB, error) {
	_, present, err := brainstore.PrivacyArtifactInfo(brainDir, patterncore.CorpusPath, "pattern corpus")
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, fmt.Errorf("pattern corpus not found: %w", os.ErrNotExist)
	}
	path, err := base.PrepareBrainRelativeSQLiteFile(brainDir, patterncore.CorpusPath)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(sqlitedriver.Name, path)
	if err != nil {
		return nil, err
	}
	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	for _, stmt := range patterncore.CorpusSchema {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}
