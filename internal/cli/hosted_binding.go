package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
)

// hostedRepoBinding is the repo-level hosted-brain connection state, written
// by `entire brain connect` and `entire brain disconnect`. Only non-secrets
// live here; tokens are minted per call (see mintHostedToken). The per-branch
// hosted-target.json (hostedFactsBinding) records which branches have synced.
//
// OptedOut records an explicit `disconnect`: no sync until
// `connect` rewrites the binding. The coordinates are kept through an opt-out
// so re-connecting works without placement resolution. Disconnect never purges
// memories already pulled into the local store.
type hostedRepoBinding struct {
	RepoID   string `json:"repo_id"`
	BaseURL  string `json:"base_url"`
	OptedOut bool   `json:"opted_out,omitempty"`
}

const hostedRepoBindingFile = "hosted.json"

func writeHostedRepoBinding(brainDir string, binding hostedRepoBinding) error {
	return brainstore.WithBrainWriteLock(brainDir, func() error {
		return writeHostedRepoBindingLocked(brainDir, binding)
	})
}

// writeHostedRepoBindingLocked is the write half for callers that already hold
// the brain write lock (it is not reentrant).
func writeHostedRepoBindingLocked(brainDir string, binding hostedRepoBinding) error {
	binding.BaseURL = strings.TrimRight(binding.BaseURL, "/")
	data, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	return brainstore.WriteBrainRelativeFileAtomic(brainDir, hostedRepoBindingFile, append(data, '\n'), 0600)
}

// setHostedOptOut records an explicit disconnect as one locked
// read-modify-write, so a concurrent writer cannot interleave between reading
// the coordinates and marking them opted out. A corrupt state file must not
// block the opt-out — the user's intent is unambiguous — so it is replaced by
// a bare one. Returns the binding as written.
func setHostedOptOut(brainDir string) (hostedRepoBinding, error) {
	var binding hostedRepoBinding
	err := brainstore.WithBrainWriteLock(brainDir, func() error {
		existing, _, readErr := readHostedRepoBinding(brainDir)
		if readErr != nil {
			existing = hostedRepoBinding{}
		}
		existing.OptedOut = true
		binding = existing
		return writeHostedRepoBindingLocked(brainDir, existing)
	})
	return binding, err
}

func readHostedRepoBinding(brainDir string) (hostedRepoBinding, bool, error) {
	var binding hostedRepoBinding
	data, present, err := readMemoryStateFile(brainDir, hostedRepoBindingFile, "hosted repo binding", base.DefaultMaxReadBytes)
	if err != nil || !present {
		return binding, false, err
	}
	if err := json.Unmarshal(data, &binding); err != nil {
		return binding, false, fmt.Errorf("invalid hosted repo binding: %w", err)
	}
	binding.BaseURL = strings.TrimRight(binding.BaseURL, "/")
	return binding, true, nil
}
