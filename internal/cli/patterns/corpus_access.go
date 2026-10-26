package patterns

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
	"github.com/entireio/entire-brain/internal/cli/internal/sqlitedriver"
)

// BeforeCorpusPublish is a deterministic failure/race seam. Production
// leaves it as a no-op; tests use it after the complete staging database has
// passed integrity checks but before the live corpus transaction begins.
var BeforeCorpusPublish = func() error { return nil }

// CorpusReadDB owns both an immutable SQLite connection and the private
// snapshot backing it. Close always removes the snapshot, including when the
// SQLite close itself reports an error.
type CorpusReadDB struct {
	*sql.DB
	cleanup  func()
	closeErr error
	closeOne sync.Once
}

func (db *CorpusReadDB) Close() error {
	db.closeOne.Do(func() {
		db.closeErr = db.DB.Close()
		db.cleanup()
	})
	return db.closeErr
}

// RejectInterruptedCorpusPublication makes the marker the publication
// fail-closed bit. It is checked on both sides of every snapshot so a reader can
// never accept a main/WAL combination observed inside the multi-file commit
// window.
func RejectInterruptedCorpusPublication(brainDir string) error {
	_, present, err := brainstore.PrivacyArtifactInfo(brainDir, CorpusPublicationMarkerPath, "pattern corpus publication marker")
	if err != nil {
		return err
	}
	if present {
		return fmt.Errorf("%s: pattern corpus publication was interrupted; run a locked pattern refresh to recover", brainstore.StateUnsafeCode)
	}
	return nil
}

// OpenCorpusReadDB opens a bounded, descriptor-pinned private snapshot
// of the corpus. SQLite never sees the Brain-controlled pathname, and immutable
// mode prevents even the private reader from creating journals or shared-memory
// files. Reads never create or migrate schema.
func OpenCorpusReadDB(brainDir string) (*CorpusReadDB, error) {
	if err := RejectInterruptedCorpusPublication(brainDir); err != nil {
		return nil, err
	}
	_, present, err := brainstore.PrivacyArtifactInfo(brainDir, CorpusPath, "pattern corpus")
	if err != nil {
		return nil, err
	}
	if !present {
		if markerErr := RejectInterruptedCorpusPublication(brainDir); markerErr != nil {
			return nil, markerErr
		}
		return nil, fmt.Errorf("pattern corpus not found (run `"+base.BrainInvocation(os.LookupEnv, "patterns refresh")+"` first): %w", os.ErrNotExist)
	}
	snapshotPath, cleanup, err := brainstore.SnapshotPrivacySQLiteStore(brainDir, CorpusPath)
	if err != nil {
		return nil, err
	}
	if err := RejectInterruptedCorpusPublication(brainDir); err != nil {
		cleanup()
		return nil, err
	}
	db, err := sql.Open(sqlitedriver.Name, brainstore.SQLiteReadOnlyDSN(snapshotPath))
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("%s: open private pattern corpus snapshot: %w", brainstore.StateCorruptCode, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		cleanup()
		return nil, fmt.Errorf("%s: open private pattern corpus snapshot: %w", brainstore.StateCorruptCode, err)
	}
	return &CorpusReadDB{DB: db, cleanup: cleanup}, nil
}

// CorpusHasTable reports whether a table exists in this snapshot.
//
// Reads open an immutable snapshot and deliberately never apply schema, so an
// on-disk corpus written before an ADDITIVE table (themes, deep_dossiers) still
// has to answer queries against it. The read path previously opened read-write
// and ran the CREATE TABLE IF NOT EXISTS schema for exactly this reason; with
// that gone, a legacy corpus made `patterns list` and `patterns skills` fail
// with "no such table" where they used to return an empty set. There is no
// read-time version gate to lean on (schema_version is written but never
// compared), so the readers check for the table and treat absent as empty. The
// next `patterns refresh` creates it.
func CorpusHasTable(db *sql.DB, table string) (bool, error) {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%s: inspect pattern corpus schema: %w", brainstore.StateCorruptCode, err)
	}
	return true, nil
}

