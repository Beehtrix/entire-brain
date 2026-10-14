package cli

// brainStatusHosted is the status report's hosted-brain connection section.
// States: "connected" (repo binding present), "local_only" (no binding yet;
// auto-connect keeps retrying), "disconnected" (the egress kill switch is set;
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
func buildHostedStatus(brainDir string, factBranches []string) (*brainStatusHosted, []string) {
	var warnings []string
	hosted := &brainStatusHosted{State: hostedStateLocalOnly}
	binding, present, err := readHostedRepoBinding(brainDir)
	switch {
	case err != nil:
		warnings = append(warnings, "hosted binding unreadable: "+err.Error())
	case present:
		hosted.State = hostedStateConnected
		hosted.RepoID = binding.RepoID
		hosted.BaseURL = binding.BaseURL
		for _, branch := range factBranches {
			if checkHostedFactsBinding(brainDir, branch, binding.RepoID, binding.BaseURL) == nil {
				hosted.SyncedBranches = append(hosted.SyncedBranches, branch)
			}
		}
	}
	if brainNoEgressMode() {
		hosted.State = hostedStateDisconnected
	}
	return hosted, warnings
}

// hostedStateLabel is the human spelling of a hosted state, shared by the
// short identity line and the verbose section so they cannot disagree.
func hostedStateLabel(state string) string {
	switch state {
	case hostedStateConnected:
		return "connected"
	case hostedStateDisconnected:
		return "disconnected (ENTIRE_BRAIN_NO_EGRESS/LOCAL_ONLY set)"
	default:
		return "local-only"
	}
}
