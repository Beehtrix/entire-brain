package cli

import (
	"strings"
	"testing"
	"time"
)

// newHostedSyncCmdFixture wires a CLI fixture whose repo is CONNECTED to a fake
// brainstore speaking the per-fact wire (hostedFake), with token minting
// scripted and every hosted env override cleared, so tests exercise exactly the
// connected-by-binding default path.
func newHostedSyncCmdFixture(t *testing.T, fake *hostedFake) (verifyFixture, string) {
	t.Helper()
	f := newVerifyFixture(t)
	if fake.callerBy == "" {
		fake.callerBy = "acct-caller"
	}
	if fake.callerHandle == "" {
		fake.callerHandle = "caller"
	}
	srv := newHostedFakeServer(t, fake)
	if err := writeHostedRepoBinding(f.brainDir, hostedRepoBinding{RepoID: "repo1", BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	f.opts.Env.PluginCacheDir = t.TempDir()
	f.runner.responses[fakeCommandKey("entire", "auth", "token")] = fakeCommandResponse{stdout: fakeJWT + "\n"}
	for _, env := range []string{"ENTIRE_BRAIN_NO_EGRESS", "ENTIRE_BRAIN_LOCAL_ONLY", "ENTIRE_BRAIN_ALLOW_HOSTED", "ENTIRE_API_URL", "ENTIRE_API_TOKEN", "ENTIRE_REPO_ID", envFactsBackend} {
		t.Setenv(env, "")
	}
	return f, srv.URL
}

// TestMemoriesSyncConnectedDefaultsToHostedWire is COR-2234's core claim: on a
// connected repo, plain `memories sync` exchanges over the hosted per-fact
// wire — no flag required.
func TestMemoriesSyncConnectedDefaultsToHostedWire(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	localFact := hostedSyncTestFact("local fact A", now)
	remoteFact := hostedSyncTestFact("remote fact B", now)
	fake := &hostedFake{}
	fake.seed(remoteFact, "trail-1", "acct-teammate", "teammate")
	f, baseURL := newHostedSyncCmdFixture(t, fake)
	f.writeFacts(t, "main", []factRecord{localFact})

	out, err := execute(t, NewRootCommand(f.opts), "memories", "sync")
	if err != nil {
		t.Fatalf("memories sync: %v\n%s", err, out)
	}
	if !fake.has(localFact.ID) {
		t.Fatal("plain sync on a connected repo must upsert local facts to the hosted store")
	}
	local, err := loadFacts(f.brainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range local {
		found = found || rec.ID == remoteFact.ID
	}
	if !found {
		t.Fatalf("plain sync must pull the team's facts, got %d records", len(local))
	}
	if !strings.Contains(out, "hosted (") {
		t.Fatalf("output must name the hosted backend, got %q", out)
	}
	if err := checkHostedFactsBinding(f.brainDir, "main", "repo1", baseURL); err != nil {
		t.Fatalf("per-branch hosted marker must be recorded: %v", err)
	}
}

// TestMemoriesSyncConnectedPullFailureFailsCommand pins the COR-2202 honesty
// rule onto the manual path: unlike background sync, a failed pull is a failed
// command, not a stderr shrug.
func TestMemoriesSyncConnectedPullFailureFailsCommand(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	fake := &hostedFake{denyFeed: true}
	f, _ := newHostedSyncCmdFixture(t, fake)
	f.writeFacts(t, "main", []factRecord{hostedSyncTestFact("local fact A", now)})

	out, err := execute(t, NewRootCommand(f.opts), "memories", "sync")
	if err == nil {
		t.Fatalf("a failed pull must fail the command:\n%s", out)
	}
	if !strings.Contains(err.Error(), "pull failed") {
		t.Fatalf("error must name the failed half, got %v", err)
	}
}

// TestMemoriesSyncConnectedPushFailureStillPullsAndFails: the push half failing
// must not block the team's facts from landing, but the exit code stays honest.
func TestMemoriesSyncConnectedPushFailureStillPullsAndFails(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	remoteFact := hostedSyncTestFact("remote fact B", now)
	fake := &hostedFake{denyPush: true}
	fake.seed(remoteFact, "trail-1", "acct-teammate", "teammate")
	f, _ := newHostedSyncCmdFixture(t, fake)
	f.writeFacts(t, "main", []factRecord{hostedSyncTestFact("local fact A", now)})

	out, err := execute(t, NewRootCommand(f.opts), "memories", "sync")
	if err == nil {
		t.Fatalf("a failed push must fail the command:\n%s", out)
	}
	if !strings.Contains(err.Error(), "push failed") {
		t.Fatalf("error must name the failed half, got %v", err)
	}
	local, loadErr := loadFacts(f.brainDir, "main")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	found := false
	for _, rec := range local {
		found = found || rec.ID == remoteFact.ID
	}
	if !found {
		t.Fatal("the pull must still land the team's facts after a failed push")
	}
}

// TestMemoriesSyncNoEgressSkipsConnectedDefault: the kill switch beats the
// connected default — zero requests, no stale local-gitmeta merge (which would
// re-raise deferred per-fact conflicts into the review queue), and a message
// saying why nothing synced.
func TestMemoriesSyncNoEgressSkipsConnectedDefault(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	fake := &hostedFake{}
	f, _ := newHostedSyncCmdFixture(t, fake)
	f.writeFacts(t, "main", []factRecord{hostedSyncTestFact("local fact A", now)})
	t.Setenv("ENTIRE_BRAIN_NO_EGRESS", "1")

	out, err := execute(t, NewRootCommand(f.opts), "memories", "sync")
	if err != nil {
		t.Fatalf("suppressed sync is not a failure: %v\n%s", err, out)
	}
	if fake.requestCount() != 0 {
		t.Fatalf("NO_EGRESS must make zero requests, got %d", fake.requestCount())
	}
	if !strings.Contains(out, "ENTIRE_BRAIN_NO_EGRESS") || !strings.Contains(out, "skipped") {
		t.Fatalf("output must say the sync was skipped and why, got %q", out)
	}
	if queue, qerr := loadFactProposals(f.brainDir, "main"); qerr != nil || len(queue) != 0 {
		t.Fatalf("a suppressed sync must not touch the review queue: %d entries, %v", len(queue), qerr)
	}
}

// TestMemoriesSyncUnconnectedDefaultsToLocal: without a binding nothing changes
// — plain sync stays on the local git-meta store with zero egress.
func TestMemoriesSyncUnconnectedDefaultsToLocal(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	f := newVerifyFixture(t)
	f.opts.Env.PluginCacheDir = t.TempDir()
	for _, env := range []string{"ENTIRE_BRAIN_NO_EGRESS", "ENTIRE_BRAIN_LOCAL_ONLY", "ENTIRE_BRAIN_ALLOW_HOSTED", envFactsBackend} {
		t.Setenv(env, "")
	}
	f.writeFacts(t, "main", []factRecord{hostedSyncTestFact("local fact A", now)})

	out, err := execute(t, NewRootCommand(f.opts), "memories", "sync")
	if err != nil {
		t.Fatalf("memories sync: %v\n%s", err, out)
	}
	if !strings.Contains(out, "local-gitmeta") {
		t.Fatalf("unconnected default must be the local store, got %q", out)
	}
}

// TestMemoriesSyncExplicitLocalBackendWinsOnConnectedRepo: an explicit
// --memories-backend=local-gitmeta keeps its meaning on a connected repo.
func TestMemoriesSyncExplicitLocalBackendWinsOnConnectedRepo(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	fake := &hostedFake{}
	f, _ := newHostedSyncCmdFixture(t, fake)
	f.writeFacts(t, "main", []factRecord{hostedSyncTestFact("local fact A", now)})

	out, err := execute(t, NewRootCommand(f.opts), "memories", "sync", "--memories-backend", "local-gitmeta")
	if err != nil {
		t.Fatalf("memories sync: %v\n%s", err, out)
	}
	if fake.requestCount() != 0 {
		t.Fatalf("explicit local backend must make zero requests, got %d", fake.requestCount())
	}
	if !strings.Contains(out, "local-gitmeta") {
		t.Fatalf("output must name the local backend, got %q", out)
	}
}
