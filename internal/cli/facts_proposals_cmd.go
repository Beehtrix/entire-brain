package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/entireio/entire-brain/internal/factsync"
	"github.com/entireio/entire-brain/internal/hostedbrain"
)

// `facts proposals` is the user-facing surface for the CROSS-MEMBER review queue: the
// conflicts another member's `facts sync` kept both sides of and routed for a human to
// settle. It is the hosted twin of `facts review` (which resolves this member's own
// low-confidence, on-disk proposals) and shares its verbs: list, show, apply, reject.
//
// Hosted egress is opt-in and off by default, exactly like `publish` (ADR-P1-A, no
// implicit egress): a network call happens only when the user explicitly runs one
// of these commands with a target — the connected binding by default, else
// explicit flags (COR-2143). The master kill switch (ENTIRE_BRAIN_NO_EGRESS)
// always wins. Every gate is checked BEFORE any HTTP request, so a refusal is
// guaranteed egress-free and the default behaviour stays strictly local.

// factsProposalsOptions bundles the flag state shared by every `facts proposals` verb.
type factsProposalsOptions struct {
	branch  string
	repoID  string
	apiURL  string
	token   string
	jsonOut bool
}

// commandSubcommandNames lists a group node's verbs in registration order, so
// the "requires a subcommand" error names exactly what exists rather than a
// hand-maintained copy that drifts the next time a verb is added.
func commandSubcommandNames(cmd *cobra.Command) []string {
	names := make([]string, 0, len(cmd.Commands()))
	for _, sub := range cmd.Commands() {
		if sub.Hidden || sub.Name() == "help" {
			continue
		}
		names = append(names, sub.Name())
	}
	return names
}

func newFactsProposalsCommand(opts Options) *cobra.Command {
	var pOpts factsProposalsOptions

	cmd := &cobra.Command{
		Use:   "proposals",
		Short: "Review the hosted cross-member conflict queue (opt-in)",
		Long: `proposals lists and settles the OPEN cross-member review proposals on the
hosted fact-set: contradictions two members' facts raised, kept both-sides by the
keep-both merge and routed to a human rather than silently overwritten.

Accepting a proposal applies its merge/supersede (the losing fact is retained as
superseded, never deleted); rejecting it keeps both facts active and clears the
conflict cross-link. Either way the resolved fact set and the shrunken open set are
pushed under compare-and-swap, so every member converges.

This surface is HOSTED and opt-in: nothing leaves your machine unless you run
one of these commands with a target — the connected binding by default, or an
explicit one. Use ` + "`memories review`" + ` for this member's own local, low-confidence
proposals — that path is fully offline.

The target resolves flag > env > connected binding: --repo-id /` + " " + envRepoID + `,
--api-url / ` + envAPIBaseURL + `, --token / ` + envAPIToken + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// `--json` is a promise that this invocation answers in JSON. The
			// group node has no result of its own, and printing human help to
			// stdout under exit 0 told a machine caller "here is your JSON" and
			// handed it prose instead. It must not quietly run `list` either:
			// that verb is HOSTED, and turning an inert invocation into network
			// egress is exactly what the opt-in gates above exist to prevent.
			// So name what is missing, in the JSON error envelope every other
			// --json surface already uses (see wrapJSONErrorRendering), and
			// exit nonzero. Without --json the human help is unchanged.
			if pOpts.jsonOut {
				return fmt.Errorf("memories proposals requires a subcommand: %s", strings.Join(commandSubcommandNames(cmd), ", "))
			}
			return cmd.Help()
		},
	}

	cmd.PersistentFlags().StringVar(&pOpts.branch, "branch", "", "Branch whose open proposals to review (default: current branch)")
	cmd.PersistentFlags().StringVar(&pOpts.repoID, "repo-id", "", "Target repo id (overrides "+envRepoID+")")
	cmd.PersistentFlags().StringVar(&pOpts.apiURL, "api-url", "", "Entire API base URL, https:// (overrides "+envAPIBaseURL+")")
	cmd.PersistentFlags().StringVar(&pOpts.token, "token", "", "Entire API bearer token (overrides "+envAPIToken+")")
	cmd.PersistentFlags().BoolVar(&pOpts.jsonOut, "json", false, "Emit the result as JSON")

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List the open cross-member proposals for a branch",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runFactsProposalsList(cmd, opts, pOpts)
			},
		},
		&cobra.Command{
			Use:   "show <proposal>",
			Short: "Show one open proposal (by id, unambiguous id prefix, or candidate memory id)",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runFactsProposalsShow(cmd, opts, pOpts, args[0])
			},
		},
		&cobra.Command{
			Use:   "apply <proposal>",
			Short: "Accept a proposal: apply its merge/supersede and converge the hosted head",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runFactsProposalsResolve(cmd, opts, pOpts, args[0], factsync.Accept)
			},
		},
		&cobra.Command{
			Use:   "reject <proposal>",
			Short: "Reject a proposal: keep both memories active and clear the conflict link",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runFactsProposalsResolve(cmd, opts, pOpts, args[0], factsync.Reject)
			},
		},
	)
	var repairApply bool
	var repairRef string
	repairCmd := &cobra.Command{Use: "repair", Short: "Preview invalid proposal IDs; optionally remove them from the reviewed queue version", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if repairApply && repairRef == "" {
				return fmt.Errorf("--apply requires --if-ref from the repair preview")
			}
			if !repairApply && repairRef != "" {
				return fmt.Errorf("--if-ref requires --apply")
			}
			target, err := resolveHostedProposalTarget(cmd.Context(), opts, pOpts)
			if err != nil {
				return err
			}
			result, err := target.client.RepairProposalIDs(cmd.Context(), target.repoID, target.branch, repairApply, repairRef)
			if err != nil {
				return err
			}
			if pOpts.jsonOut {
				return writeJSON(cmd, result)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "queue ref: %q; invalid proposals: %d; removed: %d\n", result.Ref, len(result.Invalid), result.Removed)
			for _, p := range result.Invalid {
				fmt.Fprintf(cmd.OutOrStdout(), "  invalid id: %q (derived: %q)\n", p.ID, factsync.ProposalID(p.Proposal))
			}
			if !repairApply && len(result.Invalid) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Review these entries, then use repair --apply --if-ref %q to remove them. Memories are unchanged.\n", result.Ref)
			}
			return nil
		}}
	repairCmd.Flags().BoolVar(&repairApply, "apply", false, "Remove invalid proposals from the reviewed queue version")
	repairCmd.Flags().StringVar(&repairRef, "if-ref", "", "Queue ref reported by the repair preview")
	cmd.AddCommand(repairCmd)
	return cmd
}

