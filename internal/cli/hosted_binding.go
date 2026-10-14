package cli

import (
	"encoding/json"
	"fmt"
	"strings"
)

// hostedRepoBinding is the repo-level hosted-brain connection state, written
// by `entire brain connect` and `entire brain disconnect`. Only non-secrets
// live here; tokens are minted per call (see mintHostedToken). The per-branch
// hosted-target.json (hostedFactsBinding) records which branches have synced.
//
// OptedOut records an explicit `disconnect`: no sync and no auto-connect until
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
	binding.BaseURL = strings.TrimRight(binding.BaseURL, "/")
	data, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	return withBrainWriteLock(brainDir, func() error {
		return writeBrainRelativeFileAtomic(brainDir, hostedRepoBindingFile, append(data, '\n'), 0600)
	})
}

func readHostedRepoBinding(brainDir string) (hostedRepoBinding, bool, error) {
	var binding hostedRepoBinding
	data, present, err := readMemoryStateFile(brainDir, hostedRepoBindingFile, "hosted repo binding", defaultMaxReadBytes)
	if err != nil || !present {
		return binding, false, err
	}
	if err := json.Unmarshal(data, &binding); err != nil {
		return binding, false, fmt.Errorf("invalid hosted repo binding: %w", err)
	}
	binding.BaseURL = strings.TrimRight(binding.BaseURL, "/")
	return binding, true, nil
}
