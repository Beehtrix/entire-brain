package cli

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDetailClock advances only when a fake explain call runs, so breaker
// tests measure the modeled read time instead of sleeping through it.
type fakeDetailClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeDetailClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeDetailClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// slowExplainRunner models the entireio/cli#2902 host: every `checkpoint
// explain` SUCCEEDS but only after the length of the host's metadata-fetch
// timeout, which it models by advancing the shared fake clock.
type slowExplainRunner struct {
	*explainFanoutRunner
	clock *fakeDetailClock
	cost  time.Duration
}

func (r *slowExplainRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
	r.clock.Advance(r.cost)
	return r.explainFanoutRunner.Run(ctx, dir, name, args...)
}

func TestCheckpointDetailBreakerTripsOnConsecutiveSlowReads(t *testing.T) {
	t.Parallel()
	clock := &fakeDetailClock{}
	breaker := newCheckpointDetailBreaker()
	breaker.now = clock.Now

	slowRead := func() (checkpointExportEnvelope, error) {
		clock.Advance(checkpointDetailSlowRead)
		return checkpointExportEnvelope{CheckpointID: "cp"}, nil
	}
	fastRead := func() (checkpointExportEnvelope, error) {
		return checkpointExportEnvelope{CheckpointID: "cp"}, nil
	}

	for i := 0; i < checkpointDetailSlowTrip-1; i++ {
		if _, err := breaker.do(slowRead); err != nil {
			t.Fatalf("slow read %d must still pass through, got %v", i, err)
		}
	}
	// A fast read between slow ones resets the run: slowness must be
	// consecutive to indict the host rather than one oversized checkpoint.
	if _, err := breaker.do(fastRead); err != nil {
		t.Fatalf("fast read must pass through, got %v", err)
	}
	for i := 0; i < checkpointDetailSlowTrip; i++ {
		if _, err := breaker.do(slowRead); err != nil {
			t.Fatalf("slow read %d before the trip must pass through, got %v", i, err)
		}
	}
	if _, err := breaker.do(fastRead); !errors.Is(err, errCheckpointDetailBreakerOpen) {
		t.Fatalf("after %d consecutive slow reads the breaker must be open, got %v", checkpointDetailSlowTrip, err)
	}

	var nilBreaker *checkpointDetailBreaker
	if _, err := nilBreaker.do(fastRead); err != nil {
		t.Fatalf("a nil breaker must pass reads through, got %v", err)
	}
}

// TestRoutedDetailSelectionStopsAskingASlowHost: with every explain taking the
// length of the host CLI's metadata-fetch timeout (entireio/cli#2902: 319
// routed checkpoints x 120s reads as a hang), the sequential selection must
// stop asking after the trip, keep what it read, and fold every skipped
// checkpoint into one warning instead of 316 identical lines.
func TestRoutedDetailSelectionStopsAskingASlowHost(t *testing.T) {
	t.Parallel()
	const n = 10
	checkpoints, details := fanoutCheckpointFixture(n)
	clock := &fakeDetailClock{}
	inner := newExplainFanoutRunner(details)
	inner.concurrent = false
	runner := &slowExplainRunner{explainFanoutRunner: inner, clock: clock, cost: checkpointDetailSlowRead}

	breaker := newCheckpointDetailBreaker()
	breaker.now = clock.Now
	selected, warnings, complete, err := selectRoutedCheckpointSessionsWithBreaker(
		context.Background(), runner, t.TempDir(), "entire", checkpoints, checkpointBranchDestinations{}, nil, nil, breaker)
	if err != nil {
		t.Fatalf("selectRoutedCheckpointSessions: %v", err)
	}
	if complete {
		t.Fatal("a selection that skipped checkpoints must not report complete")
	}
	calls, _ := runner.observed()
	if calls != checkpointDetailSlowTrip {
		t.Fatalf("the host must be asked exactly %d times before the trip, got %d", checkpointDetailSlowTrip, calls)
	}
	if len(selected) != checkpointDetailSlowTrip {
		t.Fatalf("the reads taken before the trip must be kept: selected %d, want %d", len(selected), checkpointDetailSlowTrip)
	}
	var breakerWarnings []string
	for _, w := range warnings {
		if strings.Contains(w, "slow") || strings.Contains(w, "2902") {
			breakerWarnings = append(breakerWarnings, w)
		}
	}
	if len(breakerWarnings) != 1 {
		t.Fatalf("skipped checkpoints must fold into ONE warning, got %d:\n%s", len(breakerWarnings), strings.Join(warnings, "\n"))
	}
	wantSkipped := n - checkpointDetailSlowTrip
	if !strings.Contains(breakerWarnings[0], checkpointScopeIncompleteCode) {
		t.Fatalf("the breaker warning must carry the scope-incomplete code: %q", breakerWarnings[0])
	}
	if !strings.Contains(breakerWarnings[0], "7") || wantSkipped != 7 {
		t.Fatalf("the breaker warning must count the %d skipped checkpoints: %q", wantSkipped, breakerWarnings[0])
	}
}

