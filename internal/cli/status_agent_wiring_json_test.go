package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-brain/internal/agentsetup"
	"github.com/spf13/cobra"
)

func TestStatusJSONDisclosesMissingAgentWiring(t *testing.T) {
	t.Setenv("ENTIRE_CLI_VERSION", "")
	for _, wired := range []bool{false, true} {
		opts, repoDir, _ := statusTruthFixture(t)
		if wired {
			if err := os.MkdirAll(filepath.Join(repoDir, ".entire"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repoDir, ".entire", "agent-guide.md"), []byte("# Guide\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repoDir, "AGENTS.md"), []byte(agentsetup.Pointer), 0644); err != nil {
				t.Fatal(err)
			}
		}
		if err := runSemanticIndex(context.Background(), &cobra.Command{}, opts, semanticIndexOptions{graphBinary: "entire"}, repoDir); err != nil {
			t.Fatal(err)
		}
		out, err := execute(t, NewRootCommand(opts), "status", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var cliPayload map[string]any
		if err := json.Unmarshal([]byte(out), &cliPayload); err != nil {
			t.Fatal(err)
		}
		for surface, payload := range map[string]map[string]any{"CLI": cliPayload} {
			warnings, _ := json.Marshal(payload["warnings"])
			hasWarning := strings.Contains(string(warnings), "agents not wired")
			if hasWarning == wired {
				t.Fatalf("%s wiring warning (wired=%t): %s", surface, wired, warnings)
			}
			if !wired && (!strings.Contains(string(warnings), "agent-guide.md") || !strings.Contains(string(warnings), "setup")) {
				t.Fatalf("%s warning lacks missing files or repair command: %s", surface, warnings)
			}
			if !wired && !strings.Contains(string(warnings), "entire-brain setup") {
				t.Fatalf("%s standalone repair names an unavailable host command: %s", surface, warnings)
			}
		}
	}
}