// hostedProposalTarget is the resolved hosted target for one `facts proposals` run.
type hostedProposalTarget struct {
	client  *hostedbrain.Client
	repoID  string
	branch  string
	baseURL string
}

// resolveHostedProposalTarget enforces the egress gates and resolves the hosted
// target. It performs NO network call, so every refusal below is egress-free.
func resolveHostedProposalTarget(ctx context.Context, opts Options, pOpts factsProposalsOptions) (hostedProposalTarget, error) {
	// The master kill switch always wins over the hosted opt-in; the opt-in
	// itself is the resolved target (mirrors publish).
	if brainNoEgressMode() {
		return hostedProposalTarget{}, fmt.Errorf("no_egress: hosted proposal review is disabled while %s is set; unset it, or use 'memories review' for this member's local proposals", noEgressToggleName())
	}

	// The binding default needs the repo's brain dir; this surface also works
	// from a checkout without one, so a failed resolution only disables the
	// default — explicit flags still carry the command.
	var brainDir, repoDir string
	if dir, local, err := resolveLocalTargetRepoDir(ctx, opts.Runner, agentSurfaceTarget(opts, nil)); err == nil && local {
		repoDir = dir
		if storage, err := repoStoragePaths(ctx, opts.Runner, opts.Env, dir); err == nil {
			brainDir = storage.BrainDir
		}
	}
	target, err := resolveManualHostedTarget(ctx, opts, brainDir, repoDir, "memories proposals", pOpts.repoID, pOpts.apiURL, pOpts.token)
	if err != nil {
		return hostedProposalTarget{}, err
	}

	return hostedProposalTarget{
		client:  &hostedbrain.Client{BaseURL: target.baseURL, Token: target.token},
		repoID:  target.repoID,
		branch:  resolveProposalsBranch(ctx, opts, pOpts.branch),
		baseURL: strings.TrimRight(target.baseURL, "/"),
	}, nil
}

// resolveProposalsBranch folds --branch, the repo's current branch, and the default
// into the branch whose open set is reviewed. The repo is only consulted for its
// branch name — the review target itself is the hosted repo id, so this works from a
// checkout without a built brain.
func resolveProposalsBranch(ctx context.Context, opts Options, flagVal string) string {
	if b := strings.TrimSpace(flagVal); b != "" {
		return b
	}
	repoDir := opts.Env.RepoRoot
	if repoDir == "" {
		repoDir = "."
	}
	if current, err := gitScalar(ctx, opts.Runner, repoDir, "branch", "--show-current"); err == nil {
		if b := strings.TrimSpace(current); b != "" {
			return b
		}
	}
	return distillDefaultBranch
}