// The fan-out must stop asking too: once the breaker opens, the remaining
// queue drains without spawning children, so a slow host costs at most one or
// two batches instead of every checkpoint.
func TestRoutedDetailFanoutStopsAskingASlowHost(t *testing.T) {
	t.Parallel()
	const n = 40
	checkpoints, details := fanoutCheckpointFixture(n)
	clock := &fakeDetailClock{}
	runner := &slowExplainRunner{explainFanoutRunner: newExplainFanoutRunner(details), clock: clock, cost: checkpointDetailSlowRead}

	breaker := newCheckpointDetailBreaker()
	breaker.now = clock.Now
	selected, warnings, complete, err := selectRoutedCheckpointSessionsWithBreaker(
		context.Background(), runner, t.TempDir(), "entire", checkpoints, checkpointBranchDestinations{}, nil, nil, breaker)
	if err != nil {
		t.Fatalf("selectRoutedCheckpointSessions: %v", err)
	}
	if complete {
		t.Fatal("a selection that skipped checkpoints must not report complete")
	}
	calls, _ := runner.observed()
	// Workers already past the open check may finish their one read, so the
	// bound is a couple of batches, never the whole list.
	if maxCalls := 2 * checkpointDetailFanoutLimit() * checkpointDetailSlowTrip; calls > maxCalls {
		t.Fatalf("a tripped breaker must stop the fan-out: %d of %d checkpoints were still asked (bound %d)", calls, n, maxCalls)
	}
	if len(selected) >= n {
		t.Fatalf("a tripped breaker cannot have selected every checkpoint: %d of %d", len(selected), n)
	}
	breakerWarnings := 0
	for _, w := range warnings {
		if strings.Contains(w, "2902") {
			breakerWarnings++
		}
	}
	if breakerWarnings != 1 {
		t.Fatalf("skipped checkpoints must fold into ONE warning, got %d:\n%s", breakerWarnings, strings.Join(warnings, "\n"))
	}
}

// The persistent-export refusal must name the slow host and say the brain is
// untouched; the generic wording sends the reader to repair checkpoints that
// are not broken. Both variants must keep the --output escape hatch.
func TestRoutedMetadataRefusalNamesTheSlowHost(t *testing.T) {
	t.Parallel()
	opened := routedMetadataRefusal(true, []string{"w1"}).Error()
	for _, want := range []string{"2902", "unchanged", "--output", checkpointScopeIncompleteCode} {
		if !strings.Contains(opened, want) {
			t.Fatalf("breaker refusal missing %q: %s", want, opened)
		}
	}
	if strings.Contains(opened, "repair unreadable") {
		t.Fatalf("breaker refusal must not prescribe per-checkpoint repair: %s", opened)
	}
	generic := routedMetadataRefusal(false, []string{"w1"}).Error()
	for _, want := range []string{"repair unreadable", "--output", checkpointScopeIncompleteCode} {
		if !strings.Contains(generic, want) {
			t.Fatalf("generic refusal missing %q: %s", want, generic)
		}
	}
}
