package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/entireio/entire-brain/internal/apiurl"

	"github.com/spf13/cobra"
)

// whereamiResult is the host CLI's `entire repo whereami --json` contract
// (COR-2029): the repo's region-local ULID and its cell API base URL.
//
// NOT SHIPPED. COR-2029 is a planned host-CLI command, not an existing one:
// `entire repo` offers clone/create/delete/get/list/mirror/visibility and
// nothing else, so every call below fails on a stock host CLI. Treat this
// struct as the contract we will decode WHEN the host ships it, and treat
// resolution failure as the normal first-run outcome rather than a local
// misconfiguration to be diagnosed.
type whereamiResult struct {
	RepoID string `json:"repo_id"`
	APIURL string `json:"api_url"`
}

type connectOptions struct {
	repoID string
	apiURL string
}

func newConnectCommand(opts Options) *cobra.Command {
	var connectOpts connectOptions
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Bind this repository to its hosted brain explicitly",
		Long: `connect binds this repository to its hosted brain: it records the repo id and
API base URL (non-secrets only — the host CLI's own login mints a fresh token
per sync), then runs one immediate sync so the first pull happens right away.

Pass --repo-id and --api-url to bind explicitly. That is the only path that
works today: automatic resolution asks the host CLI for "entire repo whereami
--json" (COR-2029), which is not shipped yet, so the daemon and session-end
hook stay local-only until this repository is bound by hand. Explicit flags are
also how you reach a non-default cell or a staging target.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConnect(cmd, opts, connectOpts)
		},
	}
	cmd.Flags().StringVar(&connectOpts.repoID, "repo-id", "", "Hosted repo id (region-local ULID)")
	cmd.Flags().StringVar(&connectOpts.apiURL, "api-url", "", "Entire API base URL, https://")
	return cmd
}

func runConnect(cmd *cobra.Command, opts Options, connectOpts connectOptions) error {
	ctx := cmd.Context()
	repoDir, local, err := resolveLocalTargetRepoDir(ctx, opts.Runner, agentSurfaceTarget(opts, nil))
	if err != nil {
		return err
	}
	if !local {
		return fmt.Errorf("connect requires a local repository path")
	}
	storage, err := repoStoragePaths(ctx, opts.Runner, opts.Env, repoDir)
	if err != nil {
		return err
	}

	repoID := strings.TrimSpace(connectOpts.repoID)
	apiURL := strings.TrimSpace(connectOpts.apiURL)
	if repoID == "" || apiURL == "" {
		resolved, whereamiErr := resolveWhereami(ctx, opts.Runner, repoDir)
		if whereamiErr != nil {
			// An already-connected repo stays re-connectable without flags:
			// re-connect means re-sync (the blind-spot note's remedy), not
			// re-resolve.
			if existing, present, readErr := readHostedRepoBinding(storage.BrainDir); readErr == nil && present {
				resolved = whereamiResult{RepoID: existing.RepoID, APIURL: existing.BaseURL}
			} else {
				// The runnable path comes FIRST. The previous message led
				// with the shell-out it had just tried, which reads as a
				// local breakage to fix; the command it named does not exist
				// anywhere yet, so there is nothing to fix and no reason to
				// go looking.
				return fmt.Errorf("connect: bind this repository explicitly: `entire brain connect --repo-id <repo id> --api-url <https://cell>`; automatic binding needs a host CLI providing `entire repo whereami --json`, which is not shipped yet (COR-2029), and resolving it here failed: %w", whereamiErr)
			}
		}
		if repoID == "" {
			repoID = resolved.RepoID
		}
		if apiURL == "" {
			apiURL = resolved.APIURL
		}
	}
	binding, err := bindHostedRepo(storage.BrainDir, repoID, apiURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "connected: repo %s via %s\n", binding.RepoID, binding.BaseURL)
	// Connect doubles as the initial pull so a fresh clone receives team
	// memories immediately; failures note and never fail the connect.
	return hostedFactsSyncAndPull(ctx, cmd.ErrOrStderr(), opts, repoDir, storage, hostedSyncBranch(ctx, opts.Runner, repoDir))
}

// bindHostedRepo validates and normalizes the target, then writes the
// repo-level binding. Cell URLs are commonly quoted with their /api/v1 prefix
// (whereami's api_url, docs examples), but the factsync client appends
// /api/v1/repos/... itself — storing the suffix would 404 every sync.
func bindHostedRepo(brainDir, repoID, apiURL string) (hostedRepoBinding, error) {
	if strings.TrimSpace(repoID) == "" {
		return hostedRepoBinding{}, fmt.Errorf("hosted repo id is empty")
	}
	validated, err := apiurl.Validate(apiURL)
	if err != nil {
		return hostedRepoBinding{}, err
	}
	binding := hostedRepoBinding{RepoID: strings.TrimSpace(repoID), BaseURL: strings.TrimSuffix(validated, "/api/v1")}
	if err := writeHostedRepoBinding(brainDir, binding); err != nil {
		return hostedRepoBinding{}, err
	}
	return binding, nil
}

// resolveWhereami shells out to the host CLI for this repo's hosted placement.
// It fails on every stock host CLI because the command is unshipped (see
// whereamiResult), so it is a best-effort probe, never a prerequisite: the
// auto-connect path wraps it in resolveHostedPlacement's negative cache and
// `connect` falls back to the explicit flags.
func resolveWhereami(ctx context.Context, runner CommandRunner, repoDir string) (whereamiResult, error) {
	ctx, cancel := context.WithTimeout(ctx, hostedShellOutTimeout)
	defer cancel()
	stdout, _, err := runner.Run(ctx, repoDir, hostEntireBinary, "repo", "whereami", "--json")
	if err != nil {
		return whereamiResult{}, err
	}
	var result whereamiResult
	if err := decodeHostCLIJSON(stdout, &result); err != nil {
		return whereamiResult{}, fmt.Errorf("parse whereami json: %w", err)
	}
	return result, nil
}
