package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// semanticMultiRemoteFixtureRunner is semanticFixtureRunner with the fixture's
// supported remote names emitted in deterministic Git-config order.
// providerRemoteURLs hoists origin, exactly as entire-graph's gitutil.RemoteURLs
// does.
func semanticMultiRemoteFixtureRunner(repoDir, snapshot string, remotes map[string]string) *fakeCommandRunner {
	runner := semanticFixtureRunner(repoDir, snapshot)
	if origin, ok := remotes["origin"]; ok {
		runner.responses[fakeCommandKey("git", "remote", "get-url", "origin")] = fakeCommandResponse{stdout: origin + "\n"}
	} else {
		runner.responses[fakeCommandKey("git", "remote", "get-url", "origin")] = fakeCommandResponse{
			stderr: "error: No such remote 'origin'\n",
			err:    errors.New("exit status 2"),
		}
	}
	var b strings.Builder
	for _, name := range []string{"origin", "upstream", "fork", "github"} {
		if url, ok := remotes[name]; ok {
			b.WriteString("remote." + name + ".url " + url + "\n")
		}
	}
	runner.responses[fakeCommandKey("git", "config", "--get-regexp", `^remote\..*\.url$`)] = fakeCommandResponse{stdout: b.String()}
	return runner
}

// TestSemanticIndexAcceptsGitHubRemoteNotNamedOrigin is the reason this mirror
// reads EVERY remote rather than just `origin`.
//
// entire-graph derives its key from gitutil.RemoteURLs — all configured remote
// URLs, origin hoisted to the front — and takes the FIRST github.com match. So a
// repository whose GitHub remote is named `upstream` (the standard fork layout,
// and the layout of any repo that keeps a mirror as origin) gets
// gh/<owner>/<name> from the provider.
//
// A mirror that reads only `origin` computes local/<basename> for that repo,
// matches neither the provider's key nor the brain's storage key, and refuses a
// perfectly valid snapshot — reintroducing the very bug the mirror exists to fix,
// just for a narrower set of repositories.
func TestSemanticIndexAcceptsGitHubRemoteNotNamedOrigin(t *testing.T) {
	repoDir := t.TempDir()
	env := semanticTestEnv(t, repoDir)
	// origin is a non-GitHub mirror; the GitHub remote is `upstream`.
	remotes := map[string]string{
		"origin":   "https://gitlab.com/acme/widget.git",
		"upstream": "git@github.com:acme/widget.git",
	}
	// entire-graph therefore stamps the GitHub key, not local/<base>.
	snapshot := `{"schema_version":"1.0","provider":"entire-graph","provider_version":"0.1.0","repo_key":"gh/acme/widget","commit":"aaa111","tree":"tree111","capabilities":["go"],"warnings":[],"partial_failures":[]}
{"record_type":"symbol","id":"gh/acme/widget:go:internal/auth/token.go:function:auth.ValidateToken","kind":"function","name":"ValidateToken","qualified_name":"auth.ValidateToken","file_path":"internal/auth/token.go","start_line":10,"end_line":20,"signature":"func ValidateToken(token string) error","language":"Go","stable_id_version":"1"}
{"record_type":"summary","warnings":[],"partial_failures":[]}
`
	runner := semanticMultiRemoteFixtureRunner(repoDir, snapshot, remotes)
	cmd := &cobra.Command{Use: "index"}
	opts := Options{Env: env, Runner: runner, Now: time.Now}

	if err := runSemanticIndex(cmd.Context(), cmd, opts, semanticIndexOptions{graphBinary: "entire"}, repoDir); err != nil {
		t.Fatalf("index a repo whose GitHub remote is named upstream: %v", err)
	}
	// Brain's storage key still comes from origin; the artifact is normalized to it.
	if got := persistedSnapshotRepoKey(t, env, "gl/acme/widget"); got != "gl/acme/widget" {
		t.Fatalf("persisted snapshot repo_key = %q, want gl/acme/widget", got)
	}
}

