package workspace

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// repoNameMaxLength bounds the workspace-local display name. It is a
// label a person reads in a list, not a key: 64 characters is past anything
// anyone types deliberately and far short of the 5000-character value that
// motivated the bound.
const repoNameMaxLength = 64

func RepoKeyMismatch(registered, hint string) bool {
	if registered == hint {
		return false
	}
	return true
}

// ValidateRepoName applies the workspace name's own rules to the
// display name `workspace add --name` attaches to a member.
//
// The two arrived in the manifest through the same command and got opposite
// treatment: the workspace name was allowlisted, while --name was stored raw,
// so a 5000-character value and a `../`-laden one both round-tripped into
// workspace.json and from there, unescaped, into the workspace README. The name
// never builds a filesystem path, which is why this is not a traversal fix --
// it is the missing half of one command's input validation.
//
// An empty name is not an error: `setup`'s re-registration carries none, and
// refusing it there would make the display name mandatory.
func ValidateRepoName(name string) error {
	if name == "" {
		return nil
	}
	if len(name) > repoNameMaxLength {
		return fmt.Errorf("workspace repo name must be at most %d characters: %d given", repoNameMaxLength, len(name))
	}
	if name == "." || name == ".." || strings.Trim(name, ".") == "" || !namePattern.MatchString(name) {
		return fmt.Errorf("workspace repo name must contain only letters, numbers, dots, underscores, or dashes: %s", name)
	}
	return nil
}

func ValidateName(name string) error {
	if name == "" || name == "." || name == ".." || strings.Trim(name, ".") == "" || !namePattern.MatchString(name) {
		return fmt.Errorf("workspace name must contain only letters, numbers, dots, underscores, or dashes: %s", name)
	}
	return nil
}

func ValidateRepoKey(key string) error {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(key)))
	if key == "" || key == "." || key == ".." || clean != key || strings.HasPrefix(key, "../") || strings.Contains(key, "/../") || filepath.IsAbs(filepath.FromSlash(key)) {
		return fmt.Errorf("workspace repo_key is unsafe: %s", key)
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "." || segment == ".." || segment == "" {
			return fmt.Errorf("workspace repo_key is unsafe: %s", key)
		}
	}
	return nil
}

func sortRepos(repos []Repo) {
	sort.Slice(repos, func(i, j int) bool {
		return repos[i].RepoKey < repos[j].RepoKey
	})
}

func renderReadme(manifest Manifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Workspace: %s\n\n", manifest.Name)
	fmt.Fprintln(&b, "Local-only workspace brain. Repository identity is stored by repo key; path hints are local metadata.")
	if len(manifest.Repos) > 0 {
		fmt.Fprintln(&b, "\n## Repositories")
		for _, repo := range manifest.Repos {
			label := repo.RepoKey
			if repo.Name != "" {
				label = repo.Name + " (" + repo.RepoKey + ")"
			}
			fmt.Fprintf(&b, "- `%s`\n", label)
		}
	}
	return b.String()
}

// FreshnessBlocksScan reports whether a repo's brain↔working-tree pairing is too unsafe to
// scan. It blocks ONLY on PairingUnsafe — the local_path_hint's repo_key no longer matches (the repo
// moved/was replaced) or could not be derived — because scanning would then compare one repo's brain
// against an UNRELATED tree and manufacture bogus "regressions". It deliberately does NOT block on
// State == "unsafe" alone: a valid pairing with a stale/broken semantic index is still scannable (the
// detector reads raw sessions regardless of index age) — blocking those would drop real findings.
func FreshnessBlocksScan(freshness RepoFreshness) bool {
	return freshness.PairingUnsafe
}
