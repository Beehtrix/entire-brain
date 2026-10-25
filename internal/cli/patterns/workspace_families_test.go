package patterns_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/patterntest"
	"github.com/entireio/entire-brain/internal/cli/patterns"
	"github.com/entireio/entire-brain/internal/cli/workspace"
)

type familyFixture struct {
	db       *sql.DB
	dir      string
	manifest workspace.Manifest
	evidence map[string][]map[string]any
	now      time.Time
}

func newFamilyFixture(t *testing.T) *familyFixture {
	t.Helper()
	f := &familyFixture{
		db:  patterntest.FreshCorpusDB(t),
		dir: filepath.Join(t.TempDir(), "platform"),
		manifest: workspace.Manifest{SchemaVersion: workspace.SchemaVersion, Name: "platform", Repos: []workspace.Repo{
			{RepoKey: "gh/acme/b"}, {RepoKey: "gh/acme/a"},
		}},
		evidence: map[string][]map[string]any{
			"gh/acme/a": {{"intent": "release:build", "title": "build a release", "commands": []string{"make release"}, "support": 12, "reinforcement": []int{12, 0, 0}}},
			"gh/acme/b": {{"intent": "release:ship", "title": "ship a release", "commands": []string{"mise deploy"}, "support": 12, "reinforcement": []int{12, 0, 0}}},
		},
		now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
	}
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.writeManifest(t)
	return f
}

func (f *familyFixture) writeManifest(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, workspace.ManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *familyFixture) propose(run func([]byte) (string, error)) (patterns.VerifyStats, error) {
	return patterns.ProposeWorkspaceFamilies(f.db, f.manifest, func(repo string) ([]map[string]any, error) {
		return f.evidence[repo], nil
	}, run, f.now)
}

func familyProposal(t *testing.T, families ...patterns.WorkspaceFamily) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"families": families})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func releaseFamily(title, verdict string) patterns.WorkspaceFamily {
	return patterns.WorkspaceFamily{
		Title: title, Trigger: "shipping a release", Purpose: "publish the build",
		CommonWorkflow: []string{"build", "publish"}, Verdict: verdict,
		PerRepo: []patterns.FamilyRepoVariant{
			{RepoKey: "gh/acme/a", Commands: []string{"make release"}},
			{RepoKey: "gh/acme/b", Commands: []string{"mise deploy"}},
		},
	}
}

