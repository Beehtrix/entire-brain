package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// workerPrepassSweepFixture establishes a checkpoint-backed persistent brain
// the same way a real foreground export does, then drops a stale cached
// transcript that is eligible for the sweep (it is not a live session, so
// nothing retains it).
func workerPrepassSweepFixture(t *testing.T) (Options, string, repoStorage, string) {
	t.Helper()
	repoDir, runner := compactV1Fixture(t, true)
	runner.responses[fakeCommandKey("git", "cat-file", "-t", v1MainRef+":aa/a111aaa111/0/transcript.jsonl")] = fakeCommandResponse{stdout: "blob\n"}
	runner.responses[fakeCommandKey("git", "remote", "get-url", "origin")] = fakeCommandResponse{stdout: "https://github.com/entireio/cli.git\n"}
	opts := Options{
		Version: "test",
		Runner:  runner,
		Env:     EntireEnv{RepoRoot: repoDir, PluginDataDir: t.TempDir(), PluginStateDir: t.TempDir(), PluginCacheDir: t.TempDir()},
		Now:     func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) },
	}
	storage, err := repoStoragePaths(context.Background(), runner, opts.Env, repoDir)
	if err != nil {
		t.Fatal(err)
	}

	if out, err := execute(t, NewRootCommand(opts), "refresh", "sessions", "--entire-binary", "entire-test"); err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}

	sweepable := "sessions/main/stale.jsonl"
	if err := writeTranscriptFile(storage.BrainDir, sweepable, []byte("stale")); err != nil {
		t.Fatal(err)
	}
	return opts, repoDir, storage, sweepable
}

// A v3 manifest is what every 0.1.0-era brain has on disk. Bumping
// brainManifestSchemaVersion must not turn those brains unreadable.
func TestManifestV3StillReadable(t *testing.T) {
	dir := writeTestManifest(t, `{"schema_version":3}`)
	if _, err := loadBrainManifest(dir); err != nil {
		t.Fatalf("v3 manifest must still be readable: %v", err)
	}
}

// A manifest from a newer build must be refused, not misread, and the
// message must tell the reader what to do about it.
func TestManifestNewerRefusedWithRemedy(t *testing.T) {
	dir := writeTestManifest(t, `{"schema_version":5}`)
	_, err := loadBrainManifest(dir)
	if err == nil {
		t.Fatal("newer manifest must be refused")
	}
	if !strings.Contains(err.Error(), memoryErrUnsupportedVersion) {
		t.Fatalf("error code: %v", err)
	}
	if !strings.Contains(err.Error(), "upgrade entire-brain") {
		t.Fatalf("remedy missing from error: %v", err)
	}
}

// Every manifest this build writes must declare the current schema version,
// so a downgraded 0.1.0 binary can detect the skew instead of misreading
// fields it was never built to understand.
func TestManifestWrittenAtV4(t *testing.T) {
	dir := t.TempDir()
	if err := writeBrainManifestAndReadme(dir, exportManifest{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, exportManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"schema_version": 4`) {
		t.Fatalf("manifest was not written at schema version 4: %s", data)
	}
	manifest, err := loadBrainManifest(dir)
	if err != nil || manifest.SchemaVersion != 4 {
		t.Fatalf("round trip: manifest=%+v err=%v", manifest, err)
	}
}

// The background memory worker is not a user-invoked export: nobody asked for
// the destructive transcript cache sweep, and nobody is watching stderr to
// learn that files were removed. The worker-driven delta export must leave
// the cache alone; user-invoked export/refresh still sweeps it.
func TestWorkerPrepassSkipsTranscriptSweep(t *testing.T) {
	opts, repoDir, storage, sweepable := workerPrepassSweepFixture(t)

	if err := memoryWorkerPrepass(context.Background(), opts, repoDir); err != nil {
		t.Fatalf("worker prepass: %v", err)
	}

	if _, err := os.Stat(filepath.Join(storage.BrainDir, filepath.FromSlash(sweepable))); err != nil {
		t.Fatalf("background worker prepass swept the transcript cache: %v", err)
	}
}

// The watch daemon's per-tick delta is just as unattended as the memory
// worker prepass: it runs as a launchd daemon with nobody watching its
// (discarded) stderr, so it must never run the destructive transcript sweep
// either. Only user-invoked export/refresh may sweep.
func TestWatchTickSkipsTranscriptSweep(t *testing.T) {
	opts, repoDir, storage, sweepable := workerPrepassSweepFixture(t)

	if _, err := watchShortTermDelta(context.Background(), &cobra.Command{}, opts, repoDir); err != nil {
		t.Fatalf("watch tick: %v", err)
	}

	if _, err := os.Stat(filepath.Join(storage.BrainDir, filepath.FromSlash(sweepable))); err != nil {
		t.Fatalf("background watch tick swept the transcript cache: %v", err)
	}
}
