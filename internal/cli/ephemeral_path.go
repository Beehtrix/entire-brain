package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ephemeral_path.go answers one question: will this install still exist
// tomorrow?
//
// A plugin binary reached through a symlink into a temp directory works
// perfectly until the directory is reaped, and then every `entire brain`
// command fails with something that reads like a server problem rather than
// a path that no longer exists.
//
// Observed on a real machine: the installed plugin resolved, through a
// symlink, into a session scratch directory. It ran fine, and was one cache
// sweep away from taking the whole brain with it.

// ephemeralPathRoots are directories an OS is entitled to empty without
// warning. Matching is on the RESOLVED path, because the hazard is where the
// bytes live, not where the link sits.
func ephemeralPathRoots() []string {
	return ephemeralPathRootsFor(runtime.GOOS, os.Getenv, os.TempDir())
}

// ephemeralPathRootsFor is ephemeralPathRoots with the platform and the
// environment passed in.
//
// Taking them as arguments is the whole point: the Windows branch is the one
// that was missing, and a test that can only run ON Windows is a test that
// found the gap three CI hours after the commit that caused it. Every branch
// below is now reachable from any machine.
func ephemeralPathRootsFor(goos string, getenv func(string) string, systemTemp string) []string {
	roots := make([]string, 0, 12)
	// add records a candidate in both its resolved and its literal form.
	// Resolved, because the matcher compares against an EvalSymlinks'd path,
	// so a root that is itself a symlink -- the macOS default /var/folders/...
	// reached via /tmp, for one -- could never match and the entry would
	// silently do nothing. Literal too, because a root that does not exist on
	// this machine cannot be resolved and is still worth carrying.
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			roots = append(roots, filepath.Clean(resolved))
		}
		roots = append(roots, filepath.Clean(dir))
	}

	// tempDirEnvVars names the variables this platform actually uses, so a
	// test can set the right one without duplicating the branch below.
	if goos == "windows" {
		// Windows has no /tmp and does not read TMPDIR, so every root this
		// list used to hold was a POSIX path that cannot exist there: the
		// check could not fire at all, and happily reported a plugin living
		// in %TEMP% as durable. Both Disk Cleanup and Storage Sense empty
		// that directory on a schedule, which is the same disaster this file
		// exists to pre-empt, on the platform where nobody was looking.
		add(getenv("TMP"))
		add(getenv("TEMP"))
	} else {
		for _, dir := range []string{"/tmp", "/var/tmp", "/private/tmp", "/private/var/tmp", "/dev/shm"} {
			add(dir)
		}
		if goos == "darwin" {
			// macOS per-user temp lives under /var/folders and is reaped on a
			// schedule most people never see.
			add("/var/folders")
			add("/private/var/folders")
		}
		add(getenv("TMPDIR"))
	}
	// Whatever Go itself would call the temp directory, on every platform.
	// On a stock machine this repeats one of the entries above; on a
	// configured one it is the only entry that is right.
	add(systemTemp)
	return roots
}

// tempDirEnvVar is the environment variable this platform uses to override
// the temp directory. Exported to tests so a fixture sets the one that is
// actually read: TMPDIR on Windows is ignored, and a test that sets it there
// is asserting nothing while looking like it asserts something.
func tempDirEnvVar() string {
	if runtime.GOOS == "windows" {
		return "TEMP"
	}
	return "TMPDIR"
}

// pathsEqualHere compares two cleaned paths the way this filesystem would.
// Windows and macOS are case-insensitive in practice, and a root that fails
// to match because of a capital letter is a check that reports "durable" for
// a binary in a directory the OS is about to empty.
func pathsEqualHere(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// pathHasPrefixHere is pathsEqualHere for the "inside this directory" test.
func pathHasPrefixHere(path, root string) bool {
	prefix := root + string(filepath.Separator)
	if len(path) < len(prefix) {
		return false
	}
	return pathsEqualHere(path[:len(prefix)], prefix)
}

// pathIsEphemeral reports whether path resolves inside a directory the system
// may delete. Symlinks are followed first: the whole failure mode is a durable
// -looking link pointing somewhere temporary.
func pathIsEphemeral(path string) (bool, string) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false, ""
	}
	resolved, err := filepath.EvalSymlinks(trimmed)
	if err != nil {
		// Unresolvable. Reported by checkEphemeralInstall as its own warn
		// rather than folded in here: "this path does not resolve" and "this
		// path is temporary" need different sentences, and for the RUNNING
		// binary the first one usually means the temp directory has already
		// been reaped -- the disaster this check exists to pre-empt.
		return false, ""
	}
	resolved = filepath.Clean(resolved)
	for _, root := range ephemeralPathRoots() {
		root = filepath.Clean(root)
		if root == "" || root == string(filepath.Separator) {
			continue
		}
		if pathsEqualHere(resolved, root) || pathHasPrefixHere(resolved, root) {
			return true, resolved
		}
	}
	return false, resolved
}

// ephemeralInstallWarning returns the operator-facing sentence for a binary
// living somewhere temporary, or "" when the install is durable.
//
// It names the RESOLVED path rather than the link, because the link looks
// fine and is the reason nobody notices.
func ephemeralInstallWarning(invokedAs, resolved string) string {
	if resolved == "" {
		return ""
	}
	msg := "this build runs from " + resolved + ", which the system may delete"
	if invokedAs != "" && filepath.Clean(invokedAs) != resolved {
		msg += " (reached via " + filepath.Clean(invokedAs) + ")"
	}
	return msg + "; reinstall to a durable location, or the next cache sweep removes the plugin and every `brain` command starts failing"
}

// checkEphemeralInstall is the doctor check. It is a warn, never an error: the
// install works right now, and failing a healthy machine would train people to
// ignore doctor.
func checkEphemeralInstall(add func(name, state, detail string)) {
	exe, err := os.Executable()
	if err != nil {
		add("install_durability", "warn", "could not resolve this executable's path: "+err.Error())
		return
	}
	ephemeral, resolved := pathIsEphemeral(exe)
	switch {
	case ephemeral:
		add("install_durability", "warn", ephemeralInstallWarning(exe, resolved))
	case resolved == "":
		// pathIsEphemeral could not resolve the executable. Reporting "ok"
		// with an empty path -- which is what this did -- is exactly wrong
		// for the running binary: the usual cause is that the directory
		// holding it has already been deleted.
		add("install_durability", "warn",
			"this build's own path does not resolve ("+exe+"); if it ran from a temp directory, that directory is already gone -- reinstall to a durable location")
	default:
		add("install_durability", "ok", "runs from "+resolved)
	}
}
