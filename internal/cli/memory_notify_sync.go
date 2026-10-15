package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// memoryCheckpointWorkLaunch spawns `memory checkpoint-work` detached, the same
// shape as memoryWorkerLaunch: `memory notify` is a fast content-free hint and
// must never carry the upload's network round trips inline — a host adapter
// may invoke it synchronously. The child runs with the worker-origin
// environment so nothing it shells out to re-notifies. Injectable for tests;
// a launch failure never fails the host.
var memoryCheckpointWorkLaunch = func(repoDir, branch string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"memory", "checkpoint-work"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	command := exec.Command(exe, args...)
	command.Dir = repoDir
	command.Env = memoryWorkerChildEnvironment(os.Environ(), repoDir)
	command.Stdout = nil
	command.Stderr = nil
	if err := command.Start(); err != nil {
		return err
	}
	// Reap like memoryWorkerLaunchAfter: Wait in a goroutine keeps the launch
	// non-blocking without leaving a <defunct> child for the parent's lifetime.
	go func() { _ = command.Wait() }()
	return nil
}

// newMemoryCheckpointWorkCommand is the detached half of the checkpoint hint:
// notify records the hint and spawns this verb; this verb uploads the fact
// store, throttled. Hidden — it is an internal dispatch target, not a user
// surface.
func newMemoryCheckpointWorkCommand(opts Options) *cobra.Command {
	var branch string
	cmd := &cobra.Command{
		Use:    "checkpoint-work",
		Hidden: true,
		Short:  "Upload the fact store for a checkpoint hint (internal)",
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			storage, err := resolveSessionsBrain(cmd.Context(), opts)
			if err != nil {
				return err
			}
			repoDir, local, err := resolveLocalTargetRepoDir(cmd.Context(), opts.Runner, agentSurfaceTarget(opts, nil))
			if err != nil || !local {
				return nil // no local repo: silence, never an error
			}
			hostedSyncOnCheckpointHint(cmd.Context(), cmd.ErrOrStderr(), opts, repoDir, storage, strings.TrimSpace(branch))
			return nil
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "", "Captured branch when the host knows it")
	return cmd
}

type memoryNotifySyncMarker struct {
	LastSyncAt time.Time `json:"last_sync_at"`
}

// hostedSyncOnCheckpointHint uploads the fact store on a checkpoint hint: the
// hinted branch, then the default branch, throttled by a shared marker. It
// deliberately does NOT distill — the hint is an upload beat for facts that
// already exist. Fact creation stays on the session-end hook and the daemon
// tick until it moves into the memory worker with the session-end deprecation.
// The marker is claimed BEFORE the work so concurrent notify processes skip
// instead of stampeding; a failed run after the claim simply waits out the
// interval, which the never-fails contract already tolerates.
//
// Same contract as hostedFactsSyncAndPull: this runs behind a host lifecycle
// hint, so nothing here may fail the caller — every outcome is reported on
// errW and swallowed. The return value says whether the work ran, for tests
// and for the caller's log line only.
func hostedSyncOnCheckpointHint(ctx context.Context, errW io.Writer, opts Options, repoDir string, storage repoStorage, branch string) bool {
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
