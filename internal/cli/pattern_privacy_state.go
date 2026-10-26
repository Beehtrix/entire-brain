package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
	patterncore "github.com/entireio/entire-brain/internal/cli/patterns"
)

// patternCorpusPrivacyArtifactRels derives the privacy inventory from the
// publication layer's canonical sidecar set. Keep the ownership of those
// names in patterns/corpus_publication.go: publication, privacy deletion, and
// verification must agree about every SQLite leaf, including rollback journals.
func patternCorpusPrivacyArtifactRels() []string {
	rels := make([]string, 0, 1+len(patterncore.CorpusSidecarNames))
	rels = append(rels, patterncore.CorpusPath)
	for _, name := range patterncore.CorpusSidecarNames {
		rels = append(rels, filepath.ToSlash(filepath.Join("patterns", name)))
	}
	return rels
}

func isPatternCorpusPrivacyArtifactRel(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, candidate := range patternCorpusPrivacyArtifactRels() {
		if rel == candidate {
			return true
		}
	}
	return false
}

func isPatternCorpusRollbackJournalRel(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, name := range patterncore.CorpusSidecarNames {
		if strings.HasSuffix(name, "-journal") && rel == filepath.ToSlash(filepath.Join("patterns", name)) {
			return true
		}
	}
	return false
}

func isPatternCorpusStagingArtifactRel(rel string) bool {
	rel = filepath.ToSlash(rel)
	dir, name := filepath.ToSlash(filepath.Dir(rel)), filepath.Base(filepath.FromSlash(rel))
	return dir == "patterns" && strings.HasPrefix(name, patterncore.CorpusStagingPrefix)
}

type patternCorpusPublicationReadState string

const (
	patternCorpusPublicationCorrupt     patternCorpusPublicationReadState = "corrupt"
	patternCorpusPublicationUnsafe      patternCorpusPublicationReadState = "unsafe"
	patternCorpusPublicationUnsupported patternCorpusPublicationReadState = "unsupported"
)

// patternCorpusPublicationLoadError keeps publication-marker failures
// machine-classifiable at privacy inventory and deletion boundaries. In
// particular, a newer producer's marker is unsupported read-only state, not a
// disposable corrupt leaf.
type patternCorpusPublicationLoadError struct {
	Code    string
	State   patternCorpusPublicationReadState
	Version int
	Err     error
}

func (e *patternCorpusPublicationLoadError) Error() string {
	detail := "pattern corpus publication marker is invalid"
	if e.Err != nil {
		detail = e.Err.Error()
	}
	if strings.HasPrefix(detail, e.Code+":") {
		return detail
	}
	return e.Code + ": " + detail
}

func (e *patternCorpusPublicationLoadError) Unwrap() error { return e.Err }

func newPatternCorpusPublicationLoadError(err error, version int) error {
	code := memoryErrorCode(err)
	state := patternCorpusPublicationCorrupt
	switch code {
	case brainstore.UnsupportedVersionCode:
		state = patternCorpusPublicationUnsupported
	case brainstore.StateUnsafeCode:
		state = patternCorpusPublicationUnsafe
	default:
		code = brainstore.StateCorruptCode
	}
	return &patternCorpusPublicationLoadError{Code: code, State: state, Version: version, Err: err}
}

// decodePatternCorpusPublication is the single classifier for publication
// markers at both recovery and privacy-cleanup boundaries. It classifies the
// version header before a strict current-schema decode so a future producer's
// additional fields remain opaque, read-only state.
func decodePatternCorpusPublication(data []byte) (patterncore.CorpusPublication, error) {
	var publication patterncore.CorpusPublication
	version, err := brainstore.DecodeStrictVersionedJSON(data, &publication, patterncore.CorpusPublicationVersion, "pattern corpus publication marker")
	if err != nil {
		return patterncore.CorpusPublication{}, newPatternCorpusPublicationLoadError(err, version)
	}
	decodedSHA, decodeErr := hex.DecodeString(publication.SHA256)
	if publication.SchemaVersion != patterncore.CorpusPublicationVersion ||
		!strings.HasPrefix(publication.Candidate, patterncore.CorpusStagingPrefix) ||
		filepath.Base(publication.Candidate) != publication.Candidate ||
		strings.ContainsAny(publication.Candidate, `/\`) ||
		decodeErr != nil || len(decodedSHA) != sha256.Size || publication.SHA256 != strings.ToLower(publication.SHA256) ||
		publication.Size < 1 || publication.Size > brainstore.PrivacySQLiteSnapshotMaxBytes {
		return patterncore.CorpusPublication{}, newPatternCorpusPublicationLoadError(
			fmt.Errorf("%s: invalid pattern corpus publication marker", brainstore.StateCorruptCode), publication.SchemaVersion)
	}
	return publication, nil
}

// loadPatternCorpusPublicationForPrivacy binds the parsed marker bytes to the
// exact regular-file identity observed by inventory. Callers may retain info
// only while holding the Brain privacy/write locks; removal rechecks it.
func loadPatternCorpusPublicationForPrivacy(brainDir string) (patterncore.CorpusPublication, os.FileInfo, bool, error) {
	info, present, err := brainstore.PrivacyArtifactInfo(brainDir, patterncore.CorpusPublicationMarkerPath, "pattern corpus publication marker")
	if err != nil {
		return patterncore.CorpusPublication{}, nil, present, newPatternCorpusPublicationLoadError(err, 0)
	}
	if !present {
		return patterncore.CorpusPublication{}, nil, false, nil
	}
	if info.Size() < 1 || info.Size() > patterncore.CorpusPublicationMarkerMax {
		return patterncore.CorpusPublication{}, nil, true, newPatternCorpusPublicationLoadError(
			fmt.Errorf("%s: pattern corpus publication marker is outside its safe size bounds", brainstore.StateUnsafeCode), 0)
	}
	data, stillPresent, err := readMemoryStateFileExpected(
		brainDir, patterncore.CorpusPublicationMarkerPath, "pattern corpus publication marker", patterncore.CorpusPublicationMarkerMax, info)
	if err != nil {
		return patterncore.CorpusPublication{}, nil, true, newPatternCorpusPublicationLoadError(err, 0)
	}
	if !stillPresent {
		return patterncore.CorpusPublication{}, nil, true, newPatternCorpusPublicationLoadError(
			fmt.Errorf("%s: pattern corpus publication marker disappeared during inventory", brainstore.StateUnsafeCode), 0)
	}
	publication, err := decodePatternCorpusPublication(data)
	if err != nil {
		return patterncore.CorpusPublication{}, nil, true, err
	}
	return publication, info, true, nil
}

// removePatternCorpusPublicationForPrivacy removes only a supported current
// marker and only the exact identity whose bytes were classified. Unknown,
// corrupt, raced, or unsafe state remains byte-for-byte untouched.
func removePatternCorpusPublicationForPrivacy(brainDir string) error {
	_, info, present, err := loadPatternCorpusPublicationForPrivacy(brainDir)
	if err != nil || !present {
		return err
	}
	if err := removeBrainRelativeFileExpected(brainDir, patterncore.CorpusPublicationMarkerPath, info); err != nil {
		return newPatternCorpusPublicationLoadError(err, patterncore.CorpusPublicationVersion)
	}
	return nil
}
