package brainstore

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/internal/sqlitedriver"
)

func ValidatePrivacySQLiteSnapshot(path, rel string) error {
	db, err := sql.Open(sqlitedriver.Name, SQLiteReadOnlyDSN(path))
	if err != nil {
		return fmt.Errorf("%s: open derived SQLite store %s: %w", StateCorruptCode, rel, err)
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("%s: verify derived SQLite store %s: %w", StateCorruptCode, rel, err)
	}
	if integrity != "ok" {
		return fmt.Errorf("%s: derived SQLite store %s failed integrity check: %s", StateCorruptCode, rel, integrity)
	}
	return nil
}

const PrivacySQLiteSnapshotMaxBytes int64 = 16 << 30

type privacySQLiteSnapshotSource struct {
	rel      string
	path     string
	expected os.FileInfo
	file     *os.File
}

// SnapshotPrivacySQLiteStore pins the Brain-relative main database and its WAL
// through descriptor-bound, no-follow opens, then streams a bounded copy into
// a private temporary directory. The private WAL is checkpointed before the
// immutable verification readers open the snapshot. Copying only the main file
// would miss committed rows that have not yet been checkpointed from WAL.
// SQLite never receives a repository-controlled path.
func SnapshotPrivacySQLiteStore(brainDir, rel string) (string, func(), error) {
	clean, err := base.CleanBrainRelativePath(filepath.ToSlash(strings.TrimSpace(rel)))
	if err != nil {
		return "", nil, fmt.Errorf("%s: unsafe SQLite store path: %w", StateUnsafeCode, err)
	}
	rel = filepath.ToSlash(clean)

	openSource := func(sourceRel, label string, required bool) (*privacySQLiteSnapshotSource, bool, error) {
		expected, present, infoErr := PrivacyArtifactInfo(brainDir, sourceRel, label)
		if infoErr != nil {
			return nil, present, infoErr
		}
		if !present {
			if required {
				return nil, false, fmt.Errorf("%s: SQLite store disappeared before verification: %s", StateCorruptCode, sourceRel)
			}
			return nil, false, nil
		}
		path := filepath.Join(brainDir, filepath.FromSlash(sourceRel))
		file, openErr := PrivacyOpen(path, os.O_RDONLY|base.FileLockOpenFlags()|base.MemoryStateReadOpenFlags(), 0)
		if openErr != nil {
			return nil, true, fmt.Errorf("%s: open %s without following links: %w", StateUnsafeCode, label, openErr)
		}
		if aliasErr := base.RejectOpenFileAlias(path, file, label); aliasErr != nil {
			_ = file.Close()
			return nil, true, fmt.Errorf("%s: validate %s identity: %w", StateUnsafeCode, label, aliasErr)
		}
		return &privacySQLiteSnapshotSource{rel: sourceRel, path: path, expected: expected, file: file}, true, nil
	}

	mainSource, _, err := openSource(rel, "SQLite store", true)
	if err != nil {
		return "", nil, err
	}
	sources := []*privacySQLiteSnapshotSource{mainSource}
	defer func() {
		for _, source := range sources {
			_ = source.file.Close()
		}
	}()
	walRel := rel + "-wal"
	walSource, walPresent, err := openSource(walRel, "SQLite WAL", false)
	if err != nil {
		return "", nil, err
	}
	if walPresent {
		sources = append(sources, walSource)
	}

	tempDir, err := os.MkdirTemp("", "entire-brain-privacy-sqlite-")
	if err != nil {
		return "", nil, fmt.Errorf("%s: create private SQLite verification directory: %w", StateCorruptCode, err)
	}
	cleanup := func() { _ = os.RemoveAll(tempDir) }
	snapshotPath := filepath.Join(tempDir, "snapshot.sqlite")
	totalCopied := int64(0)
	for i, source := range sources {
		if source.expected.Size() < 0 || source.expected.Size() > PrivacySQLiteSnapshotMaxBytes-totalCopied {
			cleanup()
			return "", nil, fmt.Errorf("%s: SQLite store and WAL exceed the bounded snapshot ceiling", StateUnsafeCode)
		}
		destination := snapshotPath
		if i > 0 {
			destination += "-wal"
		}
		snapshot, createErr := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			cleanup()
			return "", nil, fmt.Errorf("%s: create SQLite verification snapshot: %w", StateCorruptCode, createErr)
		}
		remaining := PrivacySQLiteSnapshotMaxBytes - totalCopied
		copied, copyErr := io.Copy(snapshot, io.LimitReader(source.file, remaining+1))
		closeErr := snapshot.Close()
		if copyErr != nil || closeErr != nil || copied != source.expected.Size() || copied > remaining {
			cleanup()
			return "", nil, fmt.Errorf("%s: SQLite store or WAL changed or exceeded bounds while snapshotting", StateUnsafeCode)
		}
		totalCopied += copied
	}

	// Revalidate every pinned descriptor and pathname after the complete set was
	// copied. This catches a checkpoint, append, truncation, or path replacement
	// that raced the main/WAL snapshot. Also prove an initially absent WAL did
	// not appear while the main database was being copied.
	for _, source := range sources {
		after, statErr := source.file.Stat()
		if statErr != nil || !os.SameFile(source.expected, after) || after.Size() != source.expected.Size() || !after.ModTime().Equal(source.expected.ModTime()) {
			cleanup()
			return "", nil, fmt.Errorf("%s: SQLite store or WAL changed while snapshotting", StateUnsafeCode)
		}
		if aliasErr := base.RejectOpenFileAlias(source.path, source.file, "SQLite snapshot source"); aliasErr != nil {
			cleanup()
			return "", nil, fmt.Errorf("%s: SQLite store or WAL path changed while snapshotting: %w", StateUnsafeCode, aliasErr)
		}
		current, present, infoErr := PrivacyArtifactInfo(brainDir, source.rel, "SQLite snapshot source")
		if infoErr != nil || !present || !os.SameFile(source.expected, current) || current.Size() != source.expected.Size() || !current.ModTime().Equal(source.expected.ModTime()) {
			cleanup()
			return "", nil, fmt.Errorf("%s: SQLite store or WAL identity changed while snapshotting", StateUnsafeCode)
		}
	}
	if !walPresent {
		if _, present, infoErr := PrivacyArtifactInfo(brainDir, walRel, "SQLite WAL"); infoErr != nil || present {
			cleanup()
			return "", nil, fmt.Errorf("%s: SQLite WAL appeared while snapshotting", StateUnsafeCode)
		}
	}

	// Replay committed WAL frames only inside the trusted temporary directory.
	// The later immutable readers can now inspect one self-contained main file.
	db, err := sql.Open(sqlitedriver.Name, snapshotPath)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("%s: open private SQLite snapshot: %w", StateCorruptCode, err)
	}
	db.SetMaxOpenConns(1)
	var busy, logFrames, checkpointedFrames int
	checkpointErr := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointedFrames)
	closeErr := db.Close()
	if checkpointErr != nil || closeErr != nil || busy != 0 || (logFrames > 0 && checkpointedFrames != logFrames) {
		cleanup()
		return "", nil, fmt.Errorf("%s: replay private SQLite WAL before verification", StateCorruptCode)
	}
	return snapshotPath, cleanup, nil
}
