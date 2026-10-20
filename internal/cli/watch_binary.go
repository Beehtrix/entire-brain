package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/spf13/cobra"
)

// errWatchBinaryReplaced ends the supervised loop when the executable this
// daemon was started from has been replaced on disk. A long-running daemon
// otherwise outlives its binary: after an upgrade it keeps running the old
// build, whose writer-path manifest load correctly refuses the new build's
// fields, and every tick fails forever while `status` still says running
// (COR-2174). Exiting hands the restart to the service manager, which spawns
// the current binary.
var errWatchBinaryReplaced = errors.New("binary replaced on disk")

// watchBinaryChanged returns a probe reporting whether this process's
// executable has been replaced since the probe was created, or nil when the
// executable cannot be resolved (then the staleness check is simply off).
func watchBinaryChanged() func() bool {
	path, err := os.Executable()
	if err != nil {
		return nil
	}
	return watchBinaryChangeProbe(path)
}

func watchBinaryChangeProbe(path string) func() bool {
	started, err := os.Stat(path)
	if err != nil {
		return nil
	}
	return func() bool {
		current, err := os.Stat(path)
		if err != nil {
			// Mid-replace or unreadable: decide on a later pass rather than
			// restarting onto a binary that may be half-written.
			return false
		}
		// All three signals carry weight: SameFile catches install-by-rename,
		// ModTime catches the in-place rebuild, Size catches an in-place copy
		// that preserved mtime (cp -p, rsync -t).
		return !os.SameFile(started, current) ||
			!current.ModTime().Equal(started.ModTime()) ||
			current.Size() != started.Size()
	}
}

// relaunchInstalledDaemon re-registers the launchd agent after the supervised
// daemon exits over a replaced binary.
//
// Exiting is not enough on darwin: launchd records the cdhash of an
// ad-hoc-signed service binary at registration, so after the binary is rebuilt
// every KeepAlive respawn fails with OS_REASON_CODESIGNING ("needs LWCR
// update") until the agent is re-registered (COR-2174). The re-registration
// has to run from a detached child because bootout kills this very process —
// and the child is the replaced binary itself running `daemon-reregister`, so
// the verbs, the registration lock, and the unit rendering all stay in
// installDaemon rather than being restated in a shell string. The child's
// output goes to the daemon log, so a failed bootstrap is diagnosable there.
//
// On linux a plain exit suffices — systemd's Restart=always relaunches the
// current binary with no signing pin — so everything but darwin is a no-op.
// Failures only cost the self-restart: the daemon still exits, launchd still
// attempts a respawn, and `setup` re-registers on its next run.
func relaunchInstalledDaemon(out io.Writer, env EntireEnv) {
	if runtime.GOOS != "darwin" || daemonRegistrationDisabled() {
		return
	}
	binary, err := os.Executable()
	if err != nil {
		fmt.Fprintf(out, "[watch] launchd re-registration skipped: %v\n", err)
		return
	}
	fmt.Fprintln(out, "[watch] re-registering with launchd so the restart lands on the current binary")
	logPath, _ := daemonLogPath(env)
	if _, err := spawnDetached(setupBackfillPlan{
		Binary:  binary,
		Args:    []string{"daemon-reregister"},
		LogPath: logPath,
	}); err != nil {
		fmt.Fprintf(out, "[watch] launchd re-registration failed (re-run `entire-brain setup` to restart the watcher): %v\n", err)
	}
}

// newDaemonReregisterCommand is the internal command the exiting daemon spawns
// (detached, on the replaced binary) to re-register the installed watcher.
func newDaemonReregisterCommand(opts Options) *cobra.Command {
	return &cobra.Command{
		Use:   "daemon-reregister",
		Short: "Re-register the installed background watcher with the service manager (internal)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDaemonReregister(cmd.Context(), cmd.OutOrStdout(), opts)
		},
	}
}

// runDaemonReregister reinstalls the machine's one watcher under the same lock
// `setup` uses, after rechecking that the unit still exists. The recheck is
// what keeps this safe against a concurrent `setup --uninstall-daemon`:
// re-registering a unit the user just removed would resurrect a daemon they
// declined, so a missing unit file means there is nothing to restart.
func runDaemonReregister(ctx context.Context, out io.Writer, opts Options) error {
	plan, err := installedDaemonUnitPlan(opts)
	if err != nil {
		return err
	}
	if plan.NoRegister {
		return nil
	}
	return withDaemonRegistrationLock(opts.Env, func() error {
		if _, err := os.Stat(plan.UnitPath); err != nil {
			fmt.Fprintf(out, "daemon-reregister: nothing to restart: %v\n", err)
			return nil
		}
		if err := installDaemon(ctx, opts.Runner, plan); err != nil {
			return fmt.Errorf("daemon-reregister: %w", err)
		}
		fmt.Fprintf(out, "daemon-reregister: re-registered %s\n", plan.Label)
		return nil
	})
}

// installedDaemonUnitPlan resolves the plan for the daemon this machine has
// installed: the one plan builder, under the daemon name the machine watch
// plan records (empty falls back to the default inside the builder).
func installedDaemonUnitPlan(opts Options) (daemonPlan, error) {
	watchPlan, err := loadSetupWatchPlan(opts.Env)
	if err != nil {
		return daemonPlan{}, err
	}
	setupOpts := defaultSetupOptions()
	if watchPlan.DaemonName != "" {
		setupOpts.daemonName = watchPlan.DaemonName
	}
	return brainWatchDaemonPlan(opts, setupOpts)
}
