package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func transcriptStoreFixture(t *testing.T, withManifest bool) (string, string) {
	t.Helper()
	brainDir := t.TempDir()
	rel := "sessions/main/s1.jsonl"
	full := filepath.Join(brainDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("turn one\nturn two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if withManifest {
		now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
		manifest := exportManifest{
			SchemaVersion: brainManifestSchemaVersion,
			GeneratedAt:   now,
			DefaultBranch: "main",
			Sources: &brainSources{Sessions: &sessionSourceManifest{GeneratedAt: now, DefaultBranch: "main", Sessions: []exportSession{
				{SessionID: "s1", Branch: "main", LatestCheckpoint: "cp1", TranscriptPath: rel, CreatedAt: now.Add(-time.Hour)},
			}}},
		}
		if err := writeBrainManifestAndReadme(brainDir, manifest); err != nil {
			t.Fatal(err)
		}
	}
	return brainDir, rel
}

// A materialized file reads through the store with no manifest at all — the
// standalone exported-bundle case.
func TestTranscriptStoreReadsMaterializedFile(t *testing.T) {
	brainDir, rel := transcriptStoreFixture(t, false)
	store := newTranscriptStore(ExecRunner{}, brainDir)
	data, err := store.Read(context.Background(), rel)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(data) != "turn one\nturn two\n" {
		t.Fatalf("content = %q", data)
	}
}

// The tombstone gate refuses a purged session even while its file still
// exists, and it binds reads made AFTER the tombstone was written in the same
// process — the gate must never be memoized.
func TestTranscriptStoreTombstoneGateIsFreshPerRead(t *testing.T) {
	brainDir, rel := transcriptStoreFixture(t, true)
	store := newTranscriptStore(ExecRunner{}, brainDir)

	if _, err := store.Read(context.Background(), rel); err != nil {
		t.Fatalf("pre-tombstone read: %v", err)
	}

	stones := emptySessionTombstones()
	stones.Excluded["s1"] = sessionTombstone{At: time.Now().UTC(), Reason: "privacy"}
	if err := saveSessionTombstones(brainDir, stones); err != nil {
		t.Fatal(err)
	}

	_, err := store.Read(context.Background(), rel)
	if err == nil || !strings.Contains(err.Error(), "tombstoned") {
		t.Fatalf("post-tombstone read must refuse, got %v", err)
	}
}

// With no materialized file, the store resolves the session's checkpoint blob
// from git through the snapshot source — the COR-2128 read path.
func TestTranscriptStoreFallsBackToCheckpointBlob(t *testing.T) {
	brainDir, rel := transcriptStoreFixture(t, true)
	if err := os.Remove(filepath.Join(brainDir, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}

	source, runner, base := chunkedTranscriptFixture(t, `{"type":"user"}`, `{"type":"assistant"}`)
	store := newTranscriptStore(runner, brainDir)
	store.snapOnce.Do(func() {}) // pre-resolve: inject the snapshot directly
	store.snapshot = &checkpointSnapshot{
		GitDir:    source.GitDir,
		Ref:       source.Ref,
		TreePaths: source.TreePaths,
		Sources:   map[string]checkpointSnapshotSource{"src": source},
	}
	store.byRel = map[string]transcriptLocation{rel: {sourceKey: "src", sourcePath: base}}

	data, err := store.Read(context.Background(), rel)
	if err != nil {
		t.Fatalf("Read via checkpoint blob: %v", err)
	}
	want := "{\"type\":\"user\"}\n{\"type\":\"assistant\"}"
	if string(data) != want {
		t.Fatalf("content = %q, want %q", data, want)
	}
}

// A path neither materialized nor resolvable names nothing readable.
func TestTranscriptStoreUnknownPathFails(t *testing.T) {
	brainDir, _ := transcriptStoreFixture(t, true)
	store := newTranscriptStore(ExecRunner{}, brainDir)
	store.snapOnce.Do(func() {})
	store.snapshot = &checkpointSnapshot{}
	store.byRel = map[string]transcriptLocation{}
	if _, err := store.Read(context.Background(), "sessions/main/missing.jsonl"); err == nil {
		t.Fatal("unknown transcript must not read")
	}
}
