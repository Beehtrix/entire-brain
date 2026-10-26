package cli

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	patterncore "github.com/entireio/entire-brain/internal/cli/patterns"
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

// proposeWorkspaceFamilies runs the agent judgment layer over member-repo task
// candidates and stores accepted cross-repo families as workspace dossiers.
func proposeWorkspaceFamilies(ctx context.Context, env EntireEnv, manifest workspace.Manifest, repoDir, agent, model, effort string, run base.DistillAgentRunner, now time.Time) (patterncore.VerifyStats, error) {
	var stats patterncore.VerifyStats
	if err := base.RejectAgentForNoEgress(agent); err != nil {
		return stats, err
	}
	wsBrainDir, err := workspaceStore(env).Dir(manifest.Name)
	if err != nil {
		return stats, err
	}
	err = patterncore.WithCorpusMutationLocked(wsBrainDir, func(db *sql.DB) error {
		var mutationErr error
		stats, mutationErr = proposeWorkspaceFamiliesInDB(ctx, db, env, manifest, repoDir, agent, model, effort, run, now)
		return mutationErr
	}, decodePatternCorpusPublication)
	if err != nil {
		return stats, fmt.Errorf("workspace corpus not built (run `"+base.BrainInvocation(os.LookupEnv, "workspace patterns refresh")+" %s`): %w", manifest.Name, err)
	}
	return stats, nil
}

func proposeWorkspaceFamiliesInDB(ctx context.Context, db *sql.DB, env EntireEnv, manifest workspace.Manifest, repoDir, agent, model, effort string, run base.DistillAgentRunner, now time.Time) (patterncore.VerifyStats, error) {
	return patterncore.ProposeWorkspaceFamilies(db, manifest, func(repoKey string) ([]map[string]any, error) {
		memberDir, err := brainDirForKey(env, repoKey)
		if err != nil {
			return nil, nil
		}
		cands, ok, err := loadCorpusTaskCandidatesChecked(memberDir)
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
		out, err := run(ctx, repoDir, args, payload, patterncore.DossierVerifyTimeout)
		if err != nil {
			return "", fmt.Errorf("workspace family proposal agent: %w", err)
		}
		return out, nil
	}, now)
}

// loadAcceptedWorkspaceFamilies returns the accepted cross-repo families.
func loadAcceptedWorkspaceFamilies(wsBrainDir string) []patterncore.WorkspaceFamily {
	families, _ := loadAcceptedWorkspaceFamiliesChecked(wsBrainDir)
	return families
}

func loadAcceptedWorkspaceFamiliesChecked(wsBrainDir string) ([]patterncore.WorkspaceFamily, error) {
	db, present, err := patterncore.OpenCorpusReadDBIfPresent(wsBrainDir)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	defer db.Close()
	return patterncore.LoadAcceptedWorkspaceFamilies(db.DB, wsBrainDir)
}

// getAcceptedWorkspaceFamily returns one accepted family by id.
func getAcceptedWorkspaceFamily(wsBrainDir, id string) (patterncore.WorkspaceFamily, bool) {
	family, ok, _ := getAcceptedWorkspaceFamilyChecked(wsBrainDir, id)
	return family, ok
}

func getAcceptedWorkspaceFamilyChecked(wsBrainDir, id string) (patterncore.WorkspaceFamily, bool, error) {
	families, err := loadAcceptedWorkspaceFamiliesChecked(wsBrainDir)
	if err != nil {
		return patterncore.WorkspaceFamily{}, false, err
	}
	for _, f := range families {
		if f.ID == id {
			return f, true, nil
		}
	}
	return patterncore.WorkspaceFamily{}, false, nil
}

const workspaceFamilySkillSystemPrompt = `You convert a VERIFIED cross-repo workspace family into a SKILL.md for a multi-repo workspace. The family was assembled from real per-repo evidence and accepted by a verifier.

The family JSON gives: title, trigger, purpose, common_workflow (repo-neutral steps), and per_repo (each repo's ACTUAL commands). Render it faithfully — do not invent.

Rules:
- The skill MUST describe what is COMMON across repos AND what VARIES per repo (a per-repo variations section listing each repo's commands).
- If the family is plainly generic (git add/commit/push/status), output EXACTLY one line: NOT_A_SKILL: <reason>.

Otherwise output ONLY a complete SKILL.md (no fences):
- YAML frontmatter: name (lowercase-hyphenated) + description with a concrete "Use when ..." from the trigger.
- Body: a Workflow section (the common steps) and a "Per-repo variations" section naming each repo and its commands. Be concise; include only what the family supports.`

// synthesizeWorkspaceSkill converts an accepted family into a SKILL.md.
func synthesizeWorkspaceSkill(ctx context.Context, repoDir string, f patterncore.WorkspaceFamily, agent, model, effort string, run base.DistillAgentRunner) (skillSynthesisResult, error) {
	args, err := base.DistillAgentCommandArgs(agent, nil, workspaceFamilySkillSystemPrompt)
	if err != nil {
		return skillSynthesisResult{}, err
	}
	args = base.InjectAgentModel(args, agent, model)
	args = base.InjectAgentEffort(args, agent, effort)
	out, err := run(ctx, repoDir, args, []byte(buildWorkspaceFamilyEvidence(f)), skillSynthesisTimeout)
	if err != nil {
		return skillSynthesisResult{}, fmt.Errorf("workspace family synthesis agent: %w", err)
	}
	return parseSynthesisOutput(out), nil
}

func buildWorkspaceFamilyEvidence(f patterncore.WorkspaceFamily) string {
	var b strings.Builder
	fmt.Fprintf(&b, "VERIFIED CROSS-REPO WORKSPACE FAMILY (verdict: %s)\n", nonEmptyOr(f.Verdict, "accepted"))
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
