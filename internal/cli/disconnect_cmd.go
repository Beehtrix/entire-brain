package cli

import (
	"fmt"

	"github.com/entireio/entire-brain/internal/tui"

	"github.com/spf13/cobra"
)

func newDisconnectCommand(opts Options) *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect",
		Short: "Stop syncing this repository with its hosted brain",
		Long: `disconnect opts this repository out of hosted-brain syncing: memories stop
publishing and pulling, and the daemon's auto-connect will not re-bind it.

Your local brain keeps working exactly as before, and memories already pulled
from teammates stay in the local store — disconnect is not a purge.

Run 'entire brain connect' to opt back in.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDisconnect(cmd, opts)
		},
	}
}

func runDisconnect(cmd *cobra.Command, opts Options) error {
	ctx := cmd.Context()
	repoDir, local, err := resolveLocalTargetRepoDir(ctx, opts.Runner, agentSurfaceTarget(opts, nil))
	if err != nil {
		return err
	}
	if !local {
		return fmt.Errorf("disconnect requires a local repository path")
	}
	storage, err := repoStoragePaths(ctx, opts.Runner, opts.Env, repoDir)
	if err != nil {
		return err
	}
	// Works unconnected too: the opt-out is exactly the "never auto-bind me"
	// record.
	binding, err := setHostedOptOut(storage.BrainDir)
	if err != nil {
		return fmt.Errorf("disconnect: %w", err)
	}

	out := cmd.OutOrStdout()
	render := tui.NewRenderer(out)
	if binding.RepoID != "" {
		fmt.Fprintf(out, "%s %s\n\n", render.Mark(tui.MarkDone),
			render.Bold("Disconnected: repo "+binding.RepoID+" no longer syncs memories"))
		fmt.Fprintln(out, render.Dim("  Your local brain keeps working exactly as before. Memories already pulled from teammates stay in the local store."))
		fmt.Fprintln(out)
		fmt.Fprintln(out, render.Dim("  Run `entire brain connect` to resume syncing."))
		return nil
	}
	fmt.Fprintf(out, "%s %s\n\n", render.Mark(tui.MarkDone),
		render.Bold("Disconnected: this repository will not auto-connect to a hosted brain"))
	fmt.Fprintln(out, render.Dim("  Your local brain keeps working exactly as before."))
	fmt.Fprintln(out, render.Dim("  Run `entire brain connect` to join."))
	return nil
}
