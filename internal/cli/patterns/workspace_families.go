package patterns

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/workspace"
)

// WorkspaceFamily records a proposed cross-repository workflow and its verdict.
type WorkspaceFamily struct {
	ID             string              `json:"id"`
	Title          string              `json:"title"`
	Trigger        string              `json:"trigger"`
	Purpose        string              `json:"purpose"`
	CommonWorkflow []string            `json:"common_workflow"`
	PerRepo        []FamilyRepoVariant `json:"per_repo"`
	Verdict        string              `json:"verdict"`
}

// FamilyRepoVariant retains the commands associated with one member repository.
type FamilyRepoVariant struct {
	RepoKey  string   `json:"repo_key"`
	Commands []string `json:"commands"`
}

// WorkspaceMembershipFingerprint binds a family cache to a workspace and its members.
func WorkspaceMembershipFingerprint(manifest workspace.Manifest) string {
	keys := make([]string, 0, len(manifest.Repos))
	for _, repo := range manifest.Repos {
		keys = append(keys, repo.RepoKey)
	}
	sort.Strings(keys)
	return "sha256:" + base.HexSHA(manifest.Name+"\x00"+strings.Join(keys, "\x00"))
}

// WorkspaceMinRepos is the minimum corroboration for a cross-repository pattern.
const WorkspaceMinRepos = 2

func parseWorkspaceFamilies(out string) ([]WorkspaceFamily, error) {
	raw := strings.TrimSpace(out)
	if i := strings.IndexByte(raw, '{'); i >= 0 {
		if j := strings.LastIndexByte(raw, '}'); j >= i {
			raw = raw[i : j+1]
		}
	}
	var wrap struct {
		Families []WorkspaceFamily `json:"families"`
	}
	if err := json.Unmarshal([]byte(raw), &wrap); err != nil {
		return nil, fmt.Errorf("parse workspace families: %w", err)
	}
	return wrap.Families, nil
}

