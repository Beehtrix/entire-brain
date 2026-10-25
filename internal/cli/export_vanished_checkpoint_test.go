package cli

import (
	"context"
	"strings"
	"testing"
)

// vanishedFixture builds a snapshot that can serve one session's transcript
// and knows nothing about another — the shape a squash-merged repository has,
// where a manual-commit checkpoint names a commit its branch deletion
// destroyed.
// vanishedRunner answers the object-type probe the validator makes for a
// transcript chunk it can see, so "available" really means available.
func vanishedRunner() *fakeCommandRunner {
	return &fakeCommandRunner{fallback: func(name string, args []string) (fakeCommandResponse, bool) {
		for _, arg := range args {
			if arg == "cat-file" {
				return fakeCommandResponse{stdout: "blob\n"}, true
			}
		}
		return fakeCommandResponse{}, false
	}}
}

func vanishedFixture() (*checkpointSnapshot, []exportSession) {
	snap := &checkpointSnapshot{
		TranscriptMode:     "raw",
		TranscriptFileName: "full.jsonl",
		SessionCheckpoints: []sessionCheckpoint{{
			CheckpointID: "aaa111aaa111", SessionID: "alive", SessionIndex: 0,
			TranscriptPath: "checkpoints/aaa111aaa111/0/full.jsonl", SourceKey: "src",
		}},
		// The source must actually carry the transcript path, or
		// snapshotTranscriptChunks finds no chunks and EVERY session reads as
		// unavailable. A first version of this fixture left Sources empty and
		// the "nothing available" test below passed for that reason rather
		// than for the reason it claims.
		Sources: map[string]checkpointSnapshotSource{
			"src": {
				GitDir:    "/repo/.git",
				Ref:       "refs/heads/entire/checkpoints/v1",
				TreePaths: map[string]struct{}{"checkpoints/aaa111aaa111/0/full.jsonl": {}},
			},
		},
	}
	sessions := []exportSession{
		{SessionID: "alive", LatestCheckpoint: "aaa111aaa111", SessionIndex: 0, Branch: "main"},
		{SessionID: "6772544b-bdcb-4b07-be35-c46c1a37a1a4", LatestCheckpoint: "fd86c6e4bdb2", SessionIndex: 0, Branch: "main"},
	}
	return snap, sessions
}

// One session whose bytes are gone must cost its own session and nothing else.
// It used to refuse the entire export, which on a squash-merging repository is
// permanent: the commit a manual-commit checkpoint names is destroyed when its
// branch is deleted, so no fetch recovers it and refusing preserves nothing
// while discarding every available session.
func TestVanishedCheckpointIsPartitionedOutNotFatal(t *testing.T) {
	t.Parallel()
	snap, sessions := vanishedFixture()
	usable, unavailable, err := partitionLocalTranscriptSessions(context.Background(), vanishedRunner(), snap, sessions, "raw")
	if err != nil {
		t.Fatalf("partition returned a hard error for a merely-unavailable session: %v", err)
	}
	if len(unavailable) != 1 {
		t.Fatalf("unavailable = %d, want 1: %v", len(unavailable), unavailable)
	}
	if !strings.Contains(unavailable[0], "fd86c6e4bdb2") {
		t.Fatalf("the diagnostic does not name the checkpoint whose bytes are gone: %s", unavailable[0])
	}
	if len(usable) != 1 || usable[0].SessionID != "alive" {
		t.Fatalf("usable = %+v, want just the session whose transcript is present", usable)
	}
}

// The all-or-nothing contract is kept for the caller that wants it, so nothing
// that relied on the old behaviour changes shape.
func TestValidateLocalTranscriptSessionsStillFailsOnTheFirstUnavailable(t *testing.T) {
	t.Parallel()
	snap, sessions := vanishedFixture()
	err := validateLocalTranscriptSessions(context.Background(), vanishedRunner(), snap, sessions, "raw")
	if err == nil {
		t.Fatal("the strict wrapper stopped reporting an unavailable session")
	}
	if !strings.Contains(err.Error(), "fd86c6e4bdb2") {
		t.Fatalf("the strict wrapper lost the detail: %v", err)
	}
}

// Nothing available is the source-went-unreadable case, and there the right
// answer really is to stop: republishing an empty corpus would erase an
// indexed brain. Partitioning must therefore leave zero usable sessions so the
// caller can still refuse.
func TestNothingAvailableLeavesNoUsableSessions(t *testing.T) {
	t.Parallel()
	snap, sessions := vanishedFixture()
	snap.SessionCheckpoints = nil // the source knows about nothing at all
	usable, unavailable, err := partitionLocalTranscriptSessions(context.Background(), vanishedRunner(), snap, sessions, "raw")
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if len(usable) != 0 {
		t.Fatalf("usable = %d, want 0 so the caller can refuse", len(usable))
	}
	if len(unavailable) != 2 {
		t.Fatalf("unavailable = %d, want both sessions", len(unavailable))
	}
}

// Every session available must partition to exactly the input, with no
// diagnostics — the common case must not change at all.
func TestEverythingAvailablePartitionsUnchanged(t *testing.T) {
	t.Parallel()
	snap, sessions := vanishedFixture()
	only := sessions[:1]
	usable, unavailable, err := partitionLocalTranscriptSessions(context.Background(), vanishedRunner(), snap, only, "raw")
	if err != nil {
		t.Fatal(err)
	}
	if len(unavailable) != 0 {
		t.Fatalf("a fully available selection reported %d unavailable: %v", len(unavailable), unavailable)
	}
	if len(usable) != 1 {
		t.Fatalf("usable = %d, want 1", len(usable))
	}
}

// The threshold itself, pinned as a value. A weakening sweep showed that
// changing it back to "refuse if anything is unavailable" broke no test,
// because every other guard here exercises the partition function rather than
// the caller's decision. This is that decision.
func TestUnavailableSessionsAreFatalOnlyWhenNothingIsServable(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		usable, unavailable int
		fatal               bool
	}{
		"nothing servable at all":        {usable: 0, unavailable: 1, fatal: true},
		"nothing servable, many missing": {usable: 0, unavailable: 2561, fatal: true},
		"one vanished among many":        {usable: 2560, unavailable: 1, fatal: false},
		"half missing":                   {usable: 1, unavailable: 1, fatal: false},
		"everything available":           {usable: 2561, unavailable: 0, fatal: false},
		"empty selection":                {usable: 0, unavailable: 0, fatal: false},
	} {
		if got := unavailableSessionsAreFatal(c.usable, c.unavailable); got != c.fatal {
			t.Errorf("%s: usable=%d unavailable=%d -> fatal=%v, want %v", name, c.usable, c.unavailable, got, c.fatal)
		}
	}
}
