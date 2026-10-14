package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func connectTestEnv(t *testing.T, repoDir string) EntireEnv {
	t.Helper()
	return EntireEnv{
		RepoRoot:        repoDir,
		PluginConfigDir: t.TempDir(),
		PluginDataDir:   t.TempDir(),
		PluginStateDir:  t.TempDir(),
		PluginCacheDir:  t.TempDir(),
	}
}

func runConnectCommand(t *testing.T, opts Options, args ...string) (string, string, error) {
	t.Helper()
	cmd := newConnectCommand(opts)
	cmd.SetArgs(args)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestConnectWithExplicitFlagsWritesBindingWithoutShellOut(t *testing.T) {
	repoDir := t.TempDir()
	env := connectTestEnv(t, repoDir)
	placementCalls := 0
	runner := commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		if name == "entire" && len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			placementCalls++
		}
		return nil, nil, errors.New("not available")
	})
	opts := Options{Version: "test", Env: env, Runner: runner, Now: time.Now}

	out, _, err := runConnectCommand(t, opts,
		"--repo-id", "01M2Q9WATHXVM46CD8Y4D3AX8W",
		"--api-url", "https://cell.api.example.com/api/v1")
	if err != nil {
		t.Fatal(err)
	}
	if placementCalls != 0 {
		t.Fatalf("explicit flags must bypass placement resolution entirely, got %d calls", placementCalls)
	}
	for _, want := range []string{
		"Connected: repo 01M2Q9WATHXVM46CD8Y4D3AX8W is now syncing memories",
		"sync periodically and at session end",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output must contain %q, got:\n%s", want, out)
		}
	}
	assertHostedBannerLayout(t, out)
	storage, err := repoStoragePaths(context.Background(), runner, env, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	binding, present, err := readHostedRepoBinding(storage.BrainDir)
	if err != nil || !present {
		t.Fatalf("binding must be written: present=%v err=%v", present, err)
	}
	// The factsync client appends /api/v1/repos/... itself, so connect must
	// store the origin: a stored /api/v1 suffix would 404 every sync.
	if binding.RepoID != "01M2Q9WATHXVM46CD8Y4D3AX8W" ||
		binding.BaseURL != "https://cell.api.example.com" {
		t.Fatalf("binding mismatch: %+v", binding)
	}
}

func TestConnectResolvesViaRepoView(t *testing.T) {
	repoDir := t.TempDir()
	env := connectTestEnv(t, repoDir)
	runner := commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		if name == "entire" && len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			return []byte(`{"repoId":"01AAAAAAAAAAAAAAAAAAAAAAAA","apiUrl":"https://eu.api.example.com/api/v1","name":"test/repo"}` + "\nUpdate available! 1 -> 2\n"), nil, nil
		}
		return nil, nil, errors.New("not available")
	})
	opts := Options{Version: "test", Env: env, Runner: runner, Now: time.Now}

	if _, _, err := runConnectCommand(t, opts); err != nil {
		t.Fatal(err)
	}
	storage, err := repoStoragePaths(context.Background(), runner, env, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	binding, present, err := readHostedRepoBinding(storage.BrainDir)
	if err != nil || !present {
		t.Fatalf("binding must be written: present=%v err=%v", present, err)
	}
	if binding.RepoID != "01AAAAAAAAAAAAAAAAAAAAAAAA" ||
		binding.BaseURL != "https://eu.api.example.com" {
		t.Fatalf("binding mismatch: %+v", binding)
	}
}

func TestConnectPlacementUnavailableIsCleanError(t *testing.T) {
	repoDir := t.TempDir()
	env := connectTestEnv(t, repoDir)
	runner := commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		return nil, nil, errors.New("unknown command")
	})
	opts := Options{Version: "test", Env: env, Runner: runner, Now: time.Now}

	_, _, err := runConnectCommand(t, opts)
	if err == nil {
		t.Fatal("placement-resolution failure without flags must error")
	}
	if !strings.Contains(err.Error(), "--repo-id") {
		t.Fatalf("error must point at the explicit flags, got: %v", err)
	}
	storage, storageErr := repoStoragePaths(context.Background(), runner, env, repoDir)
	if storageErr != nil {
		t.Fatal(storageErr)
	}
	if _, present, _ := readHostedRepoBinding(storage.BrainDir); present {
		t.Fatal("no binding may be written on failure")
	}
}

func TestConnectRejectsInvalidAPIURL(t *testing.T) {
	repoDir := t.TempDir()
	env := connectTestEnv(t, repoDir)
	runner := commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		return nil, nil, errors.New("not available")
	})
	opts := Options{Version: "test", Env: env, Runner: runner, Now: time.Now}

	_, _, err := runConnectCommand(t, opts,
		"--repo-id", "01M2Q9WATHXVM46CD8Y4D3AX8W",
		"--api-url", "http://evil.example/api/v1")
	if err == nil {
		t.Fatal("plaintext non-loopback URL must be rejected")
	}
	storage, storageErr := repoStoragePaths(context.Background(), runner, env, repoDir)
	if storageErr != nil {
		t.Fatal(storageErr)
	}
	if _, present, _ := readHostedRepoBinding(storage.BrainDir); present {
		t.Fatal("no binding may be written for a rejected URL")
	}
}

