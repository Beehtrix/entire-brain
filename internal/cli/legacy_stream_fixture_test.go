package cli

// Legacy artifact helpers are compiled only for migration and compatibility
// fixtures. Production Brain never builds or copies a code graph.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"
)

// streamSemanticSnapshot invokes the semantic provider and streams its NDJSON
// stdout through scanSemanticStream into out. stdout is never buffered in full;
// stderr is captured separately into a bounded buffer. Cancellation flows
// through ctx, with a configurable overall deadline and an inactivity timeout so
// large repositories are not killed by a fixed short timeout while a hung
// provider still aborts.
func streamSemanticSnapshot(ctx context.Context, runner CommandRunner, repoDir, repoKey string, indexOpts semanticIndexOptions, ignoreFiles []string, ignore brainIgnore, out io.Writer) (semanticStreamResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	args := []string{"graph", "snapshot", "--repo", repoDir, "--format", "ndjson", "--no-network"}
	for _, path := range ignoreFiles {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		args = append(args, "--ignore-file", path)
	}
	if indexOpts.worktree {
		args = append(args, "--worktree")
	}
	if profile := strings.TrimSpace(indexOpts.profile); profile != "" {
		args = append(args, "--profile", profile)
	}

	overall := indexOpts.timeout
	if overall <= 0 {
		overall = semanticSnapshotTimeout
	}
	inactivity := indexOpts.inactivityTimeout
	if inactivity <= 0 {
		inactivity = semanticSnapshotInactivityTimeout
	}

	runCtx, cancel := context.WithTimeout(ctx, overall)
	defer cancel()

	var inactivityFired atomic.Bool
	activity := make(chan struct{}, 1)

	cfg := semanticStreamScanConfig{
		ignore:   ignore,
		repoDir:  repoDir,
		repoKey:  repoKey,
		commit:   indexOpts.localCommit,
		tree:     indexOpts.localTree,
		progress: indexOpts.progress,
		counts:   indexOpts.progressCounts,
		onRecord: func(string) {
			select {
			case activity <- struct{}{}:
			default:
			}
		},
	}

	// Semantic indexing requires a streaming runner so the production path is
	// always memory-bounded: stdout is consumed record-by-record and never
	// buffered in full. The production ExecRunner and the test fake both
	// implement CommandStreamer; a runner without it is a programming error.
	streamer, ok := runner.(CommandStreamer)
	if !ok {
		return semanticStreamResult{}, errors.New("semantic provider snapshot requires a streaming command runner (CommandStreamer)")
	}

	if inactivity > 0 {
		go watchSemanticInactivity(runCtx, inactivity, activity, cancel, &inactivityFired)
	}

	stream, err := streamer.Stream(runCtx, repoDir, indexOpts.graphBinary, args...)
	if err != nil {
		return semanticStreamResult{}, fmt.Errorf("semantic provider snapshot failed: %w", err)
	}
	defer stream.Close()

	res, scanErr := scanSemanticStream(stream.Stdout(), out, cfg)
	if scanErr != nil {
		// Drain remaining stdout so Wait does not block on a full pipe.
		_, _ = io.Copy(io.Discard, stream.Stdout())
	}
	stderr, waitErr := stream.Wait()

	if cerr := semanticStreamContextError(runCtx, ctx, overall, inactivity, &inactivityFired); cerr != nil {
		return res, cerr
	}
	if waitErr != nil {
		return res, providerSnapshotWaitError(waitErr, stderr, nil)
	}
	if scanErr != nil {
		return res, scanErr
	}
	if !res.haveHeader {
		return res, errors.New("semantic provider snapshot produced no output")
	}
	if truncErr := semanticSummaryTruncationCheck(&res); truncErr != nil {
		return res, truncErr
	}
	return res, nil
}

// semanticStreamContextError maps a cancelled run context to a descriptive
// error, distinguishing inactivity, the overall deadline, and external
// cancellation. It returns nil when the run context was not cancelled.
func semanticStreamContextError(runCtx, parent context.Context, overall, inactivity time.Duration, inactivityFired *atomic.Bool) error {
	if parent.Err() != nil {
		return fmt.Errorf("semantic provider snapshot canceled: %w", parent.Err())
	}
	if runCtx.Err() == nil {
		return nil
	}
	if inactivityFired.Load() {
		return fmt.Errorf("semantic provider snapshot stalled: no output for %s", inactivity)
	}
	return fmt.Errorf("semantic provider snapshot timed out after %s", overall)
}

func providerSnapshotWaitError(err error, stderr, stdout []byte) error {
	detail := strings.TrimSpace(string(stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(stdout))
	}
	if detail != "" {
		return fmt.Errorf("semantic provider snapshot failed: %w: %s", err, truncateAgentWarning(detail))
	}
	return fmt.Errorf("semantic provider snapshot failed: %w", err)
}

func watchSemanticInactivity(ctx context.Context, d time.Duration, activity <-chan struct{}, cancel context.CancelFunc, fired *atomic.Bool) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(d)
		case <-timer.C:
			fired.Store(true)
			cancel()
			return
		}
	}
}