func runFactsProposalsList(cmd *cobra.Command, opts Options, pOpts factsProposalsOptions) error {
	ctx := cmd.Context()
	target, err := resolveHostedProposalTarget(ctx, opts, pOpts)
	if err != nil {
		return err
	}
	proposals, err := target.client.ListProposals(ctx, target.repoID, target.branch)
	// A backend that predates the queue (404/501 on the collection) means no
	// open proposals can exist there — an answer for `list`, not a transport
	// failure to dump on the reader (COR-2204).
	queueUnsupported := errors.Is(err, factsync.ErrProposalQueueUnsupported)
	if err != nil && !queueUnsupported {
		return err
	}
	if pOpts.jsonOut {
		if proposals == nil {
			proposals = []factsync.OpenProposal{}
		}
		return writeJSON(cmd, map[string]any{
			"branch":          target.branch,
			"repo_id":         target.repoID,
			"host":            target.baseURL,
			"open":            len(proposals),
			"proposals":       proposals,
			"queue_supported": !queueUnsupported,
		})
	}
	if queueUnsupported {
		fmt.Fprintf(cmd.OutOrStdout(), "no open cross-member proposals on %s (this server does not serve the proposal queue)\n", target.branch)
		return nil
	}
	out := cmd.OutOrStdout()
	if len(proposals) == 0 {
		fmt.Fprintf(out, "no open cross-member proposals on %s\n", target.branch)
		return nil
	}
	fmt.Fprintf(out, "%d open cross-member proposal(s) on %s:\n", len(proposals), target.branch)
	for _, p := range proposals {
		fmt.Fprintf(out, "  %s  %s\n", p.ID, describeProposal(p))
	}
	fmt.Fprintf(out, "resolve with: memories proposals apply|reject <proposal>\n")
	return nil
}

func runFactsProposalsShow(cmd *cobra.Command, opts Options, pOpts factsProposalsOptions, ref string) error {
	ctx := cmd.Context()
	target, err := resolveHostedProposalTarget(ctx, opts, pOpts)
	if err != nil {
		return err
	}
	// Resolve the caller's reference (id, prefix, or candidate memory id) against the
	// open set, then fetch the canonical record by its exact id.
	open, err := target.client.ListProposals(ctx, target.repoID, target.branch)
	if err != nil {
		return err
	}
	found, err := factsync.FindProposal(open, ref)
	if err != nil {
		return err
	}
	proposal, err := target.client.GetProposal(ctx, target.repoID, target.branch, found.ID)
	if err != nil {
		return err
	}
	if pOpts.jsonOut {
		return writeJSON(cmd, map[string]any{
			"branch":   target.branch,
			"repo_id":  target.repoID,
			"host":     target.baseURL,
			"proposal": proposal,
		})
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s\n", proposal.ID)
	fmt.Fprintf(out, "  branch:     %s\n", target.branch)
	fmt.Fprintf(out, "  action:     %s\n", proposal.Proposal.Action)
	fmt.Fprintf(out, "  candidate:  %s\n", proposal.Proposal.CandidateID)
	fmt.Fprintf(out, "  target:     %s\n", proposal.Proposal.TargetID)
	fmt.Fprintf(out, "  proposedBy: %s\n", proposalMemberOrUnknown(proposal))
	fmt.Fprintf(out, "  confidence: %.2f\n", proposal.Proposal.Confidence)
	return nil
}

func runFactsProposalsResolve(cmd *cobra.Command, opts Options, pOpts factsProposalsOptions, ref string, decision factsync.Decision) error {
	ctx := cmd.Context()
	target, err := resolveHostedProposalTarget(ctx, opts, pOpts)
	if err != nil {
		return err
	}
	now := opts.Now().UTC()
	var res factsync.ResolveOpenResult
	if decision == factsync.Accept {
		res, err = target.client.ApplyProposal(ctx, target.repoID, target.branch, ref, now)
	} else {
		res, err = target.client.RejectProposal(ctx, target.repoID, target.branch, ref, now)
	}
	if err != nil {
		return err
	}
	// The hosted set is now the settled truth; drop this member's local
	// review-queue copy of the same conflict so `facts review`, `facts status`,
	// and the pending-review guard stop listing a proposal a member just
	// settled — and so a stale local apply cannot later override this
	// resolution. Best-effort: the hosted resolution has already succeeded.
	if pruneErr := pruneLocalProposalCopy(ctx, opts, target.branch, target.repoID, target.baseURL, decision, res.Proposal); pruneErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: resolved on the hosted queue, but the local review-queue copy was not pruned: %v\n", pruneErr)
	}
	if pOpts.jsonOut {
		return writeJSON(cmd, map[string]any{
			"branch":   target.branch,
			"repo_id":  target.repoID,
			"host":     target.baseURL,
			"resolved": res,
		})
	}
	out := cmd.OutOrStdout()
	verb := "applied"
	detail := "candidate merged/superseded target; the losing memory is retained"
	if decision == factsync.Reject {
		verb = "rejected"
		detail = "both memories stay active; conflict link cleared"
	}
	fmt.Fprintf(out, "%s %s on %s (%s)\n", verb, res.Proposal.ID, target.branch, detail)
	fmt.Fprintf(out, "  memories head: %s (%d attempt(s))\n", refOrNone(res.FactsRef), res.Attempts)
	fmt.Fprintf(out, "  %d open proposal(s) remain\n", res.Remaining)
	return nil
}