// TestExpectedProviderRepoKeyReadsEveryRemote is the unit-level statement of the
// same rule, including the ordering guarantee: origin is consulted first, so a
// GitHub origin still wins over a GitHub upstream.
func TestExpectedProviderRepoKeyReadsEveryRemote(t *testing.T) {
	repoDir := t.TempDir()
	for name, tc := range map[string]struct {
		remotes map[string]string
		want    string
	}{
		"github upstream, non-github origin": {
			remotes: map[string]string{"origin": "https://gitlab.com/acme/widget.git", "upstream": "git@github.com:acme/widget.git"},
			want:    "gh/acme/widget",
		},
		"github upstream, no origin": {
			remotes: map[string]string{"upstream": "https://github.com/acme/widget.git"},
			want:    "gh/acme/widget",
		},
		"origin wins over upstream": {
			remotes: map[string]string{"origin": "git@github.com:acme/origin-repo.git", "upstream": "git@github.com:acme/upstream-repo.git"},
			want:    "gh/acme/origin-repo",
		},
		"no github remote at all": {
			remotes: map[string]string{"origin": "https://gitlab.com/acme/widget.git"},
			want:    "local/" + filepath.Base(repoDir),
		},
	} {
		t.Run(name, func(t *testing.T) {
			runner := semanticMultiRemoteFixtureRunner(repoDir, "", tc.remotes)
			if got := expectedProviderRepoKey(context.Background(), runner, repoDir); got != tc.want {
				t.Fatalf("expectedProviderRepoKey = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDoctorReportedRepoKeyIsAuthoritative: when the installed provider reports
// its own repo_key from `graph doctor --json`, that answer beats the brain's
// reimplementation of the provider rule. The provider is the process that will
// stamp the header, so it cannot be wrong about its own key — whereas the mirror
// is a copy that can drift the moment entire-graph's rule changes.
//
// Here the provider uses a rule the brain has never heard of. The mirror would
// compute local/<base> and reject the snapshot; the handshake accepts it.
func TestDoctorReportedRepoKeyIsAuthoritative(t *testing.T) {
	repoDir := t.TempDir()
	env := semanticTestEnv(t, repoDir)
	const futureKey = "sr/acme/widget" // a forge slug this brain's mirror does not know
	snapshot := `{"schema_version":"1.0","provider":"entire-graph","provider_version":"9.9.9","repo_key":"` + futureKey + `","commit":"aaa111","tree":"tree111","capabilities":["go"],"warnings":[],"partial_failures":[]}
{"record_type":"symbol","id":"` + futureKey + `:go:internal/auth/token.go:function:auth.ValidateToken","kind":"function","name":"ValidateToken","qualified_name":"auth.ValidateToken","file_path":"internal/auth/token.go","start_line":10,"end_line":20,"signature":"func ValidateToken(token string) error","language":"Go","stable_id_version":"1"}
{"record_type":"summary","warnings":[],"partial_failures":[]}
`
	runner := semanticMultiRemoteFixtureRunner(repoDir, snapshot, map[string]string{"origin": "https://sr.ht/~acme/widget"})
	runner.responses[fakeCommandKey("entire", "graph", "doctor", "--json")] = fakeCommandResponse{
		stdout: `{"no_egress":true,"repo_key":"` + futureKey + `"}`,
	}
	cmd := &cobra.Command{Use: "index"}
	opts := Options{Env: env, Runner: runner, Now: time.Now}

	if err := runSemanticIndex(cmd.Context(), cmd, opts, semanticIndexOptions{graphBinary: "entire"}, repoDir); err != nil {
		t.Fatalf("provider that reports its own repo_key was rejected: %v", err)
	}
}

// TestLiveHeaderRepoKeyCheckCannotFail documents why validateLiveSemanticHeader
// no longer compares repo_key.
//
// scanSemanticStream rewrites header.RepoKey to the brain's storage key before
// the header is ever handed on, so comparing it against that same storage key
// downstream compared a value with itself. A check that cannot fail is worse
// than no check: it reads as protection and provides none. The real assertions
// are validateSemanticProviderRepoKey (the provider's untrusted spelling) and
// readSemanticSnapshotSummary / validateImportedBundle (untrusted on-disk keys).
func TestLiveHeaderRepoKeyCheckCannotFail(t *testing.T) {
	const storageKey = "gl/acme/widget"
	var out strings.Builder
	res, err := scanSemanticStream(
		strings.NewReader(providerLocalKeyFixtureSnapshot("/tmp/widget")),
		&out,
		semanticStreamScanConfig{repoKey: storageKey, repoDir: "/tmp/widget"},
	)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.header.RepoKey != storageKey {
		t.Fatalf("header repo_key = %q; scanSemanticStream must normalize it to the storage key %q", res.header.RepoKey, storageKey)
	}
	if res.providerRepoKey != "local/widget" {
		t.Fatalf("provider repo_key = %q, want the provider's original spelling preserved for the real check", res.providerRepoKey)
	}
}

// TestIdentitylessSnapshotRefusalCarriesTheProviderReason pins COR-2146.
//
// A snapshot with no commit is almost never a provider malfunction: it is the
// provider declining to run git and saying why. The canonical case is a
// promisor remote -- Entire's own `filtered_fetches: true` writes one -- which
// makes entire-graph refuse git subprocesses to hold its no-egress guarantee.
//
// The detail strings below are copied verbatim from a real reproduction
// against entire-graph v0.4.1-nightly: a repo with remote.ckpt.promisor=true
// and remote.ckpt.partialclonefilter=blob:none emits an empty header commit
// plus these two warnings on the trailing summary record.
func TestIdentitylessSnapshotRefusalCarriesTheProviderReason(t *testing.T) {
	t.Parallel()
	const promisorDetail = `refuse Git subprocesses for unsafe or unreadable repository metadata under "/tmp/repo": its Git config declares a promisor remote (a partial clone), so Git commands here can fetch objects over the network; indexing falls back to a filesystem walk to stay offline`
	header := semanticHeader{
		RepoKey: "local/repo",
		Warnings: []semanticWarning{
			{Code: "W_GIT_WORKTREE_FALLBACK", Severity: "warning", Detail: "Git worktree enumeration was unavailable: ..."},
			{Code: "E_NO_GIT_HEAD", Severity: "warning", Detail: promisorDetail},
		},
	}

	err := validateLiveSemanticHeader(header, "", "", "", false)
	if err == nil {
		t.Fatal("an identity-less header must still be refused")
	}
	if !strings.Contains(err.Error(), "missing commit") {
		t.Fatalf("refusal must still say what is missing: %v", err)
	}
	// The whole point: the reader learns the cause and the remedy.
	if !strings.Contains(err.Error(), "E_NO_GIT_HEAD") {
		t.Fatalf("refusal dropped the provider's explanation: %v", err)
	}
	if !strings.Contains(err.Error(), "promisor remote") {
		t.Fatalf("refusal does not name the actual cause: %v", err)
	}
	// E_NO_GIT_HEAD explains; the worktree-fallback warning only restates the
	// symptom. Preferring the wrong one would be a regression in usefulness.
	if strings.Contains(err.Error(), "W_GIT_WORKTREE_FALLBACK") {
		t.Fatalf("refusal preferred the symptom warning over the cause: %v", err)
	}

	// A provider that said nothing must not grow an empty trailer.
	bare := validateLiveSemanticHeader(semanticHeader{RepoKey: "local/repo"}, "", "", "", false)
	if bare == nil || strings.Contains(bare.Error(), "the provider reported") {
		t.Fatalf("a header with no warnings must stay bare, got: %v", bare)
	}

	// A healthy header is still accepted.
	ok := semanticHeader{Commit: "c1", Tree: "t1", RepoKey: "local/repo"}
	if err := validateLiveSemanticHeader(ok, "c1", "t1", "", false); err != nil {
		t.Fatalf("a complete header must pass: %v", err)
	}
}

// TestRefusalReasonLosesThePathAndKeepsTheSentence is the leak this change
// nearly shipped.
//
// semanticProviderRefusalContext runs INSIDE validation, which is upstream of
// sanitizeSemanticWarnings -- so the provider's text arrived verbatim, host
// paths included, into an error that is printed and recorded. Routing it
// through the existing all-or-nothing sanitizer would have fixed the leak by
// deleting the feature: that one returns "<redacted>" for the whole string
// the moment it sees a path, and the string is the explanation.
func TestRefusalReasonLosesThePathAndKeepsTheSentence(t *testing.T) {
	t.Parallel()
	const repoDir = "/Users/someone/work/secret-project"
	detail := `refuse Git subprocesses for unsafe or unreadable repository metadata under "` + repoDir +
		`": its Git config declares a promisor remote (a partial clone), so Git commands here can fetch ` +
		`objects over the network; see /Users/someone/.config/entire/graph.toml; indexing falls back to a ` +
		`filesystem walk to stay offline`

	header := semanticHeader{
		RepoKey:  "local/repo",
		Warnings: []semanticWarning{{Code: "E_NO_GIT_HEAD", Severity: "warning", Detail: detail}},
	}
	err := validateLiveSemanticHeader(header, "", "", repoDir, false)
	if err == nil {
		t.Fatal("an identity-less header must still be refused")
	}
	got := err.Error()

	// Nothing a host path can be reconstructed from.
	for _, leaked := range []string{repoDir, "/Users/someone", "secret-project", ".config/entire"} {
		if strings.Contains(got, leaked) {
			t.Errorf("refusal leaked %q into a printed, recorded, bundle-travelling string:\n%s", leaked, got)
		}
	}
	// The repo's own directory is named as such; any other path is just gone.
	if !strings.Contains(got, "<repo>") {
		t.Errorf("the repo path should be recognisable as the repo, not erased:\n%s", got)
	}
	if !strings.Contains(got, "<path>") {
		t.Errorf("an unrelated absolute path should be marked, not silently dropped:\n%s", got)
	}
	// And the sentence still does its job.
	for _, kept := range []string{"E_NO_GIT_HEAD", "promisor remote", "partial clone", "filesystem walk", "stay offline"} {
		if !strings.Contains(got, kept) {
			t.Errorf("redaction ate the explanation; %q is gone:\n%s", kept, got)
		}
	}
	// The all-or-nothing sanitizer is what we must NOT have used here.
	if sanitizeSemanticWarningText(detail, repoDir) != "<redacted>" {
		t.Fatal("fixture no longer trips the all-or-nothing sanitizer, so this test proves nothing")
	}
}

// TestSchemaOnePointThreeLanguageIsClampedBeforeItReachesTheManifest closes
// the other half: Language arrived with schema 1.3 and was the one
// provider-supplied warning field with no sanitizer at all, while travelling
// to the same manifest -- and from there into a shared bundle -- as the three
// that have one.
func TestSchemaOnePointThreeLanguageIsClampedBeforeItReachesTheManifest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"a real tag survives", "go", "go"},
		{"so do the awkward real ones", "c++", "c++"},
		{"and the hyphenated ones", "objective-c", "objective-c"},
		{"a path is not a language", "/Users/someone/src/pkg", ""},
		{"nor is a windows path", `C:\Users\someone`, ""},
		{"nor is a sentence", "failed while parsing go", ""},
		{"nor is a novel", strings.Repeat("go", 40), ""},
		{"empty stays empty", "", ""},
	} {
		if got := sanitizeSemanticWarningLanguage(tc.in); got != tc.want {
			t.Errorf("%s: sanitizeSemanticWarningLanguage(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}

	// Through the function that actually feeds the manifest, not just the
	// helper: an unused clamp is not a clamp.
	out := sanitizeSemanticWarnings([]semanticWarning{
		{Code: "E_PARSE", Language: "/Users/someone/src/pkg"},
		{Code: "E_PARSE", Language: "rust"},
	}, "/Users/someone/src")
	if out[0].Language != "" {
		t.Errorf("a path reached the manifest as a language: %q", out[0].Language)
	}
	if out[1].Language != "rust" {
		t.Errorf("a real language was dropped: %q", out[1].Language)
	}
}
