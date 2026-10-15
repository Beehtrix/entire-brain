package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/entireio/entire-brain/internal/apiurl"

	"github.com/spf13/cobra"
)

// hostedPlacement is the slice of the host CLI's `entire repo view --json`
// output this plugin needs (entireio/cli#2758): the repo's region-local ULID
// and its cell API base URL, both top-level fields.
type hostedPlacement struct {
	RepoID string `json:"repoId"`
	APIURL string `json:"apiUrl"`
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

Running connect (or the onboarding flow) is the ONLY way a repository starts
syncing: by default memories stay local and background triggers never connect
a repo on their own. Placement resolves through the host CLI (entire repo
view); pass --repo-id and --api-url to bypass resolution — a non-default cell
or a staging target. Re-running connect re-joins after 'entire brain
disconnect'.`,
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
		resolved, placementErr := resolveHostedPlacement(ctx, opts.Runner, repoDir)
		if placementErr != nil {
			// An already-connected (or disconnected — the opt-out keeps its
			// coordinates) repo stays re-connectable without flags: re-connect
			// means re-sync, not re-resolve.
			if existing, present, readErr := readHostedRepoBinding(storage.BrainDir); readErr == nil && present && existing.RepoID != "" {
				resolved = hostedPlacement{RepoID: existing.RepoID, APIURL: existing.BaseURL}
			} else {
				return fmt.Errorf("connect: resolve hosted placement (`entire repo view --json`): %w; pass --repo-id and --api-url explicitly", placementErr)
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
	renderHostedBanner(cmd.OutOrStdout(),
		"Connected: repo "+binding.RepoID+" is now syncing memories",
		"Memories sync periodically and at session end. Teammates' memories merge into this repo's local brain.")
	// Connect doubles as the initial pull so a fresh clone receives team
	// memories immediately; failures note and never fail the connect.
	_, err = hostedFactsSyncAndPull(ctx, cmd.ErrOrStderr(), opts, repoDir, storage, hostedSyncBranch(ctx, opts.Runner, repoDir))
	return err
}

// normalizeHostedBinding validates the target and strips a quoted /api/v1
// suffix. Cell URLs are commonly quoted with that prefix (repo view's apiUrl,
// docs examples), but the factsync client appends /api/v1/repos/... itself —
// storing the suffix would 404 every sync. OptedOut is false: a fresh binding
// is an opt-in.
func normalizeHostedBinding(repoID, apiURL string) (hostedRepoBinding, error) {
	if strings.TrimSpace(repoID) == "" {
		return hostedRepoBinding{}, fmt.Errorf("hosted repo id is empty")
	}
	validated, err := apiurl.Validate(apiURL)
	if err != nil {
		return hostedRepoBinding{}, err
	}
	return hostedRepoBinding{RepoID: strings.TrimSpace(repoID), BaseURL: strings.TrimSuffix(validated, "/api/v1")}, nil
}

// bindHostedRepo is the EXPLICIT connect write: it overwrites whatever is
// there, including an opt-out — running connect is how the user opts back in.
func bindHostedRepo(brainDir, repoID, apiURL string) (hostedRepoBinding, error) {
	binding, err := normalizeHostedBinding(repoID, apiURL)
	if err != nil {
		return hostedRepoBinding{}, err
	}
	if err := writeHostedRepoBinding(brainDir, binding); err != nil {
		return hostedRepoBinding{}, err
	}
	return binding, nil
}

// resolveHostedPlacement shells out to the host CLI for this repo's hosted
// placement.
func resolveHostedPlacement(ctx context.Context, runner CommandRunner, repoDir string) (hostedPlacement, error) {
	ctx, cancel := context.WithTimeout(ctx, hostedShellOutTimeout)
	defer cancel()
	stdout, _, err := runner.Run(ctx, repoDir, hostEntireBinary, "repo", "view", "--json")
	if err != nil {
		return hostedPlacement{}, err
	}
	var result hostedPlacement
	if err := decodeHostCLIJSON(stdout, &result); err != nil {
		return hostedPlacement{}, fmt.Errorf("parse repo view json: %w", err)
	}
	return result, nil
}
