package cli

import (
	"bytes"
	"strings"
	"testing"
)

// doctor printed 40 lines and 6.4KB on a HEALTHY brain of this repository: 34
// passing findings, six that said anything, and exactly one that named a
// command. That is how a diagnostic teaches people to stop reading it, and
// then the one line that mattered is lost among the thirty-four that did not.
//
// The default report now prints only findings a reader can act on, and every
// one of them ends with a command. The full report is one documented flag
// away, and `--json` -- the machine contract -- is untouched by either.

// healthyDoctorReport is a report with nothing wrong anywhere.
func healthyDoctorReport() doctorReport {
	return doctorReport{
		Env:            map[string]string{"ENTIRE_CLI_VERSION": "1.2.3"},
		RepoRoot:       "/repo",
		RepoRootSource: "cwd",
		Dirs: []doctorCheckResult{
			{Name: "plugin config dir", State: "ok", Detail: "/cfg"},
			{Name: "plugin data dir", State: "ok", Detail: "/data"},
		},
		DirectoryHealth: []memoryInstallDirectoryHealth{{Path: "/cfg"}, {Path: "/data"}},
		Checks: []doctorCheckResult{
			{Name: "manifest", State: "ok", Detail: "/data"},
			{Name: "capture", State: "ok", Detail: "263 exported sessions"},
			{Name: "facts", State: "ok", Detail: "1194 fact(s)"},
			{Name: "history_fts", State: "ok", Detail: "BM25 index fresh"},
		},
	}
}

// TestHealthyDoctorSaysSoInTwoLines is the bar this change was measured
// against: on a healthy brain, doctor says so, and says nothing else.
func TestHealthyDoctorSaysSoInTwoLines(t *testing.T) {
	out := &bytes.Buffer{}
	renderDoctorActionable(out, healthyDoctorReport())

	lines := nonEmptyLines(out.String())
	if len(lines) > 2 {
		t.Fatalf("a healthy brain produced %d lines, want at most 2:\n%s", len(lines), out)
	}
	if !strings.Contains(out.String(), "healthy") {
		t.Fatalf("a healthy brain must be TOLD it is healthy, not left to count passing lines:\n%s", out)
	}
	// The count is kept: "I printed nothing" and "I checked nothing" must not
	// look the same.
	if !strings.Contains(out.String(), "6 check(s) passed") {
		t.Fatalf("the short report dropped the number of checks that ran:\n%s", out)
	}
	if !strings.Contains(out.String(), "`"+doctorVerboseInvocation()+"`") {
		t.Fatalf("the short report must name the flag that restores the long one:\n%s", out)
	}
}

// TestDoctorDefaultReportDropsEveryPassingLine: the saving comes from silence
// about what is fine, and from nothing else.
func TestDoctorDefaultReportDropsEveryPassingLine(t *testing.T) {
	report := healthyDoctorReport()
	report.Checks = append(report.Checks, doctorCheckResult{Name: "semantic", State: "warn", Detail: "branch_tip=stale"})

	out := &bytes.Buffer{}
	renderDoctorActionable(out, report)

	for _, name := range []string{"manifest", "capture", "facts", "history_fts", "plugin config dir", "plugin data dir"} {
		if strings.Contains(out.String(), name+": ok") {
			t.Fatalf("the short report still prints the passing finding %q:\n%s", name, out)
		}
	}
	if !strings.Contains(out.String(), "semantic: warn") {
		t.Fatalf("the short report dropped the one finding that is not ok:\n%s", out)
	}
}

