package patterns

import (
	"database/sql"
	"time"

	clibase "github.com/entireio/entire-brain/internal/cli/internal/base"
)

// BuildSynapses materializes the explanation graph for promotable evidence:
// pattern→evidence-episode, each evidence episode→fact/file/symbol/commit, and
// workspace-pattern→member-repo. Scoped to episodes that back a pattern so the
// graph explains patterns rather than mirroring the whole corpus.
func BuildSynapses(db *sql.DB, now time.Time) error {
	if _, err := db.Exec(`DELETE FROM synapses`); err != nil {
		return err
	}
	ts := now.UTC().Format(time.RFC3339)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	add := func(from, to, kind string) error {
		if from == "" || to == "" {
			return nil
		}
		id := "syn:" + clibase.HexSHA(from+"\x00"+to+"\x00"+kind)
		_, err := tx.Exec(`INSERT OR IGNORE INTO synapses (id, from_id, to_id, kind, created_at) VALUES (?,?,?,?,?)`,
			id, from, to, kind, ts)
		return err
	}

	// pattern -> evidence episode
	evRows, err := tx.Query(`SELECT pattern_id, episode_id FROM pattern_evidence`)
	if err != nil {
		return err
	}
	type pe struct{ pid, ep string }
	var pes []pe
	for evRows.Next() {
		var p pe
		if evRows.Scan(&p.pid, &p.ep) == nil {
			pes = append(pes, p)
		}
	}
	evRows.Close()
	evidenceEpisodes := map[string]bool{}
	for _, p := range pes {
		if err := add(p.pid, p.ep, "pattern_evidence"); err != nil {
			return err
		}
		evidenceEpisodes[p.ep] = true
	}

	// each evidence episode -> fact / file / symbol / commit
	edge := func(query, kind string, toPrefix string) error {
		rows, err := tx.Query(query)
		if err != nil {
			return err
		}
		type link struct{ ep, to string }
		var links []link
		for rows.Next() {
			var l link
			if rows.Scan(&l.ep, &l.to) == nil {
				links = append(links, l)
			}
		}
		rows.Close()
		for _, l := range links {
			if !evidenceEpisodes[l.ep] {
				continue
			}
			if err := add(l.ep, toPrefix+l.to, kind); err != nil {
				return err
			}
		}
		return nil
	}
	if err := edge(`SELECT episode_id, fact_id FROM episode_facts`, "episode_fact", ""); err != nil {
		return err
	}
	if err := edge(`SELECT episode_id, path FROM episode_files WHERE path != ''`, "episode_file", "file:"); err != nil {
		return err
	}
	if err := edge(`SELECT episode_id, symbol_id FROM episode_symbols`, "episode_symbol", ""); err != nil {
		return err
	}
	if err := edge(`SELECT episode_id, commit_hash FROM episode_commits`, "episode_commit", "commit:"); err != nil {
		return err
	}

	// workspace pattern -> member repo variant
	wsRows, err := tx.Query(`SELECT pattern_id, repo_key FROM workspace_pattern_repos`)
	if err != nil {
		return err
	}
	type wr struct{ pid, repo string }
	var wrs []wr
	for wsRows.Next() {
		var w wr
		if wsRows.Scan(&w.pid, &w.repo) == nil {
			wrs = append(wrs, w)
		}
	}
	wsRows.Close()
	for _, w := range wrs {
		if err := add(w.pid, "repo:"+w.repo, "workspace_repo"); err != nil {
			return err
		}
	}
	return tx.Commit()
}
