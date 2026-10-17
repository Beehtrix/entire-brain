package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostedRepoBindingRoundTrip(t *testing.T) {
	brainDir := t.TempDir()
	in := hostedRepoBinding{RepoID: "01M2Q9WATHXVM46CD8Y4D3AX8W", BaseURL: "https://cell.api.example.com/api/v1/"}
	if err := writeHostedRepoBinding(brainDir, in); err != nil {
		t.Fatal(err)
	}
	got, present, err := readHostedRepoBinding(brainDir)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("binding must be present after write")
	}
	if got.RepoID != in.RepoID {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.BaseURL != "https://cell.api.example.com/api/v1" {
		t.Fatalf("base URL must be stored without trailing slash, got %q", got.BaseURL)
	}
}

func TestHostedRepoBindingOptOutRoundTrip(t *testing.T) {
	brainDir := t.TempDir()
	in := hostedRepoBinding{RepoID: "repo1", BaseURL: "https://cell.api.example.com", OptedOut: true}
	if err := writeHostedRepoBinding(brainDir, in); err != nil {
		t.Fatal(err)
	}
	got, present, err := readHostedRepoBinding(brainDir)
	if err != nil || !present {
		t.Fatalf("opted-out binding must still be present: present=%v err=%v", present, err)
	}
	if !got.OptedOut {
		t.Fatal("opt-out must survive the round trip")
	}
	if got.RepoID != in.RepoID || got.BaseURL != in.BaseURL {
		t.Fatalf("opt-out must keep the coordinates for easy re-connect, got %+v", got)
	}
}

func TestHostedRepoBindingAbsent(t *testing.T) {
	_, present, err := readHostedRepoBinding(t.TempDir())
	if err != nil {
		t.Fatalf("absent binding must not be an error, got %v", err)
	}
	if present {
		t.Fatal("absent binding must report present=false")
	}
}

func TestHostedRepoBindingCorrupt(t *testing.T) {
	brainDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(brainDir, "hosted.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readHostedRepoBinding(brainDir); err == nil {
		t.Fatal("corrupt binding must return an error")
	}
}
