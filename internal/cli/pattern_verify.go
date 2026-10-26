package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	patterncore "github.com/entireio/entire-brain/internal/cli/patterns"
)

// Pattern consolidation verifier (V2, Phase 4).
//
// An OPTIONAL agent pass that audits a deterministic dossier against its own
// cited evidence and returns a structured verdict. It is the adversarial check
// that rejects conflated or unsupported consolidations before they can become
// skills. It is reachable ONLY through explicit write/maintenance surfaces
// (`patterns verify`), never from refresh/watch/brief/query/workspace, is
// egress-gated, and is cached by evidence fingerprint so unchanged dossiers are
// not re-sent to an agent.

const deepVerifySystemPrompt = `You audit a DEEP CONSOLIDATION RECORD for one recurring pattern in a repository. Unlike a shallow dossier, this is built from a bounded but evidence-deep export of the pattern's backing episodes — corrected/failed episodes first, the top ones carrying redacted transcript excerpts, plus concrete command parameters, verification steps, and failure modes with their recoveries.

You are given the deep dossier as JSON. Judge whether it is a sound, single, non-conflated, repo-specific pattern worth consolidating into durable guidance. Pay special attention to the corrected/failed episodes and their recoveries — a strong pattern's failure modes are instructive, not contradictory. Judge ONLY against the evidence present; do not invent claims.

Return EXACTLY one JSON object and nothing else (no prose, no code fences):
{
  "schema_version": 1,
  "verdict": "accepted" | "rejected" | "needs_split" | "low_confidence",
  "reason": "<one or two sentences>",
  "unsupported_claims": ["<claim not backed by the evidence>"],
  "conflated_subpatterns": ["<distinct sub-pattern if several are conflated>"],
  "required_edits": ["<concrete edit that would make it sound>"],
  "evidence_fingerprint": "<copy the dossier.fingerprint value verbatim>"
}

Verdicts: "accepted" (one coherent, supported, repo-specific pattern), "needs_split" (conflates distinct patterns), "low_confidence" (coherent but thin), "rejected" (generic or unsupported).`

// verifyDeepDossiers builds the bounded evidence-deep dossier for every
// promotable pattern and runs the deep verifier over the ones whose deep
// fingerprint has no
// cached verdict. Egress-gated; cached in deep_dossiers; never called from
// refresh/watch/brief/query/workspace.
func verifyDeepDossiers(ctx context.Context, db *sql.DB, brainDir, repoDir, agent, model, effort string, run base.DistillAgentRunner, now time.Time) (patterncore.VerifyStats, error) {
	var stats patterncore.VerifyStats
	if err := base.RejectAgentForNoEgress(agent); err != nil {
		return stats, err
	}
	args, err := base.DistillAgentCommandArgs(agent, nil, deepVerifySystemPrompt)
	if err != nil {
		return stats, err
	}
	args = base.InjectAgentModel(args, agent, model)
	args = base.InjectAgentEffort(args, agent, effort)

	// Deep verification targets the patterns that already cleared the shallow gate.
	rows, err := db.Query(`SELECT pattern_id FROM dossiers ORDER BY pattern_id`)
	if err != nil {
		return stats, err
	}
	var patternIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return stats, err
		}
		patternIDs = append(patternIDs, id)
	}
	rows.Close()

	ts := now.UTC().Format(time.RFC3339)
	for _, pid := range patternIDs {
		stats.Considered++
		deep, err := buildDeepDossier(db, brainDir, pid)
		if err != nil {
			stats.Failed++
			continue
		}
		var cachedFP, cachedVerdict, cachedVerifier string
		_ = db.QueryRow(`SELECT fingerprint, COALESCE(verdict,''), COALESCE(verifier_json_redacted,'') FROM deep_dossiers WHERE pattern_id=?`, pid).Scan(&cachedFP, &cachedVerdict, &cachedVerifier)
		blob, err := json.Marshal(deep)
		if err != nil {
			stats.Failed++
			continue
		}
		jsonRedacted := base.RedactText(string(blob))
		var prior patterncore.DossierVerdict
		cacheBound := json.Unmarshal([]byte(cachedVerifier), &prior) == nil && prior.Verdict == cachedVerdict && prior.EvidenceFingerprint == deep.Fingerprint
		if cachedFP == deep.Fingerprint && cachedVerdict != "" && cacheBound {
			// Refresh the deterministic export but keep the cached verdict.
			if _, err := db.Exec(`UPDATE deep_dossiers SET json_redacted=?, status='current', updated_at=? WHERE pattern_id=?`,
				jsonRedacted, ts, pid); err != nil {
				return stats, err
			}
			stats.Cached++
			continue
		}
		verdict, raw, err := patterncore.RunDeepVerifier(ctx, repoDir, args, jsonRedacted, deep.Fingerprint, run)
		if err != nil {
			stats.Failed++
			continue
		}
		if _, err := db.Exec(`INSERT INTO deep_dossiers
			(pattern_id, fingerprint, json_redacted, verifier_json_redacted, verdict, status, created_at, updated_at)
			VALUES (?,?,?,?,?,'current',?,?)
			ON CONFLICT(pattern_id) DO UPDATE SET fingerprint=excluded.fingerprint, json_redacted=excluded.json_redacted,
			    verifier_json_redacted=excluded.verifier_json_redacted, verdict=excluded.verdict, status='current', updated_at=excluded.updated_at`,
			pid, deep.Fingerprint, jsonRedacted, base.RedactText(raw), verdict.Verdict, ts, ts); err != nil {
			return stats, err
		}
		stats.Verified++
	}
	return stats, nil
}
