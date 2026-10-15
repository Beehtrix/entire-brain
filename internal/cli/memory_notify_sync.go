package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

// memoryNotifyHostedSyncMinInterval throttles the checkpoint-hint hosted fact
// sync across detached notify processes: a commit burst (a rebase, a scripted
// series) must not become one sync per commit. A minute keeps the trail fresh
// on the commit beat; the daemon tick remains the reconciliation floor.
const memoryNotifyHostedSyncMinInterval = time.Minute

// memoryNotifySyncMarkerName is the brain-relative marker recording the last
// checkpoint-hint sync. Shared by every notify process; read and claimed under
// the brain write lock so concurrent hints race safely.
const memoryNotifySyncMarkerName = "hosted_sync_notify.json"

// notifyHostedFactsSync is a test seam over hostedFactsSyncAndPull.
var notifyHostedFactsSync = hostedFactsSyncAndPull

// notifyCheckpointExport refreshes the brain's exported transcripts (changed
// sessions only) so distill sees the checkpoint that raised this hint. Test
// seam over the same short-term delta the session-end hook runs.
var notifyCheckpointExport = func(ctx context.Context, errW io.Writer, opts Options, repoDir string) error {
	sub := &cobra.Command{}
	sub.SetContext(ctx)
	sub.SetOut(errW)
	sub.SetErr(errW)
	_, err := watchShortTermDelta(ctx, sub, opts, repoDir)
	return err
}

// notifyCheckpointDistill distills the hinted session. The chunk cache makes
// this cheap per commit: chunking is prefix-stable, so only the transcript
// tail added since the previous checkpoint dispatches an agent call, and its
// facts anchor to the checkpoint that raised the hint. Test seam.
var notifyCheckpointDistill = func(ctx context.Context, errW io.Writer, opts Options, repoDir, sessionID string) error {
	sub := &cobra.Command{}
	sub.SetContext(ctx)
	sub.SetOut(errW)
	sub.SetErr(errW)
	distillOpts := distillCommandOptions{
		session:             sessionID,
		agent:               "auto",
		timeout:             defaultDistillTimeout,
		maxChunkBytes:       defaultDistillChunkSize,
		confidenceThreshold: defaultFactConfidenceThreshold,
		concurrency:         defaultDistillConcurrency,
	}
	return runDistill(ctx, sub, opts, distillOpts, repoDir)
}

type memoryNotifySyncMarker struct {
	LastSyncAt time.Time `json:"last_sync_at"`
}

// hostedSyncOnCheckpointHint runs the checkpoint-hint work: export the changed
// transcripts, distill the hinted session (tail chunks only, via the chunk
// cache), then sync the hinted branch and the default branch — throttled by a
// shared marker. The marker is claimed BEFORE the work so concurrent notify
// processes skip instead of stampeding; a failed run after the claim simply
// waits out the interval, which the never-fails contract already tolerates.
//
// Same contract as hostedFactsSyncAndPull: this runs behind a host lifecycle
// hint, so nothing here may fail the caller — every outcome is reported on
// errW and swallowed. The return value says whether the work ran, for tests
// and for the caller's log line only.
func hostedSyncOnCheckpointHint(ctx context.Context, errW io.Writer, opts Options, repoDir string, storage repoStorage, sessionID, branch string) bool {
	claimed := false
	if err := withBrainWriteLock(storage.BrainDir, func() error {
		now := opts.Now().UTC()
		marker := readMemoryNotifySyncMarker(storage.BrainDir)
		if now.Sub(marker.LastSyncAt) < memoryNotifyHostedSyncMinInterval {
			return nil
		}
		if err := writeMemoryNotifySyncMarker(storage.BrainDir, memoryNotifySyncMarker{LastSyncAt: now}); err != nil {
			return err
		}
		claimed = true
		return nil
	}); err != nil {
		fmt.Fprintf(errW, "memory notify: hosted sync throttle: %v\n", err)
		return false
	}
	if !claimed {
		return false
	}

	// Export before distill: distill reads the exported transcript, and the
	// checkpoint that raised this hint is exactly what the delta brings in.
	// Both steps are best-effort; the sync still runs so facts already in the
	// store reach the trail even when the agent is unavailable.
	if err := notifyCheckpointExport(ctx, errW, opts, repoDir); err != nil {
		fmt.Fprintf(errW, "memory notify: incremental export failed: %v\n", err)
	}
	if sessionID != "" {
		if err := notifyCheckpointDistill(ctx, errW, opts, repoDir, sessionID); err != nil {
			fmt.Fprintf(errW, "memory notify: distill skipped: %v\n", err)
		}
	}

	if branch == "" {
		branch = hostedSyncBranch(ctx, opts.Runner, repoDir)
	}
	syncBranches := []string{branch}
	if branch != distillDefaultBranch {
		syncBranches = append(syncBranches, distillDefaultBranch)
	}
	for _, syncBranch := range syncBranches {
		if err := notifyHostedFactsSync(ctx, errW, opts, repoDir, storage, syncBranch); err != nil {
			fmt.Fprintf(errW, "memory notify: hosted fact sync failed (local brain unaffected): %v\n", err)
		}
	}
	return true
}

func memoryNotifySyncMarkerPath(brainDir string) string {
	return filepath.Join(brainDir, memoryNotifySyncMarkerName)
}

// readMemoryNotifySyncMarker treats a missing or unreadable marker as "never
// synced": the worst outcome of a corrupt marker is one extra sync.
func readMemoryNotifySyncMarker(brainDir string) memoryNotifySyncMarker {
	var marker memoryNotifySyncMarker
	data, err := os.ReadFile(memoryNotifySyncMarkerPath(brainDir))
	if err != nil {
		return memoryNotifySyncMarker{}
	}
	if err := json.Unmarshal(data, &marker); err != nil {
		return memoryNotifySyncMarker{}
	}
	return marker
}

func writeMemoryNotifySyncMarker(brainDir string, marker memoryNotifySyncMarker) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return os.WriteFile(memoryNotifySyncMarkerPath(brainDir), data, 0o600)
}
