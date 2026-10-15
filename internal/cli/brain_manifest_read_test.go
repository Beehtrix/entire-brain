package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-brain/internal/agentsetup"
)

// Only aggregate shape information comes from the cohort. Every value below is
// synthetic. Session metadata is deliberately repeated through the production
// session aliases, as it is in retained manifests.
func TestBrainManifestObservedShapesRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		branches, sessions, warnings, bytes int
	}{
		{"session-heavy", 870, 5305, 111, 18569433},
		{"warning-heavy", 389, 2633, 6238, 17220291},
		// Strictly larger than both observed shapes on every axis, and larger
		// than the (test-lowered) default read cap below.
		{"larger-than-default-read-limit", 2000, 6000, 12000, 24 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "larger-than-default-read-limit" {
				// The guarded regression is a manifest reader routed through
				// the capped default read path. Lowering the cap keeps the
				// fixture above it without 80 MiB of race-instrumented JSON.
				prev := defaultMaxReadBytes
				defaultMaxReadBytes = 16 << 20
				t.Cleanup(func() { defaultMaxReadBytes = prev })
			}
			repo := t.TempDir()
			key, err := localRepoStorageKey(repo)
			if err != nil {
				t.Fatal(err)
			}
			dataDir := t.TempDir()
			dir := filepath.Join(dataDir, "repos", filepath.FromSlash(key))
			source := &sessionSourceManifest{}
			for i := 0; i < tc.branches; i++ {
				source.Branches = append(source.Branches, exportBranch{Branch: fmt.Sprintf("branch-%d", i), Directory: fmt.Sprintf("sessions/branch-%d", i), SessionCount: 1})
			}
			for i := 0; i < tc.sessions; i++ {
				source.Sessions = append(source.Sessions, exportSession{SessionID: fmt.Sprintf("session-%d", i), Branch: "main", LatestCheckpoint: fmt.Sprintf("%012x", i), TranscriptPath: fmt.Sprintf("sessions/main/session-%d.jsonl", i), Summary: &checkpointSummary{Intent: strings.Repeat("synthetic intent ", 30), Outcome: "synthetic outcome"}, FilesTouched: []string{"internal/example.go"}})
			}
			for i := 0; i < tc.warnings; i++ {
				source.Warnings = append(source.Warnings, fmt.Sprintf("synthetic checkpoint coverage warning %d", i))
			}
			manifest := exportManifest{SchemaVersion: brainManifestSchemaVersion, RepoKey: key, Sources: &brainSources{Sessions: source, Seed: &seedSourceManifest{}, Semantic: &semanticSourceManifest{}, History: &historySourceManifest{}, Docs: &docSourceManifest{}}}
			if tc.name != "warning-heavy" {
				manifest.Sources.Patterns = &patternSourceManifest{}
			}
			applySessionSourceAliases(&manifest)
			// Distribute synthetic metadata across session summaries; only a small
			// remainder goes in a known scalar to match exact observed byte sizes.
			manifest.BrainVersion = "x"
			normalizeBrainManifest(&manifest)
			data, err := json.MarshalIndent(manifest, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			padding := tc.bytes - len(data) - 1
			if padding < 0 {
				t.Fatalf("fixture already exceeds target by %d", -padding)
			}
			perSession := padding / (2 * tc.sessions)
			for i := range source.Sessions {
				source.Sessions[i].Summary.Outcome += strings.Repeat("x", perSession)
			}
			manifest.BrainVersion += strings.Repeat("x", padding%(2*tc.sessions))
			if err := writeBrainManifestAndReadme(dir, manifest); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(dir, exportManifestFileName))
			if err != nil || info.Size() != int64(tc.bytes) {
				t.Fatalf("size: %v, %v", info, err)
			}
			for _, reader := range []func(string) (*exportManifest, error){loadBrainManifest, loadBrainManifestForReplace} {
				got, err := reader(dir)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, &manifest) {
					t.Fatal("manifest records changed during round trip")
				}
			}
			got, health, issue := inspectBrainManifestHealth(dir)
			if issue != nil || health.State != "current" || len(got.Sessions) != tc.sessions {
				t.Fatalf("health failed: %+v", health)
			}
			// A stored manifest must not activate Brain agent guidance.
			setupOpts := agentsetup.Options{DataDir: dataDir, StateDir: t.TempDir(), ConfigDir: t.TempDir(), ListPlugins: func() (string, error) {
				return "Managed plugin directory: /fixture\n\n  brain v1.0.0 → /fixture/entire-brain\n  graph v1.0.0 → /fixture/entire-graph\n", nil
			}}
			render := func() (string, error) { return agentsetup.Preview(repo, "graph", setupOpts) }
			guide, err := render()
			if err != nil || notGraphOnly(guide) {
				t.Fatalf("manifest affected agent activation: %v", err)
			}
			if err := agentsetup.Install(repo, render, io.Discard); err != nil {
				t.Fatal(err)
			}
			// Exercise replacement's strict read of the existing oversized manifest.
			if err := writeBrainManifestAndReadme(dir, *got); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(dir, exportManifestFileName))
			if err != nil {
				t.Fatal(err)
			}
			got.GeneratedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := writeBrainManifestAndReadme(dir, *got); err == nil {
				t.Fatal("invalid time must fail encoding")
			}
			after, err := os.ReadFile(filepath.Join(dir, exportManifestFileName))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("failed operation changed previous manifest")
			}
			if _, err := loadBrainManifest(dir); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBrainManifestDecoderCompatibility(t *testing.T) {
	for _, tc := range []struct{ name, body, code string }{
		{"v1", `{"schema_version":1}`, ""},
		{"v2", `{"schema_version":2}`, ""},
		{"v3", `{"schema_version":3}`, ""},
		{"unknown", `{"schema_version":3,"future":{"nested":true}}`, ""},
		{"empty", ``, memoryErrStateCorrupt},
		{"malformed", `{"schema_version":3, broken}`, memoryErrStateCorrupt},
		{"truncated", `{"schema_version":3,"sessions":[`, memoryErrStateCorrupt},
		{"trailing-json", `{"schema_version":3} {}`, memoryErrStateCorrupt},
		{"trailing-garbage", `{"schema_version":3} garbage`, memoryErrStateCorrupt},
		{"missing-version", `{}`, memoryErrUnsupportedVersion},
		{"newer", `{"schema_version":4,"future":true}`, memoryErrUnsupportedVersion},
		{"older", `{"schema_version":-1}`, memoryErrUnsupportedVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTestManifest(t, tc.body)
			_, err := loadBrainManifest(dir)
			_, health, issue := inspectBrainManifestHealth(dir)
			if tc.code == "" {
				if err != nil || issue != nil || health.State != "current" {
					t.Fatalf("read=%v health=%+v", err, health)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.code) || issue == nil || health.ErrorCode != tc.code {
					t.Fatalf("read=%v health=%+v", err, health)
				}
				if err := writeBrainManifestAndReadme(dir, exportManifest{}); err == nil {
					t.Fatal("writer must refuse invalid existing state")
				}
				after, _ := os.ReadFile(filepath.Join(dir, exportManifestFileName))
				if string(after) != tc.body {
					t.Fatal("failed rewrite changed original")
				}
			}
		})
	}
}

