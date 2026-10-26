package patterns

import (
	"database/sql"
	"encoding/json"
	"sort"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
)

type ReinforcementCounts struct {
	Success   int `json:"success"`
	Corrected int `json:"corrected"`
	Neutral   int `json:"neutral"`
}
type EpisodeAnchor struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// PatternRepoStat is one repo's contribution to a workspace pattern.
type PatternRepoStat struct {
	RepoKey       string              `json:"repo_key"`
	Support       int                 `json:"support"`
	Reinforcement ReinforcementCounts `json:"reinforcement,omitempty"`
}

// TaskProcedure is a command shape (n-gram) that co-occurs within a task's
// episodes, with its corpus specificity — the "non-obvious operational evidence"
// that distinguishes a real task from a generic one.
type TaskProcedure struct {
	Commands    []string `json:"commands"`
	Count       int      `json:"count"`
	Specificity float64  `json:"specificity"`
}

type TaskCandidate struct {
	ID              string              `json:"id"`
	RepoKey         string              `json:"repo_key,omitempty"`
	IntentSignature string              `json:"intent_signature"`
	Label           string              `json:"label"`
	Support         int                 `json:"support"`
	WithCommands    int                 `json:"with_commands"`
	Reinforcement   ReinforcementCounts `json:"reinforcement"`
	Commands        []string            `json:"commands,omitempty"`
	Procedures      []TaskProcedure     `json:"procedures,omitempty"`     // co-occurring procedure evidence
	MatchingFacts   []string            `json:"matching_facts,omitempty"` // repo-specific durable facts
	SampleIntents   []string            `json:"sample_intents,omitempty"`
	Examples        []EpisodeAnchor     `json:"examples,omitempty"`
	Workspace       string              `json:"workspace,omitempty"`      // workspace scope
	Repos           int                 `json:"repos,omitempty"`          // workspace scope
	RepoBreakdown   []PatternRepoStat   `json:"repo_breakdown,omitempty"` // workspace scope
	Strength        float64             `json:"strength"`
	StrengthLabel   string              `json:"strength_label"`
}

// LoadCorpusTaskCandidates returns the corpus's promotable task patterns as
// skill candidates (id = corpus pattern id), strongest first. Returns
// (nil, false) when no corpus exists so callers fall back to the legacy source.
//
// LoadCorpusTaskCandidates is the FORM RESOLVER: it returns every promotable task
// (one that has a shallow dossier) regardless of verifier verdict, so
// `patterns skills form <id>` can resolve any promotable pattern and then apply
// the accepted-deep-dossier gate with a precise message. The `patterns skills`
// LISTING uses loadSkillProposals instead, which additionally requires an
// accepted deep dossier.
func LoadCorpusTaskCandidates(brainDir string) ([]TaskCandidate, bool) {
	candidates, present, _ := LoadCorpusTaskCandidatesChecked(brainDir)
	return candidates, present
}
func LoadCorpusTaskCandidatesChecked(brainDir string) ([]TaskCandidate, bool, error) {
	return QueryTaskCandidatesChecked(brainDir, false)
}
func QueryTaskCandidates(brainDir string, requireAcceptedDeep bool) ([]TaskCandidate, bool) {
	candidates, present, _ := QueryTaskCandidatesChecked(brainDir, requireAcceptedDeep)
	return candidates, present
}

func QueryTaskCandidatesChecked(brainDir string, requireAcceptedDeep bool) ([]TaskCandidate, bool, error) {
	db, present, err := OpenCorpusReadDBIfPresent(brainDir)
	if err != nil {
		return nil, false, err
	}
	if !present {
		return nil, false, nil
	}
	defer db.Close()

	hasDeep, err := CorpusHasTable(db.DB, "deep_dossiers")
	if err != nil {
		return nil, false, err
	}
	if requireAcceptedDeep && !hasDeep {
		// A corpus predating deep_dossiers can have no accepted dossier, so the
		// accepted-only request is legitimately empty rather than a failed read.
		return nil, true, nil
	}
	q := `
		SELECT p.id, p.repo_key, COALESCE(p.intent_sig,''), p.title, p.gram, p.strength, p.strength_label,
		       p.support, p.outcome_success, p.outcome_corrected, p.outcome_neutral
		FROM patterns p JOIN dossiers d ON d.pattern_id = p.id`
	if hasDeep {
		q += `
		LEFT JOIN deep_dossiers dd ON dd.pattern_id = p.id`
	}
	q += `
		WHERE p.type='task' AND p.scope='repo'`
	if requireAcceptedDeep {
		// A proposal must be backed by an accepted deep dossier: the verified,
		// evidence-deep record skills are synthesized from.
		q += ` AND COALESCE(dd.verdict,'') = 'accepted'`
	}
	q += ` ORDER BY p.strength DESC`
	rows, err := db.Query(q)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	var cands []TaskCandidate
	for rows.Next() {
		var (
			id, repoKey, intentSig, title, gram, strengthLabel string
			strength                                           float64
			support, succ, corr, neut                          int
		)
		if err := rows.Scan(&id, &repoKey, &intentSig, &title, &gram, &strength, &strengthLabel,
			&support, &succ, &corr, &neut); err != nil {
			return nil, true, err
		}
		c := TaskCandidate{
			ID: id, RepoKey: repoKey, IntentSignature: intentSig, Label: title,
			Support: support, WithCommands: support,
			Reinforcement: ReinforcementCounts{Success: succ, Corrected: corr, Neutral: neut},
			Commands:      SplitGramHeads(gram),
			Strength:      strength, StrengthLabel: strengthLabel,
		}
		enrichCandidateFromCorpus(db.DB, &c, id)
		cands = append(cands, c)
	}
	if rows.Err() != nil {
		return nil, true, rows.Err()
	}
	return cands, true, nil
}

// enrichCandidateFromCorpus fills the evidence the synthesis agent needs: sample
// intents and anchors from the pattern's evidence episodes, and facts from its
// dossier.
func enrichCandidateFromCorpus(db *sql.DB, c *TaskCandidate, patternID string) {
	rows, err := db.Query(`
		SELECT COALESCE(e.intent_raw,''), pe.source_path, pe.start_line
		FROM pattern_evidence pe JOIN episodes e ON e.id = pe.episode_id
		WHERE pe.pattern_id=? ORDER BY pe.rank LIMIT 5`, patternID)
	if err == nil {
		seen := map[string]bool{}
		for rows.Next() {
			var intent, path string
			var line int
			if rows.Scan(&intent, &path, &line) != nil {
				continue
			}
			if intent != "" && !seen[intent] {
				seen[intent] = true
				c.SampleIntents = append(c.SampleIntents, base.RedactText(intent))
			}
			c.Examples = append(c.Examples, EpisodeAnchor{Path: base.RedactText(path), Line: line})
		}
		rows.Close()
	}
	// Facts from the dossier (already redacted at rest).
	var blob string
	if db.QueryRow(`SELECT json_redacted FROM dossiers WHERE pattern_id=?`, patternID).Scan(&blob) == nil {
		var rec DossierRecord
		if json.Unmarshal([]byte(blob), &rec) == nil {
			c.MatchingFacts = append(c.MatchingFacts, rec.Facts...)
		}
	}
	sort.Strings(c.MatchingFacts)
}
