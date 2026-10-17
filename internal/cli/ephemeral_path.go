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
// command fails with something that reads like a server problem. The
// documented symptom for the MCP surface is CONNECTION_CLOSED, which sends
// people looking at the server rather than at a path that no longer exists.
//
// Observed on a real machine: the installed plugin resolved, through a
// symlink, into a session scratch directory. It ran fine, and was one cache
// sweep away from taking the whole brain with it.

// ephemeralPathRoots are directories an OS is entitled to empty without
// warning. Matching is on the RESOLVED path, because the hazard is where the
// bytes live, not where the link sits.
func ephemeralPathRoots() []string {
	roots := []string{"/tmp", "/var/tmp", "/private/tmp", "/private/var/tmp", "/dev/shm"}
	if runtime.GOOS == "darwin" {
		// macOS per-user temp lives under /var/folders and is reaped on a
		// schedule most people never see.
		roots = append(roots, "/var/folders", "/private/var/folders")
	}
	if tmp := strings.TrimSpace(os.Getenv("TMPDIR")); tmp != "" {
		roots = append(roots, filepath.Clean(tmp))
	}
	return roots
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
		// A path that cannot be resolved is a different problem, reported
		// elsewhere. Guessing here would turn a missing file into a scary and
		// probably wrong "ephemeral" verdict.
		return false, ""
	}
	resolved = filepath.Clean(resolved)
	for _, root := range ephemeralPathRoots() {
		root = filepath.Clean(root)
		if root == "" || root == string(filepath.Separator) {
			continue
		}
		if resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator)) {
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
	if !ephemeral {
		add("install_durability", "ok", "runs from "+resolved)
		return
	}
	add("install_durability", "warn", ephemeralInstallWarning(exe, resolved))
}