// pruneLocalProposalCopy removes the local review-queue copy of a proposal that
// was just settled on the hosted queue, AND mirrors the settlement into this
// member's local facts. Outside a local repository there is no local queue to
// prune, which is success, not an error.
//
// Mirroring here is what makes the member's own decision visible immediately.
// `facts sync` reconciles settlements eventually, but without this the member who
// just ran `facts proposals apply` would keep seeing the pre-settlement state in
// `facts list`/recall until their next sync — their local view contradicting the
// decision they had just made.
func pruneLocalProposalCopy(ctx context.Context, opts Options, branch, repoID, baseURL string, decision factsync.Decision, settled factsync.OpenProposal) error {
	repoDir, local, err := resolveLocalTargetRepoDir(ctx, opts.Runner, agentSurfaceTarget(opts, nil))
	if err != nil || !local {
		return err
	}
	storage, err := repoStoragePaths(ctx, opts.Runner, opts.Env, repoDir)
	if err != nil {
		return err
	}
	// The settled id deliberately STAYS in the shared-proposal ledger: the
	// ledger records "this conflict has reached the hosted set", and keeping it
	// suppresses a republish should an identical entry ever re-derive locally.
	return withBrainWriteLock(storage.BrainDir, func() error {
		queue, err := loadFactProposals(storage.BrainDir, branch)
		if err != nil {
			return err
		}
		// Mirror first: a crash between the two writes then leaves the queue entry
		// in place, and the next sync redoes the (idempotent) mirror rather than
		// losing it.
		facts, factsErr := loadFacts(storage.BrainDir, branch)
		if factsErr != nil {
			return factsErr
		}
		if len(facts) == 0 && len(queue) == 0 {
			return nil
		}
		if err := checkHostedFactsBinding(storage.BrainDir, branch, repoID, baseURL); err != nil {
			return err
		}
		now := opts.Now().UTC()
		if updated, changed := applySettlementLocally(facts, settled.Proposal, decision == factsync.Accept, now); changed {
			if err := writeFacts(storage.BrainDir, branch, updated); err != nil {
				return err
			}
			if err := updateFactSourceManifestLocked(storage.BrainDir, now); err != nil {
				return err
			}
		}
		next := make([]factProposal, 0, len(queue))
		for _, p := range queue {
			if factsync.ProposalID(p) == settled.ID {
				continue
			}
			next = append(next, p)
		}
		if len(next) == len(queue) {
			return nil
		}
		return writeFactProposals(storage.BrainDir, branch, next)
	})
}

// describeProposal renders one open proposal as a single review line.
func describeProposal(p factsync.OpenProposal) string {
	return fmt.Sprintf("%s %s -> %s (by %s, confidence %.2f)",
		p.Proposal.Action, p.Proposal.CandidateID, p.Proposal.TargetID, proposalMemberOrUnknown(p), p.Proposal.Confidence)
}

// proposalMemberOrUnknown renders the routing member, tolerating a proposal raised by
// an older sync that predates ProposedBy.
func proposalMemberOrUnknown(p factsync.OpenProposal) string {
	if m := strings.TrimSpace(p.Proposal.ProposedBy); m != "" {
		return m
	}
	return "(unknown member)"
}
