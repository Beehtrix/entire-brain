package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/entireio/entire-brain/internal/factgitmeta"
	"github.com/entireio/entire-brain/internal/factsync"

	"github.com/spf13/cobra"
)

const (
	// factsBackendLocalGitmeta is a bare, on-disk git-meta store under the
	// plugin cache; fully local, no network/entiredb/Postgres. Default on
	// unconnected repos.
	factsBackendLocalGitmeta = "local-gitmeta"
	// factsBackendHTTP drives the hosted per-fact wire (factsync perfact.go).
	// Default on connected repos.
	factsBackendHTTP = "http"
	// envFactsBackend selects the backend when --memories-backend is unset.
	envFactsBackend = "ENTIRE_BRAIN_FACTS_BACKEND"
)

// factsSyncOptions bundles the flag state for `facts sync`.
type factsSyncOptions struct {
	branch  string
	backend string
	member  string
	// http backend only (mirror publish's flag/env resolution):
	apiURL string
	token  string
	repoID string

	jsonOut bool
}

func newFactsSyncCommand(opts Options) *cobra.Command {
	var syncOpts factsSyncOptions
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync this branch's local memories with the shared memory set",
		Long: `sync exchanges this branch's local memories with the shared memory set.

The default backend depends on connectedness. On a repo connected to Brainstore
('` + brainInvocation(os.LookupEnv, "connect") + `'), a plain sync uses the hosted per-fact wire: push this
branch's memories, pull the team's delta feed — the same exchange the background
sync runs, except failures fail the command instead of degrading silently. On an
unconnected repo, a plain sync uses the fully local git-meta store and never
leaves the machine. ENTIRE_BRAIN_NO_EGRESS (or its deprecated alias
ENTIRE_BRAIN_LOCAL_ONLY) suppresses hosted sync regardless of defaults and
flags; a connected repo's plain sync is then skipped, loudly.

Backends (--memories-backend, or ` + envFactsBackend + `):
  local-gitmeta  a bare, on-disk git-meta store under the brain cache
                 (<cache>/brain/<repo-key>/gitmeta.git); fully local, no network,
                 entiredb, or Postgres. Default on unconnected repos.
  http           the hosted per-fact wire, explicitly. Default on connected
                 repos. Target resolves flag > env > connected binding:
                 --api-url/ENTIRE_API_URL, --repo-id/ENTIRE_REPO_ID,
                 --token/ENTIRE_API_TOKEN; on a connected repo the binding
                 supplies the target and the token is minted automatically.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFactsSync(cmd, opts, syncOpts)
		},
	}
	cmd.Flags().StringVar(&syncOpts.branch, "branch", "", "Branch to sync (default: current branch)")
	cmd.Flags().StringVar(&syncOpts.backend, "memories-backend", "", "Memory-set backend: local-gitmeta or http (default: http on a connected repo, else local-gitmeta)")
	cmd.Flags().StringVar(&syncOpts.member, "member", "", "Member identity stamped on queued review proposals (default: git user.email)")
	cmd.Flags().StringVar(&syncOpts.repoID, "repo-id", "", "Target repo id for the http backend (overrides ENTIRE_REPO_ID)")
	cmd.Flags().StringVar(&syncOpts.apiURL, "api-url", "", "Entire API base URL for the http backend, https:// (overrides ENTIRE_API_URL)")
	cmd.Flags().StringVar(&syncOpts.token, "token", "", "Entire API bearer token for the http backend (overrides ENTIRE_API_TOKEN)")
	cmd.Flags().BoolVar(&syncOpts.jsonOut, "json", false, "Emit the sync result as JSON")
	// --facts-backend predates the facts→memories rename; keep it working.
	legacyFlagAlias(cmd.Flags(), "facts-backend", "memories-backend")
	return cmd
}

func runFactsSync(cmd *cobra.Command, opts Options, syncOpts factsSyncOptions) error {
	ctx := cmd.Context()

	repoDir, local, err := resolveLocalTargetRepoDir(ctx, opts.Runner, agentSurfaceTarget(opts, nil))
	if err != nil {
		return err
	}
	if !local {
		return fmt.Errorf("memories sync requires a local repository path")
	}
	storage, err := repoStoragePaths(ctx, opts.Runner, opts.Env, repoDir)
	if err != nil {
		return err
	}

	branch := strings.TrimSpace(syncOpts.branch)
	if branch == "" {
		if current, gitErr := gitScalar(ctx, opts.Runner, repoDir, "branch", "--show-current"); gitErr == nil {
			branch = strings.TrimSpace(current)
		}
	}
	if branch == "" {
		branch = distillDefaultBranch
	}

	backend := resolveFactsBackendName(syncOpts.backend)
	if backend == "" {
		// No explicit backend: connectedness decides. Connect was the consent
		// moment (ADR-P1-A reconciliation, COR-2233), so a connected repo syncs
		// hosted by default; the no-egress kill switch still always wins and
		// falls back to the local store, loudly.
		binding, present, bindErr := readHostedRepoBinding(storage.BrainDir)
		connected := bindErr == nil && present && !binding.OptedOut
		if bindErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: hosted binding unreadable (syncing locally; run '%s' to rebind): %v\n", brainInvocation(os.LookupEnv, "connect"), bindErr)
		}
		switch {
		case connected && !brainNoEgressMode():
			return runHostedFactsSync(cmd, opts, syncOpts, repoDir, storage, branch)
		case connected:
			// Do NOT fall back to the local git-meta store here: on a connected
			// repo its head is stale (the per-fact wire has been the sync path
			// since connect), and a keep-both merge against a stale head
			// re-raises every deferred per-fact conflict into the local review
			// queue. The kill switch suppresses sync; it does not redirect it.
			fmt.Fprintf(cmd.OutOrStdout(), "memories sync: skipped: %s is set and this repo is connected to Brainstore; unset it to sync, or pass --memories-backend=local-gitmeta to use the local git-meta store\n", noEgressToggleName())
			return nil
		}
		backend = factsBackendLocalGitmeta
	}

	switch backend {
	case factsBackendHTTP:
		// The master kill switch always wins over the hosted opt-in.
		if brainNoEgressMode() {
			return fmt.Errorf("no_egress: hosted memory sync is disabled while %s is set; unset it or use --memories-backend=local-gitmeta", noEgressToggleName())
		}
		return runHostedFactsSync(cmd, opts, syncOpts, repoDir, storage, branch)
	case factsBackendLocalGitmeta:
	default:
		return fmt.Errorf("unknown --memories-backend %q (want %q or %q)", backend, factsBackendLocalGitmeta, factsBackendHTTP)
	}

	facts, err := loadFacts(storage.BrainDir, branch)
	if err != nil {
		return err
	}
	memberID := resolveSyncMemberID(ctx, opts.Runner, repoDir, syncOpts.member)

	gitDir, err := gitmetaDirForKey(opts.Env, storage.Key)
	if err != nil {
		return err
	}
	srv, err := factgitmeta.NewLocalBackend(gitDir, storage.Key, opts.Now)
	if err != nil {
		return err
	}
	backendLabel := "local-gitmeta (" + gitDir + ")"

	// The merge runs on this runner (ADR-P1-E); the backend only stores the blob
	// and CAS-swaps the head. Sync retries on a losing swap and never drops a
	// member's fact.
	res, err := factsync.Sync(ctx, srv, storage.Key, branch, memberID, facts, opts.Now().UTC())
	if err != nil {
		return err
	}

	if err := persistFactsSyncProposals(storage.BrainDir, branch, res.Proposals); err != nil {
		return err
	}

	if syncOpts.jsonOut {
		return writeJSON(cmd, map[string]any{
			"branch":    branch,
			"backend":   backendLabel,
			"member":    memberID,
			"published": res.Published,
			"ref":       res.NewRef,
			"attempts":  res.Attempts,
			"proposals": res.Proposals,
			"facts":     len(facts),
		})
	}

	out := cmd.OutOrStdout()
	if res.Published {
		fmt.Fprintf(out, "synced %d local memory(ies) on %s via %s: head advanced to %s (%d attempt(s))\n",
			len(facts), branch, backendLabel, res.NewRef, res.Attempts)
	} else {
		fmt.Fprintf(out, "synced %d local memory(ies) on %s via %s: no change; head at %s (%d attempt(s))\n",
			len(facts), branch, backendLabel, refOrNone(res.NewRef), res.Attempts)
	}
	if len(res.Proposals) > 0 {
		fmt.Fprintf(out, "%d conflict(s) queued for review ('memories review'):\n", len(res.Proposals))
		for _, p := range res.Proposals {
			fmt.Fprintf(out, "  %s %s -> %s (by %s, confidence %.2f)\n", p.Action, p.CandidateID, p.TargetID, p.ProposedBy, p.Confidence)
		}
	}
	return nil
}

// runHostedFactsSync is the manual flavor of hostedFactsSyncAndPull: the same
// per-fact exchange (batch-upsert this branch's store, pull the repo delta
// feed, rebuild the branch store), but the user explicitly asked for this
// sync, so failures surface in the exit code and output instead of degrading
// to a stderr note (COR-2202 precedent).
func runHostedFactsSync(cmd *cobra.Command, opts Options, syncOpts factsSyncOptions, repoDir string, storage repoStorage, branch string) error {
	ctx := cmd.Context()
	errW := cmd.ErrOrStderr()
	target, err := resolveManualHostedTarget(ctx, opts, storage.BrainDir, repoDir, "memories sync", syncOpts.repoID, syncOpts.apiURL, syncOpts.token)
	if err != nil {
		return err
	}
	srv := &factsync.HTTPServer{BaseURL: target.baseURL, Token: target.token}
	backendLabel := "hosted (" + strings.TrimRight(target.baseURL, "/") + ")"

	counts, skipped, pushErr := pushHostedFacts(ctx, storage, srv, target.repoID, branch)
	if skipped > 0 {
		fmt.Fprintf(errW, "warning: skipped %d unpublishable record(s) whose content id does not re-derive (kept locally, not uploaded)\n", skipped)
	}
	// The pull runs even after a failed push: the team's memories still land,
	// and the error below reports exactly which half failed.
	kept, total, manifestErr, pullErr := pullHostedFacts(ctx, opts, repoDir, storage, srv, target.repoID, target.baseURL, target.token, branch)
	if pushErr == nil && pullErr == nil {
		// Both halves completed: record the per-branch marker, same rule as the
		// background sync (the blind-spot note keys off its absence).
		if err := writeHostedFactsBinding(storage.BrainDir, branch, target.repoID, target.baseURL); err != nil {
			return fmt.Errorf("memories sync: record hosted target: %w", err)
		}
	}
	if manifestErr != nil {
		fmt.Fprintf(errW, "warning: memories updated, but the source manifest could not be refreshed: %v\n", manifestErr)
	}
	switch {
	case pushErr != nil && pullErr != nil:
		return fmt.Errorf("memories sync: push to %s failed: %v; pull failed: %v", backendLabel, pushErr, pullErr)
	case pushErr != nil:
		return fmt.Errorf("memories sync: pulled %d memory(ies) from %s (%d kept), but the push failed (local brain unaffected): %w", total, backendLabel, kept, pushErr)
	case pullErr != nil:
		return fmt.Errorf("memories sync: pushed %d memory(ies) to %s, but the pull failed (local brain unaffected): %w", counts.Added+counts.Changed+counts.Unchanged, backendLabel, pullErr)
	}

	if syncOpts.jsonOut {
		return writeJSON(cmd, map[string]any{
			"branch":                branch,
			"backend":               backendLabel,
			"pushed_added":          counts.Added,
			"pushed_changed":        counts.Changed,
			"pushed_unchanged":      counts.Unchanged,
			"skipped_unpublishable": skipped,
			"feed_facts":            total,
			"feed_kept":             kept,
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "synced %s via %s: pushed added=%d changed=%d unchanged=%d; feed facts=%d kept=%d\n",
		branch, backendLabel, counts.Added, counts.Changed, counts.Unchanged, total, kept)
	return nil
}

// persistFactsSyncProposals appends conflicts the merge raised to the same
// durable queue consumed by facts review/status and the recall pending-review
// guard. The brain write lock makes the read-merge-write atomic with other
// local writers, while writeFactProposals supplies stable ordering and
// deduplication.
func persistFactsSyncProposals(brainDir, branch string, proposals []factProposal) error {
	if len(proposals) == 0 {
		return nil
	}
	return withBrainWriteLock(brainDir, func() error {
		existing, err := loadFactProposals(brainDir, branch)
		if err != nil {
			return fmt.Errorf("memories sync: load review queue: %w", err)
		}
		if err := writeFactProposals(brainDir, branch, append(existing, proposals...)); err != nil {
			return fmt.Errorf("memories sync: persist review queue: %w", err)
		}
		return nil
	})
}

// resolveFactsBackendName folds the --memories-backend flag and the env
// override into an explicit backend name. Empty means "no explicit choice":
// the caller defaults by connectedness (hosted on a connected repo, else
// local-gitmeta).
func resolveFactsBackendName(flagVal string) string {
	if v := strings.TrimSpace(flagVal); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(envFactsBackend))
}

// resolveSyncMemberID derives the member identity stamped on queued review
// proposals: the explicit --member, else the repo's git user.email, else
// $USER@<hostname>, else "local". A non-empty id is required (Sync rejects "").
func resolveSyncMemberID(ctx context.Context, runner CommandRunner, repoDir, override string) string {
	if v := strings.TrimSpace(override); v != "" {
		return v
	}
	if email, err := gitScalar(ctx, runner, repoDir, "config", "--get", "user.email"); err == nil {
		if e := strings.TrimSpace(email); e != "" {
			return e
		}
	}
	if u := strings.TrimSpace(os.Getenv("USER")); u != "" {
		if host, err := os.Hostname(); err == nil && host != "" {
			return u + "@" + host
		}
		return u
	}
	return "local"
}

// gitmetaDirForKey resolves the local git-meta store path for a repo key
// (<cache>/brain/<key>/gitmeta.git), mirroring brainDirForKey's key handling but
// rooted in the plugin CACHE tree (the store is a local, rebuildable sync
// staging area, not authoritative brain data).
func gitmetaDirForKey(env EntireEnv, key string) (string, error) {
	if first, _, _ := strings.Cut(key, "/"); first == workspaceDirName {
		return "", fmt.Errorf("repo key uses the reserved %q segment: %s", workspaceDirName, key)
	}
	dirs, err := resolvePluginDirs(env)
	if err != nil {
		return "", err
	}
	root := filepath.Join(dirs.Cache, "brain")
	if err := rejectBrainRootPathSymlinks(root, filepath.FromSlash(key)); err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(key), "gitmeta.git"), nil
}

// refOrNone renders an empty ref as "(none)" for the no-change/empty case.
func refOrNone(ref string) string {
	if ref == "" {
		return "(none)"
	}
	return ref
}
