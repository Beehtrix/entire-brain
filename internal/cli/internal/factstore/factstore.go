package factstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/entireio/entire-brain/factmerge"
	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
	bmanifest "github.com/entireio/entire-brain/internal/cli/internal/manifest"
)

const (
	DirName          = "facts"
	TaxonomyFileName = "taxonomy.json"
	TaxonomyPath     = DirName + "/" + TaxonomyFileName
	FileName         = "facts.ndjson"
	MaxPerChunk      = 6 // quality-gate output cap per source chunk

	OriginDistilled = "distilled"
	OriginAuthored  = "authored"
	OriginImported  = "imported"
)

// factmerge.Record (factmerge.Record) and factmerge.Anchor (factmerge.Anchor) are defined
// as type aliases in facts_aliases.go; the durable-fact data model now lives in
// factmerge.

// Taxonomy is the active taxonomy snapshot. Paths are validated against
// factPathPattern; classification may only invent a new three-level path under
// an existing top-level category.
type Taxonomy struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Categories  map[string]string `json:"categories"` // top-level -> description
	Paths       []PathDef         `json:"paths"`
}

type PathDef struct {
	Path        string   `json:"path"`
	Description string   `json:"description"`
	Examples    []string `json:"examples,omitempty"`
}

// normalizeFactText, factRecordID, validFactPath, and normalizeFactPaths moved
// to factmerge (NormalizeText/RecordID/ValidPath/NormalizePaths); the
// CLI names are forwarding wrappers in facts_aliases.go.

// TopLevel returns the top-level category of a taxonomy path (the segment
// before the first dot), or "" if the path is empty.
func TopLevel(path string) string {
	if i := strings.IndexByte(path, '.'); i >= 0 {
		return path[:i]
	}
	return path
}

// --- on-disk layout -------------------------------------------------------

// BranchRelDir returns the brain-relative directory holding a branch's
// facts. The readable slug alone is not collision-free — distinct branches can
// sanitize to the same slug (e.g. "evis/search" and "evis-search", or "Main"
// and "main") — and two branches sharing a facts file would clobber each other
// and break branch isolation. A short hash of the full branch name is appended
// so each branch maps to a stable, unique directory while staying greppable.
func BranchRelDir(branch string) string {
	slug := base.SafePathComponent(branch, "branch", 80)
	sum := sha256.Sum256([]byte(branch))
	return filepath.ToSlash(filepath.Join(DirName, slug+"-"+hex.EncodeToString(sum[:4])))
}

func FileRelPath(branch string) string {
	return filepath.ToSlash(filepath.Join(BranchRelDir(branch), FileName))
}

// BranchStoreExists reports whether the brain actually holds a facts.ndjson
// for a branch.
//
// Load deliberately returns an empty slice for a missing file so first-run
// callers need no special case, which means every read surface reports a
// misspelled branch exactly as it reports a real branch with nothing on it.
// Wherever that difference matters — naming a promote endpoint, say — this is
// the check that tells the two apart.
func BranchStoreExists(brainDir, branch string) bool {
	if brainDir == "" || branch == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(brainDir, filepath.FromSlash(FileRelPath(branch))))
	return err == nil
}

// Load reads a branch's facts.ndjson. A missing file is not an error: it
// yields an empty slice so first-run callers do not special-case it. Blank
// lines are skipped; a malformed line is a hard error so a corrupt store is
// surfaced rather than silently dropping records.
func Load(brainDir, branch string) ([]factmerge.Record, error) {
	rel := FileRelPath(branch)
	if err := base.RejectExistingSymlinkPathComponents(brainDir, rel); err != nil {
		return nil, err
	}
	path := filepath.Join(brainDir, filepath.FromSlash(rel))
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("fact store is not a regular file: %s", rel)
	}
	records, err := parseFactsFile(path)
	if err != nil {
		// The bare parse error names the damage but not the repair, and this
		// is the error every read surface shows a user. Name the repair here,
		// once, the way the history index names `refresh history`.
		// parseFactsFile itself stays clean so the integrity report (which
		// quotes it inside a fuller sentence) does not say it twice.
		return nil, fmt.Errorf("%w; %s", err, RepairHint())
	}
	return records, nil

}

// parseFactsFile reads a facts.ndjson file at an absolute path. A missing file
// yields an empty slice; a malformed line is a hard error so a corrupt store is
// surfaced rather than silently dropping records. The byte-level scan lives in
// factmerge.ParseNDJSON; this keeps only the file/path handling and restores the
// historical "parse <file> line N: ..." message the core cannot know the path
// for.
func parseFactsFile(path string) ([]factmerge.Record, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	records, err := factmerge.ParseNDJSON(f)
	if err != nil {
		var perr *factmerge.ParseError
		if errors.As(err, &perr) {
			return nil, fmt.Errorf("parse %s line %d: %w", filepath.Base(path), perr.Line, perr.Err)
		}
		return nil, err
	}
	return records, nil
}

