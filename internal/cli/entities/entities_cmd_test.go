package entities

import (
	"testing"
	"time"

	"github.com/entireio/entire-brain/factmerge"
)

// TestEntityProvenanceIsDeterministicOnTimestampTies pins the resolution
// ORDER. Committer dates resolve to one second, so two commits made in the same
// second are a routine tie — and the resolver reaches them by ranging over a Go
// map, whose iteration order is randomized per run. Keeping the first-seen
// candidate on a tie therefore made identical inputs produce different anchors
// from one distill to the next. The order has to be total.
func TestEntityProvenanceIsDeterministicOnTimestampTies(t *testing.T) {
	t.Parallel()
	const (
		session = "session-tie"
		lowSHA  = "1111111111111111111111111111111111111111"
		highSHA = "9999999999999999999999999999999999999999"
	)
	tie := time.Date(2026, 7, 4, 9, 30, 15, 0, time.UTC)
	// Two commits in the SAME SECOND, of the same session, each changing an
	// entity that the single locus token "chargecard" names.
	occurrences := map[string]IndexOccurrence{
		"internal/pay/a.go#function#ChargeCard": {Commit: lowSHA, CheckpointIDs: []string{"aaaaaaaaaaaa"}, SessionIDs: []string{session}, CommittedAt: &tie},
		"internal/pay/b.go#function#ChargeCard": {Commit: highSHA, CheckpointIDs: []string{"zzzzzzzzzzzz"}, SessionIDs: []string{session}, CommittedAt: &tie},
	}
	keys := []string{"internal/pay/a.go#function#ChargeCard", "internal/pay/b.go#function#ChargeCard"}
	record := factmerge.Record{
		Text:       "`ChargeCard` must be idempotent.",
		Branch:     "main",
		Locus:      []string{"chargecard"},
		Provenance: []factmerge.Anchor{{SessionID: session, CheckpointID: "cccccccccccc", Commit: "ffffffffffffffffffffffffffffffffffffffff"}},
	}

	var first factmerge.Anchor
	for round := 0; round < 64; round++ {
		// A FRESH map every round, with the entries inserted in alternating
		// order, so neither insertion order nor Go's randomized range order is
		// held constant across rounds.
		cache := IndexCache{SchemaVersion: CacheVersion, Entries: map[string][]IndexOccurrence{}}
		order := keys
		if round%2 == 1 {
			order = []string{keys[1], keys[0]}
		}
		for _, key := range order {
			cache.Entries[key] = []IndexOccurrence{occurrences[key]}
		}
		resolver := buildEntityProvenanceResolver(cache, map[string]string{session: "main"}, nil)
		if resolver == nil {
			t.Fatal("resolver is nil despite a populated cache")
		}
		got := ApplyProvenance([]factmerge.Record{record}, resolver)[0].Provenance[0]
		if round == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("round %d resolved %+v, round 0 resolved %+v: the same input produced two different anchors", round, got, first)
		}
	}
	// And the tie-break is the documented one, not just "stable by accident".
	if first.Commit != highSHA || first.CheckpointID != "zzzzzzzzzzzz" {
		t.Fatalf("tie resolved to %+v, want the larger commit sha (%s)", first, highSHA)
	}
}
