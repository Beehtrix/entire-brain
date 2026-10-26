package cli

import (
	"context"
	"os"
	"strings"
)

// distillChildEnvKey carries a replacement child-process environment for the
// distill pipeline's agent invocations. It rides the context (like the usage
// collector) so the override stays scoped to distill runs: the exec helper is
// shared with eval, outline, patterns, skills, and query expansion, whose
// latency/quality tradeoffs were never measured.
type distillChildEnvKey struct{}

// withDistillAgentChildEnv attaches the claude-code child environment to the
// distill run's context. Other agents keep the inherited environment: ollama is
// HTTP, codex has no equivalent knob, and a --agent-command argv is user-owned.
func withDistillAgentChildEnv(ctx context.Context, agent string) context.Context {
	if agent != "claude-code" {
		return ctx
	}
	return context.WithValue(ctx, distillChildEnvKey{}, distillClaudeChildEnv(os.Environ()))
}

// distillClaudeChildEnv builds the claude child environment:
//
//   - MAX_THINKING_TOKENS=0 disables extended thinking. Measured on a fixed
//     20-session corpus (COR-2214): ~97% of extraction output tokens were
//     invisible thinking generated at output speed, making API time ~92% of
//     each call; disabling it cut the run 7x (545s -> 76s) with fact yield
//     intact and the output distribution unchanged beyond run-to-run noise.
//     A caller-set MAX_THINKING_TOKENS wins over the default.
//   - CLAUDECODE is dropped so distill works when spawned from inside a Claude
//     Code session (session-end hook): claude refuses to start nested when it
//     is set, and names unsetting it as the supported bypass. The child is a
//     headless --print worker with --no-session-persistence, so none of the
//     shared-session state the guard protects exists here.
//     CLAUDE_CODE_ENTRYPOINT is dropped with it: only CLAUDECODE gates the
//     refusal today (validated against a live session), but this package's own
//     detectedAgentHarness reads either as "inside Claude Code", and the child
//     is not.
func distillClaudeChildEnv(environ []string) []string {
	out := make([]string, 0, len(environ)+1)
	hasThinkingBudget := false
	for _, kv := range environ {
		if strings.HasPrefix(kv, "CLAUDECODE=") || strings.HasPrefix(kv, "CLAUDE_CODE_ENTRYPOINT=") {
			continue
		}
		if strings.HasPrefix(kv, "MAX_THINKING_TOKENS=") {
			hasThinkingBudget = true
		}
		out = append(out, kv)
	}
	if !hasThinkingBudget {
		out = append(out, "MAX_THINKING_TOKENS=0")
	}
	return out
}