// MissingBranchStores returns the branches a fact source manifest declares
// whose facts.ndjson is no longer on disk.
//
// Load treats an absent file as an empty branch (first-run callers must
// not special-case it), so a deleted or never-restored fact store is
// indistinguishable from "this branch has no facts" on every read surface.
// The manifest, however, names exactly which branches the store is supposed to
// hold, and that claim is checkable. Anything it names but cannot produce is a
// source that will silently contribute nothing.
func MissingBranchStores(brainDir string, source *bmanifest.FactSource) []string {
	if source == nil || brainDir == "" {
		return nil
	}
	var missing []string
	for _, branch := range source.Branches {
		if branch == "" {
			continue
		}
		path := filepath.Join(brainDir, filepath.FromSlash(FileRelPath(branch)))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			missing = append(missing, branch)
		}
	}
	return missing
}

// UnreadableBranchStore is one declared branch whose facts.ndjson is on
// disk but cannot be read back.
type UnreadableBranchStore struct {
	Branch string
	Err    error
}

// UnreadableBranchStores returns the declared branches whose facts.ndjson
// exists but does not parse.
//
// Existence is not readability. `facts status`, `recall`, `brief` and every
// other fact reader goes through Load, which fails hard on a malformed
// line so a corrupt store is never silently truncated -- so a single bad line
// takes the whole branch out of every retrieval surface while the manifest,
// the file listing, and therefore MissingBranchStores all still report the
// store as present. Doctor is the command a user is sent to when `status` says
// something is wrong, and it must not hand out a clean bill of health for a
// store every other command refuses to read.
//
// This deliberately calls the SAME loader those commands call rather than
// scanning the file itself: a second, more forgiving parser here would put
// doctor back in the business of disagreeing with the readers it is supposed
// to explain.
func UnreadableBranchStores(brainDir string, source *bmanifest.FactSource) []UnreadableBranchStore {
	if source == nil || brainDir == "" {
		return nil
	}
	var unreadable []UnreadableBranchStore
	for _, branch := range source.Branches {
		if branch == "" {
			continue
		}
		if _, err := Load(brainDir, branch); err != nil {
			unreadable = append(unreadable, UnreadableBranchStore{Branch: branch, Err: err})
		}
	}
	return unreadable
}

// MissingStoreWarning renders the caller-facing warning for the branches
// MissingBranchStores found, or "" when the declared store is intact.
func MissingStoreWarning(missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"facts declared for branch(es) %s but their %s is missing from the brain; those facts cannot be recalled — run `"+base.BrainInvocation(os.LookupEnv, "refresh")+"` and re-distill",
		strings.Join(missing, ", "), FileName,
	)
}

// Write persists a branch's facts as newline-delimited JSON, one record
// per line, sorted deterministically so the file is stable across rebuilds and
// diffs cleanly. The branch directory is created if needed. The byte-level
// sort+marshal lives in factmerge.WriteNDJSON; this keeps the atomic,
// symlink-rejecting disk write in the store.
func Write(brainDir, branch string, records []factmerge.Record) error {
	var buf bytes.Buffer
	if err := factmerge.WriteNDJSON(&buf, records); err != nil {
		return err
	}
	return brainstore.WriteBrainRelativeFileAtomic(brainDir, FileRelPath(branch), buf.Bytes(), 0o600)
}

// --- taxonomy -------------------------------------------------------------

// DefaultTaxonomy is the shipped baseline taxonomy. The top-level
// categories here are fixed in Phase A; distillation may invent a new
// three-level path only under one of these categories. The brain regenerates
// taxonomy.json from this default on refresh.
func DefaultTaxonomy(now time.Time) Taxonomy {
	return Taxonomy{
		GeneratedAt: now,
		Categories: map[string]string{
			"preferences":  "How the user wants to work and how code should be written.",
			"architecture": "Resolved design, boundary, and data-flow decisions and their rationale.",
			"project":      "Stack, tooling, CI/CD, ownership, and standing project facts.",
			"workflow":     "Branching, testing, review, and release process rules.",
			"constraints":  "Non-obvious invariants, gotchas, and hard constraints.",
		},
		Paths: []PathDef{
			{Path: "preferences.coding.style", Description: "Stated code-style preferences.", Examples: []string{"prefers table-driven tests"}},
			{Path: "preferences.workflow.general", Description: "How the user prefers to work day to day."},
			{Path: "architecture.boundaries.rationale", Description: "Why a module or package boundary exists."},
			{Path: "architecture.data.flow", Description: "How data moves through the system."},
			{Path: "project.tooling.stack", Description: "Languages, frameworks, and tools the project uses."},
			{Path: "project.ci_cd.pipeline", Description: "Build, test, and deploy pipeline facts."},
			{Path: "workflow.branching.rules", Description: "Branching and merge conventions."},
			{Path: "workflow.testing.rules", Description: "How and when tests must be run."},
			{Path: "constraints.invariants.general", Description: "Invariants that must hold across changes."},
		},
	}
}

