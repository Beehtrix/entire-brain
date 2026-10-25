// Package brainstore owns shared Brain storage primitives: process and file
// locks, scoped write and privacy locks, and atomic file replacement.
//
// Callers retain responsibility for domain policy, path validation appropriate
// to each artifact, and publication transactions. Operations that need both the
// privacy side-effect lock and the Brain write lock acquire them in that order.
// The process lock registry and failure-injection hooks each have one home here.
package brainstore
