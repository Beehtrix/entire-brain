package factsync

import (
	"testing"
	"time"

	"github.com/entireio/entire-brain/factmerge"
)

// anchoredFact builds a fact whose single provenance anchor carries BOTH opaque
// cross-member ids (session, commit, checkpoint) and LOCAL filesystem coordinates
// (transcript path + line) — the shape SanitizeForEgress must split.
func anchoredFact(text string, paths []string, a factmerge.Anchor, now time.Time) factmerge.Record {
	np := factmerge.NormalizePaths(paths)
	return factmerge.Record{
		ID:         factmerge.RecordID(text, np),
		Paths:      np,
		Text:       text,
		Branch:     "main",
		Origin:     "distilled",
		Status:     factmerge.StatusActive,
		Provenance: []factmerge.Anchor{a},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// TestSanitizeStripsLocalPathsButKeepsOpaqueProvenance proves the no-leak
// guarantee the push path relies on (pushHostedFacts sanitizes before every
// upsert): the local transcript path + line are REDACTED, the opaque
// cross-member ids (session, commit, checkpoint) survive, and the caller's own
// in-memory records are untouched.
func TestSanitizeStripsLocalPathsButKeepsOpaqueProvenance(t *testing.T) {
	now := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	const localPath = "sessions/2026-07-07/Users-local-user-secret/transcript.jsonl"
	local := []factmerge.Record{anchoredFact(
		"ci runs on self-hosted runners", []string{"ci.runner.host"},
		factmerge.Anchor{SessionID: "sess-1", Commit: "deadbeef", CheckpointID: "ckpt-9", Transcript: localPath, Line: 42},
		now,
	)}

	out := SanitizeForEgress(local)
	if len(out) != 1 || len(out[0].Provenance) != 1 {
		t.Fatalf("sanitized = %d recs; want 1 with 1 anchor", len(out))
	}
	a := out[0].Provenance[0]
	if a.Transcript != "" || a.Line != 0 {
		t.Fatalf("local coords not redacted: transcript=%q line=%d", a.Transcript, a.Line)
	}
	if a.SessionID != "sess-1" || a.Commit != "deadbeef" || a.CheckpointID != "ckpt-9" {
		t.Fatalf("opaque cross-member ids not preserved: %+v", a)
	}
	// The caller's own local slice must be untouched (still has the path locally).
	if local[0].Provenance[0].Transcript != localPath {
		t.Fatalf("SanitizeForEgress mutated the caller's local facts: %q", local[0].Provenance[0].Transcript)
	}
}

func TestSanitizeForEgressClearsClientClaimedAuthor(t *testing.T) {
	in := []factmerge.Record{{ID: "fact:x", Text: "t", Paths: []string{"p.q"}, Author: "mallory"}}
	out := SanitizeForEgress(in)
	if out[0].Author != "" {
		t.Fatalf("attribution is server-stamped; a client-claimed author must never egress, got %q", out[0].Author)
	}
	if in[0].Author != "mallory" {
		t.Fatal("the caller's in-memory record must not be mutated")
	}
}

func TestSanitizeForEgressOmitsCandidatesButKeepsRetirements(t *testing.T) {
	now := time.Now().UTC()
	var records []factmerge.Record
	for _, status := range []string{factmerge.StatusActive, factmerge.StatusCandidate, factmerge.StatusRetracted, factmerge.StatusSuperseded} {
		r := anchoredFact(status, []string{"workflow.testing.rules"}, factmerge.Anchor{SessionID: status}, now)
		r.Status = status
		records = append(records, r)
	}
	got := SanitizeForEgress(records)
	if len(got) != 3 || records[1].Status != factmerge.StatusCandidate {
		t.Fatalf("egress did not filter independently: %+v", got)
	}
	for _, r := range got {
		if r.Status == factmerge.StatusCandidate {
			t.Fatal("pending candidate uploaded")
		}
	}
}
