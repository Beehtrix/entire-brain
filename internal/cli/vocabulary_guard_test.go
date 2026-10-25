package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// Since COR-2217, user-facing help says "code context" (retrieval), "code
// index" (the built artifact), or "code graph" (symbol/relation structure);
// "semantic" survives only in machine surfaces (flag names, JSON keys, provider
// schema vocabulary) and in flag descriptions that explain the removed
// embedding retrieval. This walks every command's Short and Long so a new
// command cannot quietly reintroduce the old term.
func TestCommandHelpUsesCodeContextVocabulary(t *testing.T) {
	root := NewRootCommand(Options{Version: "test", Now: time.Now})
	var walk func(cmd *cobra.Command, path string)
	walk = func(cmd *cobra.Command, path string) {
		for _, text := range []string{cmd.Short, cmd.Long} {
			// Compat shims that explain the removed embedding retrieval cite
			// trail #278 and keep the historical term deliberately.
			if strings.Contains(text, "trail #278") {
				continue
			}
			if strings.Contains(strings.ToLower(text), "semantic") {
				t.Errorf("%s help still says \"semantic\": %q", path, text)
			}
		}
		for _, child := range cmd.Commands() {
			walk(child, path+" "+child.Name())
		}
	}
	walk(root, root.Name())
}
