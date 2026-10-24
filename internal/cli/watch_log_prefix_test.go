package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// TestWatchLogPrefixStampsEveryLine is the guard for the diagnosis that cost
// the time: a tick's lines must each carry a clock and a repo, not just the
// banner above them.
func TestWatchLogPrefixStampsEveryLine(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	at := time.Date(2026, 10, 24, 9, 30, 15, 0, time.UTC)
	w := newWatchLogPrefixWriter(&buf, "gh/entireio/entire-brain", fixedClock(at))

	fmt.Fprintf(w, "[watch] refresh failed: %v\n", errors.New("brain_locked"))
	fmt.Fprint(w, "[watch] reconciliation queued\n[watch] distill skipped\n")

	got := buf.String()
	want := "2026-10-24T09:30:15Z gh/entireio/entire-brain [watch] refresh failed: brain_locked\n" +
		"2026-10-24T09:30:15Z gh/entireio/entire-brain [watch] reconciliation queued\n" +
		"2026-10-24T09:30:15Z gh/entireio/entire-brain [watch] distill skipped\n"
	if got != want {
		t.Fatalf("stamped output\n got: %q\nwant: %q", got, want)
	}
	for i, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if !strings.HasPrefix(line, "2026-10-24T09:30:15Z gh/entireio/entire-brain ") {
			t.Fatalf("line %d unattributed: %q", i, line)
		}
	}
}

// TestWatchLogPrefixReportsCallerLength: a short count makes io.Copy and
// fmt.Fprintf treat the write as failed, so the prefix must not be counted.
func TestWatchLogPrefixReportsCallerLength(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := newWatchLogPrefixWriter(&buf, "repo", fixedClock(time.Unix(0, 0).UTC()))
	payload := []byte("[watch] line\n")
	n, err := w.Write(payload)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Write returned %d, want caller length %d", n, len(payload))
	}
	if _, err := fmt.Fprintf(w, "[watch] %s\n", "second"); err != nil {
		t.Fatalf("Fprintf saw a short write: %v", err)
	}
}

// TestWatchLogPrefixNoBarePrefixOnTrailingNewline: a newline-terminated write
// must not emit a stamp with nothing after it.
func TestWatchLogPrefixNoBarePrefixOnTrailingNewline(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := newWatchLogPrefixWriter(&buf, "repo", fixedClock(time.Unix(0, 0).UTC()))
	fmt.Fprint(w, "[watch] only line\n")
	if got, want := buf.String(), "1970-01-01T00:00:00Z repo [watch] only line\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestWatchLogPrefixDoesNotSplitAPartialLine: a write that ends mid-line is
// continued, not re-stamped in its middle.
func TestWatchLogPrefixDoesNotSplitAPartialLine(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := newWatchLogPrefixWriter(&buf, "repo", fixedClock(time.Unix(0, 0).UTC()))
	fmt.Fprint(w, "[watch] half")
	fmt.Fprint(w, " and half\n")
	fmt.Fprint(w, "[watch] next\n")
	want := "1970-01-01T00:00:00Z repo [watch] half and half\n" +
		"1970-01-01T00:00:00Z repo [watch] next\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestWatchLogPrefixOmitsEmptyRepo: workspace-level lines belong to no repo
// and must carry the clock alone, with no double space where a key would be.
func TestWatchLogPrefixOmitsEmptyRepo(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := newWatchLogPrefixWriter(&buf, "", fixedClock(time.Unix(0, 0).UTC()))
	fmt.Fprint(w, "[watch] workspace ws — 3 repos\n")
	if got, want := buf.String(), "1970-01-01T00:00:00Z [watch] workspace ws — 3 repos\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestWatchLogPrefixAdvancesTheClock: the stamp is taken per write, so two
// writes a minute apart are distinguishable in the log.
func TestWatchLogPrefixAdvancesTheClock(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	cur := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	w := newWatchLogPrefixWriter(&buf, "repo", func() time.Time {
		at := cur
		cur = cur.Add(time.Minute)
		return at
	})
	fmt.Fprint(w, "[watch] first\n")
	fmt.Fprint(w, "[watch] second\n")
	want := "2026-10-24T09:00:00Z repo [watch] first\n" +
		"2026-10-24T09:01:00Z repo [watch] second\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestWatchLoopStampsTickLines is the single-repo half of COR-2176, driven
// through the real loop rather than the writer: watchLoop, not the tick,
// decides attribution, so a tick added later cannot write an unattributed
// line. The previous attempt at this fix stamped only the banner printed
// above the loop, which is exactly the line that was never the problem.
func TestWatchLoopStampsTickLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cursor := filepath.Join(dir, "watch.json")
	at := time.Date(2026, 10, 24, 9, 30, 15, 0, time.UTC)
	var refreshed, seeded, distilled int
	w := defaultWatchOptions()
	w.once = true
	var buf bytes.Buffer
	if err := watchLoop(context.Background(), &buf, "gh/entireio/entire-brain", fixedClock(at),
		w, cursor, fakeWatchSteps("ck1:head1", &refreshed, &seeded, &distilled)); err != nil {
		t.Fatalf("watchLoop: %v", err)
	}
	out := buf.String()
	if strings.TrimSpace(out) == "" {
		t.Fatalf("tick wrote nothing; the guard would pass vacuously")
	}
	want := "2026-10-24T09:30:15Z gh/entireio/entire-brain "
	for i, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if !strings.HasPrefix(line, want) {
			t.Fatalf("tick line %d is unattributed: %q\nfull log:\n%s", i, line, out)
		}
	}
}
