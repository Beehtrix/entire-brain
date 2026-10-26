package patterns

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
)

const DossierVerifyTimeout = 240 * time.Second

const DossierVerifySchemaVersion = 1

const dossierVerifySystemPrompt = `You audit a CONSOLIDATION RECORD (a "dossier") that was assembled deterministically from a code agent's session history for one recurring pattern in a repository.

You are given the dossier as JSON: its trigger, workflow (the actual commands run), variants, verification steps, failure modes, the durable facts that corroborate it, and exact source anchors.

Your job is to decide whether this dossier is a sound, single, non-conflated pattern worth consolidating — NOT to rewrite it. Judge ONLY against the evidence present. Do not invent claims.

Return EXACTLY one JSON object and nothing else (no prose, no code fences):
{
  "schema_version": 1,
  "verdict": "accepted" | "rejected" | "needs_split" | "low_confidence",
  "reason": "<one or two sentences>",
  "unsupported_claims": ["<claim in the dossier not backed by its evidence>"],
  "conflated_subpatterns": ["<distinct sub-pattern if the dossier conflates several>"],
  "required_edits": ["<concrete edit that would make it sound>"],
  "evidence_fingerprint": "<copy the dossier.fingerprint value verbatim>"
}

Verdicts:
- "accepted": one coherent pattern, claims supported, worth consolidating.
- "needs_split": the workflow/variants conflate two or more distinct patterns.
- "low_confidence": coherent but thin/weak evidence.
- "rejected": generic or unsupported — nothing repo-specific worth keeping.`

type DossierVerdict struct {
	SchemaVersion        int      `json:"schema_version"`
	Verdict              string   `json:"verdict"`
	Reason               string   `json:"reason"`
	UnsupportedClaims    []string `json:"unsupported_claims"`
	ConflatedSubpatterns []string `json:"conflated_subpatterns"`
	RequiredEdits        []string `json:"required_edits"`
	EvidenceFingerprint  string   `json:"evidence_fingerprint"`
}

// VerifyDossiers runs the agent verifier over the promotable dossiers that need
// it (no cached verdict for the current evidence fingerprint). Egress-gated and
// cached; deterministic dossiers themselves are never mutated here.
func VerifyDossiers(ctx context.Context, db *sql.DB, brainDir, repoDir, agent, model, effort string, run base.DistillAgentRunner, now time.Time) (VerifyStats, error) {
	var stats VerifyStats
	if err := base.RejectAgentForNoEgress(agent); err != nil {
		return stats, err
	}
	args, err := base.DistillAgentCommandArgs(agent, nil, dossierVerifySystemPrompt)
	if err != nil {
		return stats, err
	}
	args = base.InjectAgentModel(args, agent, model)
	args = base.InjectAgentEffort(args, agent, effort)

	rows, err := db.Query(`SELECT pattern_id, fingerprint, json_redacted, COALESCE(verified_fingerprint,'') FROM dossiers ORDER BY pattern_id`)
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	type todo struct {
		patternID, fingerprint, jsonRedacted, verified string
	}
	var work []todo
	for rows.Next() {
		var t todo
		if err := rows.Scan(&t.patternID, &t.fingerprint, &t.jsonRedacted, &t.verified); err != nil {
			return stats, err
		}
		work = append(work, t)
	}
	if err := rows.Err(); err != nil {
		return stats, err
	}
	rows.Close()

	ts := now.UTC().Format(time.RFC3339)
	for _, t := range work {
		stats.Considered++
		if t.verified == t.fingerprint && t.verified != "" {
			stats.Cached++
			continue
		}
		verdict, raw, err := runDossierVerifier(ctx, repoDir, args, t.jsonRedacted, t.fingerprint, run)
		if err != nil {
			stats.Failed++
			continue
		}
		if _, err := db.Exec(`UPDATE dossiers
			SET verifier_json_redacted=?, verdict=?, verified_fingerprint=?, status='current', updated_at=?
			WHERE pattern_id=?`, base.RedactText(raw), verdict.Verdict, t.fingerprint, ts, t.patternID); err != nil {
			return stats, err
		}
		stats.Verified++
	}
	return stats, nil
}

func RunDeepVerifier(ctx context.Context, repoDir string, args []string, deepJSON, fingerprint string, run base.DistillAgentRunner) (DossierVerdict, string, error) {
	input := base.RedactText(deepJSON)
	out, err := run(ctx, repoDir, args, []byte(input), DossierVerifyTimeout)
	if err != nil {
		return DossierVerdict{}, "", fmt.Errorf("deep verifier agent: %w", err)
	}
	v, _, err := ParseDossierVerdict(out)
	if err != nil {
		return DossierVerdict{}, "", err
	}
	v.EvidenceFingerprint = fingerprint
	raw, err := json.Marshal(v)
	return v, string(raw), err
}

func runDossierVerifier(ctx context.Context, repoDir string, args []string, dossierJSON, fingerprint string, run base.DistillAgentRunner) (DossierVerdict, string, error) {
	// Redaction boundary: the dossier is already redacted at rest, but redact
	// again defensively before it leaves the brain as agent input.
	input := base.RedactText(dossierJSON)
	out, err := run(ctx, repoDir, args, []byte(input), DossierVerifyTimeout)
	if err != nil {
		return DossierVerdict{}, "", fmt.Errorf("verifier agent: %w", err)
	}
	v, raw, err := ParseDossierVerdict(out)
	if err != nil {
		return DossierVerdict{}, "", err
	}
	// Pin the fingerprint to the evidence actually verified, regardless of what
	// the agent echoed back.
	v.EvidenceFingerprint = fingerprint
	return v, raw, nil
}

func ParseDossierVerdict(out string) (DossierVerdict, string, error) {
	raw := strings.TrimSpace(out)
	if i := strings.IndexByte(raw, '{'); i >= 0 {
		if j := strings.LastIndexByte(raw, '}'); j >= i {
			raw = raw[i : j+1]
		}
	}
	var v DossierVerdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return DossierVerdict{}, "", fmt.Errorf("parse verifier output: %w", err)
	}
	v.Verdict = strings.ToLower(strings.TrimSpace(v.Verdict))
	if !AllowedVerdicts[v.Verdict] {
		return DossierVerdict{}, "", fmt.Errorf("invalid verdict %q", v.Verdict)
	}
	if v.SchemaVersion == 0 {
		v.SchemaVersion = DossierVerifySchemaVersion
	}
	return v, raw, nil
}
