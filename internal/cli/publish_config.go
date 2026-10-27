package cli

import (
	"context"

	"github.com/entireio/entire-brain/internal/cli/command/publish"
	"github.com/entireio/entire-brain/internal/cli/internal/base"
)

// publishConfig binds repo identity resolution, the hosted-target resolver
// (shared with facts sync), the session privacy guard composition, and
// repo-remote parsing at the assembly boundary. Command packages never import
// cli, and no mutable hook is copied.
func publishConfig(opts Options) publish.Config {
	return publish.Config{Options: opts, Deps: publish.Deps{
		RepoStoragePaths: repoStoragePaths,
		ResolveManualHostedTarget: func(ctx context.Context, opts base.Options, brainDir, repoDir, command, flagRepoID, flagAPIURL, flagToken string) (string, string, string, error) {
			target, err := resolveManualHostedTarget(ctx, opts, brainDir, repoDir, command, flagRepoID, flagAPIURL, flagToken)
			return target.repoID, target.baseURL, target.token, err
		},
		LoadSessionReadGuard: loadSessionReadGuard,
		GuardFactRecords:     guardFactRecords,
		ParseRepoRemote:      parseRepoRemote,
		NormalizeRepoHost:    normalizeRepoHost,
	}}
}
