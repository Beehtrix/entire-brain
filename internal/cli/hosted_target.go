package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/entireio/entire-brain/internal/apiurl"
	"github.com/entireio/entire-brain/internal/repoid"
)

// manualHostedTarget is the resolved destination for a manual hosted command
// (publish, facts sync --facts-backend=http, facts proposals).
type manualHostedTarget struct {
	repoID  string
	baseURL string
	token   string
}

// resolveManualHostedTarget resolves where a manual hosted command may send
// data, per field: explicit flag > env override > the connected binding
// (COR-2143). The binding contributes only while the repo is connected — an
// opt-out or absent binding never fills a field, so on a disconnected repo the
// command refuses unless the caller supplies the full target explicitly (the
// operator escape hatch). It performs no network call except token minting,
// which happens only after the target itself is fully resolved and validated.
//
// The token is minted through the host CLI only when the effective base URL is
// the connected binding's own — a minted login token must never be sent to a
// host the user pointed at explicitly, only to the one they connected to.
func resolveManualHostedTarget(ctx context.Context, opts Options, brainDir, repoDir, command string, flagRepoID, flagAPIURL, flagToken string) (manualHostedTarget, error) {
	var t manualHostedTarget
	var binding hostedRepoBinding
	connected := false
	if brainDir != "" {
		b, present, err := readHostedRepoBinding(brainDir)
		if err == nil && present && !b.OptedOut {
			binding, connected = b, true
		}
	}

	t.repoID = publishFlagOrEnv(flagRepoID, envRepoID)
	if t.repoID == "" && connected {
		t.repoID = binding.RepoID
	}
	if t.repoID == "" {
		return t, fmt.Errorf("%s: target repo id is required; run 'entire brain connect', or set --repo-id or %s", command, envRepoID)
	}
	if err := repoid.Validate(t.repoID); err != nil {
		return t, fmt.Errorf("%s: %w", command, err)
	}

	t.baseURL = publishFlagOrEnv(flagAPIURL, envAPIBaseURL)
	if t.baseURL == "" && connected {
		t.baseURL = binding.BaseURL
	}
	if t.baseURL == "" {
		return t, fmt.Errorf("%s: API base URL is required; run 'entire brain connect', or set --api-url or %s", command, envAPIBaseURL)
	}
	baseURL, err := apiurl.Validate(t.baseURL)
	if err != nil {
		return t, fmt.Errorf("%s: %w", command, err)
	}
	t.baseURL = baseURL

	t.token = publishFlagOrEnv(flagToken, envAPIToken)
	if t.token == "" {
		if !connected || strings.TrimRight(t.baseURL, "/") != binding.BaseURL {
			return t, fmt.Errorf("%s: API token is required; set --token or %s (minted automatically when the target is the connected binding)", command, envAPIToken)
		}
		if repoDir == "" {
			repoDir = "."
		}
		t.token, err = mintHostedToken(ctx, opts.Runner, repoDir, hostEntireBinary)
		if err != nil {
			return t, fmt.Errorf("%s: %w", command, err)
		}
	}
	return t, nil
}
