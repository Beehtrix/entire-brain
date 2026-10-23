package cli

// Legacy artifact helpers are compiled only for migration and compatibility
// fixtures. Production Brain never builds or copies a code graph.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// semanticRecordedArtifactsReadable re-reads the two files the recorded
// semantic source points at -- the provider snapshot and the derived SQLite
// store -- and reports the first that will not load. It answers "is what the
// manifest claims actually still there and openable", which is the half of
// freshness that a commit comparison cannot see.
func semanticRecordedArtifactsReadable(brainDir string, source *semanticSourceManifest) error {
	if source.SnapshotPath == "" {
		return errors.New("semantic snapshot_path is missing")
	}
	snapshot, err := validateSemanticSnapshotPath(source.SnapshotPath)
	if err != nil {
		return err
	}
	if err := rejectSymlinkPathComponents(brainDir, snapshot); err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Join(brainDir, snapshot))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("semantic snapshot must be a regular file: %s", source.SnapshotPath)
	}
	manifest, err := loadBrainManifest(brainDir)
	if err != nil {
		return err
	}
	header, counts, err := readSemanticSnapshotSummary(filepath.Join(brainDir, snapshot), manifest.RepoKey)
	if err != nil {
		return err
	}
	if err := validateSemanticSourceMatchesSnapshot(source, header, counts); err != nil {
		return err
	}
	if source.StorePath == "" {
		return nil
	}
	generation, err := validateSemanticGenerationPath(source.GenerationPath)
	if err != nil {
		return err
	}
	store, err := validateSemanticGenerationFilePath(source.StorePath, generation, semanticSQLiteName)
	if err != nil {
		return err
	}
	if err := rejectSymlinkPathComponents(brainDir, store); err != nil {
		return err
	}
	return validateSemanticSQLiteStore(filepath.Join(brainDir, store), source.Symbols, source.Relations)
}

// semanticProviderRefusalContext renders the provider's own explanation for an
// identity-less snapshot, for appending to an otherwise bare refusal.
//
// A snapshot with no commit is almost never the provider malfunctioning: it is
// the provider declining to run git and SAYING SO. The canonical case is a
// promisor remote -- Entire's own `filtered_fetches: true` writes one -- which
// makes entire-graph refuse git subprocesses to keep its no-egress guarantee
// and fall back to a filesystem walk. It reports that as E_NO_GIT_HEAD, naming
// the cause and the remedy.
//
// mergeSemanticSummary has already folded the trailing summary record into the
// header by the time this runs, so those warnings are in hand. Reporting only
// "header missing commit" throws away the one sentence that tells the reader
// what to do, and turns a self-describing refusal into a mystery. That cost
// four wrong hypotheses and an instrumented bisect to rediscover something the
// provider had already printed.
func semanticProviderRefusalContext(header semanticHeader, repoDir string) string {
	best := ""
	for _, w := range append(append([]semanticWarning(nil), header.Warnings...), header.PartialFailures...) {
		detail := strings.TrimSpace(w.Detail)
		if detail == "" {
			detail = strings.TrimSpace(w.Effect)
		}
		if detail == "" {
			continue
		}
		// This runs BEFORE sanitizeSemanticWarnings does -- validation is the
		// gate the snapshot has not passed yet -- so the text here is exactly
		// what the provider wrote, host paths and all. The error is printed,
		// and the component detail built from it is recorded and can travel
		// in a bundle, so it is redacted here rather than trusted to a later
		// pass that this path never reaches.
		detail = redactSemanticPathsInText(detail, repoDir)
		line := w.Code + ": " + detail
		// E_NO_GIT_HEAD is the one that names why identity is absent; prefer it
		// over the worktree-fallback warning that merely reports the symptom.
		if w.Code == "E_NO_GIT_HEAD" {
			return "; the provider reported " + line
		}
		if best == "" {
			best = line
		}
	}
	if best == "" {
		return ""
	}
	return "; the provider reported " + best
}

func semanticAvailableGenerationID(root, base, suffix string) (string, error) {
	for i := 0; ; i++ {
		candidate := base
		if i == 1 {
			candidate = base + "-" + suffix
		} else if i > 1 {
			candidate = fmt.Sprintf("%s-%s-%d", base, suffix, i-1)
		}
		_, err := os.Lstat(filepath.Join(root, candidate))
		if os.IsNotExist(err) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect semantic generation candidate: %w", err)
		}
	}
}

func semanticGenerationContentSuffix(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:8])
}
