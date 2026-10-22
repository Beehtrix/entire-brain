package factmerge

import (
	"fmt"
	"sort"
	"strings"
)

// Sort orders records by path, then text, then id so the on-disk ndjson is
// deterministic regardless of insertion order.
func Sort(records []Record) {
	sort.Slice(records, func(i, j int) bool {
		li, lj := strings.Join(records[i].Paths, ","), strings.Join(records[j].Paths, ",")
		if li != lj {
			return li < lj
		}
		if records[i].Text != records[j].Text {
			return records[i].Text < records[j].Text
		}
		return records[i].ID < records[j].ID
	})
}

// Upsert inserts incoming into the set keyed by id, or — when a fact with
// the same id already exists — unions the new provenance anchors into the
// existing fact and advances its UpdatedAt. Exact duplicates (same normalized
// text and paths) therefore collapse for free, which is what makes re-distilling
// a turn idempotent. It returns the updated set; the input slice may be reused.
//
// DedupByID collapses records that repeat an id into one record per id,
// keeping first position and folding each repeat in through Upsert's merge
// rules (anchor union, newest UpdatedAt, kind/author fill), so nothing a
// duplicate carried is dropped. It returns the collapsed set and how many
// records were folded away. An id is defined to identify exactly one record;
// a store that violates that (e.g. after a writer concatenated two copies of
// itself) is repaired losslessly by this pass.
//
// Lifecycle is folded one-way: when any copy says superseded or retracted,
// the survivor does too, whatever order the file held them in. A heal must
// never revive a fact a later write retired — retraction is a privacy
// surface — so active never wins over non-active. Between differing
// non-active copies the first stands; the remaining fields follow Upsert's
// ordinary first-copy-wins policy, the same one every live writer applies.
func DedupByID(records []Record) ([]Record, int) {
	index := make(map[string]int, len(records))
	out := make([]Record, 0, len(records))
	folded := 0
	for _, rec := range records {
		i, ok := index[rec.ID]
		if !ok {
			index[rec.ID] = len(out)
			out = append(out, rec)
			continue
		}
		Upsert(out[i:i+1], rec)
		if out[i].Status == StatusActive && rec.Status != StatusActive && rec.Status != "" {
			out[i].Status = rec.Status
			out[i].SupersededBy = rec.SupersededBy
		}
		folded++
	}
	return out, folded
}

// Upsert deliberately does not implement merge/supersede across *different*
// ids: that is the agent's judgment during distillation, handled separately.
func Upsert(records []Record, incoming Record) []Record {
	for i := range records {
		if records[i].ID != incoming.ID {
			continue
		}
		records[i].Provenance = UnionAnchors(records[i].Provenance, incoming.Provenance)
		if incoming.UpdatedAt.After(records[i].UpdatedAt) {
			records[i].UpdatedAt = incoming.UpdatedAt
		}
		// Fill Kind from a re-distill when the stored fact lacks a valid one, but
		// don't overwrite an existing valid kind (avoids thrash between an
		// agent-labeled and an inferred value across runs).
		if !ValidFactKind(records[i].Kind) && ValidFactKind(incoming.Kind) {
			records[i].Kind = incoming.Kind
		}
		// Author: a non-empty incoming value wins — its only writer is the
		// hosted pull stamping server-side attribution, which is authoritative
		// (a server-side handle correction must propagate). An empty incoming
		// value keeps the stored handle so a local re-distill or a server
		// without the authors map never erases attribution.
		if incoming.Author != "" {
			records[i].Author = incoming.Author
		}
		return records
	}
	return append(records, incoming)
}

// UnionAnchors appends anchors from b that are not already present in a,
// preserving order. Anchors are compared on their identifying fields so the
// same source turn is never recorded twice.
func UnionAnchors(a, b []Anchor) []Anchor {
	seen := make(map[string]struct{}, len(a)+len(b))
	key := func(anchor Anchor) string {
		return strings.Join([]string{anchor.SessionID, anchor.Commit, anchor.CheckpointID, anchor.TurnID, anchor.Transcript, fmt.Sprint(anchor.Line)}, "\x00")
	}
	out := make([]Anchor, 0, len(a)+len(b))
	for _, anchor := range a {
		k := key(anchor)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, anchor)
	}
	for _, anchor := range b {
		k := key(anchor)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, anchor)
	}
	return out
}
