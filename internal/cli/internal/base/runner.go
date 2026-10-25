package base

import (
	"context"
)

// CommandRunner runs external commands. Tests replace it so export behavior
// can be verified without requiring a real Entire checkout.
type CommandRunner interface {
	Run(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error)
}
