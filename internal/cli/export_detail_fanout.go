package cli

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"time"
)

// checkpointDetailSlowRead is the duration at which one `checkpoint explain`
// stops looking like a read and starts looking like the host CLI's remote
// metadata fetch timing out: a warm read takes about a second, and the
// pathological read observed in entireio/cli#2902 takes exactly the host's
// 120s fetch deadline, burns it per checkpoint, and succeeds degraded — so a
// 319-checkpoint export ran 2.7 hours with no error anywhere. 60s sits far
// above any healthy read and safely below that deadline.
const checkpointDetailSlowRead = 60 * time.Second

// checkpointDetailSlowTrip is how many CONSECUTIVE slow reads indict the host
// rather than one oversized checkpoint. Three keeps the worst pre-trip cost to
// a few minutes even at the full fan-out width.
const checkpointDetailSlowTrip = 3

// errCheckpointDetailBreakerOpen marks a detail read that was skipped, not
// attempted. The selection pass folds every skip into one counted warning; it
// must never surface as a per-checkpoint "unreadable" line, which would read
// as hundreds of distinct failures instead of one slow host.
var errCheckpointDetailBreakerOpen = errors.New("routed checkpoint detail reads stopped: consecutive reads each ran as long as a host metadata-fetch timeout")

// checkpointDetailBreaker stops asking a host that has shown it will spend a
// full remote-fetch timeout on every single `checkpoint explain`. Without it,
// per-checkpoint reads multiply one slow fetch into hours of silent stalling
// (entireio/cli#2902); with it, the export degrades after a few minutes and
// says so once. Reads that merely fail fast never trip it — failure is the
// selection pass's business, slowness is this breaker's.
type checkpointDetailBreaker struct {
	// now is a seam for tests, which model a 120s read by advancing a fake
	// clock instead of sleeping through five of them.
	now  func() time.Time
	mu   sync.Mutex
	slow int
	open bool
}

func newCheckpointDetailBreaker() *checkpointDetailBreaker {
	return &checkpointDetailBreaker{now: time.Now}
}

// opened reports whether the breaker has tripped, so the export's refusal can
// name the slow host instead of telling the reader to repair checkpoints that
// are not broken.
func (b *checkpointDetailBreaker) opened() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}

// do runs one detail read through the breaker. A nil breaker passes reads
// through untouched, so call sites need no guard.
func (b *checkpointDetailBreaker) do(fetch func() (checkpointExportEnvelope, error)) (checkpointExportEnvelope, error) {
	if b == nil {
		return fetch()
	}
	b.mu.Lock()
	open := b.open
	b.mu.Unlock()
	if open {
		return checkpointExportEnvelope{}, errCheckpointDetailBreakerOpen
	}
	start := b.now()
	detail, err := fetch()
	elapsed := b.now().Sub(start)
	b.mu.Lock()
	if elapsed >= checkpointDetailSlowRead {
		b.slow++
		if b.slow >= checkpointDetailSlowTrip {
			b.open = true
		}
	} else {
		b.slow = 0
	}
	b.mu.Unlock()
	return detail, err
}

// checkpointDetailFanoutLimit is the measured knee for `entire checkpoint
// explain`, which spends its time reading git objects and parsing transcripts
// rather than on process start-up: a child starts in 22ms and the read itself
// takes well over a second. Sixteen checkpoints of a real repository, warm,
// on an 18-core host, across two host-CLI builds:
//
//	children   1      4      8      16
//	build A    25.1s  13.6s  12.6s  13.9s
//	build B    23.7s  13.4s  14.4s  15.1s
//
// Four and eight are within a second of each other and sixteen is worse in
// both, so the knee is four: past it the children contend for the same disk
// and cores and buy nothing, and a smaller fan-out leaves headroom on a host
// that is already busy. Half the machine's cores keeps a small host from being
// starved by an export at all.
func checkpointDetailFanoutLimit() int {
	limit := runtime.NumCPU() / 2
	if limit > 4 {
		limit = 4
	}
	if limit < 1 {
		limit = 1
	}
	return limit
}

