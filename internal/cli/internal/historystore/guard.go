package historystore

import "github.com/entireio/entire-brain/internal/cli/internal/brainstore"

func GuardBlocksRecord(g brainstore.SessionReadGuard, r Record) bool {
	return g.BlocksSession(r.SessionID) || g.BlocksPath(r.Path)
}
