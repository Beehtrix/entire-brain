package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-brain/internal/agentsetup"
)

// agent_wiring_test.go covers the first-run defect that made a successful
// `setup` useless: the brain was built and NOTHING TOLD AN AGENT IT EXISTED.
// setup.go carried no reference to the agentsetup package at all, so a
// setup-only repository had no guide, no AGENTS.md/CLAUDE.md pointer and no
// pre-edit hook, and every agent that opened it walked straight past the brain.

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestSetupWritesAgentInstructionsSoAgentsDiscoverTheBrain is UX-07: a plain
// `setup` must leave the repository carrying a pointer an agent will read.
func TestSetupWritesAgentInstructionsSoAgentsDiscoverTheBrain(t *testing.T) {
	f := newSetupTestFixture(t)
	rec := &recordedSetup{}
	opts := defaultSetupOptions()
	opts.noDaemon = true
	opts.noBackfill = true

	out := runSetupForTest(t, f, opts, rec)

	guide := filepath.Join(f.repoDir, agentsetup.Path)
	if _, err := os.Stat(guide); err != nil {
		t.Fatalf("setup left no agent guide at %s: %v", agentsetup.Path, err)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		content := readFileString(t, filepath.Join(f.repoDir, name))
		if !strings.Contains(content, agentsetup.Path) {
			t.Fatalf("%s does not point at the agent guide, so no agent learns the brain exists:\n%s", name, content)
		}
	}
	settings := filepath.Join(f.repoDir, claudeSettingsDir, claudeSettingsFile)
	if !strings.Contains(readFileString(t, settings), "pre-edit") {
		t.Fatalf("setup wired no PreToolUse pre-edit hook into %s", settings)
	}
	if !strings.Contains(out, "agent instructions") {
		t.Fatalf("setup said nothing about agent instructions:\n%s", out)
	}
}

// TestSetupAgentWiringIsIdempotent proves running setup twice is safe: the
// managed block is replaced in place and the settings entry is not duplicated.
// Without this the second run would append a second pointer block and a second
// PreToolUse entry, and the hook would run twice per edit.
func TestSetupAgentWiringIsIdempotent(t *testing.T) {
	f := newSetupTestFixture(t)
	opts := defaultSetupOptions()
	opts.noDaemon = true
	opts.noBackfill = true

	runSetupForTest(t, f, opts, &recordedSetup{})
	first := map[string]string{}
	for _, name := range []string{agentsetup.Path, "AGENTS.md", "CLAUDE.md", claudeSettingsDir + "/" + claudeSettingsFile} {
		first[name] = readFileString(t, filepath.Join(f.repoDir, filepath.FromSlash(name)))
	}

	runSetupForTest(t, f, opts, &recordedSetup{})
	for name, before := range first {
		after := readFileString(t, filepath.Join(f.repoDir, filepath.FromSlash(name)))
		if before != after {
			t.Fatalf("second setup rewrote %s:\n--- first ---\n%s\n--- second ---\n%s", name, before, after)
		}
	}
	if n := strings.Count(first["AGENTS.md"], agentPointerBeginMarkerForTest); n != 1 {
		t.Fatalf("AGENTS.md carries %d managed pointer blocks, want exactly 1", n)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(first[claudeSettingsDir+"/"+claudeSettingsFile]), &settings); err != nil {
		t.Fatal(err)
	}
	preEdits := 0
	for _, matcher := range settings.Hooks["PreToolUse"] {
		for _, hook := range matcher.Hooks {
			if commandInvokesPreEditHook(hook.Command) {
				preEdits++
			}
		}
	}
	if preEdits != 1 {
		t.Fatalf("settings.json carries %d pre-edit hooks, want exactly 1", preEdits)
	}
}

// agentPointerBeginMarkerForTest is the installer's managed-block marker. It is
// spelled out here rather than imported so a change to the marker shows up as a
// failing idempotence test rather than silently agreeing with itself.
const agentPointerBeginMarkerForTest = "<!-- entire-agent:begin -->"

