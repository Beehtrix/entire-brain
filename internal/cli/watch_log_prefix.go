package cli

import (
	"bytes"
	"io"
	"time"
)

// watchLogPrefixWriter stamps every watch line with the time and the repo it
// is about.
//
// watch.log is the only record of what a background daemon did, and it was
// written as bare `[watch] ...` lines: no clock, and on a workspace fan-out no
// way to tell which repo a line belonged to. Diagnosing COR-2175 meant
// correlating "reconciliation queued" with a "refresh failed" line further
// down and GUESSING they were the same tick on the same repo — which is only
// possible at all because one repo dominated that machine's log.
//
// The prefix is applied at the writer, not at the 26 call sites, so a line
// added later cannot forget it.
type watchLogPrefixWriter struct {
	out  io.Writer
	repo string
	now  func() time.Time
}

func newWatchLogPrefixWriter(out io.Writer, repo string, now func() time.Time) io.Writer {
	if now == nil {
		now = time.Now
	}
	return &watchLogPrefixWriter{out: out, repo: repo, now: now}
}

func (w *watchLogPrefixWriter) Write(p []byte) (int, error) {
	// Split on newlines so a multi-line write gets one stamp per line rather
	// than one for the batch. The trailing empty element of a newline-
	// terminated write is skipped, so no bare prefix is emitted.
	lines := bytes.Split(p, []byte("\n"))
	var buf bytes.Buffer
	stamp := w.now().UTC().Format(time.RFC3339)
	for i, line := range lines {
		if i == len(lines)-1 && len(line) == 0 {
			buf.WriteByte('\n')
			break
		}
		if i > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(stamp)
		if w.repo != "" {
			buf.WriteString(" " + w.repo)
		}
		buf.WriteByte(' ')
		buf.Write(line)
	}
	if _, err := w.out.Write(buf.Bytes()); err != nil {
		return 0, err
	}
	// Report the caller's length: the prefix is ours, not theirs, and a short
	// count makes io.Copy and friends think the write failed.
	return len(p), nil
}
