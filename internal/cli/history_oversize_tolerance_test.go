package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
)

// historyOversizeFixture writes a brain holding three sessions: two small
// transcripts and one deliberately larger than the cap the test sets.
func historyOversizeFixture(t *testing.T, now time.Time) (brainDir string, oversizeRel string, capBytes int64) {
	t.Helper()
	brainDir = t.TempDir()

	exchange := func(text string) string {
		return `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"ask ` + text + `"}]}}` + "\n" +
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Decision: ` + text + `."}]}}` + "\n"
	}

	small := map[string]string{
		"sessions/main/small-one.jsonl": exchange("keep the first"),
		"sessions/main/small-two.jsonl": exchange("keep the second"),
	}
	oversizeRel = "sessions/main/huge.jsonl"

	largest := 0
	for rel, body := range small {
		if len(body) > largest {
			largest = len(body)
		}
		writeHistoryFixtureFile(t, brainDir, rel, body)
	}
	// The cap sits above both readable transcripts and below the big one, so
	// exactly one file is unreadable and the other two are not.
	capBytes = int64(largest) + 1
	writeHistoryFixtureFile(t, brainDir, oversizeRel, strings.Repeat("x", int(capBytes)*4)+"\n")

	sessions := []exportSession{
		{SessionID: "small-one", Branch: "main", LatestCheckpoint: "cp1", TranscriptPath: "sessions/main/small-one.jsonl", CreatedAt: now.Add(-3 * time.Hour)},
		{SessionID: "small-two", Branch: "main", LatestCheckpoint: "cp2", TranscriptPath: "sessions/main/small-two.jsonl", CreatedAt: now.Add(-2 * time.Hour)},
		{SessionID: "huge", Branch: "main", LatestCheckpoint: "cp3", TranscriptPath: oversizeRel, CreatedAt: now.Add(-time.Hour)},
	}
	manifest := exportManifest{
		SchemaVersion: brainManifestSchemaVersion, GeneratedAt: now.Add(-time.Hour), DefaultBranch: "main",
		Sources: &brainSources{Sessions: &sessionSourceManifest{GeneratedAt: now.Add(-time.Hour), DefaultBranch: "main", Sessions: sessions}},
	}
	if err := writeBrainManifestAndReadme(brainDir, manifest); err != nil {
		t.Fatal(err)
	}
	return brainDir, oversizeRel, capBytes
}