func TestBrainManifestReaderKeepsFileProtections(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, exportManifestFileName)
			target := filepath.Join(t.TempDir(), "target.json")
			if err := os.WriteFile(target, []byte(`{"schema_version":3}`), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(path, 0700)
			case "symlink":
				err = os.Symlink(target, path)
			case "hardlink":
				err = os.Link(target, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loadBrainManifest(dir); err == nil || !strings.Contains(err.Error(), memoryErrStateUnsafe) {
				t.Fatalf("unsafe read: %v", err)
			}
			_, health, _ := inspectBrainManifestHealth(dir)
			if health.State != "unsafe" {
				t.Fatalf("health: %+v", health)
			}
		})
	}
}

func TestBrainManifestLargeUnknownFieldsRemainReadOnly(t *testing.T) {
	// The unknown-field payload must exceed maxManifestBytes so the bounded
	// reader below still refuses it; lowering the bound keeps that property
	// without 17 MiB of race-instrumented JSON per decode pass.
	prev := maxManifestBytes
	maxManifestBytes = 1 << 20
	t.Cleanup(func() { maxManifestBytes = prev })
	dir := writeTestManifest(t, `{"schema_version":3,"sessions":[],"future":"`+strings.Repeat("x", 2<<20)+`"}`)
	_, dropped, err := readBrainManifest(dir, true)
	if err != nil || !dropped {
		t.Fatalf("tolerant read: dropped=%v err=%v", dropped, err)
	}
	path := filepath.Join(dir, exportManifestFileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeBrainManifestAndReadme(dir, exportManifest{}); err == nil {
		t.Fatal("writer erased unknown fields")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("unknown fields were changed")
	}
	if _, err := safeReadFile(path, maxManifestBytes); err == nil {
		t.Fatal("unrelated bounded reader lost its limit")
	}
	// Explicit forced-refresh cleanup shares the dedicated decoder, while still
	// using the checked remover. This only removes synthetic test state.
	removed, err := discardManifestThisBuildCannotRewrite(dir)
	if err != nil || !removed {
		t.Fatalf("checked cleanup: removed=%v err=%v", removed, err)
	}
}
