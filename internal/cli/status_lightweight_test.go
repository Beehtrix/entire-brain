package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Reintroducing store scans into any status presentation must fail this test:
// malformed artifact contents belong to doctor, not the freshness overview.
func TestStatusLeavesDeepValidationToDoctor(t *testing.T) {
	opts, repoDir, brainDir := statusTruthFixture(t)
	if err := runSemanticIndex(context.Background(), &cobra.Command{Use: "index"}, opts, semanticIndexOptions{graphBinary: "entire"}, repoDir); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadBrainManifest(brainDir)
	if err != nil {
		t.Fatal(err)
	}
	source := manifest.Sources.Semantic
	for _, rel := range []string{source.SnapshotPath, source.StorePath} {
		if err := os.WriteFile(filepath.Join(brainDir, filepath.FromSlash(rel)), []byte("not a valid artifact\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, flags := range [][]string{{"--json"}, {"--json", "--verbose"}, {"--json", "--details"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			out, err := execute(t, NewRootCommand(opts), append([]string{"status"}, flags...)...)
			if err != nil {
				t.Fatalf("status: %v\n%s", err, out)
			}
			var report struct {
				Integrity string               `json:"integrity"`
				Memory    map[string]any       `json:"memory"`
				Semantic  *brainStatusSemantic `json:"semantic"`
			}
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatal(err)
			}
			if report.Integrity != "not_checked" {
				t.Fatalf("integrity = %q, want explicit not_checked", report.Integrity)
			}
			if report.Memory != nil {
				t.Fatal("status ran the memory audit")
			}
			if report.Semantic == nil || report.Semantic.Coverage == nil {
				t.Fatal("missing recorded semantic inventory")
			}
			for _, name := range []string{"snapshot", "store"} {
				if got := report.Semantic.Freshness.Axes[name].State; got != "present" {
					t.Fatalf("%s = %q; status should check presence, not contents", name, got)
				}
			}
			if len(report.Semantic.Coverage.Languages) != 0 {
				t.Fatal("status scanned the store for coverage histograms")
			}
		})
	}
	out, err := execute(t, NewRootCommand(opts), "doctor", "--json", "--fail-on", "none")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	var doctor doctorReport
	if err := json.Unmarshal([]byte(out), &doctor); err != nil {
		t.Fatal(err)
	}
	for _, check := range doctor.Checks {
		if check.Name == "semantic" && check.State == "error" {
			return
		}
	}
	t.Fatalf("doctor missed corrupt semantic artifacts: %s", out)
}

func TestStatusTextDistinguishesFreshnessFromIntegrity(t *testing.T) {
	opts, _, _ := statusTruthFixture(t)
	for _, flags := range [][]string{nil, {"--verbose"}, {"--details"}} {
		out, err := execute(t, NewRootCommand(opts), append([]string{"status"}, flags...)...)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "Integrity not checked") || !strings.Contains(out, "doctor`") {
			t.Fatalf("status must name the audit boundary and doctor: %s", out)
		}
	}
}