func writeHistoryFixtureFile(t *testing.T, brainDir, rel, body string) {
	t.Helper()
	full := filepath.Join(brainDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// One transcript too large to read must cost its own session and nothing else.
// It used to cost the whole index: on a real brain a single 438.9MB raw
// transcript produced 0 of 323 sessions indexed, with every other session
// readable and none of them read.
func TestHistoryIndexSkipsAnOversizeTranscriptAndIndexesTheRest(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	brainDir, oversizeRel, capBytes := historyOversizeFixture(t, now)

	oldMax := base.MaxDocumentTranscriptBytes
	base.MaxDocumentTranscriptBytes = capBytes
	t.Cleanup(func() { base.MaxDocumentTranscriptBytes = oldMax })

	source, err := writeBrainHistoryIndexAndSource(brainDir, now, nil)
	if err != nil {
		t.Fatalf("one oversize transcript refused the whole index: %v", err)
	}
	if source == nil {
		t.Fatal("no history source manifest was written")
	}

	index, err := loadBrainHistoryIndex(brainDir, source)
	if err != nil {
		t.Fatalf("load history index: %v", err)
	}
	if len(index.Records) == 0 {
		t.Fatal("the index came out empty; the readable sessions were not indexed")
	}
	sessions := map[string]struct{}{}
	for _, record := range index.Records {
		sessions[record.SessionID] = struct{}{}
	}
	for _, want := range []string{"small-one", "small-two"} {
		if _, ok := sessions[want]; !ok {
			t.Errorf("session %s is readable but absent from the index: got %v", want, sessions)
		}
	}
	if _, ok := sessions["huge"]; ok {
		t.Error("the oversize session produced records, so the cap was not applied")
	}

	// Skipped, not silently dropped.
	joined := strings.Join(index.Warnings, "\n")
	if !strings.Contains(joined, oversizeRel) || !strings.Contains(joined, memoryErrInputTooLarge) {
		t.Fatalf("the skip was not reported with its file and reason: %v", index.Warnings)
	}
}

// The three readers of the transcript inventory have to skip the SAME file.
// Identity capture runs before the build and the freshness check runs after,
// so a disagreement means either a refusal the build would have survived or an
// index that is forever short of its own inventory and rebuilds every refresh.
func TestOversizeTranscriptIsSkippedByEveryInventoryReader(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	brainDir, oversizeRel, capBytes := historyOversizeFixture(t, now)

	oldMax := base.MaxDocumentTranscriptBytes
	base.MaxDocumentTranscriptBytes = capBytes
	t.Cleanup(func() { base.MaxDocumentTranscriptBytes = oldMax })

	// Identity capture, which runs first and used to refuse here.
	identity, manifest, _, err := captureHistoryProjectionIdentityMode(context.Background(), brainDir, true)
	if err != nil {
		t.Fatalf("identity capture refused over one oversize transcript: %v", err)
	}
	if identity.SessionContents == "" {
		t.Fatal("identity capture produced no content identity")
	}

	// The freshness check, which must agree with what the build indexed.
	digests, err := collectHistorySessionDigests(context.Background(), brainDir, map[string]string{})
	if err != nil {
		t.Fatalf("digest collection refused over one oversize transcript: %v", err)
	}
	for _, file := range digests {
		if file.Rel == oversizeRel {
			t.Fatalf("the freshness check counted %s, which the build skips; the index would rebuild forever", oversizeRel)
		}
	}
	if len(digests) != 2 {
		t.Fatalf("expected the two readable transcripts, got %d", len(digests))
	}

	// And the index it then builds is reported current, not perpetually stale.
	if _, err := writeBrainHistoryIndexAndSource(brainDir, now, nil); err != nil {
		t.Fatalf("index build: %v", err)
	}
	manifest, err = loadBrainManifest(brainDir)
	if err != nil {
		t.Fatalf("reload manifest: %v", err)
	}
	if !historyIndexCurrent(brainDir, manifest) {
		t.Fatal("the index built over a skipped transcript reports stale, so every refresh would rebuild it")
	}
}

// The store branch is the one that mattered and the one I nearly left out.
// A fileless, checkpoint-backed brain reads every transcript through the
// transcript store, so a sentinel raised only on the on-disk path would
// classify nothing in the configuration the whole-index refusal was observed
// in. This pins the boundary: a size refusal arriving from the store must be
// the same classified error the three inventory readers skip on.
func TestStoreSizeRefusalClassifiesAsTheSkippableOversizeError(t *testing.T) {
	t.Parallel()
	bound := &readBoundExceededError{Source: "transcript sessions/main/huge.jsonl", Max: 1024}

	classified := classifyTranscriptReadError(bound)
	if !errors.Is(classified, errHistoryTranscriptTooLarge) {
		t.Fatalf("a store size refusal does not classify as the skippable oversize error, so the store path still refuses the whole index: %v", classified)
	}

	// The wording is load-bearing elsewhere: history_delta.go classifies the
	// same condition by substring, so the code must still appear in the text.
	if !strings.Contains(classified.Error(), memoryErrInputTooLarge) {
		t.Fatalf("the classified error lost the %s code that other consumers match on: %v", memoryErrInputTooLarge, classified)
	}
	if !strings.Contains(classified.Error(), "exceeds maximum size of 1024 bytes") {
		t.Fatalf("the classified error lost the underlying bound detail: %v", classified)
	}

	// Anything that is not a size refusal must pass through untouched, or an
	// absent or corrupt transcript would start being skipped too.
	other := errors.New("read transcript: unexpected EOF")
	if got := classifyTranscriptReadError(other); got != other {
		t.Fatalf("a non-size read error was reclassified: %v", got)
	}
	if errors.Is(classifyTranscriptReadError(other), errHistoryTranscriptTooLarge) {
		t.Fatal("a non-size read error classified as skippable oversize")
	}
}
