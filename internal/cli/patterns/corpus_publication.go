package patterns

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
)

const CorpusStagingPrefix = ".corpus.sqlite.build-"

const (
	CorpusPublicationMarkerName = ".corpus.sqlite.publish.json"
	CorpusPublicationMarkerPath = "patterns/" + CorpusPublicationMarkerName
	CorpusPublicationMarkerMax  = 4 << 10
	CorpusPublicationVersion    = 1
)

// CorpusPublishBoundary is a deterministic crash-boundary seam. Once
// the durable publication marker exists, an injected error deliberately leaves
// the marker (and, when still present, the candidate) in place. Readers then
// fail closed until the next locked writer completes recovery.
var CorpusPublishBoundary = func(string) error { return nil }

type CorpusPublication struct {
	SchemaVersion int    `json:"schema_version"`
	Candidate     string `json:"candidate"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
}

// PrepareCorpusStaging creates a process-private build leaf outside the
// Brain tree. When a corpus already exists it clones a checked, descriptor-bound
// SQLite snapshot so expensive verifier caches survive the deterministic
// rebuild. SQLite never receives a path beneath a replaceable Brain directory
// while analysis is in progress.
func PrepareCorpusStaging(brainDir string) (string, func(), error) {
	tempDir, err := os.MkdirTemp("", "entire-brain-pattern-corpus-")
	if err != nil {
		return "", nil, fmt.Errorf("create private pattern corpus staging directory: %w", err)
	}
	stagePath := filepath.Join(tempDir, "corpus.sqlite")
	stage, err := os.OpenFile(stagePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	cleanup := func() {
		if stage != nil {
			_ = stage.Close()
		}
		_ = os.RemoveAll(tempDir)
	}
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("create staged pattern corpus: %w", err)
	}
	_, present, err := brainstore.PrivacyArtifactInfo(brainDir, CorpusPath, "pattern corpus")
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if present {
		snapshot, snapshotCleanup, err := brainstore.SnapshotPrivacySQLiteStore(brainDir, CorpusPath)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		defer snapshotCleanup()
		source, err := os.Open(snapshot)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		copied, copyErr := io.Copy(stage, io.LimitReader(source, brainstore.PrivacySQLiteSnapshotMaxBytes+1))
		closeErr := source.Close()
		if copyErr != nil || closeErr != nil || copied > brainstore.PrivacySQLiteSnapshotMaxBytes {
			cleanup()
			return "", nil, fmt.Errorf("copy checked pattern corpus into staging")
		}
	}
	if err := stage.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return stagePath, cleanup, nil
}

var CorpusSidecarNames = []string{"corpus.sqlite-wal", "corpus.sqlite-shm", "corpus.sqlite-journal"}

type pinnedPatternCorpusDirectory struct {
	brainDir     string
	brainInfo    os.FileInfo
	patternsInfo os.FileInfo
	root         *os.Root
	patternsRoot *os.Root
}

func (p *pinnedPatternCorpusDirectory) Close() {
	if p == nil {
		return
	}
	if p.patternsRoot != nil {
		_ = p.patternsRoot.Close()
	}
	if p.root != nil {
		_ = p.root.Close()
	}
}

func openPinnedPatternCorpusDirectory(brainDir string, create bool) (*pinnedPatternCorpusDirectory, bool, error) {
	brainInfo, err := os.Lstat(brainDir)
	if err != nil {
		return nil, false, fmt.Errorf("%s: inspect Brain root for corpus publication: %w", brainstore.StateUnsafeCode, err)
	}
	if brainInfo.Mode()&os.ModeSymlink != 0 || !brainInfo.IsDir() {
		return nil, false, fmt.Errorf("%s: Brain root is not a stable directory", brainstore.StateUnsafeCode)
	}
	root, err := os.OpenRoot(brainDir)
	if err != nil {
		return nil, false, fmt.Errorf("%s: open Brain root for corpus publication: %w", brainstore.StateUnsafeCode, err)
	}
	openedBrain, err := root.Stat(".")
	if err != nil || !openedBrain.IsDir() || !os.SameFile(brainInfo, openedBrain) {
		_ = root.Close()
		return nil, false, fmt.Errorf("%s: Brain root changed while it was being pinned", brainstore.StateUnsafeCode)
	}
	if create {
		if err := root.MkdirAll("patterns", 0o700); err != nil {
			_ = root.Close()
			return nil, false, fmt.Errorf("%s: create pinned pattern corpus directory: %w", brainstore.StateUnsafeCode, err)
		}
	}
	patternsInfo, err := root.Lstat("patterns")
	if os.IsNotExist(err) && !create {
		_ = root.Close()
		return nil, false, nil
	}
	if err != nil {
		_ = root.Close()
		return nil, false, fmt.Errorf("%s: inspect pattern corpus directory: %w", brainstore.StateUnsafeCode, err)
	}
	if patternsInfo.Mode()&os.ModeSymlink != 0 || !patternsInfo.IsDir() {
		_ = root.Close()
		return nil, false, fmt.Errorf("%s: pattern corpus directory is not a regular directory", brainstore.StateUnsafeCode)
	}
	patternsRoot, err := root.OpenRoot("patterns")
	if err != nil {
		_ = root.Close()
		return nil, false, fmt.Errorf("%s: pin pattern corpus directory: %w", brainstore.StateUnsafeCode, err)
	}
	pinned := &pinnedPatternCorpusDirectory{
		brainDir: brainDir, brainInfo: brainInfo, patternsInfo: patternsInfo,
		root: root, patternsRoot: patternsRoot,
	}
	if err := pinned.validate(); err != nil {
		pinned.Close()
		return nil, false, err
	}
	return pinned, true, nil
}

func (p *pinnedPatternCorpusDirectory) validate() error {
	currentBrain, err := os.Lstat(p.brainDir)
	if err != nil || currentBrain.Mode()&os.ModeSymlink != 0 || !os.SameFile(p.brainInfo, currentBrain) {
		return fmt.Errorf("%s: Brain root changed during corpus publication", brainstore.StateUnsafeCode)
	}
	pinnedBrain, err := p.root.Stat(".")
	if err != nil || !pinnedBrain.IsDir() || !os.SameFile(p.brainInfo, pinnedBrain) {
		return fmt.Errorf("%s: pinned Brain root changed during corpus publication", brainstore.StateUnsafeCode)
	}
	currentPatterns, err := p.root.Lstat("patterns")
	if err != nil || currentPatterns.Mode()&os.ModeSymlink != 0 || !os.SameFile(p.patternsInfo, currentPatterns) {
		return fmt.Errorf("%s: pattern corpus directory changed during publication", brainstore.StateUnsafeCode)
	}
	pinned, err := p.patternsRoot.Stat(".")
	if err != nil || !pinned.IsDir() || !os.SameFile(p.patternsInfo, pinned) {
		return fmt.Errorf("%s: pinned pattern corpus directory changed during publication", brainstore.StateUnsafeCode)
	}
	return nil
}

func inspectPinnedPatternCorpusLeaf(root *os.Root, name, label string) (os.FileInfo, bool, error) {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%s: inspect %s: %w", brainstore.StateUnsafeCode, label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s: %s is not a regular file", brainstore.StateUnsafeCode, label)
	}
	return info, true, nil
}

func removePinnedPatternCorpusSidecars(pinned *pinnedPatternCorpusDirectory, useHooks bool) error {
	for _, sidecar := range CorpusSidecarNames {
		if useHooks {
			if err := CorpusPublishBoundary("before_remove_" + sidecar); err != nil {
				return err
			}
		}
		_, present, err := inspectPinnedPatternCorpusLeaf(pinned.patternsRoot, sidecar, "pattern corpus sidecar "+sidecar)
		if err != nil {
			return err
		}
		if present {
			if err := pinned.patternsRoot.Remove(sidecar); err != nil {
				return fmt.Errorf("remove stale pattern corpus sidecar %s: %w", sidecar, err)
			}
		}
		if useHooks {
			if err := CorpusPublishBoundary("after_remove_" + sidecar); err != nil {
				return err
			}
		}
	}
	return nil
}

func readPinnedPatternCorpusPublication(pinned *pinnedPatternCorpusDirectory, decode func([]byte) (CorpusPublication, error)) (CorpusPublication, os.FileInfo, error) {
	var publication CorpusPublication
	info, present, err := inspectPinnedPatternCorpusLeaf(pinned.patternsRoot, CorpusPublicationMarkerName, "pattern corpus publication marker")
	if err != nil {
		return publication, nil, err
	}
	if !present || info.Size() < 1 || info.Size() > CorpusPublicationMarkerMax {
		return publication, nil, fmt.Errorf("%s: pattern corpus publication marker is missing or malformed", brainstore.StateUnsafeCode)
	}
	marker, err := pinned.patternsRoot.OpenFile(CorpusPublicationMarkerName, os.O_RDONLY|base.FileLockOpenFlags()|base.MemoryStateReadOpenFlags(), 0)
	if err != nil {
		return publication, nil, fmt.Errorf("%s: open pattern corpus publication marker: %w", brainstore.StateUnsafeCode, err)
	}
	defer marker.Close()
	opened, err := marker.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Size() != info.Size() {
		return publication, nil, fmt.Errorf("%s: pattern corpus publication marker changed while opening", brainstore.StateUnsafeCode)
	}
	if err := base.RejectOpenFileHardlink(CorpusPublicationMarkerName, marker, opened, "pattern corpus publication marker"); err != nil {
		return publication, nil, fmt.Errorf("%s: %w", brainstore.StateUnsafeCode, err)
	}
	data, err := io.ReadAll(io.LimitReader(marker, CorpusPublicationMarkerMax+1))
	if err != nil || int64(len(data)) > CorpusPublicationMarkerMax {
		return publication, nil, fmt.Errorf("%s: read bounded pattern corpus publication marker", brainstore.StateUnsafeCode)
	}
	current, present, err := inspectPinnedPatternCorpusLeaf(pinned.patternsRoot, CorpusPublicationMarkerName, "pattern corpus publication marker")
	if err != nil || !present || !os.SameFile(info, current) || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
		return publication, nil, fmt.Errorf("%s: pattern corpus publication marker changed while reading", brainstore.StateUnsafeCode)
	}
	publication, err = decode(data)
	if err != nil {
		return CorpusPublication{}, nil, err
	}
	return publication, info, nil
}

// validatePinnedPatternCorpusDatabase copies a descriptor-pinned leaf to a
// private directory before asking SQLite to inspect it. Recovery never lets
// SQLite open a Brain-controlled pathname or create a sidecar there.
func validatePinnedPatternCorpusDatabase(root *os.Root, name string, publication CorpusPublication) (os.FileInfo, error) {
	info, present, err := inspectPinnedPatternCorpusLeaf(root, name, "pattern corpus recovery candidate")
	if err != nil {
		return nil, err
	}
	if !present || info.Size() != publication.Size {
		return nil, fmt.Errorf("%s: pattern corpus recovery candidate is missing or changed", brainstore.StateUnsafeCode)
	}
	source, err := root.OpenFile(name, os.O_RDONLY|base.FileLockOpenFlags()|base.MemoryStateReadOpenFlags(), 0)
	if err != nil {
		return nil, fmt.Errorf("%s: open pattern corpus recovery candidate: %w", brainstore.StateUnsafeCode, err)
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Size() != info.Size() {
		return nil, fmt.Errorf("%s: pattern corpus recovery candidate changed while opening", brainstore.StateUnsafeCode)
	}
	if err := base.RejectOpenFileHardlink(name, source, opened, "pattern corpus recovery candidate"); err != nil {
		return nil, fmt.Errorf("%s: %w", brainstore.StateUnsafeCode, err)
	}
	tempDir, err := os.MkdirTemp("", "entire-brain-pattern-recovery-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)
	snapshotPath := filepath.Join(tempDir, "corpus.sqlite")
	snapshot, err := os.OpenFile(snapshotPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	copied, copyErr := io.Copy(io.MultiWriter(snapshot, hash), io.LimitReader(source, brainstore.PrivacySQLiteSnapshotMaxBytes+1))
	syncErr := snapshot.Sync()
	closeErr := snapshot.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || copied != publication.Size || copied > brainstore.PrivacySQLiteSnapshotMaxBytes || hex.EncodeToString(hash.Sum(nil)) != publication.SHA256 {
		return nil, fmt.Errorf("%s: pattern corpus recovery candidate failed its durable digest", brainstore.StateUnsafeCode)
	}
	after, statErr := source.Stat()
	current, currentPresent, currentErr := inspectPinnedPatternCorpusLeaf(root, name, "pattern corpus recovery candidate")
	if statErr != nil || currentErr != nil || !currentPresent || !os.SameFile(info, after) || !os.SameFile(info, current) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, fmt.Errorf("%s: pattern corpus recovery candidate changed while validating", brainstore.StateUnsafeCode)
	}
	if err := brainstore.ValidatePrivacySQLiteSnapshot(snapshotPath, name); err != nil {
		return nil, err
	}
	return info, nil
}

// RecoverCorpusPublicationLocked completes an interrupted marked
// publication. The marker names and hashes one normalized candidate. If that
// candidate has already been renamed, the same digest must be present at the
// live name. Any ambiguous state remains marked and therefore unreadable.
// The caller must hold the Brain write lock and provide its strict marker decoder.
// Decoder errors are returned unchanged, including unsupported-version errors.
func RecoverCorpusPublicationLocked(brainDir string, decode func([]byte) (CorpusPublication, error)) error {
	_, present, err := brainstore.PrivacyArtifactInfo(brainDir, CorpusPublicationMarkerPath, "pattern corpus publication marker")
	if err != nil || !present {
		return err
	}
	pinned, present, err := openPinnedPatternCorpusDirectory(brainDir, false)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("%s: pattern corpus publication directory disappeared during recovery", brainstore.StateUnsafeCode)
	}
	defer pinned.Close()
	publication, markerInfo, err := readPinnedPatternCorpusPublication(pinned, decode)
	if err != nil {
		return err
	}
	if err := pinned.validate(); err != nil {
		return err
	}
	candidateInfo, candidatePresent, err := inspectPinnedPatternCorpusLeaf(pinned.patternsRoot, publication.Candidate, "pattern corpus recovery candidate")
	if err != nil {
		return err
	}
	if candidatePresent {
		candidateInfo, err = validatePinnedPatternCorpusDatabase(pinned.patternsRoot, publication.Candidate, publication)
	} else {
		candidateInfo, err = validatePinnedPatternCorpusDatabase(pinned.patternsRoot, "corpus.sqlite", publication)
	}
	if err != nil {
		return err
	}
	if err := removePinnedPatternCorpusSidecars(pinned, false); err != nil {
		return err
	}
	if candidatePresent {
		current, stillPresent, err := inspectPinnedPatternCorpusLeaf(pinned.patternsRoot, publication.Candidate, "pattern corpus recovery candidate")
		if err != nil || !stillPresent || !os.SameFile(candidateInfo, current) {
			return fmt.Errorf("%s: pattern corpus recovery candidate changed before rename", brainstore.StateUnsafeCode)
		}
		if err := pinned.patternsRoot.Rename(publication.Candidate, "corpus.sqlite"); err != nil {
			return fmt.Errorf("recover pinned pattern corpus: %w", err)
		}
	}
	if err := brainstore.SyncPinnedDirectory(pinned.patternsRoot); err != nil {
		return fmt.Errorf("sync recovered pattern corpus directory: %w", err)
	}
	if _, err := validatePinnedPatternCorpusDatabase(pinned.patternsRoot, "corpus.sqlite", publication); err != nil {
		return err
	}
	currentMarker, markerPresent, err := inspectPinnedPatternCorpusLeaf(pinned.patternsRoot, CorpusPublicationMarkerName, "pattern corpus publication marker")
	if err != nil || !markerPresent || !os.SameFile(markerInfo, currentMarker) {
		return fmt.Errorf("%s: pattern corpus publication marker changed before recovery commit", brainstore.StateUnsafeCode)
	}
	if err := pinned.patternsRoot.Remove(CorpusPublicationMarkerName); err != nil {
		return fmt.Errorf("remove recovered pattern corpus marker: %w", err)
	}
	if err := brainstore.SyncPinnedDirectory(pinned.patternsRoot); err != nil {
		return fmt.Errorf("sync recovered pattern corpus marker removal: %w", err)
	}
	return pinned.validate()
}

// PublishCorpusLocked installs a complete, normalized staging database
// through descriptor-rooted creation and rename. Replacing a SQLite main file
// and removing its possible old sidecars is not one portable atomic operation,
// so a durable marker brackets that window. A crash before the marker leaves
// the old logical database untouched; a crash after it makes every reader fail
// closed until the locked recovery above completes either the named candidate
// or the already-renamed database.
// The caller must hold the Brain write lock and supply the same strict marker
// decoder used by privacy inventory and recovery.
func PublishCorpusLocked(brainDir, stagingPath string, decode func([]byte) (CorpusPublication, error)) error {
	if err := RecoverCorpusPublicationLocked(brainDir, decode); err != nil {
		return err
	}
	staging, err := os.Open(stagingPath)
	if err != nil {
		return err
	}
	defer staging.Close()
	stagingInfo, err := staging.Stat()
	if err != nil || !stagingInfo.Mode().IsRegular() || stagingInfo.Size() < 1 || stagingInfo.Size() > brainstore.PrivacySQLiteSnapshotMaxBytes {
		return fmt.Errorf("%s: invalid staged pattern corpus", brainstore.StateUnsafeCode)
	}
	pinned, _, err := openPinnedPatternCorpusDirectory(brainDir, true)
	if err != nil {
		return err
	}
	defer pinned.Close()
	if _, markerPresent, err := inspectPinnedPatternCorpusLeaf(pinned.patternsRoot, CorpusPublicationMarkerName, "pattern corpus publication marker"); err != nil || markerPresent {
		if err != nil {
			return err
		}
		return fmt.Errorf("%s: pattern corpus publication marker unexpectedly exists", brainstore.StateUnsafeCode)
	}
	if _, currentPresent, err := inspectPinnedPatternCorpusLeaf(pinned.patternsRoot, "corpus.sqlite", "live pattern corpus"); err != nil {
		return err
	} else if !currentPresent {
		// A first publication has no old logical database; the marker still
		// brackets its transition from absent to complete.
	}
	for _, sidecar := range CorpusSidecarNames {
		if _, _, err := inspectPinnedPatternCorpusLeaf(pinned.patternsRoot, sidecar, "pattern corpus sidecar "+sidecar); err != nil {
			return err
		}
	}

	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tempName := CorpusStagingPrefix + hex.EncodeToString(random[:])
	temp, err := pinned.patternsRoot.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL|base.FileLockOpenFlags(), 0o600)
	if err != nil {
		return fmt.Errorf("%s: create pinned corpus publication leaf: %w", brainstore.StateUnsafeCode, err)
	}
	removeTemp := true
	defer func() {
		_ = temp.Close()
		if removeTemp {
			_ = pinned.patternsRoot.Remove(tempName)
		}
	}()
	hash := sha256.New()
	copied, copyErr := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(staging, brainstore.PrivacySQLiteSnapshotMaxBytes+1))
	if copyErr != nil || copied != stagingInfo.Size() || copied > brainstore.PrivacySQLiteSnapshotMaxBytes {
		return fmt.Errorf("%s: staged pattern corpus changed or exceeded bounds during publication", brainstore.StateUnsafeCode)
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := brainstore.SyncPinnedDirectory(pinned.patternsRoot); err != nil {
		return fmt.Errorf("sync pattern corpus candidate directory entry: %w", err)
	}
	publication := CorpusPublication{
		SchemaVersion: CorpusPublicationVersion,
		Candidate:     tempName,
		SHA256:        hex.EncodeToString(hash.Sum(nil)),
		Size:          copied,
	}
	markerData, err := json.Marshal(publication)
	if err != nil {
		return err
	}
	markerData = append(markerData, '\n')
	if err := CorpusPublishBoundary("before_marker_create"); err != nil {
		return err
	}
	marker, err := pinned.patternsRoot.OpenFile(CorpusPublicationMarkerName, os.O_WRONLY|os.O_CREATE|os.O_EXCL|base.FileLockOpenFlags(), 0o600)
	if err != nil {
		return fmt.Errorf("%s: create pattern corpus publication marker: %w", brainstore.StateUnsafeCode, err)
	}
	written, writeErr := marker.Write(markerData)
	syncErr := marker.Sync()
	closeErr := marker.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || written != len(markerData) {
		_ = pinned.patternsRoot.Remove(CorpusPublicationMarkerName)
		return fmt.Errorf("write durable pattern corpus publication marker")
	}
	// Retain the candidate as soon as a complete marker file can become
	// visible. Even if the following directory sync reports an error, recovery
	// must have both artifacts available whenever that marker survives.
	removeTemp = false
	if err := brainstore.SyncPinnedDirectory(pinned.patternsRoot); err != nil {
		return fmt.Errorf("sync pattern corpus publication marker: %w", err)
	}
	// From this point on the marker is the durable fail-closed bit.
	if err := CorpusPublishBoundary("after_marker_create"); err != nil {
		return err
	}
	if err := pinned.validate(); err != nil {
		return err
	}
	if err := removePinnedPatternCorpusSidecars(pinned, true); err != nil {
		return err
	}
	if err := CorpusPublishBoundary("before_main_rename"); err != nil {
		return err
	}
	if err := pinned.patternsRoot.Rename(tempName, "corpus.sqlite"); err != nil {
		return fmt.Errorf("publish pinned pattern corpus: %w", err)
	}
	if err := CorpusPublishBoundary("after_main_rename"); err != nil {
		return err
	}
	if err := brainstore.SyncPinnedDirectory(pinned.patternsRoot); err != nil {
		return fmt.Errorf("sync published pattern corpus: %w", err)
	}
	if err := CorpusPublishBoundary("after_main_sync"); err != nil {
		return err
	}
	if err := pinned.validate(); err != nil {
		return err
	}
	publicationMarker, markerInfo, err := readPinnedPatternCorpusPublication(pinned, decode)
	if err != nil || publicationMarker != publication {
		return fmt.Errorf("%s: pattern corpus publication marker changed before commit", brainstore.StateUnsafeCode)
	}
	if err := CorpusPublishBoundary("before_marker_remove"); err != nil {
		return err
	}
	currentMarker, markerPresent, err := inspectPinnedPatternCorpusLeaf(pinned.patternsRoot, CorpusPublicationMarkerName, "pattern corpus publication marker")
	if err != nil || !markerPresent || !os.SameFile(markerInfo, currentMarker) {
		return fmt.Errorf("%s: pattern corpus publication marker changed before removal", brainstore.StateUnsafeCode)
	}
	if err := pinned.patternsRoot.Remove(CorpusPublicationMarkerName); err != nil {
		return fmt.Errorf("commit pattern corpus publication: %w", err)
	}
	if err := CorpusPublishBoundary("after_marker_remove"); err != nil {
		return err
	}
	if err := brainstore.SyncPinnedDirectory(pinned.patternsRoot); err != nil {
		return fmt.Errorf("sync pattern corpus publication commit: %w", err)
	}
	if err := CorpusPublishBoundary("after_marker_remove_sync"); err != nil {
		return err
	}
	return pinned.validate()
}
