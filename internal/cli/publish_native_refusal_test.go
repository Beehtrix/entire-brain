package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/entireio/entire-brain/internal/cli/command/publish"
)

// A refused publish must name the supported path for pushing memories:
// `memories sync` works on any connected repo over the per-fact wire, while
// publish's brain-bundle surface is GitHub-keyed. Customers on Entire-native
// repos read the bare "github.com-hosted repos only" refusal as "memories
// cannot be pushed" and dead-ended (COR-2290).
func TestPublishRefusalNamesMemoriesSync(t *testing.T) {
	remotes := map[string]string{
		"entire-native": "entire://aws-us-east-2.entire.io/et/entirehq/brainstore",
		"gitlab":        "https://gitlab.com/example/repo",
		"no remote":     "",
	}
	for name, remote := range remotes {
		t.Run(name, func(t *testing.T) {
			runner := &fakeCommandRunner{responses: map[string]fakeCommandResponse{
				fakeCommandKey("git", "remote", "get-url", "origin"): {stdout: remote + "\n"},
			}}
			_, err := publish.CanonicalWireRepoKey(context.Background(), publishConfig(Options{Runner: runner}), ".")
			if err == nil {
				t.Fatal("want a refusal error")
			}
			if !strings.Contains(err.Error(), "memories sync") {
				t.Fatalf("refusal must name the supported memories-push path; got: %v", err)
			}
		})
	}
}
