package cli

import (
	"context"
	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/entireio/entire-brain/internal/cli/workspace"
	"github.com/spf13/cobra"
)

func TestWorkspaceRouteTopKDeduplicatesCanonicalPair(t *testing.T) {
	caller := workspace.SymbolRef{RepoKey: "a", ID: "caller", Count: 5}
	lower := caller
	lower.Count = 3
	other := workspace.SymbolRef{RepoKey: "a", ID: "other", Count: 1}
	handler := workspace.SymbolRef{RepoKey: "b", ID: "handler", Count: 1}
	index := map[string]*workspace.ExternalContractAggregate{
		"one":     {Endpoint: "external:route:/users/:id", Type: "HTTP_CALLS", Participants: []workspace.SymbolRef{caller, other}},
		"two":     {Endpoint: "external:route:/users/{user}", Type: "HTTP_CALLS", Participants: []workspace.SymbolRef{lower}},
		"handler": {Endpoint: "external:route:/users/{name}", Type: "HANDLES_ROUTE", Participants: []workspace.SymbolRef{handler}},
	}
	got := workspace.GraphRouteCallCrossEdges(index, 2)
	if len(got) != 2 {
		t.Fatalf("got %d edges, want two unique pairs", len(got))
	}
	if workspace.GraphCrossEdgeID(got[0]) == workspace.GraphCrossEdgeID(got[1]) {
		t.Fatalf("duplicate pair displaced another edge: %+v", got)
	}
	if got[0].FromSymbol.ID != "caller" || got[0].SharedCount != 6 || got[1].FromSymbol.ID != "other" {
		t.Fatalf("unexpected best distinct pairs: %+v", got)
	}
}

func TestWorkspaceChannelTopKDeduplicatesPair(t *testing.T) {
	for _, counts := range [][]int{{3, 5}, {5, 3}} {
		index := map[string]*workspace.ExternalContractAggregate{
			"emits": {Endpoint: "external:channel:updates", Type: "EMITS", Participants: []workspace.SymbolRef{
				{RepoKey: "a", ID: "caller", Count: counts[0]},
				{RepoKey: "a", ID: "caller", Count: counts[1]},
				{RepoKey: "a", ID: "other", Count: 1},
			}},
			"listens": {Endpoint: "external:channel:updates", Type: "LISTENS_ON", Participants: []workspace.SymbolRef{{RepoKey: "b", ID: "listener", Count: 1}}},
		}
		got := workspace.GraphChannelCrossEdges(index, 2)
		if len(got) != 2 || got[0].FromSymbol.ID != "caller" || got[0].SharedCount != 6 || got[1].FromSymbol.ID != "other" {
			t.Fatalf("counts %v: best distinct pairs lost: %+v", counts, got)
		}
	}
}

func TestWorkspaceGraphDegradedMemberKeepsHealthyEdges(t *testing.T) {
	env := semanticTestEnv(t, t.TempDir())
	runner := &fakeCommandRunner{responses: map[string]fakeCommandResponse{}}
	opts := Options{Version: "test", Env: env, Runner: runner, Now: func() time.Time { return time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC) }}
	manifest := workspace.Manifest{SchemaVersion: workspace.SchemaVersion, Name: "degraded"}
	var brokenPath string
	var original []byte
	for _, name := range []string{"A", "B", "Broken"} {
		repo := t.TempDir()
		key := testLocalRepoStorageKey(t, repo)
		indexWorkspaceGraphRepo(t, &cobra.Command{Use: "index"}, opts, runner, repo, key, "HandleShared"+name)
		manifest.Repos = append(manifest.Repos, workspace.Repo{RepoKey: key, LocalPathHint: repo})
		if name == "Broken" {
			brainDir, err := base.BrainDirForKey(env, key)
			if err != nil {
				t.Fatal(err)
			}
			brokenPath = filepath.Join(brainDir, "manifest.json")
			original, err = os.ReadFile(brokenPath)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(brokenPath, []byte("{malformed"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := workspace.DefaultStore(env).WriteManifest(manifest); err != nil {
		t.Fatal(err)
	}
	payload, err := buildWorkspaceGraphPayload(context.Background(), opts, manifest, 20)
	if err != nil {
		t.Fatalf("one broken member aborted healthy graph: %v", err)
	}
	if len(payload.Results) != 3 || payload.Results[0].Error != "" || payload.Results[1].Error != "" || payload.Results[2].Error == "" || len(payload.CrossEdges) == 0 {
		t.Fatalf("degraded member diagnostics or healthy graph lost: %+v", payload)
	}
	if _, err = writeWorkspaceGraphPayload(env, payload); err != nil {
		t.Fatal(err)
	}
	cached, err := loadWorkspaceGraphPayload(env, manifest.Name)
	if err != nil || len(cached.CrossEdges) == 0 {
		t.Fatalf("degraded cache could not serve healthy edges: %+v %v", cached, err)
	}
	if err = os.WriteFile(brokenPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	cached, err = loadWorkspaceGraphPayload(env, manifest.Name)
	if err != nil || len(cached.CrossEdges) != 0 {
		t.Fatalf("healed member did not invalidate degraded generation: %+v %v", cached, err)
	}
}
