package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/entire-brain/factmerge"
	"github.com/entireio/entire-brain/internal/factsync"
)

func TestHostedFeedCacheRoundTrip(t *testing.T) {
	env := EntireEnv{PluginCacheDir: t.TempDir()}
	dir, err := hostedFeedDirForKey(env, "gh/acme/widgets")
	if err != nil {
		t.Fatalf("hostedFeedDirForKey: %v", err)
	}
	state := hostedFeedState{RepoID: "r1", BaseURL: "https://api.example.com", Cursor: "tok-7"}
	items := map[string]factsync.FeedItem{
		"f1": {Record: factmerge.Record{ID: "f1", Text: "one", Status: "active"}, TrailID: "trail-1", CreatedBy: "acct-1", CreatedByHandle: "alice"},
		"f2": {Record: factmerge.Record{ID: "f2", Text: "two", Status: "retracted"}},
	}
	if err := writeHostedFeedCache(dir, state, items); err != nil {
		t.Fatalf("writeHostedFeedCache: %v", err)
	}
	gotState, gotItems, err := loadHostedFeedCache(dir)
	if err != nil {
		t.Fatalf("loadHostedFeedCache: %v", err)
	}
	if gotState != state {
		t.Fatalf("state = %+v, want %+v", gotState, state)
	}
	if len(gotItems) != 2 || gotItems["f1"].CreatedByHandle != "alice" || gotItems["f1"].TrailID != "trail-1" || gotItems["f2"].Record.Status != "retracted" {
		t.Fatalf("items = %+v", gotItems)
	}
}

func TestHostedFeedCacheMissingDirIsEmpty(t *testing.T) {
	state, items, err := loadHostedFeedCache(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("missing cache must load as empty, got %v", err)
	}
	if state != (hostedFeedState{}) || len(items) != 0 {
		t.Fatalf("state=%+v items=%d, want zero values", state, len(items))
	}
}

func TestHostedFeedCacheSkipsCorruptLines(t *testing.T) {
	env := EntireEnv{PluginCacheDir: t.TempDir()}
	dir, err := hostedFeedDirForKey(env, "gh/acme/widgets")
	if err != nil {
		t.Fatalf("hostedFeedDirForKey: %v", err)
	}
	if err := writeHostedFeedCache(dir, hostedFeedState{RepoID: "r1"}, map[string]factsync.FeedItem{
		"f1": {Record: factmerge.Record{ID: "f1", Text: "one"}},
	}); err != nil {
		t.Fatalf("writeHostedFeedCache: %v", err)
	}
	path := filepath.Join(dir, "records.ndjson")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte("{corrupt\n"), data...), 0o600); err != nil {
		t.Fatal(err)
	}
	_, items, err := loadHostedFeedCache(dir)
	if err != nil {
		t.Fatalf("corrupt line must not fail the load: %v", err)
	}
	if len(items) != 1 || items["f1"].Record.Text != "one" {
		t.Fatalf("items = %+v, want the intact record only", items)
	}
}

func TestHostedFeedDirForKeyRejectsReservedSegment(t *testing.T) {
	env := EntireEnv{PluginCacheDir: t.TempDir()}
	if _, err := hostedFeedDirForKey(env, workspaceDirName+"/acme/widgets"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("want reserved-segment error, got %v", err)
	}
}

func TestHostedFeedCacheMissingRecordsResetsCursor(t *testing.T) {
	// Bugbot PR#403: state.json surviving without records.ndjson would resume
	// the cursor over an empty cache and permanently skip earlier feed pages.
	// writeHostedFeedCache always writes both files, so a missing half means
	// the cache is broken: load it as empty, cursor included.
	env := EntireEnv{PluginCacheDir: t.TempDir()}
	dir, err := hostedFeedDirForKey(env, "gh/acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeHostedFeedCache(dir, hostedFeedState{RepoID: "r1", Cursor: "tok-7"}, map[string]factsync.FeedItem{
		"f1": {Record: factmerge.Record{ID: "f1", Text: "one"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "records.ndjson")); err != nil {
		t.Fatal(err)
	}
	state, items, err := loadHostedFeedCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state != (hostedFeedState{}) || len(items) != 0 {
		t.Fatalf("a cache missing its records must load as empty, got state=%+v items=%d", state, len(items))
	}
}
