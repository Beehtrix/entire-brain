package base

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ResolveLocalTargetRepoDir(ctx context.Context, runner CommandRunner, target string) (string, bool, error) {
	info, err := os.Stat(target)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		return "", false, nil
	default:
		return "", false, fmt.Errorf("stat target path: %w", err)
	}

	dir := target
	if !info.IsDir() {
		dir = filepath.Dir(target)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", true, fmt.Errorf("resolve target path: %w", err)
	}

	if runner != nil {
		stdout, _, err := runner.Run(ctx, abs, "git", "rev-parse", "--show-toplevel")
		if err == nil {
			root := filepath.Clean(strings.TrimSpace(string(stdout)))
			if root != "" {
				if !filepath.IsAbs(root) {
					root = filepath.Join(abs, root)
				}
				rootAbs, absErr := filepath.Abs(root)
				if absErr != nil {
					return "", true, fmt.Errorf("resolve git root: %w", absErr)
				}
				// Git may report an ancestor's physical spelling even though the
				// requested path used an established logical spelling (notably
				// macOS /var versus /private/var). Prefer the matching lexical
				// ancestor so path-hashed local identities remain stable.
				// A relative target carries no lexical root spelling of its own.
				// On macOS, filepath.Abs after Chdir may expand /var to
				// /private/var; in that case Git's absolute root is the only
				// caller-provided spelling and must win. Preserve a matching
				// lexical ancestor only for an explicitly absolute target.
				if filepath.IsAbs(target) {
					if lexicalRoot, ok := MatchingLexicalAncestor(abs, rootAbs); ok {
						abs = lexicalRoot
					} else {
						abs = rootAbs
					}
				} else {
					abs = rootAbs
				}
			}
		}
	}

	return abs, true, nil
}

func MatchingLexicalAncestor(path, target string) (string, bool) {
	targetInfo, err := os.Stat(target)
	if err != nil {
		return "", false
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil && os.SameFile(info, targetInfo) {
			return current, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
	}
}