func familyRepoKeysJoined(f WorkspaceFamily) string {
	keys := make([]string, 0, len(f.PerRepo))
	for _, v := range f.PerRepo {
		keys = append(keys, v.RepoKey)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// RepoCount counts distinct nonempty repository keys.
func (f WorkspaceFamily) RepoCount() int {
	seen := map[string]bool{}
	for _, v := range f.PerRepo {
		if v.RepoKey != "" {
			seen[v.RepoKey] = true
		}
	}
	return len(seen)
}

// ProposeWorkspaceFamilies validates and caches agent-proposed cross-repo families.
// loadCandidates returns each member's capped, redacted candidates; a nil list
// skips that member. propose runs only when the evidence cache is stale;
// it receives redacted JSON and owns agent configuration and execution errors.
// The caller owns the staged database, privacy gates, and publication locks.
func ProposeWorkspaceFamilies(db *sql.DB, manifest workspace.Manifest, loadCandidates func(string) ([]map[string]any, error), propose func([]byte) (string, error), now time.Time) (VerifyStats, error) {
	var stats VerifyStats
	perRepo := map[string][]map[string]any{}
	contributing := 0
	repos := append([]workspace.Repo(nil), manifest.Repos...)
	sort.Slice(repos, func(i, j int) bool { return repos[i].RepoKey < repos[j].RepoKey })
	for _, repo := range repos {
		list, err := loadCandidates(repo.RepoKey)
		if err != nil {
			return stats, err
		}
		if len(list) > 0 {
			perRepo[repo.RepoKey] = list
			contributing++
		}
	}
	if contributing < WorkspaceMinRepos {
		if _, err := db.Exec(`DELETE FROM deep_dossiers WHERE pattern_id LIKE 'family:%'`); err != nil {
			return stats, err
		}
		if _, err := db.Exec(`DELETE FROM meta WHERE key='workspace_families_fingerprint'`); err != nil {
			return stats, err
		}
		return stats, nil
	}

	// Fingerprint the actual evidence payload (per-repo intent + title + commands +
	// support + reinforcement), so changed member evidence invalidates the cached
	// families even when the set of repos/intents is unchanged.
	payload, _ := json.Marshal(map[string]any{"workspace": manifest.Name, "repos": perRepo})
	fp := ProposalSampleFingerprint(payload)
	if CorpusMeta(db, "workspace_families_fingerprint") == fp &&
		CorpusScalar(db, `SELECT COUNT(*) FROM deep_dossiers WHERE pattern_id LIKE 'family:%' AND status!='current'`) == 0 &&
		CorpusScalar(db, `SELECT COUNT(*) FROM deep_dossiers WHERE pattern_id LIKE 'family:%'`) > 0 {
		stats.Cached = CorpusScalar(db, `SELECT COUNT(*) FROM deep_dossiers WHERE pattern_id LIKE 'family:%'`)
		stats.Considered = stats.Cached
		return stats, nil
	}

	out, err := propose([]byte(base.RedactText(string(payload))))
	if err != nil {
		return stats, err
	}
	families, err := parseWorkspaceFamilies(out)
	if err != nil {
		return stats, err
	}

	members := map[string]bool{}
	for k := range perRepo {
		members[k] = true
	}
	ts := now.UTC().Format(time.RFC3339)
	prepared := make([]WorkspaceFamily, 0, len(families))
	for _, f := range families {
		stats.Considered++
		// Curate per-repo variants to real members; require >=2 distinct repos.
		var variants []FamilyRepoVariant
		for _, v := range f.PerRepo {
			if members[v.RepoKey] {
				variants = append(variants, FamilyRepoVariant{RepoKey: v.RepoKey, Commands: base.RedactStrings(v.Commands)})
			}
		}
		f.PerRepo = variants
		if f.RepoCount() < WorkspaceMinRepos {
			stats.Failed++
			continue
		}
		f.Verdict = strings.ToLower(strings.TrimSpace(f.Verdict))
		if !AllowedVerdicts[f.Verdict] {
			// A missing/unknown verdict must never be treated as accepted — it would
			// surface an unverified family. Default to the softest non-accepting verdict.
			f.Verdict = "low_confidence"
		}
		f.ID = "family:" + base.HexSHA(manifest.Name+"\x00"+f.Title+"\x00"+familyRepoKeysJoined(f))
		f.Title = base.RedactText(f.Title)
		prepared = append(prepared, f)
		if f.Verdict == "accepted" {
			stats.Verified++
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM deep_dossiers WHERE pattern_id LIKE 'family:%'`); err != nil {
		return stats, err
	}
	for _, f := range prepared {
		blob, err := json.Marshal(f)
		if err != nil {
			return stats, err
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO deep_dossiers
			(pattern_id, fingerprint, json_redacted, verdict, status, created_at, updated_at)
			VALUES (?,?,?,?, 'current', ?, ?)`, f.ID, fp, base.RedactText(string(blob)), f.Verdict, ts, ts); err != nil {
			return stats, err
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('workspace_families_fingerprint', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fp); err != nil {
		return stats, err
	}
	if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('workspace_families_membership',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, WorkspaceMembershipFingerprint(manifest)); err != nil {
		return stats, err
	}
	return stats, tx.Commit()
}

// LoadAcceptedWorkspaceFamilies reads current accepted families from a validated
// corpus snapshot and checks them against the persisted workspace membership.
// The caller owns snapshot acquisition, privacy checks, and database lifetime.
func LoadAcceptedWorkspaceFamilies(db *sql.DB, wsBrainDir string) ([]WorkspaceFamily, error) {
	// A cached family is only eligible in the current persisted workspace.
	if err := base.RejectExistingSymlinkPathComponents(wsBrainDir, workspace.ManifestName); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(wsBrainDir, workspace.ManifestName))
	if err != nil {
		return nil, err
	}
	var manifest workspace.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion != workspace.SchemaVersion || manifest.Name != filepath.Base(wsBrainDir) {
		return nil, fmt.Errorf("invalid workspace manifest identity")
	}
	members := map[string]bool{}
	for _, repo := range manifest.Repos {
		if err := workspace.ValidateRepoKey(repo.RepoKey); err != nil {
			return nil, err
		}
		members[repo.RepoKey] = true
	}
	membershipCurrent := len(members) >= WorkspaceMinRepos && CorpusMeta(db, "workspace_families_membership") == WorkspaceMembershipFingerprint(manifest)
	rows, err := db.Query(`SELECT json_redacted FROM deep_dossiers WHERE pattern_id LIKE 'family:%' AND verdict='accepted' AND status='current' ORDER BY pattern_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !membershipCurrent {
		return nil, nil
	}
	var out []WorkspaceFamily
	for rows.Next() {
		var blob string
		if err := rows.Scan(&blob); err != nil {
			return out, err
		}
		var f WorkspaceFamily
		if json.Unmarshal([]byte(blob), &f) == nil && f.RepoCount() >= WorkspaceMinRepos {
			current := true
			for _, variant := range f.PerRepo {
				if !members[variant.RepoKey] {
					current = false
				}
			}
			if current {
				out = append(out, f)
			}
		}
	}
	return out, rows.Err()
}
