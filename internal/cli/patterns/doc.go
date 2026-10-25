// Package patterns owns the corpus schema, deterministic candidate and dossier
// construction, and workspace family validation and caching. Callers own
// database lifetime, privacy checks, publication locks, and agent configuration.
// Deterministic candidate rebuilding does not invoke an agent.
package patterns
