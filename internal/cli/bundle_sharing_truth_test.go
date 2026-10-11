package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Sharing a brain with a teammate is the one path that works today, and it is
// lossy: import with --overwrite replaces the fact layer wholesale. These pin
// the three places the product used to stay quiet about that.

func writeAuditLine(t *testing.T, brainDir, line string) {
	t.Helper()
	dir := filepath.Join(brainDir, semanticDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, semanticAuditLogName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatalf("write audit: %v", err)
	}
}

func TestLastBundleImportReadsTheNewestEntry(t *testing.T) {
	t.Parallel()
	brainDir := t.TempDir()

	writeAuditLine(t, brainDir, `{"action":"bundle_export","generated_at":"2026-10-09T10:00:00Z"}`)
	writeAuditLine(t, brainDir, `{"action":"bundle_import","generated_at":"2026-10-10T10:00:00Z"}`)
	writeAuditLine(t, brainDir, `{"action":"bundle_import","generated_at":"2026-10-11T10:00:00Z"}`)

	at, ok := lastBundleImportAt(brainDir)
	if !ok {
		t.Fatal("a recorded import must be found, or the note can never fire")
	}
	if got := at.Format("2006-01-02"); got != "2026-10-11" {
		t.Fatalf("the newest import must win, got %s", got)
	}
}

// The audit log is local operational state: it may be absent, truncated
// mid-write, or hold lines from a newer build. None of that may turn a working
// recall into an error, so every failure mode degrades to "no note".
func TestLastBundleImportToleratesAnUnusableLog(t *testing.T) {
	t.Parallel()

	if _, ok := lastBundleImportAt(t.TempDir()); ok {
		t.Fatal("a brain with no audit log must report no import")
	}

	brainDir := t.TempDir()
	writeAuditLine(t, brainDir, `{"action":"bundle_impo`) // truncated mid-write
	writeAuditLine(t, brainDir, `{"action":"bundle_import","generated_at":"not-a-time"}`)
	writeAuditLine(t, brainDir, `{"action":"bundle_export","generated_at":"2026-10-11T10:00:00Z"}`)
	if _, ok := lastBundleImportAt(brainDir); ok {
		t.Fatal("no parseable import was recorded, so none may be reported")
	}

	writeAuditLine(t, brainDir, `{"action":"bundle_import","generated_at":"2026-10-11T12:00:00Z"}`)
	if _, ok := lastBundleImportAt(brainDir); !ok {
		t.Fatal("a good line after bad ones must still be read, or one truncated write hides every import")
	}
}

// The defect this exists for: after an import replaced Bob's facts, `recall`
// told him "no distillation timestamp is recorded; run refresh, then distill".
// Re-distilling cannot bring back what an import replaced, so the advice was
// not merely unhelpful, it was wrong.
func TestEmptyRecallNamesTheImportThatReplacedTheFacts(t *testing.T) {
	t.Parallel()
	brainDir := t.TempDir()
	now := time.Now().UTC()
	// A real brain, or the "no brain has been built" branch answers first and
	// this guard passes without ever reaching the code it is about.
	// Facts really present on the branch queried: the integrity branch must
	// pass (a store that LOST facts is a more precise answer and rightly wins),
	// and the other-branch branch must not fire either.
	if err := writeFacts(brainDir, "main", []factRecord{
		vitalityTestFact("retries are capped at three", "main", now),
	}); err != nil {
		t.Fatalf("write facts: %v", err)
	}
	writeDistillFixtureAt(t, brainDir, now)
	declareFactsAt(t, brainDir, now, now, 1, "main")
	writeAuditLine(t, brainDir, `{"action":"bundle_import","generated_at":"2026-10-11T10:00:00Z"}`)

	note := emptyResultBlindSpotOnBranch(brainDir, "main")
	if note == "" {
		t.Fatal("an empty result after an import must still be explained")
	}
	if strings.Contains(note, "distillation") {
		t.Fatalf("re-distilling cannot restore what an import replaced; got: %s", note)
	}
	for _, want := range []string{"bundle import", "2026-10-11", "not evidence of absence"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note must name the import, when it happened, and its limits; missing %q in: %s", want, note)
		}
	}
}

// A brain that never imported anything must keep its old note exactly, or this
// fix has silently replaced every other explanation with its own.
func TestABrainThatNeverImportedKeepsItsUsualNote(t *testing.T) {
	t.Parallel()
	brainDir := t.TempDir()
	now := time.Now().UTC()
	// Facts really present on the branch queried: the integrity branch must
	// pass (a store that LOST facts is a more precise answer and rightly wins),
	// and the other-branch branch must not fire either.
	if err := writeFacts(brainDir, "main", []factRecord{
		vitalityTestFact("retries are capped at three", "main", now),
	}); err != nil {
		t.Fatalf("write facts: %v", err)
	}
	writeDistillFixtureAt(t, brainDir, now)
	declareFactsAt(t, brainDir, now, now, 1, "main")
	writeAuditLine(t, brainDir, `{"action":"bundle_export","generated_at":"2026-10-11T10:00:00Z"}`)

	note := emptyResultBlindSpotOnBranch(brainDir, "main")
	if strings.Contains(note, "bundle import") {
		t.Fatalf("an export is not an import; got: %s", note)
	}
	if !strings.Contains(note, "distillation") {
		t.Fatalf("the ordinary coverage note must survive; got: %s", note)
	}
}

// The refusal used to end "or import into a brain without them" -- a state a
// real receiver is never in, because `setup` always writes seed and docs.
func TestTheImportRefusalNamesOnlyReachableChoices(t *testing.T) {
	t.Parallel()
	brainDir := t.TempDir()
	bundleRoot := t.TempDir()
	_ = brainDir
	_ = bundleRoot

	err := bundleImportConflictError([]string{"seed", "docs", "facts"})
	if err == nil {
		t.Fatal("conflicting layers must be refused")
	}
	msg := err.Error()
	if strings.Contains(msg, "import into a brain without them") {
		t.Fatalf("the refusal names a state a set-up brain can never be in: %s", msg)
	}
	for _, want := range []string{"--overwrite", "facts", "no longer be retrievable"} {
		if !strings.Contains(msg, want) && !strings.Contains(msg, "stops being retrievable") {
			t.Errorf("the refusal must say what --overwrite costs; missing %q in: %s", want, msg)
		}
	}
}
