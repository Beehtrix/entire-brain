// Package brainstore owns shared Brain storage primitives: process and file
// locks, scoped write and privacy locks, atomic file replacement, bounded
// artifact reads, and descriptor-pinned private SQLite snapshots.
//
// Callers retain responsibility for domain policy, path validation appropriate
// to each artifact, and publication transactions. Operations that need both the
// privacy side-effect lock and the Brain write lock acquire them in that order.
// The process lock registry and failure-injection hooks each have one home here.
// Snapshot readers copy and replay the source WAL in a private directory;
// callers own the returned cleanup and retain publication-marker and policy
// checks around snapshot acquisition.
package brainstore
