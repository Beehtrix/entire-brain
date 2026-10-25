package workspace

import (
	"time"
)

type Manifest struct {
	SchemaVersion int             `json:"schema_version"`
	Name          string          `json:"name"`
	Repos         []Repo          `json:"repos"`
	RefreshedAt   time.Time       `json:"refreshed_at,omitempty"`
	Freshness     []RepoFreshness `json:"freshness,omitempty"`
}

type Repo struct {
	RepoKey       string `json:"repo_key"`
	Name          string `json:"name,omitempty"`
	LocalPathHint string `json:"local_path_hint,omitempty"`
}

type RepoFreshness struct {
	RepoKey string `json:"repo_key"`
	Name    string `json:"name,omitempty"`
	State   string `json:"state"`
	// PairingUnsafe is the scan-block signal: true only when the brain<->tree IDENTITY can't be
	// trusted (the local_path_hint's repo_key no longer matches, or it could not be derived), so a
	// regression scan would compare one repo's brain against an unrelated tree. This is distinct from
	// State == "unsafe", which also covers a stale/broken semantic index (a valid pairing the scan
	// can still run against raw sessions). See FreshnessBlocksScan.
	PairingUnsafe  bool   `json:"pairing_unsafe,omitempty"`
	Detail         string `json:"detail,omitempty"`
	ContractState  string `json:"contract_state,omitempty"`
	ContractDetail string `json:"contract_detail,omitempty"`
}

type Summary struct {
	Name  string `json:"name"`
	Repos int    `json:"repos"`
}

// AddResult reports what the locked add actually did, so callers can
// say "created", "added" or "already a member" without re-reading the manifest
// and racing the very window the lock closed.
type AddResult struct {
	Created bool
	Already bool
	// PreviousPathHint is the checkout an existing member pointed at before this
	// add rebound it, so a caller can say WHICH tree the workspace now scans.
	// Empty unless Already is true.
	PreviousPathHint string
}