func TestWorkspaceFamilyProposalCuratesMembersAndVerdicts(t *testing.T) {
	t.Parallel()
	f := newFamilyFixture(t)
	const secret = "secret-value-123"
	f.evidence["gh/acme/a"][0]["title"] = "API_TOKEN=" + secret
	accepted := releaseFamily("release", " ACCEPTED ")
	accepted.PerRepo[0].Commands = append(accepted.PerRepo[0].Commands, "API_TOKEN="+secret)
	accepted.PerRepo = append(accepted.PerRepo, patterns.FamilyRepoVariant{RepoKey: "gh/acme/outsider"})
	unknown := releaseFamily("uncertain release", "unknown")
	oneMember := releaseFamily("one member", "accepted")
	oneMember.PerRepo[1].RepoKey = "gh/acme/outsider"
	duplicate := releaseFamily("duplicate member", "accepted")
	duplicate.PerRepo[1].RepoKey = duplicate.PerRepo[0].RepoKey
	stats, err := f.propose(func(payload []byte) (string, error) {
		if strings.Contains(string(payload), secret) {
			t.Fatal("proposal payload leaked a credential")
		}
		return "```json\n" + familyProposal(t, accepted, unknown, oneMember, duplicate) + "\n```", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Considered != 4 || stats.Verified != 1 || stats.Failed != 2 {
		t.Fatalf("unexpected proposal results: %+v", stats)
	}
	families, err := patterns.LoadAcceptedWorkspaceFamilies(f.db, f.dir)
	if err != nil || len(families) != 1 {
		t.Fatalf("accepted families = %+v, err = %v", families, err)
	}
	if families[0].RepoCount() != 2 || len(families[0].PerRepo) != 2 {
		t.Fatalf("unrecognized or duplicate members survived: %+v", families[0])
	}
	if families[0].PerRepo[1].Commands[0] != "mise deploy" {
		t.Fatalf("member variation was lost: %+v", families[0])
	}
	var blobs, verdict string
	if err := f.db.QueryRow(`SELECT group_concat(json_redacted) FROM deep_dossiers`).Scan(&blobs); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(blobs, secret) || strings.Contains(blobs, "gh/acme/outsider") {
		t.Fatal("stored families contain a credential or unrecognized member")
	}
	if err := f.db.QueryRow(`SELECT verdict FROM deep_dossiers WHERE verdict != 'accepted'`).Scan(&verdict); err != nil || verdict != "low_confidence" {
		t.Fatalf("unknown verdict = %q, err = %v", verdict, err)
	}
}

func TestWorkspaceFamilyProposalCacheTracksEvidence(t *testing.T) {
	t.Parallel()
	f := newFamilyFixture(t)
	calls := 0
	run := func([]byte) (string, error) {
		calls++
		return familyProposal(t, releaseFamily("release", "accepted")), nil
	}
	if _, err := f.propose(run); err != nil {
		t.Fatal(err)
	}
	stats, err := f.propose(run)
	if err != nil || stats.Cached != 1 || calls != 1 {
		t.Fatalf("unchanged evidence did not reuse cache: %+v, calls=%d, err=%v", stats, calls, err)
	}
	f.evidence["gh/acme/a"][0]["title"] = "changed release evidence"
	if _, err := f.propose(run); err != nil || calls != 2 {
		t.Fatalf("changed evidence did not invoke proposal: calls=%d, err=%v", calls, err)
	}
	if _, err := f.db.Exec(`UPDATE deep_dossiers SET status='stale'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.propose(run); err != nil || calls != 3 {
		t.Fatalf("stale family did not invoke proposal: calls=%d, err=%v", calls, err)
	}
	delete(f.evidence, "gh/acme/b")
	if _, err := f.propose(run); err != nil || calls != 3 {
		t.Fatalf("insufficient member evidence invoked proposal: calls=%d, err=%v", calls, err)
	}
	if got := patterntest.CorpusCount(t, f.db, "deep_dossiers"); got != 0 {
		t.Fatalf("families survived loss of corroboration: %d", got)
	}
	if patterns.CorpusMeta(f.db, "workspace_families_fingerprint") != "" {
		t.Fatal("evidence fingerprint survived loss of corroboration")
	}
}

func TestWorkspaceFamilyProposalFailurePreservesCache(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"candidate load", "agent", "parse", "database"} {
		t.Run(failure, func(t *testing.T) {
			f := newFamilyFixture(t)
			if _, err := f.propose(func([]byte) (string, error) {
				return familyProposal(t, releaseFamily("original release", "accepted")), nil
			}); err != nil {
				t.Fatal(err)
			}
			readStored := func() string {
				var blob string
				if err := f.db.QueryRow(`SELECT json_redacted FROM deep_dossiers`).Scan(&blob); err != nil {
					t.Fatal(err)
				}
				return blob
			}
			before := readStored()
			fingerprint := patterns.CorpusMeta(f.db, "workspace_families_fingerprint")
			membership := patterns.CorpusMeta(f.db, "workspace_families_membership")
			f.evidence["gh/acme/a"][0]["title"] = "changed release evidence"
			injected := errors.New("injected proposal failure")
			load := func(repo string) ([]map[string]any, error) {
				if failure == "candidate load" {
					return nil, injected
				}
				return f.evidence[repo], nil
			}
			if failure == "database" {
				if _, err := f.db.Exec(`CREATE TRIGGER reject_family BEFORE INSERT ON deep_dossiers BEGIN SELECT RAISE(ABORT, 'injected write failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			_, err := patterns.ProposeWorkspaceFamilies(f.db, f.manifest, load, func([]byte) (string, error) {
				if failure == "candidate load" {
					t.Fatal("proposal ran after candidate load failed")
				}
				if failure == "agent" {
					return "", injected
				}
				if failure == "parse" {
					return "not JSON", nil
				}
				return familyProposal(t, releaseFamily("new release", "accepted")), nil
			}, f.now)
			if err == nil {
				t.Fatal("expected proposal failure")
			}
			if (failure == "agent" || failure == "candidate load") && !errors.Is(err, injected) {
				t.Fatalf("callback error identity was lost: %v", err)
			}
			if readStored() != before || patterns.CorpusMeta(f.db, "workspace_families_fingerprint") != fingerprint || patterns.CorpusMeta(f.db, "workspace_families_membership") != membership {
				t.Fatal("failed proposal changed the previously committed family cache")
			}
		})
	}
}

func TestAcceptedWorkspaceFamiliesRequireCurrentManifest(t *testing.T) {
	t.Parallel()
	f := newFamilyFixture(t)
	if _, err := f.propose(func([]byte) (string, error) {
		return familyProposal(t, releaseFamily("release", "accepted")), nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := patterns.LoadAcceptedWorkspaceFamilies(f.db, f.dir); err != nil || len(got) != 1 {
		t.Fatalf("current family unavailable: %+v, %v", got, err)
	}
	f.manifest.Repos = append(f.manifest.Repos, workspace.Repo{RepoKey: "gh/acme/c"})
	f.writeManifest(t)
	if got, err := patterns.LoadAcceptedWorkspaceFamilies(f.db, f.dir); err != nil || len(got) != 0 {
		t.Fatalf("stale membership exposed a family: %+v, %v", got, err)
	}
	f.manifest.Name = "a-different-workspace"
	f.writeManifest(t)
	if _, err := patterns.LoadAcceptedWorkspaceFamilies(f.db, f.dir); err == nil || !strings.Contains(err.Error(), "manifest identity") {
		t.Fatalf("expected manifest identity rejection, got %v", err)
	}
}
