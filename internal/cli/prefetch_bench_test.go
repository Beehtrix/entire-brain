package cli

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type slowRunner struct{ d time.Duration }

func (s slowRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
	time.Sleep(s.d)
	id := args[len(args)-1]
	return []byte(fmt.Sprintf(`{"checkpoint_id":%q,"sessions":[]}`, id)), nil, nil
}

// TestPrefetchIsFasterThanSerial proves the fan-out HAPPENS. It does not
// predict the real-world gain: its "work" is a sleep, so workers overlap
// perfectly and it reports ~6x. Measured against real `checkpoint explain`
// children, which contend for CPU and IO, the gain is ~2.2x (64 distinct
// child PIDs in 30s = 2.13/sec, against 0.97/sec serial). Quote 2.2x.
func TestPrefetchIsFasterThanSerial(t *testing.T) {
	const n = 200
	const per = 5 * time.Millisecond
	cps := make([]checkpointListEntry, n)
	for i := range cps {
		cps[i] = checkpointListEntry{CheckpointID: fmt.Sprintf("01K%023d", i), IsLogsOnly: true}
	}
	r := slowRunner{d: per}

	start := time.Now()
	got := prefetchCheckpointDetails(context.Background(), r, "/repo", "entire", cps, nil)
	parallel := time.Since(start)

	if len(got) != n {
		t.Fatalf("want %d results, got %d", n, len(got))
	}
	// Order must be preserved: result i must be checkpoint i.
	for i := range got {
		if got[i].err != nil {
			t.Fatalf("result %d errored: %v", i, got[i].err)
		}
		if got[i].detail.CheckpointID != cps[i].CheckpointID {
			t.Fatalf("result %d is out of order: got %q want %q", i, got[i].detail.CheckpointID, cps[i].CheckpointID)
		}
	}
	serial := time.Duration(n) * per
	t.Logf("serial would be %v; parallel took %v (%.1fx)", serial, parallel, float64(serial)/float64(parallel))
	if parallel >= serial {
		t.Fatalf("fan-out gave no speedup: %v vs serial %v", parallel, serial)
	}
}