// OpenCorpusReadDBIfPresent preserves the only graceful fallback: a
// genuinely absent rebuildable corpus. Unsafe aliases, inconsistent snapshots,
// corruption, and permission failures remain errors and must reach the command
// boundary rather than masquerading as "not built".
func OpenCorpusReadDBIfPresent(brainDir string) (*CorpusReadDB, bool, error) {
	db, err := OpenCorpusReadDB(brainDir)
	if err == nil {
		return db, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return nil, false, err
}

// ValidateCorpusReadSafety is the cheap command-boundary check used by
// best-effort surfaces whose legacy loader signatures cannot return errors. The
// snapshot opener repeats these checks and pins descriptors, so a path race can
// only make the optional read disappear; it can never redirect SQLite outside
// the Brain. Missing corpora remain a supported legacy/fresh-install state.
func ValidateCorpusReadSafety(brainDir string) error {
	if err := RejectInterruptedCorpusPublication(brainDir); err != nil {
		return err
	}
	_, present, err := brainstore.PrivacyArtifactInfo(brainDir, CorpusPath, "pattern corpus")
	if err != nil {
		return err
	}
	if !present {
		return RejectInterruptedCorpusPublication(brainDir)
	}
	_, _, err = brainstore.PrivacyArtifactInfo(brainDir, CorpusPath+"-wal", "pattern corpus WAL")
	if err != nil {
		return err
	}
	return RejectInterruptedCorpusPublication(brainDir)
}

// WithCorpusMutationLocked clones the live corpus into a private
// staging database, applies one complete mutation there, and publishes only a
// normalized, integrity-checked result. The caller must hold the Brain lock.
func WithCorpusMutationLocked(brainDir string, mutate func(*sql.DB) error, decode func([]byte) (CorpusPublication, error)) error {
	if err := RecoverCorpusPublicationLocked(brainDir, decode); err != nil {
		return err
	}
	_, present, err := brainstore.PrivacyArtifactInfo(brainDir, CorpusPath, "pattern corpus")
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("pattern corpus not found (run `"+base.BrainInvocation(os.LookupEnv, "patterns refresh")+"` first): %w", os.ErrNotExist)
	}
	path, cleanup, err := PrepareCorpusStaging(brainDir)
	if err != nil {
		return err
	}
	defer cleanup()
	db, err := sql.Open(sqlitedriver.Name, path)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(pragma); err != nil {
			return err
		}
	}
	for _, stmt := range CorpusSchema {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	if err := mutate(db); err != nil {
		return err
	}
	return FinalizeCorpusStagingLocked(brainDir, path, db, decode)
}

func FinalizeCorpusStagingLocked(brainDir, path string, db *sql.DB, decode func([]byte) (CorpusPublication, error)) error {
	// No committed rows may remain only in a staging WAL.
	var busy, logFrames, checkpointedFrames int
	if err := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil || busy != 0 || (logFrames > 0 && checkpointedFrames != logFrames) {
		return fmt.Errorf("checkpoint staged pattern corpus before publication")
	}
	// Candidate construction legitimately uses more than one database/sql
	// connection. Close the pool before changing journal mode, which requires
	// the normalizer to be the sole live connection.
	if err := db.Close(); err != nil {
		return err
	}
	normalizer, err := sql.Open(sqlitedriver.Name, path)
	if err != nil {
		return err
	}
	normalizer.SetMaxOpenConns(1)
	if _, err := normalizer.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		_ = normalizer.Close()
		return err
	}
	if _, err := normalizer.Exec(`PRAGMA journal_mode=DELETE`); err != nil {
		_ = normalizer.Close()
		return fmt.Errorf("normalize staged pattern corpus journal: %w", err)
	}
	var integrity string
	if err := normalizer.QueryRow(`PRAGMA quick_check`).Scan(&integrity); err != nil || integrity != "ok" {
		_ = normalizer.Close()
		return fmt.Errorf("%s: staged pattern corpus failed integrity check", brainstore.StateCorruptCode)
	}
	if err := normalizer.Close(); err != nil {
		return err
	}
	if err := BeforeCorpusPublish(); err != nil {
		return err
	}
	return PublishCorpusLocked(brainDir, path, decode)
}