// checkpointDetailResult is one prefetched `checkpoint explain`, kept beside
// the position it was requested from so the selection pass can consume the
// results in list order no matter which child finished first.
//
// fetched is what separates a read that found nothing from a position no
// worker reached: a cancelled context stops the queue mid-list, and without
// the flag those untouched entries would read back as successful empty details
// and drop their checkpoints without a warning. A position left unfetched is
// refetched inline by the selection pass, which is also how its error reaches
// the warnings.
type checkpointDetailResult struct {
	detail  checkpointExportEnvelope
	err     error
	fetched bool
}

// prefetchCheckpointDetails reads the routed detail metadata for every
// checkpoint the selection pass will ask about, several children at a time.
//
// It returns nil when the fan-out does not apply — a runner that has not
// claimed ConcurrentCommandRunner, or too little work to be worth it — and the
// caller then falls back to fetching each detail inline. The returned slice is
// indexed by position in checkpoints; entries the selection pass skips before
// it would have asked (an uncommitted list entry, or one with no checkpoint
// ID) are never fetched, so a reader can index it without consulting the skip
// rules a second time.
//
// Determinism is structural rather than argued: nothing here interprets a
// detail. The fan-out only moves the reads, and every decision taken from them
// — the warnings, the counters, which session wins a collision — still happens
// in one sequential pass over the list.
func prefetchCheckpointDetails(ctx context.Context, runner CommandRunner, repoDir, entireBinary string, checkpoints []checkpointListEntry, local *localCheckpointMetadata, progress func(exportProgress), breaker *checkpointDetailBreaker) []checkpointDetailResult {
	concurrent, ok := runner.(ConcurrentCommandRunner)
	if !ok || !concurrent.ConcurrentRunAllowed() {
		return nil
	}
	limit := checkpointDetailFanoutLimit()
	if limit < 2 {
		return nil
	}

	wanted := make([]int, 0, len(checkpoints))
	for i, checkpoint := range checkpoints {
		if !checkpoint.IsLogsOnly || checkpoint.CheckpointID == "" {
			continue
		}
		// A checkpoint the local store already accounts for is never asked
		// about, so prefetching it would spend a child on an answer nobody
		// reads — the whole saving this shortcut exists for.
		if local.covers(checkpoint.CheckpointID) {
			continue
		}
		wanted = append(wanted, i)
	}
	if len(wanted) < 2 {
		return nil
	}
	if limit > len(wanted) {
		limit = len(wanted)
	}

	results := make([]checkpointDetailResult, len(checkpoints))
	positions := make(chan int)
	var done sync.WaitGroup
	var reporting sync.Mutex
	completed := 0

	for worker := 0; worker < limit; worker++ {
		done.Add(1)
		go func() {
			defer done.Done()
			for i := range positions {
				detail, err := breaker.do(func() (checkpointExportEnvelope, error) {
					return checkpointDetail(ctx, runner, repoDir, entireBinary, checkpoints[i].CheckpointID)
				})
				// Each worker owns its own index, so the slice needs no lock.
				results[i] = checkpointDetailResult{detail: detail, err: err, fetched: true}

				// Held across the report as well as the count: the progress
				// sink is a renderer that never expected two callers, and the
				// count is monotonic even though completion order is not,
				// which is all a progress reader can use from a fan-out.
				reporting.Lock()
				completed++
				reportExportProgress(progress, completed, len(checkpoints), 0)
				reporting.Unlock()
			}
		}()
	}

	for _, i := range wanted {
		select {
		case <-ctx.Done():
			// Stop queueing. The positions still unsent stay unfetched, and
			// the selection pass refetches them against the same cancelled
			// context, which is how a sequential pass would have failed them.
			close(positions)
			done.Wait()
			return results
		case positions <- i:
		}
	}
	close(positions)
	done.Wait()
	return results
}
