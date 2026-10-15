package cli

import (
	"context"
	"encoding/json"
	"errors"
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
	for _, flags := range [][]string{nil, {"--verbose"}, {"--details"}} {
		t.Run("text "+strings.Join(flags, " "), func(t *testing.T) {
			out, err := execute(t, NewRootCommand(opts), append([]string{"status"}, flags...)...)
			if err != nil {
				t.Fatalf("status: %v\n%s", err, out)
			}
			if !strings.Contains(out, "Integrity not checked") || !strings.Contains(out, "doctor`") {
				t.Fatalf("status must name the audit boundary and doctor: %s", out)
			}
			for _, diagnosis := range []string{"snapshot=unsafe", "store=unsafe", "snapshot: unsafe", "store: unsafe", "not a database", "invalid character", "unreadable", "corrupt"} {
				if strings.Contains(out, diagnosis) {
					t.Fatalf("status diagnosed artifact contents (%s): %s", diagnosis, out)
				}
			}
			if !strings.Contains(out, "semantic") || !strings.Contains(out, "symbols") {
				t.Fatalf("status lost recorded semantic inventory: %s", out)
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

// Status must not read dirty snapshot contents, even when a stored fingerprint
// could match. Strict reports must still detect an untracked content-only edit.
func TestStatusSkipsDirtySnapshotFingerprints(t *testing.T) {
	for _, layer := range []string{"semantic", "seed"} {
		t.Run(layer, func(t *testing.T) {
			opts, repoDir, brainDir := statusTruthFixture(t)
			ctx := context.Background()
			if err := runSemanticIndex(ctx, &cobra.Command{Use: "index"}, opts, semanticIndexOptions{graphBinary: "entire"}, repoDir); err != nil {
				t.Fatal(err)
			}
			runner := opts.Runner.(*fakeCommandRunner)
			runner.responses[fakeCommandKey("git", "status", "--porcelain")] = fakeCommandResponse{stdout: "?? new.go\n"}
			runner.responses[fakeCommandKey("git", "status", "--porcelain", "--untracked-files=all")] = fakeCommandResponse{stdout: "?? new.go\n"}
			runner.responses[fakeCommandKey("git", "diff", "--binary", "HEAD")] = fakeCommandResponse{}
			runner.responses[fakeCommandKey("git", "diff", "--cached", "--binary", "HEAD")] = fakeCommandResponse{}
			untracked := filepath.Join(repoDir, "new.go")
			if err := os.WriteFile(untracked, []byte("package before\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			hash, err := worktreeFingerprint(ctx, runner, repoDir)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := loadBrainManifest(brainDir)
			if err != nil {
				t.Fatal(err)
			}
			if layer == "semantic" {
				manifest.Sources.Semantic.WorktreeMode = "worktree"
				manifest.Sources.Semantic.WorktreeHash = hash
			} else {
				manifest.Sources.Seed = &seedSourceManifest{GeneratedAt: opts.Now(), Commit: "aaa111", WorktreeMode: "worktree", WorktreeHash: hash}
				manifest.Sources.Docs = &docSourceManifest{GeneratedAt: opts.Now(), IndexPath: docIndexPath}
				if err := writeBrainRelativeFileAtomic(brainDir, docIndexPath, []byte(`{"records":[]}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeBrainManifestAndReadme(brainDir, *manifest); err != nil {
				t.Fatal(err)
			}
			axis := func(report brainStatusReport) staleAxis {
				if layer == "semantic" {
					return report.Semantic.Freshness.Axes["worktree"]
				}
				return report.Retrieval.Freshness.Axes["seed"]
			}
			for _, changed := range []bool{false, true} {
				if changed {
					if err := os.WriteFile(untracked, []byte("package after\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				for _, flags := range [][]string{{"--json"}, {"--json", "--verbose"}, {"--json", "--details"}} {
					runner.calls = nil
					out, err := execute(t, NewRootCommand(opts), append([]string{"status"}, flags...)...)
					if err != nil {
						t.Fatalf("status: %v\n%s", err, out)
					}
					var report brainStatusReport
					if err := json.Unmarshal([]byte(out), &report); err != nil {
						t.Fatal(err)
					}
					if got := axis(report); got.State != "degraded" || !strings.Contains(got.Detail, "not verified") {
						t.Errorf("dirty snapshot must be unverified, changed=%v: %+v", changed, got)
					}
					freshness := report.Semantic.Freshness
					if layer == "seed" {
						freshness = report.Retrieval.Freshness
					}
					if freshness.Severity != "degraded" {
						t.Errorf("unverified snapshot severity = %q, want degraded", freshness.Severity)
					}
					for _, call := range runner.calls {
						if call.name == "git" && strings.Contains(strings.Join(call.args, " "), "--binary") {
							t.Errorf("status read worktree contents: %v", call.args)
						}
					}
				}
				out, err := execute(t, NewRootCommand(opts), "status", "--json", "--fail-on", "release")
				if !errors.Is(err, errSemanticAuditGate) {
					t.Errorf("release gate accepted unverified content: %v\n%s", err, out)
				}
				runner.calls = nil
				strict, err := buildBrainStatusReport(ctx, opts, repoDir)
				if err != nil {
					t.Fatal(err)
				}
				want := "dirty-indexed"
				if changed {
					want = "dirty-stale"
				}
				if got := axis(strict).State; got != want {
					t.Errorf("strict snapshot state = %s, want %s", got, want)
				}
				if !fakeRunnerCalled(runner, "git", "diff", "--binary", "HEAD") || !fakeRunnerCalled(runner, "git", "diff", "--cached", "--binary", "HEAD") {
					t.Error("strict report stopped fingerprinting dirty contents")
				}
			}
			for _, tc := range []struct {
				name, mode, hash, head, status, want string
			}{
				{"missing fingerprint", "worktree", "", "aaa111", "?? new.go\n", "unsafe"},
				{"committed index", "tracked", hash, "aaa111", "?? new.go\n", "dirty-unindexed"},
				{"cleaned overlay", "worktree", hash, "aaa111", "", "worktree-overlay-stale"},
				{"head mismatch", "worktree", hash, "newhead", "?? new.go\n", "stale"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if layer == "semantic" {
						manifest.Sources.Semantic.WorktreeMode = tc.mode
						manifest.Sources.Semantic.WorktreeHash = tc.hash
					} else {
						manifest.Sources.Seed.WorktreeMode = tc.mode
						manifest.Sources.Seed.WorktreeHash = tc.hash
					}
					if err := writeBrainManifestAndReadme(brainDir, *manifest); err != nil {
						t.Fatal(err)
					}
					runner.responses[fakeCommandKey("git", "rev-parse", "HEAD")] = fakeCommandResponse{stdout: tc.head + "\n"}
					runner.responses[fakeCommandKey("git", "status", "--porcelain")] = fakeCommandResponse{stdout: tc.status}
					runner.responses[fakeCommandKey("git", "status", "--porcelain", "--untracked-files=all")] = fakeCommandResponse{stdout: tc.status}
					runner.calls = nil
					report, err := buildAvailableBrainStatusReport(ctx, opts, repoDir)
					if err != nil {
						t.Fatal(err)
					}
					got := axis(report).State
					if layer == "semantic" && tc.name == "head mismatch" {
						got = report.Semantic.Freshness.Axes["head"].State
					}
					if got != tc.want {
						t.Errorf("state = %q, want %q", got, tc.want)
					}
					for _, call := range runner.calls {
						if call.name == "git" && strings.Contains(strings.Join(call.args, " "), "--binary") {
							t.Errorf("status read worktree contents: %v", call.args)
						}
					}
				})
			}
		})
	}
}
