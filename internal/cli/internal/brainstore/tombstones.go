package brainstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	clibase "github.com/entireio/entire-brain/internal/cli/internal/base"
)

// HistoryDirName is the brain-relative directory holding the history
// projection and the session privacy state beside it.
const HistoryDirName = "history"

const (
	SessionTombstonesFileName = "tombstones.json"
	SessionTombstonesPath     = HistoryDirName + "/" + SessionTombstonesFileName
	SessionTombstonesVersion  = 1
)

type SessionTombstones struct {
	Version int `json:"version"`
	// Excluded maps session_id -> tombstone. Content-free by design: a
	// tombstone must never retain what it excludes.
	Excluded map[string]SessionTombstone `json:"excluded"`
}

type SessionTombstone struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
}

type SessionTombstoneState string

const (
	SessionTombstoneAbsent      SessionTombstoneState = "absent"
	SessionTombstoneCurrent     SessionTombstoneState = "current"
	SessionTombstoneCorrupt     SessionTombstoneState = "corrupt"
	SessionTombstoneUnsupported SessionTombstoneState = "unsupported"
)

// SessionTombstoneFileState is content-free state for status and projection
// identity checks. Identity hashes the exact bytes parsed by the checked
// loader, avoiding a second-read race during generation preparation.
type SessionTombstoneFileState struct {
	State    SessionTombstoneState
	Version  int
	Identity string
}

// SessionTombstoneLoadError keeps privacy failures machine-classifiable on
// every CLI/MCP serving and derivation boundary. Its Error prefix is also the
// repository's established JSON command-error contract.
type SessionTombstoneLoadError struct {
	Code    string
	State   SessionTombstoneState
	Version int
	Err     error
}

func (e *SessionTombstoneLoadError) Error() string {
	detail := "session tombstone state is invalid"
	if e.Err != nil {
		detail = e.Err.Error()
	}
	return e.Code + ": " + detail
}

func (e *SessionTombstoneLoadError) Unwrap() error { return e.Err }

func EmptySessionTombstones() SessionTombstones {
	return SessionTombstones{Version: SessionTombstonesVersion, Excluded: map[string]SessionTombstone{}}
}

// LoadSessionTombstonesChecked is the only loader allowed on serving,
// derivation, worker, privacy, and administration boundaries. Missing means a
// valid empty policy. Once the file exists, unreadable/malformed state fails
// closed: treating it as empty could disclose or re-derive excluded content.
func LoadSessionTombstonesChecked(brainDir string) (SessionTombstones, SessionTombstoneFileState, error) {
	empty := EmptySessionTombstones()
	data, present, err := ReadPrivacyArtifact(brainDir, SessionTombstonesPath, "session tombstones", clibase.MaxManifestBytes)
	if err != nil {
		state := SessionTombstoneFileState{State: SessionTombstoneCorrupt}
		return SessionTombstones{}, state, &SessionTombstoneLoadError{
			Code: StateCorruptCode, State: state.State,
			Err: fmt.Errorf("read session tombstones: %w", err),
		}
	}
	if !present {
		return empty, SessionTombstoneFileState{State: SessionTombstoneAbsent, Version: SessionTombstonesVersion, Identity: "absent"}, nil
	}
	sum := sha256.Sum256(data)
	state := SessionTombstoneFileState{Identity: "sha256:" + hex.EncodeToString(sum[:])}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		state.State = SessionTombstoneCorrupt
		return SessionTombstones{}, state, &SessionTombstoneLoadError{
			Code: StateCorruptCode, State: state.State,
			Err: errors.New("session tombstones cannot be parsed"),
		}
	}
	state.Version = header.Version
	if header.Version > SessionTombstonesVersion {
		state.State = SessionTombstoneUnsupported
		return SessionTombstones{}, state, &SessionTombstoneLoadError{
			Code: UnsupportedVersionCode, State: state.State, Version: header.Version,
			Err: fmt.Errorf("session tombstone schema %d is unsupported (current %d)", header.Version, SessionTombstonesVersion),
		}
	}
	var stones SessionTombstones
	if _, err := DecodeVersionedJSONBody(data, &stones, false); err != nil {
		message := "session tombstones cannot be parsed strictly"
		if errors.Is(err, ErrTrailingJSONData) {
			message = "session tombstones contain trailing JSON data"
		}
		state.State = SessionTombstoneCorrupt
		return SessionTombstones{}, state, &SessionTombstoneLoadError{
			Code: StateCorruptCode, State: state.State,
			Err: errors.New(message),
		}
	}
	if stones.Version != SessionTombstonesVersion {
		state.State = SessionTombstoneCorrupt
		return SessionTombstones{}, state, &SessionTombstoneLoadError{
			Code: StateCorruptCode, State: state.State, Version: stones.Version,
			Err: fmt.Errorf("session tombstone schema %d is invalid (current %d)", stones.Version, SessionTombstonesVersion),
		}
	}
	if stones.Excluded == nil {
		state.State = SessionTombstoneCorrupt
		return SessionTombstones{}, state, &SessionTombstoneLoadError{
			Code: StateCorruptCode, State: state.State,
			Err: errors.New("session tombstones excluded map is missing"),
		}
	}
	state.State = SessionTombstoneCurrent
	return stones, state, nil
}

// LoadSessionTombstones is a convenience for tests and locked code that has
// already established a valid tombstone file. It panics instead of ever
// converting invalid present state into an empty policy. Production boundaries
// must use LoadSessionTombstonesChecked and return its typed error.
func LoadSessionTombstones(brainDir string) SessionTombstones {
	stones, _, err := LoadSessionTombstonesChecked(brainDir)
	if err != nil {
		panic(err)
	}
	return stones
}

func SaveSessionTombstones(brainDir string, stones SessionTombstones) error {
	if _, _, err := LoadSessionTombstonesChecked(brainDir); err != nil {
		return err
	}
	data, err := MarshalSessionTombstones(stones)
	if err != nil {
		return err
	}
	return WriteBrainRelativeFileAtomic(brainDir, SessionTombstonesPath, data, 0o600)
}

func MarshalSessionTombstones(stones SessionTombstones) ([]byte, error) {
	stones.Version = SessionTombstonesVersion
	data, err := json.MarshalIndent(stones, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// NormalizePrivacyTranscriptPath normalizes a transcript path for exclusion
// matching: trimmed, cleaned, and slash-separated; empty when nothing remains.
func NormalizePrivacyTranscriptPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	clean := filepath.Clean(filepath.FromSlash(trimmed))
	if clean == "." {
		return ""
	}
	return filepath.ToSlash(clean)
}
