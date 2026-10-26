package brainstore

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	clibase "github.com/entireio/entire-brain/internal/cli/internal/base"
)

// SessionsDirName is the brain-relative directory holding exported session
// transcripts.
const SessionsDirName = "sessions"

// ConversationSessionIDPrefix is the id vocabulary prefix for a conversation
// session reference.
const ConversationSessionIDPrefix = "conversation-session:"

const (
	PrivacyTransactionVersion       = 2
	PrivacyTransactionLegacyVersion = 1
	PrivacyTransactionSessionRefMax = 256

	PrivacyStateRequested  = "requested"
	PrivacyStateGuarded    = "guarded"
	PrivacyStateRebuilding = "rebuilding"
	PrivacyStateVerified   = "verified"
	PrivacyStateComplete   = "complete"
	PrivacyStateError      = "error"
)

type PrivacyTransaction struct {
	SchemaVersion int       `json:"schema_version"`
	SessionID     string    `json:"session_id"`
	Operation     string    `json:"operation"` // exclude | purge
	State         string    `json:"state"`
	StartedAt     time.Time `json:"started_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	// Artifacts carries identities and sizes only, never content.
	Artifacts   []PurgeArtifact `json:"artifacts,omitempty"`
	SessionRefs []string        `json:"session_refs,omitempty"`
	Error       string          `json:"error,omitempty"`
}

type PurgeArtifact struct {
	Path  string `json:"path"` // brain-relative
	Bytes int64  `json:"bytes"`
}

// windowsReservedDeviceNames are the DOS device names Windows still resolves in
// every directory, with or without an extension: `CON.json` names the console,
// not a file. The comparison is case-insensitive and the list has not changed
// in decades.
var windowsReservedDeviceNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// EscapePrivacyTransactionID turns a session id into one path component that is
// a legal filename on every platform this ships to.
//
// This used to be url.PathEscape alone, and PathEscape is a URL function, not a
// filename function: RFC 3986 allows a colon inside a path segment, so it
// deliberately leaves one unescaped. NTFS does not -- `a:b` names an alternate
// data stream -- so on Windows the FIRST write both `privacy exclude` and
// `privacy purge` perform (the transaction record, written before the
// tombstone) failed with ERROR_INVALID_PARAMETER, "The parameter is incorrect",
// for any id containing a colon, and neither command could run at all.
//
// Captured session ids are UUIDs and were never affected, which is why this
// survived: every test in the suite used a hyphenated fixture id. Ids an
// OPERATOR types are affected, and this tool's own id vocabulary is full of
// colons -- `conversation-session:…`, `fact:…`, `history:…` are what `get`,
// `search` and `multi-get` print and what their documented examples show -- so
// pasting an id the tool itself emitted was enough to hit it.
//
// PathEscape already escapes the rest of the Windows-illegal set (\ / * ? " < >
// |, control bytes, and a trailing space), so the fix is exactly the colon plus
// the reserved device names and nothing else. Every id whose characters are
// already safe -- every UUID, and so every real captured session -- keeps the
// byte-identical filename it has on disk today, and no existing record is
// renamed.
//
// This escaping is for FILENAMES ONLY. Do not reach for it at the three sites
// that escape a session id for the distill cache -- distillSessionCacheKey,
// the purge/exclude eviction in purgeDistillCacheEntries, and the check in
// verifySessionPrivacy. Those build a JSON MAP KEY, not a path, a colon is
// harmless in one, and the three match each other by suffix: hardening one
// without the others makes purge silently stop evicting and verification start
// reporting the leftovers.
//
// One residual: a POSIX brain that ran exclude/purge with a colon id before
// this fix (which could only ever have been a phantom, since no captured id
// contains one) keeps a file under the old name that nothing now reads. It is
// inert -- nothing enumerates this directory, transactions are only ever looked
// up by id, and the tombstone that actually enforces an exclusion lives in
// history/tombstones.json keyed by the raw id, so `privacy include` still
// clears it. A read-fallback to the old name was rejected deliberately: it
// would have to be threaded through includeSessionLocked's two-file rollback
// protocol, which is carefully built around a single transaction path, to
// recover a record that describes an operation that did nothing.
func EscapePrivacyTransactionID(sessionID string) string {
	escaped := strings.ReplaceAll(url.PathEscape(sessionID), ":", "%3A")
	if windowsReservedDeviceNames[strings.ToUpper(escaped)] {
		// Escaping the first byte is enough to stop the name resolving as a
		// device, and cannot collide with any other id: PathEscape never emits
		// %XX for an ASCII letter, so only "CON" can produce "%43ON".
		escaped = fmt.Sprintf("%%%02X%s", escaped[0], escaped[1:])
	}
	return escaped
}

func PrivacyTransactionRel(sessionID string) string {
	return filepath.ToSlash(filepath.Join(HistoryDirName, "privacy", EscapePrivacyTransactionID(sessionID)+".json"))
}

func WritePrivacyTransaction(brainDir string, tx PrivacyTransaction, now time.Time) error {
	if _, _, err := LoadPrivacyTransactionChecked(brainDir, tx.SessionID); err != nil {
		return err
	}
	tx.SchemaVersion = PrivacyTransactionVersion
	tx.UpdatedAt = now
	data, err := json.MarshalIndent(tx, "", "  ")
	if err != nil {
		return err
	}
	if _, err := DecodePrivacyTransaction(data, tx.SessionID); err != nil {
		return err
	}
	return WriteBrainRelativeFileAtomic(brainDir, PrivacyTransactionRel(tx.SessionID), append(data, '\n'), 0o600)
}

func LoadPrivacyTransaction(brainDir, sessionID string) (PrivacyTransaction, bool) {
	tx, present, err := LoadPrivacyTransactionChecked(brainDir, sessionID)
	if err != nil {
		return PrivacyTransaction{}, false
	}
	return tx, present
}

func LoadPrivacyTransactionChecked(brainDir, sessionID string) (PrivacyTransaction, bool, error) {
	rel := PrivacyTransactionRel(sessionID)
	data, present, err := ReadPrivacyArtifact(brainDir, rel, "privacy transaction", clibase.MaxManifestBytes)
	if err != nil || !present {
		return PrivacyTransaction{}, present, err
	}
	tx, err := DecodePrivacyTransaction(data, sessionID)
	if err != nil {
		return PrivacyTransaction{}, true, err
	}
	return tx, true, nil
}

func DecodePrivacyTransaction(data []byte, sessionID string) (PrivacyTransaction, error) {
	var tx PrivacyTransaction
	version, err := DecodeStrictVersionedJSON(data, &tx, PrivacyTransactionVersion, "privacy transaction")
	if err != nil {
		return PrivacyTransaction{}, err
	}
	validState := tx.State == PrivacyStateRequested || tx.State == PrivacyStateGuarded || tx.State == PrivacyStateRebuilding ||
		tx.State == PrivacyStateVerified || tx.State == PrivacyStateComplete || tx.State == PrivacyStateError
	if (version != PrivacyTransactionLegacyVersion && version != PrivacyTransactionVersion) || tx.SchemaVersion != version || tx.SessionID != sessionID ||
		(tx.Operation != "exclude" && tx.Operation != "purge") || !validState || tx.StartedAt.IsZero() || tx.UpdatedAt.IsZero() ||
		tx.UpdatedAt.Before(tx.StartedAt) {
		return PrivacyTransaction{}, fmt.Errorf("%s: invalid privacy transaction %s", StateCorruptCode, PrivacyTransactionRel(sessionID))
	}
	for _, artifact := range tx.Artifacts {
		if strings.TrimSpace(artifact.Path) == "" || artifact.Bytes < 0 {
			return PrivacyTransaction{}, fmt.Errorf("%s: invalid privacy transaction artifact %s", StateCorruptCode, PrivacyTransactionRel(sessionID))
		}
	}
	if len(tx.SessionRefs) > PrivacyTransactionSessionRefMax {
		return PrivacyTransaction{}, fmt.Errorf("%s: privacy transaction has too many session refs %s", StateCorruptCode, PrivacyTransactionRel(sessionID))
	}
	seenRefs := map[string]bool{}
	for _, ref := range tx.SessionRefs {
		if !strings.HasPrefix(ref, ConversationSessionIDPrefix) || strings.TrimSpace(strings.TrimPrefix(ref, ConversationSessionIDPrefix)) == "" || seenRefs[ref] {
			return PrivacyTransaction{}, fmt.Errorf("%s: invalid privacy transaction session ref %s", StateCorruptCode, PrivacyTransactionRel(sessionID))
		}
		seenRefs[ref] = true
	}
	return tx, nil
}

// PrivacyTransactionTranscriptPaths recovers the content-free transcript scope
// captured before cleanup. Validate persisted paths before classifying them so
// traversal cannot silently discard scope or turn a transcript into another
// artifact. Transactions written by cleanup use canonical brain-relative paths.
func PrivacyTransactionTranscriptPaths(tx PrivacyTransaction) ([]string, error) {
	var paths []string
	for _, artifact := range tx.Artifacts {
		clean, err := clibase.CleanBrainRelativePath(artifact.Path)
		if err != nil || !fs.ValidPath(artifact.Path) || filepath.ToSlash(clean) != artifact.Path {
			return nil, fmt.Errorf("%s: unsafe privacy transaction artifact path %q", StateUnsafeCode, artifact.Path)
		}
		if strings.HasPrefix(artifact.Path, SessionsDirName+"/") {
			paths = append(paths, artifact.Path)
		}
	}
	return paths, nil
}
