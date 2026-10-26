package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
	"github.com/entireio/entire-brain/internal/cli/workspace"
	"github.com/entireio/entire-brain/internal/factsync"
)

// The hosted feed cache is the repo-level staging copy of brainstore's
// per-fact delta feed: the resume cursor plus every feed item seen so far,
// keyed by fact id. The branch stores are rebuilt from it on every pull, so
// it lives in the plugin CACHE tree (rebuildable from the server by resetting
// the cursor — never authoritative brain data), beside gitmetaDirForKey's
// store. Callers mutate it under the brain write lock.
const (
	hostedFeedStateFile   = "state.json"
	hostedFeedRecordsFile = "records.ndjson"
)

// hostedFeedState pins the cache to one hosted target; a repo id or base URL
// change invalidates the whole cache, cursor included.
type hostedFeedState struct {
	RepoID  string `json:"repo_id"`
	BaseURL string `json:"base_url"`
	Cursor  string `json:"cursor"`
}

// hostedFeedDirForKey resolves the feed cache path for a repo key —
// <cache>/brain/<key>/hostedfeed — with gitmetaDirForKey's key guards.
func hostedFeedDirForKey(env EntireEnv, key string) (string, error) {
	if first, _, _ := strings.Cut(key, "/"); first == workspace.DirName {
		return "", fmt.Errorf("repo key uses the reserved %q segment: %s", workspace.DirName, key)
	}
	dirs, err := resolvePluginDirs(env)
	if err != nil {
		return "", err
	}
	root := filepath.Join(dirs.Cache, "brain")
	if err := rejectBrainRootPathSymlinks(root, filepath.FromSlash(key)); err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(key), "hostedfeed"), nil
}

// loadHostedFeedCache reads the cache; an absent cache is an empty one. A
// corrupt record line is skipped rather than failing the sync — the cache is
// rebuildable, and the worst outcome is refetching one fact.
func loadHostedFeedCache(dir string) (hostedFeedState, map[string]factsync.FeedItem, error) {
	var state hostedFeedState
	items := make(map[string]factsync.FeedItem)
	data, err := os.ReadFile(filepath.Join(dir, hostedFeedStateFile))
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &state); err != nil {
			return hostedFeedState{}, items, nil
		}
	case os.IsNotExist(err):
		return hostedFeedState{}, items, nil
	default:
		return hostedFeedState{}, nil, err
	}
	f, err := os.Open(filepath.Join(dir, hostedFeedRecordsFile))
	if err != nil {
		if os.IsNotExist(err) {
			// writeHostedFeedCache always writes both files, so a surviving
			// state.json without its records is a broken cache. Keeping its
			// cursor would resume past feed pages whose facts are now in
			// neither the cache nor the store — reset everything instead.
			return hostedFeedState{}, items, nil
		}
		return hostedFeedState{}, nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item factsync.FeedItem
		if err := json.Unmarshal([]byte(line), &item); err != nil || item.Record.ID == "" {
			continue
		}
		items[item.Record.ID] = item
	}
	if err := scanner.Err(); err != nil {
		return hostedFeedState{}, nil, err
	}
	return state, items, nil
}

// writeHostedFeedCache rewrites both cache files via temp+rename so a crashed
// writer never leaves a truncated file behind.
func writeHostedFeedCache(dir string, state hostedFeedState, items map[string]factsync.FeedItem) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var records strings.Builder
	for _, item := range items {
		line, err := json.Marshal(item)
		if err != nil {
			return err
		}
		records.Write(line)
		records.WriteByte('\n')
	}
	if err := brainstore.WriteFileAtomic(filepath.Join(dir, hostedFeedRecordsFile), []byte(records.String()), 0o600); err != nil {
		return err
	}
	stateData, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return brainstore.WriteFileAtomic(filepath.Join(dir, hostedFeedStateFile), append(stateData, '\n'), 0o600)
}
