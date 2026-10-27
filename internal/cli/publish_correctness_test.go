package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-brain/internal/brainwire"
	"github.com/entireio/entire-brain/internal/cli/internal/factstore"
)

func TestPublishRefusesMalformedOrAmbiguousFactStreams(t *testing.T) {
	for _, data := range []string{"not-json\n", "not-json\n{\"branch\":\"main\"}\n", "{}\n", "null\n", "{\"branch\":\"main\"}\n{}\n"} {
		t.Run(data, func(t *testing.T) {
			brainDir := t.TempDir()
			writePublishBrainFixture(t, brainDir)
			writePublishFile(t, filepath.Join(brainDir, filepath.FromSlash(factstore.FileRelPath("main"))), []byte(data))
			art := brainwire.NewBrainArtifact("gh/example/repo", "main", time.Now())
			got, err := collectFactArtifacts(art, brainDir)
			if err == nil || len(got) != 0 {
				t.Fatalf("bad stream accepted: %q; %d artifacts, %v", data, len(got), err)
			}
		})
	}
}

func TestPublishRequiresExactAcknowledgements(t *testing.T) {
	body := publishRequestBody{Artifacts: []publishArtifact{{Kind: "manifest", Ref: "head"}, {Kind: "facts", Ref: "main"}}}
	for _, tc := range []struct {
		name, stored string
		success      bool
	}{
		{"partial", `[{"kind":"manifest","ref":"head"}]`, false},
		{"unrelated", `[{"kind":"facts","ref":"other"}]`, false},
		{"duplicate", `[{"kind":"manifest","ref":"head"},{"kind":"manifest","ref":"head"},{"kind":"facts","ref":"main"}]`, false},
		{"extra", `[{"kind":"manifest","ref":"head"},{"kind":"facts","ref":"main"},{"kind":"facts","ref":"other"}]`, false},
		{"complete-reordered", `[{"kind":"facts","ref":"main"},{"kind":"manifest","ref":"head"}]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"stored":` + tc.stored + `}`))
			}))
			defer server.Close()
			_, err := postBrainArtifacts(context.Background(), server.URL, "repo", "audit-token", body)
			if (err == nil) != tc.success {
				t.Fatalf("success=%v, error=%v", tc.success, err)
			}
		})
	}
}

func TestPublishAcknowledgementErrorRedactsToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "audit-token\x1b[31m", "stored": []any{}})
	}))
	defer server.Close()
	_, err := postBrainArtifacts(context.Background(), server.URL, "repo", "audit-token", publishRequestBody{Artifacts: []publishArtifact{{Kind: "facts", Ref: "main"}}})
	if err == nil {
		t.Fatal("missing acknowledgement succeeded")
	}
	if strings.Contains(err.Error(), "audit-token") || strings.ContainsRune(err.Error(), '\x1b') {
		t.Fatalf("unsafe error: %q", err)
	}
}

// Hosted pull rebuilds a branch store from the repo-wide feed
// (pullHostedFacts), so a store legitimately carries records from other
// branches. The stream's owner is the branch that re-derives its directory
// name, not the records' uniform branch field.
func TestPublishAcceptsPulledCrossBranchFactStreams(t *testing.T) {
	for name, tc := range map[string]struct {
		branch string
		data   string
	}{
		"mixed":        {"evis/feature", "{\"branch\":\"main\",\"text\":\"pulled\"}\n{\"branch\":\"evis/feature\",\"text\":\"local\"}\n"},
		"pure-foreign": {"feature-x", "{\"branch\":\"main\",\"text\":\"pulled\"}\n"},
	} {
		t.Run(name, func(t *testing.T) {
			brainDir := t.TempDir()
			writePublishBrainFixture(t, brainDir)
			writePublishFile(t, filepath.Join(brainDir, filepath.FromSlash(factstore.FileRelPath(tc.branch))), []byte(tc.data))
			art := brainwire.NewBrainArtifact("gh/example/repo", "main", time.Now())
			got, err := collectFactArtifacts(art, brainDir)
			if err != nil || len(got) != 2 {
				t.Fatalf("cross-branch stream plus fixture main: %d artifacts, %v", len(got), err)
			}
			refs := []string{got[0].Ref, got[1].Ref}
			sort.Strings(refs)
			want := []string{tc.branch, "main"}
			sort.Strings(want)
			if refs[0] != want[0] || refs[1] != want[1] {
				t.Fatalf("streams attributed to %v, want %v", refs, want)
			}
		})
	}
}

// A slash branch sanitizes to a different slug, so a store holding ONLY pulled
// foreign records cannot be re-derived from either the records or the slug.
// Refusal is correct — publishing under a guessed branch would corrupt the
// artifact coordinate — but the error must name the directory and the branches
// it found.
func TestPublishRefusesUnresolvableFactStream(t *testing.T) {
	brainDir := t.TempDir()
	writePublishBrainFixture(t, brainDir)
	writePublishFile(t, filepath.Join(brainDir, filepath.FromSlash(factstore.FileRelPath("evis/feature"))), []byte("{\"branch\":\"main\",\"text\":\"pulled\"}\n"))
	art := brainwire.NewBrainArtifact("gh/example/repo", "main", time.Now())
	got, err := collectFactArtifacts(art, brainDir)
	if err == nil || len(got) != 0 {
		t.Fatalf("unresolvable stream accepted: %d artifacts, %v", len(got), err)
	}
	if !strings.Contains(err.Error(), "evis-feature") || !strings.Contains(err.Error(), "main") {
		t.Fatalf("error names neither directory nor record branches: %v", err)
	}
}

func TestPublishRefusesDuplicateBranchStreams(t *testing.T) {
	brainDir := t.TempDir()
	writePublishBrainFixture(t, brainDir)
	writePublishFile(t, filepath.Join(brainDir, factstore.DirName, "duplicate", factstore.FileName), []byte("{\"branch\":\"main\",\"text\":\"second stream\"}\n"))
	art := brainwire.NewBrainArtifact("gh/example/repo", "main", time.Now())
	got, err := collectFactArtifacts(art, brainDir)
	if err == nil || len(got) != 0 {
		t.Fatalf("ambiguous branch accepted: %d artifacts, %v", len(got), err)
	}
}

func TestPublishEmptyFactStreamRemainsOptional(t *testing.T) {
	brainDir := t.TempDir()
	writePublishBrainFixture(t, brainDir)
	writePublishFile(t, filepath.Join(brainDir, filepath.FromSlash(factstore.FileRelPath("main"))), []byte("\n \n"))
	art := brainwire.NewBrainArtifact("gh/example/repo", "main", time.Now())
	got, err := collectFactArtifacts(art, brainDir)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty stream: %d artifacts, %v", len(got), err)
	}
}
