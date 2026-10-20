package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
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
// update") until the agent is re-registered. `launchctl bootout` + `bootstrap`
// is the sequence verified to clear the recorded constraint (COR-2174). It has
// to run from a detached helper because bootout kills this very process.
//
// On linux a plain exit suffices — systemd's Restart=always relaunches the
// current binary with no signing pin — so everything but darwin is a no-op.
// Failures only cost the self-restart: the daemon still exits, launchd still
// attempts a respawn, and `setup` re-registers on its next run.
func relaunchInstalledDaemon(out io.Writer, env EntireEnv) {
	if runtime.GOOS != "darwin" || daemonRegistrationDisabled() {
		return
	}
	plan, err := installedDaemonUnitPlan(env)
	if err != nil {
		fmt.Fprintf(out, "[watch] launchd re-registration skipped: %v\n", err)
		return
	}
	if _, err := os.Stat(plan.UnitPath); err != nil {
		fmt.Fprintf(out, "[watch] launchd re-registration skipped: %v\n", err)
		return
	}
	script := `launchctl bootout "$1" "$2"; launchctl enable "$1/$3"; launchctl bootstrap "$1" "$2"`
	if _, err := spawnDetached(setupBackfillPlan{
		Binary: "/bin/sh",
		Args:   []string{"-c", script, "sh", launchdGUIDomain(), plan.UnitPath, plan.Label},
	}); err != nil {
		fmt.Fprintf(out, "[watch] launchd re-registration failed (re-run `entire-brain setup` to restart the watcher): %v\n", err)
		return
	}
	fmt.Fprintln(out, "[watch] re-registering with launchd so the restart lands on the current binary")
}

// installedDaemonUnitPlan resolves the unit the machine's supervised watcher is
// registered under, by the daemon name the machine watch plan records. Only
// Label and UnitPath are meaningful on the result; the spec is a stub.
func installedDaemonUnitPlan(env EntireEnv) (daemonPlan, error) {
	watchPlan, err := loadSetupWatchPlan(env)
	if err != nil {
		return daemonPlan{}, err
	}
	binary, err := os.Executable()
	if err != nil {
		return daemonPlan{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return daemonPlan{}, err
	}
	return planBrainWatchDaemon(runtime.GOOS, home, os.Getenv(xdgConfigHome), os.Getenv(envDaemonUnitDir),
		daemonSpec{Name: watchPlan.DaemonName, Binary: binary})
}
