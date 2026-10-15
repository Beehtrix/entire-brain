package cli

import (
	"fmt"
	"strings"
)

// brainStatusHosted is the status report's hosted-brain connection section.
// States: "connected" (repo binding present), "local_only" (no binding; only
// `entire brain connect` creates one), "disconnected" (the egress kill switch is set;
// COR-2109 adds the explicit opt-out as a second cause of this state).
type brainStatusHosted struct {
	State   string `json:"state"`
	RepoID  string `json:"repo_id,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
	// SyncedBranches are the declared fact branches whose per-branch marker
	// matches the current binding; a marker left by an older target does not
	// count.
	SyncedBranches []string `json:"synced_branches,omitempty"`
}

const (
	hostedStateConnected    = "connected"
	hostedStateLocalOnly    = "local_only"
	hostedStateDisconnected = "disconnected"
)

// buildHostedStatus reports this repo's hosted connection state with the same
// gate order as hostedFactsSyncAndPull: the kill switch wins, then the binding
// decides. The binding stays visible under the kill switch so the reader sees
// what it suppresses. A corrupt binding degrades to local_only with a warning
// rather than failing status over a sync-state file.
func buildHostedStatus(brainDir string, factBranches []string) (*brainStatusHosted, string) {
	var warning string
	hosted := &brainStatusHosted{State: hostedStateLocalOnly}
	binding, present, err := readHostedRepoBinding(brainDir)
	switch {
	case err != nil:
		warning = "hosted binding unreadable (run 'entire brain connect' to rebind): " + err.Error()
	case present && binding.OptedOut:
		// Coordinates survive an opt-out (reconnect needs no placement
		// resolution), so the target stays visible here too.
		hosted.State = hostedStateDisconnected
		hosted.RepoID = binding.RepoID
		hosted.BaseURL = binding.BaseURL
	case present:
		hosted.State = hostedStateConnected
		hosted.RepoID = binding.RepoID
		hosted.BaseURL = binding.BaseURL
		// A broken marker is corruption, not "never synced" — report it like
		// the unreadable top-level binding above instead of posing as a
		// healthy unsynced branch. The two warnings cannot co-occur: markers
		// are only inspected when the binding itself was readable.
		var broken []string
		var brokenErr error
		for _, branch := range factBranches {
			marker, markerPresent, markerErr := readHostedFactsBinding(brainDir, branch)
			switch {
			case markerErr != nil:
				broken = append(broken, branch)
				if brokenErr == nil {
					brokenErr = markerErr
				}
			case markerPresent && marker.RepoID == binding.RepoID && marker.BaseURL == binding.BaseURL:
				hosted.SyncedBranches = append(hosted.SyncedBranches, branch)
			}
		}
		if len(broken) > 0 {
			warning = fmt.Sprintf("hosted sync marker unreadable for branch(es) %s: %v", strings.Join(broken, ", "), brokenErr)
		}
	}
	if brainNoEgressMode() {
		hosted.State = hostedStateDisconnected
	}
	return hosted, warning
}

// hostedStateLabel is the short-report value for the "Brainstore:" item; the
// cause and remedy live in the verbose line (hostedStatusLine).
func hostedStateLabel(state string) string {
	switch state {
	case hostedStateConnected:
		return "connected"
	case hostedStateDisconnected:
		return "disconnected"
	default:
		return "local-only"
	}
}

// hostedStatusLine is the verbose report's one-line hosted state: the label
// plus the cause or remedy. Coordinates are machine detail and stay in --json.
func hostedStatusLine(h *brainStatusHosted) string {
	switch h.State {
	case hostedStateConnected:
		if n := len(h.SyncedBranches); n > 0 {
			word := "branches"
			if n == 1 {
				word = "branch"
			}
			return fmt.Sprintf("Connected to Brainstore (%d %s synced)", n, word)
		}
		return "Connected to Brainstore (no branches synced yet)"
	case hostedStateDisconnected:
		// Two causes share the state and the kill switch wins the gate order,
		// so it also wins the blame line. Name the variable actually set.
		switch {
		case securityToggleEnabled("ENTIRE_BRAIN_NO_EGRESS"):
			return "Disconnected from Brainstore (ENTIRE_BRAIN_NO_EGRESS set)"
		case securityToggleEnabled("ENTIRE_BRAIN_LOCAL_ONLY"):
			return "Disconnected from Brainstore (ENTIRE_BRAIN_LOCAL_ONLY set)"
		default:
			return "Disconnected from Brainstore (run 'entire brain connect' to rejoin)"
		}
	default:
		return "Local-only (run 'entire brain connect' to join Brainstore)"
	}
}
