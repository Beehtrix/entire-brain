package cli

import "os"

// observedScale reports whether fixture-heavy tests should build the recorded
// production-cohort sizes. The default run scales those fixtures down: every
// OS and every Windows shard was paying ~35s per run re-proving behavior at
// sizes that only change when the cohort is re-recorded. The nightly workflow
// sets ENTIRE_BRAIN_OBSERVED_SCALE=1 to keep the true-size soak.
// ponytail: the scaled-down default proves shapes and cap relations, not
// behavior at observed scale — that coverage now lives only in the nightly.
func observedScale() bool { return os.Getenv("ENTIRE_BRAIN_OBSERVED_SCALE") == "1" }
