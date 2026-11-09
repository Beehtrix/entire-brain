package patterns

const CorpusPath = "patterns/corpus.sqlite"

// CorpusSchema is the full target schema (additive). Tables not yet
// populated by this slice are created so later slices are migration-free.
var CorpusSchema = []string{
	`PRAGMA journal_mode=WAL`,
	`PRAGMA foreign_keys=ON`,
	`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS indexed_sessions (
		id TEXT PRIMARY KEY,
		repo_key TEXT NOT NULL,
		session_id TEXT NOT NULL,
		checkpoint_id TEXT,
		transcript_path TEXT NOT NULL,
		branch TEXT,
		author_name TEXT,
		agent TEXT,
		created_at TEXT,
		size INTEGER NOT NULL,
		mtime_unix_nano INTEGER NOT NULL,
		content_sha TEXT NOT NULL,
		parser_version INTEGER NOT NULL,
		episodes INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS episodes (
		id TEXT PRIMARY KEY,
		episode_key TEXT NOT NULL UNIQUE,
		repo_key TEXT NOT NULL,
		workspace TEXT,
		session_id TEXT NOT NULL,
		checkpoint_id TEXT,
		turn_id TEXT,
		turn_ord INTEGER NOT NULL,
		branch TEXT,
		author_name TEXT,
		agent TEXT,
		created_at TEXT,
		source_path TEXT NOT NULL,
		start_line INTEGER NOT NULL,
		end_line INTEGER NOT NULL,
		intent_raw TEXT,
		intent_sig TEXT,
		n_tools INTEGER NOT NULL DEFAULT 0,
		tool_mix TEXT,
		files TEXT,
		n_cmds INTEGER NOT NULL DEFAULT 0,
		exit_fails INTEGER NOT NULL DEFAULT 0,
		outcome TEXT NOT NULL DEFAULT 'neutral',
		outcome_source TEXT,
		freshness_state TEXT
	)`,
	`CREATE TABLE IF NOT EXISTS episode_tools (
		episode_id TEXT NOT NULL,
		ord INTEGER NOT NULL,
		tool_type TEXT NOT NULL,
		tool_name TEXT,
		line INTEGER,
		PRIMARY KEY (episode_id, ord),
		FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS episode_commands (
		episode_id TEXT NOT NULL,
		ord INTEGER NOT NULL,
		head TEXT NOT NULL,
		raw_redacted TEXT,
		line INTEGER,
		exit_code INTEGER,
		failed INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (episode_id, ord),
		FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS grams (
		episode_id TEXT NOT NULL,
		n INTEGER NOT NULL,
		gram TEXT NOT NULL,
		PRIMARY KEY (episode_id, n, gram),
		FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS meta_hits (
		episode_id TEXT NOT NULL, meta_id TEXT NOT NULL, quote_redacted TEXT, line INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (episode_id, meta_id, line),
		FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS episode_files (
		episode_id TEXT NOT NULL, path TEXT NOT NULL, action TEXT NOT NULL, line INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (episode_id, path, action, line),
		FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE
	)`,
	// episode_symbols links an episode to the semantic symbol ids defined in the
	// files it touched (populated only when a semantic index is present).
	`CREATE TABLE IF NOT EXISTS episode_symbols (
		episode_id TEXT NOT NULL,
		symbol_id TEXT NOT NULL,
		name TEXT,
		file_path TEXT,
		PRIMARY KEY (episode_id, symbol_id),
		FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE
	)`,
	// episode_commits records git commits an episode landed, parsed from the
	// transcript's local `[branch hash] subject` confirmation — local evidence
	// only, never the network.
	`CREATE TABLE IF NOT EXISTS episode_commits (
		episode_id TEXT NOT NULL,
		commit_hash TEXT NOT NULL,
		branch TEXT,
		subject TEXT,
		line INTEGER,
		PRIMARY KEY (episode_id, commit_hash),
		FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS episode_facts (
		episode_id TEXT NOT NULL, fact_id TEXT NOT NULL, branch TEXT, kind TEXT, paths TEXT, weight REAL NOT NULL DEFAULT 1.0,
		PRIMARY KEY (episode_id, fact_id),
		FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS synapses (
		id TEXT PRIMARY KEY, from_id TEXT NOT NULL, to_id TEXT NOT NULL, kind TEXT NOT NULL,
		weight REAL NOT NULL DEFAULT 1.0, source_anchor TEXT, evidence_sha TEXT, created_at TEXT NOT NULL
	)`,
	// episode_shapes is the deterministic shape of each episode
	// (conversation | read_only | shell | write), derived from its tools,
	// commands, and file actions. Populated as a post-pass each build.
	`CREATE TABLE IF NOT EXISTS episode_shapes (
		episode_id TEXT PRIMARY KEY,
		shape TEXT NOT NULL,
		FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE
	)`,
	// themes are conversational/read-only/judgment-heavy clusters that the
	// command/task channel misses.
	`CREATE TABLE IF NOT EXISTS themes (
		id TEXT PRIMARY KEY,
		repo_key TEXT,
		scope TEXT NOT NULL DEFAULT 'repo',
		title TEXT NOT NULL,
		description TEXT,
		shape TEXT,
		member_keys TEXT,
		support INTEGER NOT NULL DEFAULT 0,
		fingerprint TEXT NOT NULL,
		strength REAL NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'active',
		verdict TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS patterns (
		id TEXT PRIMARY KEY, type TEXT NOT NULL, scope TEXT NOT NULL, repo_key TEXT, workspace TEXT,
		cluster_key TEXT NOT NULL UNIQUE, title TEXT NOT NULL, intent_sig TEXT, gram TEXT, meta_id TEXT, theme_id TEXT,
		strength REAL NOT NULL, strength_label TEXT NOT NULL, support INTEGER NOT NULL,
		n_repos INTEGER NOT NULL DEFAULT 1, n_authors INTEGER NOT NULL DEFAULT 0, n_branches INTEGER NOT NULL DEFAULT 0,
		outcome_success INTEGER NOT NULL DEFAULT 0, outcome_corrected INTEGER NOT NULL DEFAULT 0,
		outcome_neutral INTEGER NOT NULL DEFAULT 0, outcome_failed INTEGER NOT NULL DEFAULT 0,
		fingerprint TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS pattern_evidence (
		pattern_id TEXT NOT NULL, episode_id TEXT NOT NULL, rank INTEGER NOT NULL, outcome TEXT,
		source_path TEXT NOT NULL, start_line INTEGER NOT NULL, end_line INTEGER NOT NULL,
		PRIMARY KEY (pattern_id, episode_id),
		FOREIGN KEY (pattern_id) REFERENCES patterns(id) ON DELETE CASCADE,
		FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE
	)`,
	// dossiers cache the consolidation record for each promotable pattern. The
	// deterministic consolidation (json_redacted) is rebuilt token-free on every
	// refresh; the optional agent verifier output (verifier_json_redacted/verdict)
	// is cached by evidence fingerprint and survives pattern rebuilds (no FK
	// cascade — reconciled manually so verifier work is not lost). status flips to
	// 'stale' when the evidence fingerprint changes under a cached verdict.
	`CREATE TABLE IF NOT EXISTS dossiers (
		pattern_id TEXT PRIMARY KEY,
		cluster_key TEXT NOT NULL,
		fingerprint TEXT NOT NULL,
		json_redacted TEXT NOT NULL,
		verifier_json_redacted TEXT,
		verdict TEXT,
		verified_fingerprint TEXT,
		status TEXT NOT NULL DEFAULT 'current',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	// deep_dossiers cache the bounded evidence-deep consolidation (capped anchors,
	// top ones with redacted transcript excerpts) + deep verifier result for a
	// pattern. Built on demand by the explicit `patterns verify --deep`, never by
	// refresh. Keyed by pattern id; cached by the backing-set fingerprint.
	`CREATE TABLE IF NOT EXISTS deep_dossiers (
		pattern_id TEXT PRIMARY KEY,
		fingerprint TEXT NOT NULL,
		json_redacted TEXT NOT NULL,
		verifier_json_redacted TEXT,
		verdict TEXT,
		status TEXT NOT NULL DEFAULT 'current',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	// workspace_pattern_repos holds the per-member-repo breakdown for a
	// workspace-scope pattern (which member repos share it and with what support).
	// Only populated in a workspace corpus; empty in a repo corpus.
	`CREATE TABLE IF NOT EXISTS workspace_pattern_repos (
		pattern_id TEXT NOT NULL,
		repo_key TEXT NOT NULL,
		support INTEGER NOT NULL DEFAULT 0,
		outcome_success INTEGER NOT NULL DEFAULT 0,
		outcome_corrected INTEGER NOT NULL DEFAULT 0,
		outcome_neutral INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (pattern_id, repo_key),
		FOREIGN KEY (pattern_id) REFERENCES patterns(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_episodes_repo_branch ON episodes(repo_key, branch)`,
	`CREATE INDEX IF NOT EXISTS idx_episodes_intent_sig ON episodes(intent_sig)`,
	`CREATE INDEX IF NOT EXISTS idx_episodes_outcome ON episodes(outcome)`,
	`CREATE INDEX IF NOT EXISTS idx_episodes_source ON episodes(source_path, start_line)`,
	`CREATE INDEX IF NOT EXISTS idx_commands_head ON episode_commands(head)`,
	`CREATE INDEX IF NOT EXISTS idx_grams_gram ON grams(gram)`,
	`CREATE INDEX IF NOT EXISTS idx_meta_hits_meta ON meta_hits(meta_id)`,
	`CREATE INDEX IF NOT EXISTS idx_episode_files_path ON episode_files(path)`,
	`CREATE INDEX IF NOT EXISTS idx_episode_facts_fact ON episode_facts(fact_id)`,
	`CREATE INDEX IF NOT EXISTS idx_synapses_from ON synapses(from_id)`,
	`CREATE INDEX IF NOT EXISTS idx_synapses_to ON synapses(to_id)`,
	`CREATE INDEX IF NOT EXISTS idx_patterns_type_scope ON patterns(type, scope)`,
	`CREATE INDEX IF NOT EXISTS idx_patterns_strength ON patterns(strength DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_pattern_evidence_episode ON pattern_evidence(episode_id)`,
}
