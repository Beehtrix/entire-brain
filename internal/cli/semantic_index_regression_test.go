package cli

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

func semanticRegressionPathDigest(t *testing.T, repoDir, clean string, ignore brainIgnore) ([32]byte, int64) {
	t.Helper()
	h := sha256.New()
	var total int64
	if err := appendWorktreePathContent(h, &total, repoDir, clean, ignore); err != nil {
		t.Fatal(err)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, total
}

func TestSemanticWorktreePathFingerprintIncludesFilesAndExcludesIgnoredAndSymlinks(t *testing.T) {
	repoDir := t.TempDir()
	for rel, body := range map[string]string{
		"untracked/kept.go":          "package kept\n",
		"untracked/ignored/key.pem":  "secret-one\n",
		"untracked/ignored-file.pem": "secret-one\n",
	} {
		path := filepath.Join(repoDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ignore := brainIgnore{patterns: []string{"untracked/ignored/**", "untracked/ignored-file.pem"}}
	base, baseBytes := semanticRegressionPathDigest(t, repoDir, "untracked", ignore)
	if baseBytes == 0 {
		t.Fatal("included worktree file contributed no fingerprint bytes")
	}
	if _, singleBytes := semanticRegressionPathDigest(t, repoDir, "untracked/kept.go", ignore); singleBytes == 0 {
		t.Fatal("top-level untracked file contributed no fingerprint bytes")
	}
	if err := os.WriteFile(filepath.Join(repoDir, "untracked", "kept.go"), []byte("package changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	included, _ := semanticRegressionPathDigest(t, repoDir, "untracked", ignore)
	if included == base {
		t.Fatal("changing an included file did not change the worktree fingerprint")
	}
	if err := os.WriteFile(filepath.Join(repoDir, "untracked", "ignored", "key.pem"), []byte("secret-two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ignored, _ := semanticRegressionPathDigest(t, repoDir, "untracked", ignore)
	if ignored != included {
		t.Fatal("changing an ignored file changed the worktree fingerprint")
	}
	if err := os.WriteFile(filepath.Join(repoDir, "untracked", "ignored-file.pem"), []byte("secret-two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ignoredFile, _ := semanticRegressionPathDigest(t, repoDir, "untracked", ignore)
	if ignoredFile != ignored {
		t.Fatal("changing an ignored sibling file changed the worktree fingerprint")
	}
	missing, missingBytes := semanticRegressionPathDigest(t, repoDir, "missing", ignore)
	if missing != sha256.Sum256(nil) || missingBytes != 0 {
		t.Fatalf("missing path contributed to fingerprint: %x bytes=%d", missing, missingBytes)
	}
	t.Run("symlink paths", func(t *testing.T) {
		external := filepath.Join(t.TempDir(), "external.go")
		if err := os.WriteFile(external, []byte("external-one\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		symlink := filepath.Join(repoDir, "untracked", "linked.go")
		if err := os.Symlink(external, symlink); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if directLink, linkBytes := semanticRegressionPathDigest(t, repoDir, "untracked/linked.go", ignore); directLink != sha256.Sum256(nil) || linkBytes != 0 {
			t.Fatalf("top-level symlink contributed to fingerprint: %x bytes=%d", directLink, linkBytes)
		}
		if err := os.WriteFile(external, []byte("external-two\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		linked, _ := semanticRegressionPathDigest(t, repoDir, "untracked", ignore)
		if linked != ignoredFile {
			t.Fatal("changing a symlink target outside the repository changed the fingerprint")
		}
	})
}

func TestSemanticWorktreePathFingerprintRejectsOversizedFilesAndTotals(t *testing.T) {
	repoDir := t.TempDir()
	path := filepath.Join(repoDir, "oversized.go")
	if err := os.WriteFile(path, make([]byte, semanticWorktreeFingerprintMaxFile+1), 0o600); err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	var total int64
	if err := appendWorktreePathContent(h, &total, repoDir, "oversized.go", brainIgnore{}); err == nil {
		t.Fatal("oversized worktree path was accepted into fingerprint")
	}

	if err := os.WriteFile(filepath.Join(repoDir, "small.go"), []byte("package small\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	total = semanticWorktreeFingerprintMaxTotal
	if err := appendWorktreePathContent(h, &total, repoDir, "small.go", brainIgnore{}); err == nil {
		t.Fatal("fingerprint exceeding total size budget was accepted")
	}
}

func TestSemanticWorktreePathFingerprintPropagatesWalkErrors(t *testing.T) {
	repoDir := t.TempDir()
	blocked := filepath.Join(repoDir, "untracked", "blocked")
	if err := os.MkdirAll(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "hidden.go"), []byte("package hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	h := sha256.New()
	var total int64
	err := appendWorktreePathContent(h, &total, repoDir, "untracked", brainIgnore{})
	if err == nil {
		t.Skip("test environment permits traversal of mode-000 directories")
	}
}
