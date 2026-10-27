package cli

import (
	"github.com/entireio/entire-brain/internal/cli/command/facts"
)

// factsCommandConfig binds the cli-private seams the extracted facts commands
// still depend on (COR-2268): target resolution over repoStoragePaths, the
// distill agent runners, and the brain-manifest writer.
func factsCommandConfig(opts Options) facts.Config {
	return facts.Config{
		Options: opts,
		Deps: facts.Deps{
			ResolveFactsTarget:       resolveFactsTarget,
			InARepository:            inARepository,
			AgentSurfaceTarget:       agentSurfaceTarget,
			DefaultRefreshAgent:      defaultRefreshAgent,
			DistillAgentRunner:       defaultDistillAgentRunner,
			UpdateFactSourceManifest: updateFactSourceManifestLocked,
			DistillTimeout:           defaultDistillTimeout,
		},
	}
}