// TestUnhealthyDoctorKeepsEveryActionableLine is the negative control: the
// quiet default must not be quiet about problems. A brain with many findings
// prints many lines -- nothing is capped, collapsed or summarised away.
func TestUnhealthyDoctorKeepsEveryActionableLine(t *testing.T) {
	report := healthyDoctorReport()
	report.Dirs[1] = doctorCheckResult{Name: "plugin data dir", State: "warn", Detail: "/data: mode bits currently deny writes (mode 0o500)"}
	broken := []doctorCheckResult{
		{Name: "facts", State: "error", Detail: "1 fact(s) declared, store missing for: feature"},
		{Name: "manifest", State: "error", Detail: "ERR_UNSUPPORTED_VERSION: upgrade"},
		{Name: "history_index", State: "error", Detail: "declared but unreadable"},
		{Name: "memory_schemas", State: "error", Detail: "2 observed schema health error(s)"},
		{Name: "memory_overlay", State: "warn", Detail: "stale"},
		{Name: "memory_vectors", State: "warn", Detail: "pending"},
		{Name: "conversation", State: "warn", Detail: "no conversation projection"},
		{Name: "semantic", State: "error", Detail: "store=unsafe (file is not a database)"},
		{Name: "memory_reconciliation", State: "warn", Detail: "absent"},
		{Name: "setup semantic index", State: "error", Detail: `unknown command "graph" for "entire"`},
	}
	report.Checks = broken

	out := &bytes.Buffer{}
	renderDoctorActionable(out, report)
	printed := out.String()

	for _, finding := range broken {
		if !strings.Contains(printed, finding.Name+": "+finding.State) {
			t.Fatalf("the short report dropped the actionable finding %q:\n%s", finding.Name, printed)
		}
	}
	if !strings.Contains(printed, "plugin data dir: warn") {
		t.Fatalf("the short report dropped an unwritable plugin directory:\n%s", printed)
	}
	// One line per finding, plus the summary. Nothing elided.
	if got, want := len(nonEmptyLines(printed)), len(broken)+2; got != want {
		t.Fatalf("short report printed %d lines for %d findings, want %d:\n%s", got, len(broken), want, printed)
	}
	if !strings.Contains(printed, "6 error") {
		t.Fatalf("the summary miscounted the errors:\n%s", printed)
	}
}

// TestEveryActionableLineNamesACommand is the second half of the bar: a
// finding without a remedy is a complaint, not a diagnosis.
func TestEveryActionableLineNamesACommand(t *testing.T) {
	report := healthyDoctorReport()
	report.Dirs[0] = doctorCheckResult{Name: "plugin config dir", State: "warn", Detail: "/cfg: mode bits currently deny writes (mode 0o500)"}
	report.Checks = nil
	for name := range doctorRemedies {
		report.Checks = append(report.Checks, doctorCheckResult{Name: name, State: "warn", Detail: "something is wrong"})
	}
	// A check added later with no table entry still has to carry a command.
	report.Checks = append(report.Checks,
		doctorCheckResult{Name: "a_check_invented_after_this_test", State: "error", Detail: "no table entry"},
		doctorCheckResult{Name: "setup semantic index", State: "error", Detail: "component failed"},
	)

	out := &bytes.Buffer{}
	renderDoctorActionable(out, report)

	lines := nonEmptyLines(out.String())
	for _, line := range lines[:len(lines)-1] { // all but the summary
		if strings.Count(line, "`") < 2 {
			t.Fatalf("an actionable line names no command to run: %q", line)
		}
	}
	// The path is shell-quoted now (an unquoted path with a space chmods the
	// wrong thing), so this asserts the INTENT — the chmod and the directory are
	// both named — rather than the exact concatenation it used to produce.
	if !strings.Contains(out.String(), "chmod u+w "+shellQuote("/cfg")) {
		t.Fatalf("an unwritable plugin directory must name the chmod and the path:\n%s", out)
	}
}

// TestDoctorRemediesAreRealCommands resolves the table against the live
// command tree, so a renamed verb fails the build instead of sending a user to
// a command that no longer exists.
func TestDoctorRemediesAreRealCommands(t *testing.T) {
	root := NewRootCommand(Options{Version: "test"})
	checked := 0
	for name, remedy := range doctorRemedies {
		// The table holds VERBS now, not whole commands: how the verb is
		// spelled depends on the reader's install, so the invocation is built
		// where the line is printed. Resolve the verb itself.
		rest := remedy
		var path []string
		for _, field := range strings.Fields(rest) {
			if strings.HasPrefix(field, "-") || strings.HasPrefix(field, "<") {
				break
			}
			path = append(path, field)
		}
		found, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("doctor sends %q to `%s`, which does not exist: %v", name, remedy, err)
		}
		// Find falls back to the nearest parent, so confirm the leaf is real.
		if found.Name() != path[len(path)-1] {
			t.Fatalf("doctor sends %q to `%s`, but %q is not a command (resolved to %q)",
				name, remedy, path[len(path)-1], found.CommandPath())
		}
		for _, field := range strings.Fields(rest) {
			if !strings.HasPrefix(field, "--") {
				continue
			}
			flag := strings.TrimPrefix(strings.SplitN(field, "=", 2)[0], "--")
			if found.Flags().Lookup(flag) == nil && found.PersistentFlags().Lookup(flag) == nil && root.PersistentFlags().Lookup(flag) == nil {
				t.Fatalf("doctor sends %q to `%s`, but %s has no --%s flag", name, remedy, found.CommandPath(), flag)
			}
		}
		checked++
	}
	if checked < 15 {
		t.Fatalf("only resolved %d remedies; the extractor is probably broken", checked)
	}
	// The flag the short report advertises has to exist too.
	doctor, _, err := root.Find([]string{"doctor"})
	if err != nil || doctor.Flags().Lookup("verbose") == nil {
		t.Fatalf("the short report advertises `%s`, but doctor has no --verbose flag", doctorVerboseInvocation())
	}
}

