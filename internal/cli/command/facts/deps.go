// Package facts hosts the durable-facts command surface as it moves out of
// package cli (COR-2268). The remaining cli-private seams — repo/target
// resolution, the distill agent runners, and the brain-manifest writer — are
// injected through Deps; everything else comes from factmerge, factstore, and
// base directly.
package facts

import (
	"context"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
)

// Deps are the cli-bound seams the facts commands still need. root.go wires
// them from package cli; tests may supply fakes.
type Deps struct {
	// ResolveFactsTarget resolves the repo, brain directory, and the branch
	// facts are scoped to (the live git branch unless overridden).
	ResolveFactsTarget func(ctx context.Context, opts base.Options, target, branchOverride string) (repoDir, brainDir, branch string, err error)
	// InARepository reports whether the current directory is actually inside a
	// git working tree, erring only when repository identity is undecidable.
	InARepository func(ctx context.Context, opts base.Options) (bool, error)
	// AgentSurfaceTarget resolves the repository path argument for a command.
	AgentSurfaceTarget func(opts base.Options, args []string) string
	// DefaultRefreshAgent picks the agent an "auto" classification resolves to.
	DefaultRefreshAgent func(ctx context.Context, runner base.CommandRunner, repoDir string) string
	// DistillAgentRunner returns the runner that executes the named agent.
	DistillAgentRunner func(agent string) base.DistillAgentRunner
	// UpdateFactSourceManifest refreshes sources.facts in the brain manifest;
	// the caller already holds the brain write lock.
	UpdateFactSourceManifest func(brainDir string, now time.Time) error
	// DistillTimeout bounds one agent classification call.
	DistillTimeout time.Duration
}

// Config carries the CLI options plus the injected cli-bound seams.
type Config struct {
	Options base.Options
	Deps    Deps
}
