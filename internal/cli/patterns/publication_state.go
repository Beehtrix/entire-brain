package patterns

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
)

// CorpusPrivacyArtifactRels derives the privacy inventory from the
// publication layer's canonical sidecar set. Keep the ownership of those
// names in patterns/corpus_publication.go: publication, privacy deletion, and
// verification must agree about every SQLite leaf, including rollback journals.
func CorpusPrivacyArtifactRels() []string {
	rels := make([]string, 0, 1+len(CorpusSidecarNames))
	rels = append(rels, CorpusPath)
	for _, name := range CorpusSidecarNames {
		rels = append(rels, filepath.ToSlash(filepath.Join("patterns", name)))
	}
	return rels
}

func IsCorpusPrivacyArtifactRel(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, candidate := range CorpusPrivacyArtifactRels() {
		if rel == candidate {
			return true
		}
	}
	return false
}

func IsCorpusRollbackJournalRel(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, name := range CorpusSidecarNames {
		if strings.HasSuffix(name, "-journal") && rel == filepath.ToSlash(filepath.Join("patterns", name)) {
			return true
		}
	}
	return false
}

func IsCorpusStagingArtifactRel(rel string) bool {
	rel = filepath.ToSlash(rel)
	dir, name := filepath.ToSlash(filepath.Dir(rel)), filepath.Base(filepath.FromSlash(rel))
	return dir == "patterns" && strings.HasPrefix(name, CorpusStagingPrefix)
}

type PublicationReadState string

const (
	PublicationCorrupt     PublicationReadState = "corrupt"
	PublicationUnsafe      PublicationReadState = "unsafe"
	PublicationUnsupported PublicationReadState = "unsupported"
)

// PublicationLoadError keeps publication-marker failures
// machine-classifiable at privacy inventory and deletion boundaries. In
// particular, a newer producer's marker is unsupported read-only state, not a
// disposable corrupt leaf.
type PublicationLoadError struct {
	Code    string
	State   PublicationReadState
	Version int
	Err     error
}

func (e *PublicationLoadError) Error() string {
	detail := "pattern corpus publication marker is invalid"
	if e.Err != nil {
		detail = e.Err.Error()
	}
	if strings.HasPrefix(detail, e.Code+":") {
		return detail
	}
	return e.Code + ": " + detail
}

func (e *PublicationLoadError) Unwrap() error { return e.Err }

func newPublicationLoadError(err error, version int) error {
	code := brainstore.MemoryErrorCode(err)
	state := PublicationCorrupt
	switch code {
	case brainstore.UnsupportedVersionCode:
		state = PublicationUnsupported
	case brainstore.StateUnsafeCode:
		state = PublicationUnsafe
	default:
		code = brainstore.StateCorruptCode
	}
	return &PublicationLoadError{Code: code, State: state, Version: version, Err: err}
}

// DecodeCorpusPublication is the single classifier for publication
// markers at both recovery and privacy-cleanup boundaries. It classifies the
// version header before a strict current-schema decode so a future producer's
// additional fields remain opaque, read-only state.
func DecodeCorpusPublication(data []byte) (CorpusPublication, error) {
	var publication CorpusPublication
	version, err := brainstore.DecodeStrictVersionedJSON(data, &publication, CorpusPublicationVersion, "pattern corpus publication marker")
	if err != nil {
		return CorpusPublication{}, newPublicationLoadError(err, version)
	}
	decodedSHA, decodeErr := hex.DecodeString(publication.SHA256)
	if publication.SchemaVersion != CorpusPublicationVersion ||
		!strings.HasPrefix(publication.Candidate, CorpusStagingPrefix) ||
		filepath.Base(publication.Candidate) != publication.Candidate ||
		strings.ContainsAny(publication.Candidate, `/\`) ||
		decodeErr != nil || len(decodedSHA) != sha256.Size || publication.SHA256 != strings.ToLower(publication.SHA256) ||
		publication.Size < 1 || publication.Size > brainstore.PrivacySQLiteSnapshotMaxBytes {
		return CorpusPublication{}, newPublicationLoadError(
			fmt.Errorf("%s: invalid pattern corpus publication marker", brainstore.StateCorruptCode), publication.SchemaVersion)
	}
	return publication, nil
}

// LoadCorpusPublicationForPrivacy binds the parsed marker bytes to the
// exact regular-file identity observed by inventory. Callers may retain info
// only while holding the Brain privacy/write locks; removal rechecks it.
func LoadCorpusPublicationForPrivacy(brainDir string) (CorpusPublication, os.FileInfo, bool, error) {
	info, present, err := brainstore.PrivacyArtifactInfo(brainDir, CorpusPublicationMarkerPath, "pattern corpus publication marker")
	if err != nil {
		return CorpusPublication{}, nil, present, newPublicationLoadError(err, 0)
	}
	if !present {
		return CorpusPublication{}, nil, false, nil
	}
	if info.Size() < 1 || info.Size() > CorpusPublicationMarkerMax {
		return CorpusPublication{}, nil, true, newPublicationLoadError(
			fmt.Errorf("%s: pattern corpus publication marker is outside its safe size bounds", brainstore.StateUnsafeCode), 0)
	}
	data, stillPresent, err := brainstore.ReadMemoryStateFileExpected(
		brainDir, CorpusPublicationMarkerPath, "pattern corpus publication marker", CorpusPublicationMarkerMax, info)
	if err != nil {
		return CorpusPublication{}, nil, true, newPublicationLoadError(err, 0)
	}
	if !stillPresent {
		return CorpusPublication{}, nil, true, newPublicationLoadError(
			fmt.Errorf("%s: pattern corpus publication marker disappeared during inventory", brainstore.StateUnsafeCode), 0)
	}
	publication, err := DecodeCorpusPublication(data)
	if err != nil {
		return CorpusPublication{}, nil, true, err
	}
	return publication, info, true, nil
}

// RemoveCorpusPublicationForPrivacy removes only a supported current
// marker and only the exact identity whose bytes were classified. Unknown,
// corrupt, raced, or unsafe state remains byte-for-byte untouched.
func RemoveCorpusPublicationForPrivacy(brainDir string) error {
	_, info, present, err := LoadCorpusPublicationForPrivacy(brainDir)
	if err != nil || !present {
		return err
	}
	if err := brainstore.RemoveBrainRelativeFileExpected(brainDir, CorpusPublicationMarkerPath, info); err != nil {
		return newPublicationLoadError(err, CorpusPublicationVersion)
	}
	return nil
}
