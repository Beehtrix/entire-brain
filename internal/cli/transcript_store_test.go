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
			RepoRoot:      t.TempDir(),
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
	store.resolved = true // pre-resolve: inject the snapshot directly
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

// The manifest's recorded transcript mode pins which stored bytes a member
// serves; a member missing that mode's pointer is unreadable, never a silent
// switch to the other mode's bytes.
func TestMemberTranscriptLocationPinsManifestMode(t *testing.T) {
	member := sessionCheckpoint{
		CheckpointID:          "cp1",
		SessionIndex:          0,
		TranscriptPath:        "aa/cp1/0/full.jsonl",
		CompactTranscriptPath: "aa/cp1/0/compact.jsonl",
		SourceKey:             "src",
	}
	if loc, ok := memberTranscriptLocation(member, "compact", "full.jsonl"); !ok || loc.sourcePath != "aa/cp1/0/compact.jsonl" {
		t.Fatalf("compact mode = %+v ok=%v", loc, ok)
	}
	if loc, ok := memberTranscriptLocation(member, "raw", "full.jsonl"); !ok || loc.sourcePath != "aa/cp1/0/full.jsonl" {
		t.Fatalf("raw mode = %+v ok=%v", loc, ok)
	}
	member.CompactTranscriptPath = ""
	if _, ok := memberTranscriptLocation(member, "compact", "full.jsonl"); ok {
		t.Fatal("compact mode without a compact pointer must be unreadable, not raw")
	}
	member.TranscriptPath = ""
	if loc, ok := memberTranscriptLocation(member, "raw", "full.jsonl"); !ok || loc.sourcePath == "" {
		t.Fatalf("raw mode falls back to the snapshot path layout, got %+v ok=%v", loc, ok)
	}
}

// A cancelled first resolve must not poison the memoized store for the
// process: context failures retry, other failures stay cached.
func TestTranscriptStoreRetriesAfterCancelledResolve(t *testing.T) {
	brainDir, rel := transcriptStoreFixture(t, true)
	if err := os.Remove(filepath.Join(brainDir, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
	store := newTranscriptStore(ExecRunner{}, brainDir)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.initSnapshot(cancelled); err == nil {
		t.Fatal("cancelled resolve must fail")
	}
	if store.resolved {
		t.Fatal("a context failure must not be memoized")
	}

	// A fresh context retries; the fixture repo has no checkpoints so the
	// resolve fails PERMANENTLY now, and that failure is cached.
	if err := store.initSnapshot(context.Background()); err == nil {
		t.Fatal("resolve against a checkpoint-less repo must fail")
	}
	if !store.resolved {
		t.Fatal("a permanent failure memoizes")
	}
}

// A path neither materialized nor resolvable names nothing readable.
func TestTranscriptStoreUnknownPathFails(t *testing.T) {
	brainDir, _ := transcriptStoreFixture(t, true)
	store := newTranscriptStore(ExecRunner{}, brainDir)
	store.resolved = true
	store.snapshot = &checkpointSnapshot{}
	store.byRel = map[string]transcriptLocation{}
	if _, err := store.Read(context.Background(), "sessions/main/missing.jsonl"); err == nil {
		t.Fatal("unknown transcript must not read")
	}
}