// TestSetupAgentWiringFailureIsAWarningNotAFailedSetup: the brain stays usable
// when the repository will not accept the instruction files. The guide write is
// made impossible by occupying `.entire` with a regular file.
func TestSetupAgentWiringFailureIsAWarningNotAFailedSetup(t *testing.T) {
	f := newSetupTestFixture(t)
	if err := os.WriteFile(filepath.Join(f.repoDir, ".entire"), []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := defaultSetupOptions()
	opts.noDaemon = true
	opts.noBackfill = true

	out := runSetupForTest(t, f, opts, &recordedSetup{})
	if !strings.Contains(out, "agent instructions not written") {
		t.Fatalf("a failed wiring must be reported, not swallowed:\n%s", out)
	}
	if !strings.Contains(out, "init-agents") {
		t.Fatalf("a failed wiring must name the command that repairs it:\n%s", out)
	}
}

// TestSetupRemembersDeclinedDaemonAndBackfill is UX-19. Declining the persistent
// login service and the token spend, then following our own "re-running setup is
// safe" advice, used to install the service and start the spend anyway.
func TestSetupRemembersDeclinedDaemonAndBackfill(t *testing.T) {
	f := newSetupTestFixture(t, "s1")
	declined := defaultSetupOptions()
	declined.noDaemon = true
	declined.noBackfill = true
	declined.agent = "codex"
	rec := &recordedSetup{agent: "codex"}
	runSetupForTest(t, f, declined, rec)
	if rec.installCalls != 0 || rec.spawnCalls != 0 {
		t.Fatalf("the declining run itself must install and spend nothing: install=%d spawn=%d", rec.installCalls, rec.spawnCalls)
	}

	// The bare re-run our own output recommends.
	bare := defaultSetupOptions()
	bare.agent = "codex"
	rerun := &recordedSetup{agent: "codex"}
	out := runSetupForTest(t, f, bare, rerun)
	if rerun.installCalls != 0 {
		t.Fatalf("a bare re-run installed the persistent watcher the user declined (%d installs)\n%s", rerun.installCalls, out)
	}
	if rerun.spawnCalls != 0 {
		t.Fatalf("a bare re-run started the token spend the user declined (%d spawns)\n%s", rerun.spawnCalls, out)
	}
}

// TestSetupExplicitAffirmativeOverridesRememberedDecline is the negative
// control for UX-19: remembering a decline must not make it unescapable.
func TestSetupExplicitAffirmativeOverridesRememberedDecline(t *testing.T) {
	f := newSetupTestFixture(t, "s1")
	declined := defaultSetupOptions()
	declined.noDaemon = true
	declined.noBackfill = true
	declined.agent = "codex"
	runSetupForTest(t, f, declined, &recordedSetup{agent: "codex"})

	affirmed := defaultSetupOptions()
	affirmed.agent = "codex"
	affirmed.daemon = true
	affirmed.backfill = true
	rec := &recordedSetup{agent: "codex"}
	out := &bytes.Buffer{}
	cmd := setupTestCommand(t, out, affirmed)
	if err := runSetup(context.Background(), cmd, f.opts, affirmed, f.repoDir, rec.steps(f)); err != nil {
		t.Fatalf("runSetup: %v\n%s", err, out.String())
	}
	if rec.installCalls != 1 {
		t.Fatalf("--daemon must override the remembered decline: installs=%d\n%s", rec.installCalls, out.String())
	}
	if rec.spawnCalls != 1 {
		t.Fatalf("--backfill must override the remembered decline: spawns=%d\n%s", rec.spawnCalls, out.String())
	}
}

// TestSetupRejectsContradictoryToggles: a run that both declines and demands the
// same thing has no safe reading, and guessing is how a reader gets the service
// they declined.
func TestSetupRejectsContradictoryToggles(t *testing.T) {
	f := newSetupTestFixture(t)
	opts := defaultSetupOptions()
	out := &bytes.Buffer{}
	cmd := setupTestCommand(t, out, opts)
	for _, name := range []string{setupFlagNoDaemon, setupFlagDaemon} {
		if err := cmd.Flags().Set(name, "true"); err != nil {
			t.Fatal(err)
		}
	}
	err := runSetup(context.Background(), cmd, f.opts, opts, f.repoDir, (&recordedSetup{}).steps(f))
	if err == nil || !strings.Contains(err.Error(), "contradict") {
		t.Fatalf("--no-daemon with --daemon must be refused, got %v", err)
	}
}

// TestSetupRemembersDeclinedAgentHooks keeps the third decline in the same
// contract as the other two.
func TestSetupRemembersDeclinedAgentHooks(t *testing.T) {
	f := newSetupTestFixture(t)
	declined := defaultSetupOptions()
	declined.noDaemon = true
	declined.noBackfill = true
	declined.noAgentHooks = true
	runSetupForTest(t, f, declined, &recordedSetup{})
	settings := filepath.Join(f.repoDir, claudeSettingsDir, claudeSettingsFile)
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Fatalf("--no-agent-hooks still wrote %s (err %v)", settings, err)
	}

	bare := defaultSetupOptions()
	runSetupForTest(t, f, bare, &recordedSetup{})
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Fatalf("a bare re-run wired the hook the user declined: %v", err)
	}
}

