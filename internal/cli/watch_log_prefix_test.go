package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func fixedNow() func() time.Time {
	t := time.Date(2026, 10, 21, 4, 5, 6, 0, time.UTC)
	return func() time.Time { return t }
}

// TestWatchLogLinesCarryTimeAndRepo pins COR-2176. watch.log is the only
// record of what a background daemon did, and bare `[watch] ...` lines could
// not be placed in time or attributed to a repo.
func TestWatchLogLinesCarryTimeAndRepo(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := newWatchLogPrefixWriter(&buf, "gh/entireio/entire-brain", fixedNow())
	fmt.Fprintln(w, "[watch] long-term memory reconciliation queued")

	got := buf.String()
	for _, want := range []string{"2026-10-21T04:05:06Z", "gh/entireio/entire-brain", "[watch] long-term memory reconciliation queued"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if n := strings.Count(got, "\n"); n != 1 {
		t.Errorf("one line in, %d newlines out: %q", n, got)
	}
}

// A multi-line write gets one stamp PER LINE, not one for the batch — a tick
// that logs two things must not leave the second line unattributed.
func TestWatchLogStampsEveryLineOfAMultiLineWrite(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := newWatchLogPrefixWriter(&buf, "repo", fixedNow())
	fmt.Fprint(w, "first\nsecond\n")

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), buf.String())
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "2026-10-21T04:05:06Z repo ") {
			t.Errorf("line not stamped: %q", l)
		}
	}
	if strings.Contains(buf.String(), "repo \n") {
		t.Error("emitted a bare prefix with no content")
	}
}

// The writer must report the CALLER's byte count. Returning the prefixed
// length makes io.Copy and fmt think more was written than was asked for;
// returning less makes them think the write failed.
func TestWatchLogWriterReportsTheCallersLength(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := newWatchLogPrefixWriter(&buf, "repo", fixedNow())
	payload := []byte("[watch] tick\n")
	n, err := w.Write(payload)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("reported %d bytes for a %d-byte write; short counts read as failures", n, len(payload))
	}
}
