package cli

import (
	"context"
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestDistillClaudeChildEnvDisablesThinkingAndStripsClaudeCode(t *testing.T) {
	got := distillClaudeChildEnv([]string{"PATH=/bin", "CLAUDECODE=1", "HOME=/u"})
	want := []string{"PATH=/bin", "HOME=/u", "MAX_THINKING_TOKENS=0"}
	if !slices.Equal(got, want) {
		t.Fatalf("env = %v, want %v", got, want)
	}

	// A caller-set budget wins over the pipeline default.
	got = distillClaudeChildEnv([]string{"MAX_THINKING_TOKENS=1024"})
	if !slices.Equal(got, []string{"MAX_THINKING_TOKENS=1024"}) {
		t.Fatalf("caller budget overridden: %v", got)
	}
}

func TestWithDistillAgentChildEnvOnlyAppliesToClaude(t *testing.T) {
	ctx := context.Background()
	for _, agent := range []string{"codex", "ollama", "command", "none"} {
		if withDistillAgentChildEnv(ctx, agent) != ctx {
			t.Fatalf("agent %q must keep the inherited environment", agent)
		}
	}
	if withDistillAgentChildEnv(ctx, "claude-code").Value(distillChildEnvKey{}) == nil {
		t.Fatal("claude-code must carry a child environment")
	}
}

func TestExecDistillAgentAppliesContextChildEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh-based agent stub")
	}
	ctx := context.WithValue(context.Background(), distillChildEnvKey{},
		distillClaudeChildEnv([]string{"PATH=/usr/bin:/bin", "CLAUDECODE=1"}))
	out, err := execDistillAgent(ctx, t.TempDir(),
		[]string{"sh", "-c", `printf '%s|%s' "$MAX_THINKING_TOKENS" "${CLAUDECODE-unset}"`},
		nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if out != "0|unset" {
		t.Fatalf("child env = %q, want %q", out, "0|unset")
	}
}
