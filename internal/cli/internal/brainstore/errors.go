package brainstore

// PrivacyBusyCode is the stable code for contention on the privacy side-effect lock.
const PrivacyBusyCode = "memory_privacy_busy"

// PrivacyDirtyCode reports that excluded-session cleanup has not verified
// clean, so privacy-gated reads and response writes must fail closed.
const PrivacyDirtyCode = "memory_privacy_dirty"

// Stable storage failure codes shared by artifact readers and their callers.
const (
	StateCorruptCode = "memory_state_corrupt"
	StateUnsafeCode  = "memory_state_unsafe"
)

// UnsupportedVersionCode reports persisted state written at a schema version
// newer than this build supports; the bytes are read-only, not corrupt.
const UnsupportedVersionCode = "memory_unsupported_version"
