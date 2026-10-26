package base

import (
	"context"
	"time"
)

// DistillAgentRunner executes the seed agent with the distillation prompt for
// one transcript chunk and returns the agent's raw stdout. It is injected so
// the orchestration can be tested with a fake agent (the real implementation,
// execDistillAgent, shells out).
type DistillAgentRunner func(ctx context.Context, dir string, args []string, input []byte, timeout time.Duration) (string, error)
