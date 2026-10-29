package watch

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
	"github.com/entireio/entire-brain/internal/cli/internal/historystore"
)

func TestWatchDirtyRefreshRetriesWithoutCommit(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "first_refresh", true: "existing_context"}[existing], func(t *testing.T) {
			now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
			path := filepath.Join(t.TempDir(), "watch.json")
			var lastSuccess time.Time
			if existing {
				lastSuccess = now.Add(-time.Hour)
				if err := SaveCursor(path, Cursor{LastFingerprint: "old", LastRefreshAt: lastSuccess}); err != nil {
					t.Fatal(err)
				}
			}
			calls, agentCalls := 0, 0
			var out bytes.Buffer
			steps := Steps{
				Now:         func() time.Time { return now },
				Fingerprint: func(context.Context) string { return "same-head" },
				Refresh: func(context.Context) error {
					calls++
					if calls == 1 {
						return errors.New(brainstore.DirtyWorktreeCode + ": uncommitted edits")
					}
					return nil
				},
			}
			flags := DefaultFlags()
			Tick(context.Background(), &out, flags, path, steps, &agentCalls)
			if strings.Contains(out.String(), "[watch] refreshed (deterministic") {
				t.Errorf("blocked refresh reported success: %s", out.String())
			}
			if c := LoadCursor(path); !c.LastRefreshAt.Equal(lastSuccess) {
				t.Errorf("blocked refresh changed last success from %v to %v", lastSuccess, c.LastRefreshAt)
			}
			// Stashing changes and restarting the watcher change neither HEAD nor
			// checkpoint refs. The persisted deferred refresh must still run.
			now = now.Add(time.Hour)
			agentCalls = 0
			out.Reset()
			Tick(context.Background(), &out, flags, path, steps, &agentCalls)
			if calls != 2 || !LoadCursor(path).LastRefreshAt.Equal(now) {
				t.Fatalf("clean tree was not refreshed: calls=%d cursor=%+v output=%s", calls, LoadCursor(path), out.String())
			}
			now = now.Add(time.Hour)
			Tick(context.Background(), &out, flags, path, steps, &agentCalls)
			if calls != 2 || agentCalls != 0 {
				t.Fatalf("completed refresh repeated or spent tokens: refreshes=%d agent calls=%d", calls, agentCalls)
			}
		})
	}
}

func TestWatchDirtyRefreshRetriesAreBoundedWithoutRepeatingAgentWork(t *testing.T) {
	for _, consolidation := range []time.Duration{0, 30 * time.Minute} {
		t.Run(consolidation.String(), func(t *testing.T) {
			now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
			path := filepath.Join(t.TempDir(), "watch.json")
			flags := DefaultFlags()
			flags.ConsolidateEvery = consolidation
			flags.Distill, flags.SeedAgent = true, "codex"
			flags.DistillEvery = 0 // No spend throttle to hide accidental requeueing.
			refreshes, seeds, distills, deltas, syncs, agentCalls := 0, 0, 0, 0, 0, 0
			dirty := true
			steps := Steps{
				Now:         func() time.Time { return now },
				Fingerprint: func(context.Context) string { return "same-head" },
				Delta: func(context.Context) (historystore.ShortTermStats, error) {
					deltas++
					return historystore.ShortTermStats{}, nil
				},
				FactsSync: func(context.Context) (bool, error) { syncs++; return true, nil },
				Refresh: func(context.Context) error {
					refreshes++
					if dirty {
						return errors.New(brainstore.DirtyWorktreeCode + ": uncommitted edits")
					}
					return nil
				},
				Seed:    func(context.Context) error { seeds++; return nil },
				Distill: func(context.Context) error { distills++; return nil },
			}
			var out bytes.Buffer
			tick := func() { Tick(context.Background(), &out, flags, path, steps, &agentCalls) }
			tick()
			agentCalls = 0 // Restart cannot bypass the persisted retry cadence.
			now = now.Add(time.Minute)
			tick()
			if refreshes != 1 {
				t.Fatalf("deferred refresh retried before its cadence: %d", refreshes)
			}
			now = now.Add(time.Hour)
			tick() // Still dirty; retry once without repeating completed agent work.
			if refreshes != 2 || seeds != 1 || distills != 1 || agentCalls != 0 {
				t.Fatalf("retries=%d seeds=%d distills=%d agent calls=%d", refreshes, seeds, distills, agentCalls)
			}
			dirty = false
			now = now.Add(time.Hour)
			tick()
			if refreshes != 3 || seeds != 1 || distills != 1 || agentCalls != 0 {
				t.Fatalf("recovery repeated agent work: refreshes=%d seeds=%d distills=%d agent calls=%d", refreshes, seeds, distills, agentCalls)
			}
			if deltas != 4 || syncs != 3 {
				t.Fatalf("deferred refresh blocked background work: delta=%d sync=%d", deltas, syncs)
			}
		})
	}
}
