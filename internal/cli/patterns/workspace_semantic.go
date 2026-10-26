package patterns

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/workspace"
)

// Workspace semantic merge (SR5).
//
// Exact (type,intent_sig,gram,meta_id) aggregation stays as a cheap signal, but
// equivalent cross-repo workflows that differ in command spelling/order never
// merge that way, and generic exact matches can survive. SR5 adds an agent
// judgment layer: it groups each member repo's promotable task candidates into
// cross-repo FAMILIES by semantic trigger + procedure purpose, records what is
// common and what varies per repo, and requires an accepted workspace family
// dossier before a workspace skill can be formed. Explicit (`workspace patterns
// verify`), egress-gated, cached by the member-candidate fingerprint.

const workspaceFamilyMaxPerRepo = 15 // cap member candidates handed to the agent per repo

const workspaceFamilyProposeSystemPrompt = `You are given task workflows from MULTIPLE repositories in a workspace, grouped per repo (each with its trigger/intent and the actual commands run).

Group workflows that share the same TRIGGER and PROCEDURE PURPOSE into cross-repo families — even when the exact commands differ in spelling or order across repos (e.g. "mise deploy" in one repo and "make release" in another are the same release workflow). Judge by MEANING, not by command string.

Rules:
- A family must span at least 2 distinct repos.
- Reject GENERIC workflows that are not repo-specific practice (plain git add/commit/push/status/diff, ls, cat) — those are not families.
- Capture what is COMMON (a repo-neutral workflow) and what VARIES per repo (each repo's actual commands).

Return EXACTLY one JSON object and nothing else (no prose, no code fences):
{
  "families": [
    {
      "title": "<short title>",
      "trigger": "<when this applies / Use when>",
      "purpose": "<the procedure purpose>",
      "common_workflow": ["<repo-neutral step>", ...],
      "per_repo": [ {"repo_key": "<repo>", "commands": ["<actual command>", ...]}, ... ],
      "verdict": "accepted" | "rejected" | "needs_split" | "low_confidence"
    }
  ]
}
Use "accepted" only for a genuine cross-repo family spanning >=2 repos. Do not invent repo keys not provided.`

// ProposeFamilies runs the agent judgment layer over member-repo task
// candidates and stores accepted cross-repo families as workspace dossiers.
func ProposeFamilies(ctx context.Context, env base.EntireEnv, manifest workspace.Manifest, repoDir, agent, model, effort string, run base.DistillAgentRunner, now time.Time) (VerifyStats, error) {
	var stats VerifyStats
	if err := base.RejectAgentForNoEgress(agent); err != nil {
		return stats, err
	}
	wsBrainDir, err := workspace.DefaultStore(env).Dir(manifest.Name)
	if err != nil {
		return stats, err
	}
	err = WithCorpusMutationLocked(wsBrainDir, func(db *sql.DB) error {
		var mutationErr error
		stats, mutationErr = proposeFamiliesInDB(ctx, db, env, manifest, repoDir, agent, model, effort, run, now)
		return mutationErr
	}, DecodeCorpusPublication)
	if err != nil {
		return stats, fmt.Errorf("workspace corpus not built (run `"+base.BrainInvocation(os.LookupEnv, "workspace patterns refresh")+" %s`): %w", manifest.Name, err)
	}
	return stats, nil
}

func proposeFamiliesInDB(ctx context.Context, db *sql.DB, env base.EntireEnv, manifest workspace.Manifest, repoDir, agent, model, effort string, run base.DistillAgentRunner, now time.Time) (VerifyStats, error) {
	return ProposeWorkspaceFamilies(db, manifest, func(repoKey string) ([]map[string]any, error) {
		memberDir, err := base.BrainDirForKey(env, repoKey)
		if err != nil {
			return nil, nil
		}
		cands, ok, err := LoadCorpusTaskCandidatesChecked(memberDir)
		if err != nil {
			return nil, err
		}
		if !ok || len(cands) == 0 {
			return nil, nil
		}
		var list []map[string]any
		for i, c := range cands {
			if i >= workspaceFamilyMaxPerRepo {
				break
			}
			r := c.Reinforcement
			list = append(list, map[string]any{
				"intent": c.IntentSignature, "title": base.RedactText(c.Label), "commands": base.RedactStrings(c.Commands),
				"support": c.Support, "reinforcement": []int{r.Success, r.Corrected, r.Neutral},
			})
		}
		return list, nil
	}, func(payload []byte) (string, error) {
		args, err := base.DistillAgentCommandArgs(agent, nil, workspaceFamilyProposeSystemPrompt)
		if err != nil {
			return "", err
		}
		args = base.InjectAgentModel(args, agent, model)
		args = base.InjectAgentEffort(args, agent, effort)
		out, err := run(ctx, repoDir, args, payload, DossierVerifyTimeout)
		if err != nil {
			return "", fmt.Errorf("workspace family proposal agent: %w", err)
		}
		return out, nil
	}, now)
}

// AcceptedFamilies returns the accepted cross-repo families.
func AcceptedFamilies(wsBrainDir string) []WorkspaceFamily {
	families, _ := AcceptedFamiliesChecked(wsBrainDir)
	return families
}

func AcceptedFamiliesChecked(wsBrainDir string) ([]WorkspaceFamily, error) {
	db, present, err := OpenCorpusReadDBIfPresent(wsBrainDir)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	defer db.Close()
	return LoadAcceptedWorkspaceFamilies(db.DB, wsBrainDir)
}

// AcceptedFamilyByID returns one accepted family by id.
func AcceptedFamilyByID(wsBrainDir, id string) (WorkspaceFamily, bool) {
	family, ok, _ := AcceptedFamilyByIDChecked(wsBrainDir, id)
	return family, ok
}

func AcceptedFamilyByIDChecked(wsBrainDir, id string) (WorkspaceFamily, bool, error) {
	families, err := AcceptedFamiliesChecked(wsBrainDir)
	if err != nil {
		return WorkspaceFamily{}, false, err
	}
	for _, f := range families {
		if f.ID == id {
			return f, true, nil
		}
	}
	return WorkspaceFamily{}, false, nil
}

func BuildWorkspaceFamilyEvidence(f WorkspaceFamily) string {
	var b strings.Builder
	fmt.Fprintf(&b, "VERIFIED CROSS-REPO WORKSPACE FAMILY (verdict: %s)\n", base.NonEmptyOr(f.Verdict, "accepted"))
	fmt.Fprintf(&b, "Title: %s\n", f.Title)
	fmt.Fprintf(&b, "Trigger / Use when: %s\n", f.Trigger)
	if f.Purpose != "" {
		fmt.Fprintf(&b, "Purpose: %s\n", f.Purpose)
	}
	if len(f.CommonWorkflow) > 0 {
		fmt.Fprintf(&b, "Common workflow (repo-neutral):\n")
		for i, s := range f.CommonWorkflow {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, s)
		}
	}
	fmt.Fprintf(&b, "Per-repo variations (what differs):\n")
	for _, v := range f.PerRepo {
		fmt.Fprintf(&b, "  - %s: %s\n", v.RepoKey, strings.Join(v.Commands, " → "))
	}
	return base.RedactText(b.String())
}
