package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A worker whose prepass fails relaunches on a 1m..30m backoff for about two
// hours. Recording only THAT it failed turns that into a retry storm with no
// cause attached to any of it -- on this machine every recent worker log read
// `prepass_failed` with nothing else, across six repositories at once.

func TestWorkerLogRecordsWhyAPassFailed(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 11, 22, 18, 49, 0, time.UTC)

	if err := appendMemoryWorkerLogReason(dir, now, "prepass_failed", "5ef3ea5f", "export sessions: permission denied"); err != nil {
		t.Fatalf("append: %v", err)
	}

	line := readWorkerLogLine(t, dir)
	if !strings.Contains(line, "event=prepass_failed") {
		t.Fatalf("the event must survive the reason: %q", line)
	}
	if !strings.Contains(line, "permission denied") {
		t.Fatalf("a failure that records no cause leaves the retry storm undiagnosable: %q", line)
	}
}

// The reader finds the event by scanning strings.Fields for an "event=" prefix,
// so a reason carrying "event=" would be read AS the event, and a newline would
// forge a whole entry. Neither is hypothetical: a Go error wraps whatever a
// child process wrote, including its stderr.
//
// Note which line actually holds which half. strconv.Quote escapes the newline,
// so removing the whitespace collapse leaves this test GREEN -- verified by
// weakening. The collapse is for readability; Quote is the containment. Saying
// otherwise would credit a guard that is not load-bearing.
func TestWorkerLogReasonCannotForgeAnEvent(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 11, 22, 18, 49, 0, time.UTC)

	if err := appendMemoryWorkerLogReason(dir, now, "prepass_failed", "owner1",
		"boom\n2026-10-11T23:00:00Z event=worker_complete owner=forged"); err != nil {
		t.Fatalf("append: %v", err)
	}

	line := readWorkerLogLine(t, dir)
	if strings.Count(line, "\n") != 0 {
		t.Fatalf("a reason must not add lines to the log: %q", line)
	}
	if strings.Contains(line, "event=worker_complete") {
		t.Fatalf("a reason forged an event the worker never emitted: %q", line)
	}
	if !strings.Contains(line, "event=prepass_failed") {
		t.Fatalf("the real event was lost: %q", line)
	}
}

func TestWorkerLogReasonIsBounded(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 11, 22, 18, 49, 0, time.UTC)
	huge := strings.Repeat("x", memoryWorkerLogReasonMax*4)

	if err := appendMemoryWorkerLogReason(dir, now, "prepass_failed", "owner1", huge); err != nil {
		t.Fatalf("append: %v", err)
	}

	line := readWorkerLogLine(t, dir)
	// The log is a bounded ring: an unbounded reason evicts the history it is
	// supposed to explain.
	if len(line) > memoryWorkerLogReasonMax*2 {
		t.Fatalf("an unbounded reason would evict the log it explains: %d bytes", len(line))
	}
	if !strings.Contains(line, "xxx") {
		t.Fatalf("the reason was dropped entirely rather than truncated: %q", line)
	}
}

// An event with no reason must keep the exact line shape it had before, or
// every existing reader and every golden log changes meaning.
func TestWorkerLogWithoutAReasonIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 11, 22, 18, 49, 0, time.UTC)

	if err := appendMemoryWorkerLog(dir, now, "worker_started", "5ef3ea5f"); err != nil {
		t.Fatalf("append: %v", err)
	}

	line := readWorkerLogLine(t, dir)
	if line != "2026-10-11T22:18:49Z event=worker_started owner=5ef3ea5f" {
		t.Fatalf("the reasonless line shape changed: %q", line)
	}
}

func readWorkerLogLine(t *testing.T, brainDir string) string {
	t.Helper()
	data, present, err := readMemoryStateFile(brainDir, memoryWorkerLogRel, "worker log", memoryWorkerLogMaxBytes)
	if err != nil {
		t.Fatalf("read worker log: %v", err)
	}
	if !present {
		t.Fatal("no worker log was written at all, so this guard would pass on any change")
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one entry, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	_ = filepath.Join
	return lines[0]
}