// LoadTaxonomy reads the active taxonomy snapshot, falling back to the
// shipped default when the file is absent so callers always get a usable
// taxonomy. A present-but-malformed file is a hard error.
func LoadTaxonomy(brainDir string, now time.Time) (Taxonomy, error) {
	path := filepath.Join(brainDir, filepath.FromSlash(TaxonomyPath))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultTaxonomy(now), nil
		}
		return Taxonomy{}, err
	}
	var taxonomy Taxonomy
	if err := json.Unmarshal(data, &taxonomy); err != nil {
		return Taxonomy{}, fmt.Errorf("parse %s: %w", TaxonomyPath, err)
	}
	return taxonomy, nil
}

// WriteTaxonomy persists the active taxonomy snapshot.
func WriteTaxonomy(brainDir string, taxonomy Taxonomy) error {
	data, err := json.MarshalIndent(taxonomy, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return brainstore.WriteBrainRelativeFileAtomic(brainDir, TaxonomyPath, data, 0o600)
}

// TaxonomyTopLevels returns the set of top-level categories the taxonomy
// recognizes. A fact whose path's top-level is absent from this set is an
// orphan (reported, never auto-deleted).
func TaxonomyTopLevels(taxonomy Taxonomy) map[string]struct{} {
	tops := make(map[string]struct{}, len(taxonomy.Categories))
	for category := range taxonomy.Categories {
		tops[category] = struct{}{}
	}
	return tops
}

// SortedTaxonomyTopLevels returns the taxonomy's top-level categories sorted
// alphabetically, for stable user-facing messages (e.g. rejecting an explicit
// --path under an unknown category).
func SortedTaxonomyTopLevels(taxonomy Taxonomy) []string {
	cats := make([]string, 0, len(taxonomy.Categories))
	for category := range taxonomy.Categories {
		cats = append(cats, category)
	}
	sort.Strings(cats)
	return cats
}

// PathKnownTopLevel reports whether path's top-level category exists in the
// taxonomy. This is the conservative drift check: a fact keeps its assigned
// paths literally, and only its top-level category must still exist for it to
// remain valid.
func PathKnownTopLevel(taxonomy Taxonomy, path string) bool {
	_, ok := TaxonomyTopLevels(taxonomy)[TopLevel(path)]
	return ok
}

// --- manifest summary -----------------------------------------------------

// SummarizeSource folds a per-branch view of the fact store into the
// source manifest recorded under sources.facts in the brain manifest.
func SummarizeSource(now time.Time, byBranch map[string][]factmerge.Record, chunksScanned, chunksDistilled, proposals int, warnings []string) *bmanifest.FactSource {
	source := &bmanifest.FactSource{
		GeneratedAt:     now,
		TaxonomyPath:    TaxonomyPath,
		Proposals:       proposals,
		ChunksScanned:   chunksScanned,
		ChunksDistilled: chunksDistilled,
		Warnings:        append([]string(nil), warnings...),
		ByKind:          map[string]int{},
	}
	branches := make([]string, 0, len(byBranch))
	for branch := range byBranch {
		branches = append(branches, branch)
	}
	sort.Strings(branches)
	source.Branches = branches
	for _, branch := range branches {
		for _, record := range byBranch[branch] {
			// A stored line carrying no id is not a fact (see IsFactRecord): it
			// cannot be fetched, recalled, verified or superseded. Counting it
			// here is how a damaged store re-declared its own damage as healthy
			// on the next `remember` or `refresh`, and it is what the integrity
			// cross-check compares against.
			if !IsFactRecord(record) {
				continue
			}
			if record.Status == factmerge.StatusCandidate {
				// A staged candidate is not a published memory yet: counting it
				// into Facts would inflate every surface after a crashed run,
				// while hiding it would make the staged work invisible. Its own
				// counter keeps both honest until the next distill reconciles it.
				source.PendingReconcile++
				continue
			}
			source.Facts++
			switch record.Origin {
			case OriginDistilled:
				source.Distilled++
			case OriginAuthored:
				source.Authored++
			case OriginImported:
				// Without this bucket an imported fact raises the total and no
				// line item, so the origins visibly do not sum to the count and
				// nothing explains the difference.
				source.Imported++
			}
			if record.Status == factmerge.StatusSuperseded {
				source.Superseded++
			}
			// by_kind counts only active facts, matching what recall/tree surface
			// (superseded/retracted facts are not retrievable), so the histogram
			// answers "what kinds can I recall" rather than paralleling Facts.
			if record.Status == factmerge.StatusActive {
				source.ByKind[KindOrInferred(record)]++
			}
			for _, anchor := range record.Provenance {
				if anchor.Verified {
					source.Verified++
				} else {
					source.Unsigned++
				}
			}
		}
	}
	return source
}

// MissingStoreWarningForBranch scopes retrieval warnings to the store read.
func MissingStoreWarningForBranch(brainDir string, source *bmanifest.FactSource, branch string) string {
	integrity := Inspect(brainDir, source)
	switch integrity.Mode {
	case DefectMissing:
		for _, missing := range integrity.Missing {
			if missing == branch {
				return MissingStoreWarning([]string{branch})
			}
		}
		return ""
	case DefectUnreadable:
		for _, unreadable := range integrity.Unreadable {
			if unreadable == branch {
				return integrity.Warning()
			}
		}
		return ""
	default:
		// Aggregate count discrepancies cannot be assigned to one branch.
		return integrity.Warning()
	}
}
