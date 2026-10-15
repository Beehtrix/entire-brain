package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// countingPlacementRunner answers `entire auth token` like the hosted harness
// does and fails `entire repo view` the way a logged-out user, an unregistered
// repo, or a host CLI without the command does, counting how often the
// resolver reached for it.
func countingPlacementRunner(calls *int) commandRunnerFunc {
	return commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		if name == "entire" && len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			*calls++
			return nil, nil, errors.New("repo not registered")
		}
		if name == "entire" && len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
			return []byte(fakeJWT + "\n"), nil, nil
		}
		return nil, nil, errors.New("not available")
	})
}

func stderrLines(errOut string) []string {
	trimmed := strings.TrimRight(errOut, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// ND-2: the auto-connect gate used to `return nil` with no stderr line — alone
// among the gates in hostedFactsSyncAndPull, all of which report. The hosted
// loop therefore looked installed and simply never engaged.
func TestHostedSyncAutoConnectFailureIsReported(t *testing.T) {
	fake := &hostedFake{}
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, false)
	viewCalls := 0
	opts.Runner = countingPlacementRunner(&viewCalls)

	var errOut bytes.Buffer
	if err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatalf("an unresolvable placement must stay a graceful local fallback, got %v", err)
	}
	if viewCalls != 1 {
		t.Fatalf("the gate must have actually attempted resolution, got %d calls", viewCalls)
	}
	lines := stderrLines(errOut.String())
	if len(lines) != 1 {
		t.Fatalf("auto-connect failure must print exactly one stderr line, got %d: %q", len(lines), errOut.String())
	}
	line := lines[0]
	for _, want := range []string{
		repoDir,                         // which repository stayed local
		"branch main",                   // and which branch
		"entire repo view --json",       // what was tried
		"--repo-id",                     // the runnable remedy
		"the local brain is unaffected", // and that nothing was lost
	} {
		// Errorf, not Fatalf: every missing element should be named in one
		// run, so a weakening proves each of these assertions fires rather
		// than only the first.
		if !strings.Contains(line, want) {
			t.Errorf("stderr line must contain %q, got %q", want, line)
		}
	}
	if _, present, _ := readHostedRepoBinding(storage.BrainDir); present {
		t.Fatal("no binding may be written when placement is unresolved")
	}
	if fake.requestCount() != 0 {
		t.Fatalf("local fallback must make zero hosted requests, got %d", fake.requestCount())
	}
}

// ND-3: the session-end hook runs this path for the current branch and then
// the default branch, and each resolveHostedPlacement attempt is bounded only by
// hostedShellOutTimeout — so an unresolvable placement cost up to 60s of
// shelling out per session end. One attempt per process (per TTL window) is the cap.
func TestHostedSyncResolvesPlacementAtMostOncePerProcess(t *testing.T) {
	fake := &hostedFake{}
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, false)
	viewCalls := 0
	opts.Runner = countingPlacementRunner(&viewCalls)

	var errOut bytes.Buffer
	// Exactly the session-end hook's pair: current branch, then the default.
	for _, branch := range []string{"feature", distillDefaultBranch} {
		if err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, branch); err != nil {
			t.Fatalf("branch %s must stay a graceful local fallback, got %v", branch, err)
		}
	}
	if viewCalls != 1 {
		t.Fatalf("a failed resolution must not be retried in the same process: want 1 shell-out, got %d", viewCalls)
	}
	// The suppressed attempt must also stay quiet, or the fix for ND-2 would
	// reintroduce the per-branch noise the cache exists to remove.
	if lines := stderrLines(errOut.String()); len(lines) != 1 {
		t.Fatalf("a suppressed attempt must not print again: want 1 line, got %d: %q", len(lines), errOut.String())
	}
}

// The negative cache must not become a permanent negative: a long-lived watch
// daemon has to notice that the user logged in, the repo was registered, or
// the host CLI gained placement support.
func TestHostedPlacementRetriesAfterTheNegativeTTL(t *testing.T) {
	repoDir := t.TempDir()
	viewCalls := 0
	runner := countingPlacementRunner(&viewCalls)
	start := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	if _, attempted, err := resolveHostedPlacementCached(context.Background(), runner, repoDir, start); err == nil || !attempted {
		t.Fatalf("first call must attempt and fail: attempted=%v err=%v", attempted, err)
	}
	if _, attempted, err := resolveHostedPlacementCached(context.Background(), runner, repoDir, start.Add(hostedPlacementNegativeTTL-time.Second)); attempted || !errors.Is(err, errHostedPlacementSuppressed) {
		t.Fatalf("inside the TTL the shell-out must be suppressed: attempted=%v err=%v", attempted, err)
	}
	if _, attempted, _ := resolveHostedPlacementCached(context.Background(), runner, repoDir, start.Add(hostedPlacementNegativeTTL)); !attempted {
		t.Fatal("past the TTL the resolver must try again")
	}
	if viewCalls != 2 {
		t.Fatalf("want 2 shell-outs (first attempt, post-TTL retry), got %d", viewCalls)
	}
}

// ND-1: `connect` with no flags cannot succeed on a first run when placement
// does not resolve; the error must name the command it tried and both flags the
// reader can run instead.
func TestConnectErrorNamesTheRunnableFlagsWhenPlacementFails(t *testing.T) {
	repoDir := t.TempDir()
	env := connectTestEnv(t, repoDir)
	viewCalls := 0
	runner := countingPlacementRunner(&viewCalls)
	opts := Options{Version: "test", Env: env, Runner: runner, Now: time.Now}

	_, _, err := runConnectCommand(t, opts)
	if err == nil {
		t.Fatal("connect without flags must fail while placement is unresolvable")
	}
	msg := err.Error()
	for _, want := range []string{"--repo-id", "--api-url", "entire repo view --json"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error must contain %q, got: %v", want, err)
		}
	}
	if viewCalls != 1 {
		t.Fatalf("connect must always try the real resolver, got %d calls", viewCalls)
	}
}