// TestDoctorVerboseRestoresTheFullReport: everything the short report drops is
// still exactly one flag away, unchanged.
func TestDoctorVerboseRestoresTheFullReport(t *testing.T) {
	report := healthyDoctorReport()
	out := &bytes.Buffer{}
	renderDoctorVerbose(out, report)
	printed := out.String()

	for _, want := range []string{
		"ENTIRE_CLI_VERSION=1.2.3",
		"repo root: /repo (inferred from cwd)",
		"plugin config dir: ok (/cfg)",
		"manifest: ok (/data)",
		"history_fts: ok (BM25 index fresh)",
	} {
		if !strings.Contains(printed, want) {
			t.Fatalf("the full report lost %q:\n%s", want, printed)
		}
	}
}

// TestLongDetailsAreTruncatedInTheShortReportOnly: the blind-spot groups carry
// every skipped path -- 3.5KB on this repository -- which is a list to read
// under --verbose, not a wall to scroll past when looking for what to fix.
func TestLongDetailsAreTruncatedInTheShortReportOnly(t *testing.T) {
	long := "44 data files skipped (minified/bundled): " + strings.Repeat("benchmarks/agent-brain/a-very-long-path.json, ", 60)
	report := doctorReport{Checks: []doctorCheckResult{{Name: "semantic blind spots", State: "warn", Detail: long}}}

	short := &bytes.Buffer{}
	renderDoctorActionable(short, report)
	if len(short.String()) > 400 {
		t.Fatalf("the short report printed %d bytes for one finding:\n%s", len(short.String()), short)
	}
	if !strings.Contains(short.String(), "44 data files skipped") {
		t.Fatalf("truncation ate the part that says WHAT happened:\n%s", short)
	}

	full := &bytes.Buffer{}
	renderDoctorVerbose(full, report)
	if !strings.Contains(full.String(), long) {
		t.Fatalf("the full report truncated a detail it must print whole:\n%s", full)
	}
}

func nonEmptyLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// Raised as a shell-injection finding. The stated threat model is weak — the
// path comes from the reader's own ENTIRE_PLUGIN_* variables or home directory
// — but the quoting is right for a duller reason that bites far more often: an
// unquoted path containing a SPACE chmods the wrong thing, and "/Users/me/My
// Brain" is an ordinary path on a Mac.
func TestTheDirectoryRemedyIsSafeToPaste(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, path, want string }{
		{"space", "/Users/me/My Brain/data", `chmod u+w '/Users/me/My Brain/data'`},
		{"backtick", "/tmp/x`whoami`", "chmod u+w '/tmp/x`whoami`'"},
		{"semicolon", "/tmp/a;rm -rf b", `chmod u+w '/tmp/a;rm -rf b'`},
	} {
		if got := doctorDirectoryRemedy(c.path); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	// A path containing a single quote must close, escape and reopen, or the
	// quoting itself becomes the injection.
	got := doctorDirectoryRemedy("/tmp/it's")
	if strings.Contains(got, "it's'") && !strings.Contains(got, `'\''`) {
		t.Errorf("an embedded single quote escapes the quoting: %q", got)
	}
	// And an empty path must still fall back rather than suggest chmod on ''.
	if r := doctorDirectoryRemedy("  "); strings.Contains(r, "chmod") {
		t.Errorf("an empty path produced a chmod suggestion: %q", r)
	}
}
