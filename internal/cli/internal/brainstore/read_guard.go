package brainstore

import "strings"

// SessionReadGuard is the retrieval-time tombstone view: every
// conversation/history/fact retrieval boundary consults it so an excluded
// session becomes unreadable the moment its tombstone lands, without waiting
// for the derived rebuild. Defense in depth, not a substitute for cleanup.
type SessionReadGuard struct {
	IDs            map[string]SessionTombstone
	Paths          map[string]string // excluded transcript rel -> session id, from manifest and durable scope
	PolicyIdentity string            // exact checked tombstone bytes used to build the guard
}

// DurableExcludedTranscriptPaths extends paths with each excluded session's
// durable transaction scope so refresh cannot reopen or reindex an excluded
// export it no longer names. A nil paths map starts empty.
func DurableExcludedTranscriptPaths(brainDir string, stones SessionTombstones, paths map[string]string) (map[string]string, error) {
	if paths == nil {
		paths = map[string]string{}
	}
	for id := range stones.Excluded {
		tx, _, err := LoadPrivacyTransactionChecked(brainDir, id)
		if err != nil {
			return nil, err
		}
		durable, err := PrivacyTransactionTranscriptPaths(tx)
		if err != nil {
			return nil, err
		}
		for _, rel := range durable {
			paths[rel] = id
		}
	}
	return paths, nil
}

// LoadSessionReadGuard builds the guard from tombstones, durable transaction
// paths, and any additional excluded paths the caller resolves from its
// manifest (nil when there is none). Records that lost their session id still
// resolve by path. Invalid present state is a
// typed error; serving must stop rather than silently restore excluded data.
func LoadSessionReadGuard(brainDir string, manifestPaths func(SessionTombstones) map[string]string) (SessionReadGuard, error) {
	stones, state, err := LoadSessionTombstonesChecked(brainDir)
	if err != nil {
		return SessionReadGuard{}, err
	}
	guard := SessionReadGuard{PolicyIdentity: state.Identity}
	if len(stones.Excluded) == 0 {
		return guard, nil
	}
	guard.IDs = stones.Excluded
	seed := map[string]string{}
	if manifestPaths != nil {
		seed = manifestPaths(stones)
	}
	guard.Paths, err = DurableExcludedTranscriptPaths(brainDir, stones, seed)
	if err != nil {
		return SessionReadGuard{}, err
	}
	return guard, nil
}

func (g SessionReadGuard) Empty() bool { return len(g.IDs) == 0 }

func (g SessionReadGuard) BlocksSession(sessionID string) bool {
	if g.Empty() {
		return false
	}
	_, ok := g.IDs[strings.TrimSpace(sessionID)]
	return ok
}

// BlocksPath reports whether a transcript path belongs to an excluded session,
// matching under the shared privacy path normalization.
func (g SessionReadGuard) BlocksPath(path string) bool {
	_, ok := g.Paths[NormalizePrivacyTranscriptPath(path)]
	return ok
}
