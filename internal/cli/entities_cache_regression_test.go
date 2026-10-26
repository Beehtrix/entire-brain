package cli

import (
	"context"
	"errors"
	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
	"os"
	"path/filepath"
	"testing"

	"github.com/entireio/entire-brain/internal/cli/entities"
	"github.com/entireio/entire-brain/internal/cli/internal/base"
)

func TestEntityWarmCacheHonorsPrivacyFailure(t *testing.T) {
	f := newEntityIndexFixture(t)
	f.writeSessionManifest(t)
	f.run(t, "entities", "backfill", "--json")
	p := filepath.Join(f.storage.BrainDir, filepath.FromSlash(brainstore.SessionTombstonesPath))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{broken policy"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := entities.History(context.Background(), entityConfig(f.opts), f.repoDir, "ChargeCard", "", 20); err == nil {
		t.Fatal("warm cache bypassed unreadable privacy policy")
	}
}

func TestEntityCacheIncludesNewSessionJoin(t *testing.T) {
	f := newEntityIndexFixture(t)
	f.run(t, "entities", "backfill", "--json")
	f.writeSessionManifest(t)
	r, err := entities.History(context.Background(), entityConfig(f.opts), f.repoDir, "RefundCard", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Matches) != 1 || len(r.Matches[0].Occurrences) != 1 {
		t.Fatalf("unexpected matches: %+v", r)
	}
	if !base.ContainsString(r.Matches[0].Occurrences[0].SessionIDs, entityFixtureSession) {
		t.Fatalf("warm cache ignored session join: %+v", r)
	}
}

func TestEntityUnknownBranchCannotUnfilter(t *testing.T) {
	f := newEntityIndexFixture(t)
	f.run(t, "entities", "backfill", "--json")
	f.runner.responses[entityFixtureRevListKey("nonexistent")] = fakeCommandResponse{err: errors.New("unknown revision")}
	if _, err := entities.History(context.Background(), entityConfig(f.opts), f.repoDir, "ChargeCard", "nonexistent", 20); err == nil {
		t.Fatal("unresolved branch produced an unfiltered answer")
	}
}
