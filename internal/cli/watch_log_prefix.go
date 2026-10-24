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
// way to tell which repo a line belonged to. Diagnosing the write.lock race
// (COR-2175) meant correlating a "reconciliation queued" line with a "refresh
// failed" line further down and GUESSING they were the same tick on the same
// repo -- which was only possible at all because one repo dominated that
// machine's log.
//
// The prefix is applied at the writer rather than at the ~30 call sites, so a
// line added later cannot forget it, and per line rather than per write, so a
// two-line write does not leave its second line unattributed.
type watchLogPrefixWriter struct {
	out  io.Writer
	repo string
	now  func() time.Time
	// midLine records that the previous write ended without a newline, so the
	// continuation of that line is not stamped a second time in its middle.
	midLine bool
}

// newWatchLogPrefixWriter wraps out so each line gets "<RFC3339> <repo> ".
// repo may be empty for workspace-level lines, which belong to no single repo.
// Wrap a given sink once: nesting two of these stamps every line twice.
func newWatchLogPrefixWriter(out io.Writer, repo string, now func() time.Time) io.Writer {
	if now == nil {
		now = time.Now
	}
	return &watchLogPrefixWriter{out: out, repo: repo, now: now}
}

func (w *watchLogPrefixWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	stamp := w.now().UTC().Format(time.RFC3339)
	var buf bytes.Buffer
	lines := bytes.SplitAfter(p, []byte("\n"))
	for _, line := range lines {
		// SplitAfter yields a trailing empty element for a newline-terminated
		// input; skip it rather than emit a bare prefix.
		if len(line) == 0 {
			continue
		}
		if !w.midLine {
			buf.WriteString(stamp)
			if w.repo != "" {
				buf.WriteByte(' ')
				buf.WriteString(w.repo)
			}
			buf.WriteByte(' ')
		}
		buf.Write(line)
		w.midLine = line[len(line)-1] != '\n'
	}
	if _, err := w.out.Write(buf.Bytes()); err != nil {
		return 0, err
	}
	// Report the caller's length: the prefix is ours, not theirs, and a short
	// count makes io.Copy and friends treat the write as failed.
	return len(p), nil
}
