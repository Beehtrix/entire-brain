// Package patterns owns the corpus schema and deterministic candidate and
// dossier construction. Callers own database lifetime, privacy checks, and
// publication locks; rebuilding candidates does not invoke an agent.
package patterns