// TestInitAgentsMergesPreToolUseHookPreservingOtherSettings is UX-18: the verb
// `hook pre-edit` existed and nothing called it. The merge must add the entry
// without destroying the reader's other settings — a typed-struct round trip
// would silently delete every key this binary does not know.
func TestInitAgentsMergesPreToolUseHookPreservingOtherSettings(t *testing.T) {
	repo := t.TempDir()
	existing := `{
  "model": "opus",
  "permissions": {"allow": ["Bash(gh pr merge:*)"]},
  "hooks": {
    "SessionEnd": [
      {"matcher": "", "hooks": [{"type": "command", "command": "entire hooks claude-code session-end"}]}
    ],
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "rtk hook claude"}]}
    ]
  }
}`
	if err := os.MkdirAll(filepath.Join(repo, claudeSettingsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(repo, claudeSettingsDir, claudeSettingsFile)
	if err := os.WriteFile(settingsPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	path, changed, err := installPreEditHook(repo)
	if err != nil {
		t.Fatalf("installPreEditHook: %v", err)
	}
	if !changed || path != settingsPath {
		t.Fatalf("expected a change at %s, got changed=%v path=%s", settingsPath, changed, path)
	}
	merged := readFileString(t, settingsPath)
	for _, keep := range []string{`"opus"`, "Bash(gh pr merge:*)", "rtk hook claude", "session-end"} {
		if !strings.Contains(merged, keep) {
			t.Fatalf("the merge destroyed %q from the reader's settings:\n%s", keep, merged)
		}
	}
	if !strings.Contains(merged, "hook pre-edit --claude-hook") {
		t.Fatalf("no pre-edit hook was merged:\n%s", merged)
	}

	// Second call is a no-op: an entry already invokes the hook.
	if _, changed, err := installPreEditHook(repo); err != nil || changed {
		t.Fatalf("second merge must be a no-op, got changed=%v err=%v", changed, err)
	}
	if again := readFileString(t, settingsPath); again != merged {
		t.Fatalf("second merge rewrote the file:\n%s", again)
	}
}

// TestInstallPreEditHookRefusesSettingsItCannotUnderstand: overwriting a
// settings file whose shape we did not parse would cost the reader their whole
// harness configuration.
func TestInstallPreEditHookRefusesSettingsItCannotUnderstand(t *testing.T) {
	for name, body := range map[string]string{
		"invalid json":               "{ not json",
		"hooks is not an object":     `{"hooks": "none"}`,
		"PreToolUse is not an array": `{"hooks": {"PreToolUse": {"matcher": "Edit"}}}`,
	} {
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, claudeSettingsDir), 0o755); err != nil {
			t.Fatal(err)
		}
		settingsPath := filepath.Join(repo, claudeSettingsDir, claudeSettingsFile)
		if err := os.WriteFile(settingsPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := installPreEditHook(repo); err == nil {
			t.Fatalf("%s: expected a refusal", name)
		}
		if got := readFileString(t, settingsPath); got != body {
			t.Fatalf("%s: a refused merge still rewrote the file:\n%s", name, got)
		}
	}
}

// TestPreEditHookCommandSurvivesAManagedPluginInstall: on a managed install the
// brain binary is only on PATH inside the Entire CLI's own process, so a bare
// `entire-brain ...` hook command would be "command not found" on every edit.
func TestPreEditHookCommandSurvivesAManagedPluginInstall(t *testing.T) {
	command := preEditHookCommand()
	if !strings.Contains(command, setupEntireBinaryName+" "+setupBrainPluginVerb+" hook pre-edit") {
		t.Fatalf("the wired command has no host-CLI fallback for a managed install: %s", command)
	}
	if !commandInvokesPreEditHook(command) {
		t.Fatalf("the command we write is not recognised by our own idempotence check: %s", command)
	}
}

// TestCommandInvokesPreEditHookRejectsOtherEntireHooks: a repository with only
// the session-end hook wired must not be read as having the pre-edit hook, or
// setup would claim a delivery path nothing calls.
func TestCommandInvokesPreEditHookRejectsOtherEntireHooks(t *testing.T) {
	if commandInvokesPreEditHook("entire hooks claude-code session-end") {
		t.Fatal("the session-end hook was accepted as the pre-edit hook")
	}
	if commandInvokesPreEditHook("some-other-tool hook pre-edit") {
		t.Fatal("another tool's pre-edit hook was accepted as ours")
	}
	if !commandInvokesPreEditHook("/opt/bin/entire-brain hook pre-edit --claude-hook") {
		t.Fatal("our own hook spelled absolutely was rejected")
	}
}

// TestDecodeClaudeHookEventPath pins the field Claude Code actually delivers.
// The verb took only --file, so the wired hook would have seen no path at all.
func TestDecodeClaudeHookEventPath(t *testing.T) {
	event := `{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Edit",` +
		`"tool_input":{"file_path":"/repo/internal/cli/history_fts.go","old_string":"a","new_string":"b"}}`
	path, ok := decodeClaudeHookEventPath(strings.NewReader(event))
	if !ok || path != "/repo/internal/cli/history_fts.go" {
		t.Fatalf("tool_input.file_path not read: path=%q ok=%v", path, ok)
	}
	if path, ok := decodeClaudeHookEventPath(strings.NewReader(`{"tool_input":{"notebook_path":"/repo/a.ipynb"}}`)); !ok || path != "/repo/a.ipynb" {
		t.Fatalf("notebook_path not read: path=%q ok=%v", path, ok)
	}
	for _, bad := range []string{"", "   ", "not json", `{"tool_input":{}}`, `{"tool_name":"Bash"}`} {
		if path, ok := decodeClaudeHookEventPath(strings.NewReader(bad)); ok {
			t.Fatalf("%q must yield no path, got %q", bad, path)
		}
	}
}

// TestHookPreEditClaudeHookEmitsTheHostEnvelope is the other half of UX-18:
// plain stdout is NOT added to the agent's context for PreToolUse, so the text
// emission could never have reached an agent however it was wired.
func TestHookPreEditClaudeHookEmitsTheHostEnvelope(t *testing.T) {
	opts, _, brainDir := commandRegressionFixture(t)
	text := "`history_fts.go` keeps the BM25 relevance cutoff relative to the top score, not absolute."
	paths := []string{"architecture.boundaries.rationale"}
	fact := factRecord{
		ID: factRecordID(text, paths), Text: text, Paths: paths, Kind: factKindDecision,
		Branch: "main", Origin: factOriginAuthored, Status: factStatusActive,
		CreatedAt: opts.Now(), UpdatedAt: opts.Now(),
	}
	if err := writeFacts(brainDir, "main", []factRecord{fact}); err != nil {
		t.Fatal(err)
	}

	cmd := newHookCommand(opts)
	cmd.SetIn(strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Edit",` +
		`"tool_input":{"file_path":"internal/cli/history_fts.go"}}`))
	out, err := execute(t, cmd, "pre-edit", "--claude-hook")
	if err != nil {
		t.Fatalf("pre-edit --claude-hook: %v\n%s", err, out)
	}
	var envelope struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("stdout is not the host's hook envelope: %v\n%s", err, out)
	}
	if envelope.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Fatalf("hookEventName is required and must be PreToolUse, got %q", envelope.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(envelope.HookSpecificOutput.AdditionalContext, "BM25 relevance cutoff") {
		t.Fatalf("the matched fact did not reach additionalContext:\n%s", out)
	}
}

// TestHookPreEditClaudeHookStaysSilentWithoutAMatch keeps the harness-safety
// contract: an empty envelope is noise, and a pathless event is not misuse.
func TestHookPreEditClaudeHookStaysSilentWithoutAMatch(t *testing.T) {
	opts, _, brainDir := commandRegressionFixture(t)
	// A brain with facts that do NOT match. Without this the hook returns early
	// on "no facts at all" and the emission path is never reached, so the test
	// would pass however the emitter behaved.
	text := "The export cursor is advanced only after a successful flush."
	paths := []string{"architecture.data.flow"}
	if err := writeFacts(brainDir, "main", []factRecord{{
		ID: factRecordID(text, paths), Text: text, Paths: paths, Kind: factKindDecision,
		Branch: "main", Origin: factOriginAuthored, Status: factStatusActive,
		CreatedAt: opts.Now(), UpdatedAt: opts.Now(),
	}}); err != nil {
		t.Fatal(err)
	}
	if facts, err := loadFacts(brainDir, "main"); err != nil || len(facts) == 0 {
		t.Fatalf("fixture must hold facts for this test to mean anything: %d facts, err %v", len(facts), err)
	}
	cmd := newHookCommand(opts)
	cmd.SetIn(strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`))
	out, err := execute(t, cmd, "pre-edit", "--claude-hook")
	if err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("a pathless event must be silent success, got %q (err %v)", out, err)
	}

	cmd = newHookCommand(opts)
	cmd.SetIn(strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"internal/cli/nothing_matches_here.go"}}`))
	out, err = execute(t, cmd, "pre-edit", "--claude-hook")
	if err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("no matching fact must emit nothing, got %q (err %v)", out, err)
	}
}