func TestConnectRunsInitialPull(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	remoteFact := hostedSyncTestFact("team fact", now)
	fake := &hostedFake{ref: "facts-remote", authors: map[string]string{remoteFact.ID: "teammate"}}
	fake.data = encodeFactsNDJSON(t, []factRecord{remoteFact})
	srv := newHostedFakeServer(t, fake)

	repoDir := t.TempDir()
	env := connectTestEnv(t, repoDir)
	runner := commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		if name == "entire" && len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
			return []byte(fakeJWT + "\n"), nil, nil
		}
		return nil, nil, errors.New("not available")
	})
	opts := Options{Version: "test", Env: env, Runner: runner, Now: func() time.Time { return now }}

	if _, _, err := runConnectCommand(t, opts,
		"--repo-id", "repo1", "--api-url", srv.URL); err != nil {
		t.Fatal(err)
	}
	storage, err := repoStoragePaths(context.Background(), runner, env, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	local, err := loadFacts(storage.BrainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range local {
		if rec.ID == remoteFact.ID && rec.Author == "teammate" {
			found = true
		}
	}
	if !found {
		t.Fatalf("connect must perform the initial pull, got %d local records", len(local))
	}
}

func TestResolveHostedPlacementBoundsTheShellOut(t *testing.T) {
	runner := commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the placement shell-out must carry a deadline")
		}
		return []byte(`{"repoId":"r","apiUrl":"https://cell.api.example.com"}`), nil, nil
	})
	if _, err := resolveHostedPlacement(context.Background(), runner, "/repo"); err != nil {
		t.Fatal(err)
	}
}

func TestConnectReusesTheExistingBindingWhenPlacementUnavailable(t *testing.T) {
	// A connected repo stays re-connectable without flags even before the host
	// CLI ships repo view placement: connect falls back to the stored binding,
	// so the blind-spot note's remedy ("run entire brain connect") always pulls.
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	remoteFact := hostedSyncTestFact("team fact via reconnect", now)
	fake := &hostedFake{ref: "facts-remote", authors: map[string]string{remoteFact.ID: "teammate"}}
	fake.data = encodeFactsNDJSON(t, []factRecord{remoteFact})
	srv := newHostedFakeServer(t, fake)

	repoDir := t.TempDir()
	env := connectTestEnv(t, repoDir)
	runner := commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		if name == "entire" && len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
			return []byte(fakeJWT + "\n"), nil, nil
		}
		return nil, nil, errors.New("not available")
	})
	opts := Options{Version: "test", Env: env, Runner: runner, Now: func() time.Time { return now }}
	storage, err := repoStoragePaths(context.Background(), runner, env, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeHostedRepoBinding(storage.BrainDir, hostedRepoBinding{RepoID: "repo1", BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runConnectCommand(t, opts); err != nil {
		t.Fatalf("connect on an already-connected repo must not require placement resolution, got %v", err)
	}
	local, err := loadFacts(storage.BrainDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range local {
		if rec.ID == remoteFact.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("reconnect must pull, got %d local records", len(local))
	}
}

func TestConnectClearsOptOut(t *testing.T) {
	// Re-joining after a disconnect must clear the opt-out, and the opted-out
	// binding's kept coordinates must serve as the no-flag fallback.
	repoDir := t.TempDir()
	env := connectTestEnv(t, repoDir)
	fake := &hostedFake{}
	srv := newHostedFakeServer(t, fake)
	runner := commandRunnerFunc(func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		if name == "entire" && len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
			return []byte(fakeJWT + "\n"), nil, nil
		}
		return nil, nil, errors.New("not available")
	})
	opts := Options{Version: "test", Env: env, Runner: runner, Now: time.Now}
	storage, err := repoStoragePaths(context.Background(), runner, env, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeHostedRepoBinding(storage.BrainDir, hostedRepoBinding{RepoID: "repo1", BaseURL: srv.URL, OptedOut: true}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runConnectCommand(t, opts); err != nil {
		t.Fatal(err)
	}
	binding, present, err := readHostedRepoBinding(storage.BrainDir)
	if err != nil || !present {
		t.Fatalf("binding must be present: present=%v err=%v", present, err)
	}
	if binding.OptedOut {
		t.Fatal("connect must clear the opt-out")
	}
	if binding.RepoID != "repo1" || binding.BaseURL != srv.URL {
		t.Fatalf("connect must rebind the kept coordinates, got %+v", binding)
	}
}
