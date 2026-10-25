package brainstore

// PrivacyBusyCode is the stable code for contention on the privacy side-effect lock.
const PrivacyBusyCode = "memory_privacy_busy"

// Stable storage failure codes shared by artifact readers and their callers.
const (
	StateCorruptCode = "memory_state_corrupt"
	StateUnsafeCode  = "memory_state_unsafe"
)
