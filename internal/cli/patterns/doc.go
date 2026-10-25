// Package patterns owns the corpus schema, deterministic candidate and dossier
// construction, workspace family validation and caching, and corpus publication
// and recovery. Callers own database lifetime, privacy checks, publication locks,
// strict marker decoding, and agent configuration.
// Deterministic candidate rebuilding does not invoke an agent.
package patterns
