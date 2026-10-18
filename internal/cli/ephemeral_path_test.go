package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The failure this exists for: a durable-LOOKING symlink pointing into a temp
// directory. The link is what people see, so the check has to follow it.
//
// The link MUST live somewhere genuinely durable. An earlier version of this
// test put it in t.TempDir(), which is itself under TMPDIR -- so the test
// passed whether or not symlinks were followed, and proved nothing. The
// weakening check below is what exposed that.
func TestSymlinkIntoTempIsEphemeral(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	durableDir, err := os.MkdirTemp(home, ".entire-ephemeral-test-")
	if err != nil {
		t.Skipf("cannot create a durable directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(durableDir) })

	// Guard the fixture itself: if this directory is somehow ephemeral, the
	// assertion below would pass for the wrong reason.
	if ephemeral, resolved := pathIsEphemeral(durableDir); ephemeral {
		t.Skipf("fixture directory is itself ephemeral (%s); nothing to prove", resolved)
	}

	tmp := t.TempDir() // reapable
	target := filepath.Join(tmp, "entire-brain")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(durableDir, "entire-brain")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ephemeral, resolved := pathIsEphemeral(link)
	if !ephemeral {
		t.Fatalf("a durable link into %s must be reported ephemeral; resolved=%s", tmp, resolved)
	}

	msg := ephemeralInstallWarning(link, resolved)
	if !strings.Contains(msg, resolved) {
		t.Errorf("the warning must name the RESOLVED path, the part nobody sees: %s", msg)
	}
	if !strings.Contains(msg, filepath.Clean(link)) {
		t.Errorf("the warning must also name the link the reader recognises: %s", msg)
	}
	if !strings.Contains(msg, "reinstall") {
		t.Errorf("the warning must say what to do: %s", msg)
	}
}

// A normal install must not warn, or the check trains people to ignore doctor.
func TestDurablePathIsNotEphemeral(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	durable := filepath.Join(home, ".local", "share", "entire", "plugins", "bin")
	if err := os.MkdirAll(durable, 0o755); err != nil {
		t.Skipf("cannot create a durable path to test with: %v", err)
	}
	f := filepath.Join(durable, "ephemeral-path-probe")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Skipf("cannot write a durable probe: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(f) })

	if ephemeral, resolved := pathIsEphemeral(f); ephemeral {
		t.Fatalf("a path under the user's home must not be ephemeral: %s", resolved)
	}
}

// Every root claimed as ephemeral must actually be MATCHED by the predicate.
// The previous version of this test looped over the roots and asserted
// nothing, which is the shape that passes forever while the list rots.
func TestEveryEphemeralRootIsActuallyMatched(t *testing.T) {
	t.Parallel()
	checked := 0
	for _, root := range ephemeralPathRoots() {
		if _, err := os.Stat(root); err != nil {
			continue // not present on this machine
		}
		probe, err := os.CreateTemp(root, "ephemeral-probe-")
		if err != nil {
			continue // not writable here; nothing to prove
		}
		name := probe.Name()
		_ = probe.Close()
		t.Cleanup(func() { _ = os.Remove(name) })

		ephemeral, resolved := pathIsEphemeral(name)
		if !ephemeral {
			t.Errorf("root %q is listed as ephemeral but a file inside it was not flagged (resolved=%s)", root, resolved)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no ephemeral root was writable, so this test proved nothing")
	}
}

// An unresolvable path is a different problem and must not be reported as
// ephemeral: a missing file turned into a scary, probably wrong verdict is
// worse than no verdict.
func TestMissingPathIsNotCalledEphemeral(t *testing.T) {
	t.Parallel()
	if ephemeral, _ := pathIsEphemeral(filepath.Join(t.TempDir(), "does-not-exist")); ephemeral {
		t.Fatal("a path that cannot be resolved must not be reported ephemeral")
	}
	if ephemeral, _ := pathIsEphemeral(""); ephemeral {
		t.Fatal("an empty path must not be reported ephemeral")
	}
}

// macOS reaps /var/folders on a schedule nobody watches, which is exactly
// where this bug was found.
func TestMacOSPerUserTempIsEphemeral(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("macOS-specific temp root")
	}
	found := false
	for _, root := range ephemeralPathRoots() {
		if strings.Contains(root, "/var/folders") {
			found = true
		}
	}
	if !found {
		t.Fatal("/var/folders must be treated as ephemeral on macOS")
	}
}

// A TMPDIR that is itself a symlink must still match. It was appended
// Clean()'d but not resolved, while matching compares against an
// EvalSymlinks'd path — so the root could never fire.
//
// The real directory MUST live somewhere no other root covers. The first
// version of this test put it under os.MkdirTemp(""), which lands in
// /var/folders and is matched by a hardcoded root, so it stayed green with
// the fix removed and proved nothing. That is exactly the masking the fix is
// about.
func TestSymlinkedTMPDIRStillMatches(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	realTmp, err := os.MkdirTemp(home, ".entire-tmpdir-test-")
	if err != nil {
		t.Skipf("cannot create a directory outside the known roots: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(realTmp) })

	probe := filepath.Join(realTmp, "binary")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Guard the fixture: with TMPDIR unset this location must NOT be
	// ephemeral, or the assertion below could pass via some other root.
	t.Setenv("TMPDIR", "")
	if ephemeral, resolved := pathIsEphemeral(probe); ephemeral {
		t.Skipf("fixture location is already covered by another root (%s)", resolved)
	}

	linkDir := filepath.Join(t.TempDir(), "link-to-tmp")
	if err := os.Symlink(realTmp, linkDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Point TMPDIR at the LINK, as a shell exporting a symlinked path does.
	t.Setenv("TMPDIR", linkDir)

	if ephemeral, resolved := pathIsEphemeral(probe); !ephemeral {
		t.Fatalf("a file under a symlinked TMPDIR must be flagged; resolved=%s", resolved)
	}
}

// An executable whose own path does not resolve is the live form of this
// disaster: the directory holding it has already been reaped. Reporting "ok"
// with an empty path is the worst available answer.
func TestUnresolvableInstallWarnsRatherThanReportingOK(t *testing.T) {
	t.Parallel()
	var name, state, detail string
	gone := filepath.Join(t.TempDir(), "already-reaped", "entire-brain")

	// Drive the same branch checkEphemeralInstall takes for an unresolvable
	// executable.
	ephemeral, resolved := pathIsEphemeral(gone)
	if ephemeral {
		t.Fatalf("a missing path must not be called ephemeral; resolved=%s", resolved)
	}
	if resolved != "" {
		t.Fatalf("an unresolvable path must report no resolved location, got %q", resolved)
	}
	add := func(n, s, d string) { name, state, detail = n, s, d }
	switch {
	case ephemeral:
		add("install_durability", "warn", ephemeralInstallWarning(gone, resolved))
	case resolved == "":
		add("install_durability", "warn", "this build's own path does not resolve ("+gone+")")
	default:
		add("install_durability", "ok", "runs from "+resolved)
	}
	if name != "install_durability" || state != "warn" {
		t.Fatalf("want a warn for an unresolvable install, got %s/%s", name, state)
	}
	if !strings.Contains(detail, gone) {
		t.Fatalf("the warning must name the path it could not resolve: %s", detail)
	}
}
