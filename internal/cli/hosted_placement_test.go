package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// countingWhereamiRunner answers `entire auth token` like the hosted harness
// does and fails `entire repo whereami` the way every stock host CLI does
// (COR-2029 is unshipped), counting how often the resolver reached for it.
func countingWhereamiRunner(calls *int) commandRunnerFunc {
	return commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		if name == "entire" && len(args) >= 2 && args[0] == "repo" && args[1] == "whereami" {
			*calls++
			return nil, nil, errors.New("unknown command \"whereami\" for \"entire repo\"")
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
	whereamiCalls := 0
	opts.Runner = countingWhereamiRunner(&whereamiCalls)

	var errOut bytes.Buffer
	if err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, "main"); err != nil {
		t.Fatalf("an unresolvable placement must stay a graceful local fallback, got %v", err)
	}
	if whereamiCalls != 1 {
		t.Fatalf("the gate must have actually attempted resolution, got %d calls", whereamiCalls)
	}
	lines := stderrLines(errOut.String())
	if len(lines) != 1 {
		t.Fatalf("auto-connect failure must print exactly one stderr line, got %d: %q", len(lines), errOut.String())
	}
	line := lines[0]
	for _, want := range []string{
		repoDir,                         // which repository stayed local
		"branch main",                   // and which branch
		"COR-2029",                      // why: the host contract is unshipped
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
// the default branch, and each resolveWhereami attempt is bounded only by
// hostedShellOutTimeout — so an unshipped whereami cost up to 60s of shelling
// out per session end. One attempt per process (per TTL window) is the cap.
func TestHostedSyncResolvesPlacementAtMostOncePerProcess(t *testing.T) {
	fake := &hostedFake{}
	opts, storage, repoDir, _ := hostedSyncHarness(t, fake, false)
	whereamiCalls := 0
	opts.Runner = countingWhereamiRunner(&whereamiCalls)

	var errOut bytes.Buffer
	// Exactly the session-end hook's pair: current branch, then the default.
	for _, branch := range []string{"feature", distillDefaultBranch} {
		if err := hostedFactsSyncAndPull(context.Background(), &errOut, opts, repoDir, storage, branch); err != nil {
			t.Fatalf("branch %s must stay a graceful local fallback, got %v", branch, err)
		}
	}
	if whereamiCalls != 1 {
		t.Fatalf("a failed resolution must not be retried in the same process: want 1 shell-out, got %d", whereamiCalls)
	}
	// The suppressed attempt must also stay quiet, or the fix for ND-2 would
	// reintroduce the per-branch noise the cache exists to remove.
	if lines := stderrLines(errOut.String()); len(lines) != 1 {
		t.Fatalf("a suppressed attempt must not print again: want 1 line, got %d: %q", len(lines), errOut.String())
	}
}

// The negative cache must not become a permanent negative: a long-lived watch
// daemon has to notice that the user logged in, the repo was registered, or
// the host CLI finally shipped whereami.
func TestHostedPlacementRetriesAfterTheNegativeTTL(t *testing.T) {
	repoDir := t.TempDir()
	whereamiCalls := 0
	runner := countingWhereamiRunner(&whereamiCalls)
	start := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	if _, attempted, err := resolveHostedPlacement(context.Background(), runner, repoDir, start); err == nil || !attempted {
		t.Fatalf("first call must attempt and fail: attempted=%v err=%v", attempted, err)
	}
	if _, attempted, err := resolveHostedPlacement(context.Background(), runner, repoDir, start.Add(hostedPlacementNegativeTTL-time.Second)); attempted || !errors.Is(err, errHostedPlacementSuppressed) {
		t.Fatalf("inside the TTL the shell-out must be suppressed: attempted=%v err=%v", attempted, err)
	}
	if _, attempted, _ := resolveHostedPlacement(context.Background(), runner, repoDir, start.Add(hostedPlacementNegativeTTL)); !attempted {
		t.Fatal("past the TTL the resolver must try again")
	}
	if whereamiCalls != 2 {
		t.Fatalf("want 2 shell-outs (first attempt, post-TTL retry), got %d", whereamiCalls)
	}
}

// ND-1: `connect` with no flags cannot succeed on a first run, and its error
// used to lead with `entire repo whereami` — a command that exists nowhere,
// so the reader goes looking for a local breakage that is not there. The
// runnable path has to come first.
func TestConnectErrorNamesTheRunnableFlagsBeforeTheUnshippedCommand(t *testing.T) {
	repoDir := t.TempDir()
	env := connectTestEnv(t, repoDir)
	whereamiCalls := 0
	runner := countingWhereamiRunner(&whereamiCalls)
	opts := Options{Version: "test", Env: env, Runner: runner, Now: time.Now}

	_, _, err := runConnectCommand(t, opts)
	if err == nil {
		t.Fatal("connect without flags must fail while whereami is unshipped")
	}
	msg := err.Error()
	repoIDAt := strings.Index(msg, "--repo-id")
	apiURLAt := strings.Index(msg, "--api-url")
	whereamiAt := strings.Index(msg, "whereami")
	if repoIDAt < 0 || apiURLAt < 0 {
		t.Fatalf("error must name both flags the user can actually run, got: %v", err)
	}
	if whereamiAt >= 0 && repoIDAt > whereamiAt {
		t.Fatalf("the runnable path must come before the unshipped command, got: %v", err)
	}
	if !strings.Contains(msg, "COR-2029") || !strings.Contains(msg, "not shipped") {
		t.Fatalf("error must say automatic binding is unshipped (COR-2029), got: %v", err)
	}
}
