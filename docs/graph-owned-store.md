# Graph-owned code storage

Graph owns code indexing and the sole queryable code database. Brain retains
memory, session/history data, derived workspace reports, and authored runtime
observations. Those non-code layers may have their own storage; this change
removes the duplicate **code graph**, not every SQLite database in Brain.

`refresh index` runs `entire graph index --head` with the requested profile and
`.brainignore` policy. Graph controls cache identity, invalidation, atomic
publication and rebuilds. Brain records inventory metadata, without copying
Graph's database or snapshot. `--force` requests a Graph rebuild. Persistent
worktree indexing is unsupported; Graph queries without `--head` inspect the
working tree directly.

Each inspection resolves `entire graph store`. Contract version 1 supplies an
absolute database path and entry digest, plus provider metadata and counts.
Brain opens it read-only and checks the digest on that connection before using
Graph's `brain_*_v1` views. TEMP views preserve Brain's inspection response
shapes; they neither copy code rows nor create another file. The descriptor is
never saved as a durable database path. A missing or replaced entry causes an
explicit inspection failure; briefs continue with their other sources.

Runtime observations live in `semantic/traces/observations.json`, written
atomically under Brain's writer lock. They are joined through a connection-local
TEMP table when needed. Refresh extracts observations from the active legacy
SQLite generation before removing `semantic/generations`, `semantic/snapshots`
and `semantic/overlays`. An unreadable legacy store blocks deletion so unknown
observations cannot be silently lost. Old bundles are validated for migration;
new bundles include observations and Brain layers without any code store.

Release the companion Graph change before this Brain version. Older Graph
versions without `graph store` are treated as unsupported for code inspection;
refresh still builds the other Brain layers. No installed plugin is upgraded
implicitly. The native `graph context` brief path remains in use; a failed native
context request may use the same Graph-owned SQL projection, never a legacy
Brain code database.
