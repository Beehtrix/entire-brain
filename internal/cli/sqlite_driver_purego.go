package cli

// The brain's SQLite driver is the pure-Go modernc build, which ships FTS5
// unconditionally — every index (history, facts, docs) runs on it. The
// brain_cgo variant (mattn/go-sqlite3 + sqlite-vec) existed for the Stage 1b
// vector store and the planned qmd/Librarian shared-store convergence; both
// died with the semantic-retrieval removal (trail #278), so the tag and the C
// toolchain requirement were retired with it. If a shared mattn stack returns,
// reintroduce the tag here with the FTS5 pairing guard it used to carry.

import (
	_ "modernc.org/sqlite"
)

// sqliteDriverName is the database/sql driver this build registers.
const sqliteDriverName = "sqlite"

// brainCGOBuild is retained for the capabilities report (build.brain_cgo),
// which committed agent-guides parse; it is permanently false.
const brainCGOBuild = false
