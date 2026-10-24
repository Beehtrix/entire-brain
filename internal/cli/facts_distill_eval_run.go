package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// distillEvalExec is the shell-out seam: every child process the eval harness
// spawns (refresh sessions, distill) goes through it so tests can intercept.
// The child is the current binary; env replaces the process environment.
var distillEvalExec = func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve own executable: %w", err)
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := out
		if len(tail) > 2000 {
			tail = tail[len(tail)-2000:]
		}
		return out, fmt.Errorf("%s: %w\n%s", args[0], err, tail)
	}
	return out, nil
}

// distillEvalChildEnv builds the child environment: the parent's, minus any
// XDG data/state/cache and distill override leakage, plus the sandbox and the
// variant's overrides. XDG_CONFIG_HOME is inherited on purpose — the child's
// agent (claude/codex) needs the user's auth and config.
func distillEvalChildEnv(sandbox string, extra map[string]string) []string {
	drop := map[string]bool{
		"XDG_DATA_HOME": true, "XDG_STATE_HOME": true, "XDG_CACHE_HOME": true,
		envDistillTemplate: true, envFactsMaxPerChunk: true,
	}
	var env []string
	for _, kv := range os.Environ() {
		key, _, ok := strings.Cut(kv, "=")
		if ok && drop[key] {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"XDG_DATA_HOME="+filepath.Join(sandbox, "data"),
		"XDG_STATE_HOME="+filepath.Join(sandbox, "state"),
		"XDG_CACHE_HOME="+filepath.Join(sandbox, "cache"),
	)
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+extra[k])
	}
	return env
}

// findSandboxBrainDir locates the one brain directory a sandboxed `refresh
// sessions` produced, by walking for its manifest.json — layout-agnostic so a
// storage-path change in the CLI does not break the harness.
func findSandboxBrainDir(sandbox string) (string, error) {
	var found string
	err := filepath.WalkDir(sandbox, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "manifest.json" {
			found = filepath.Dir(path)
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("no brain manifest found under sandbox %s; did `refresh sessions` export anything?", sandbox)
	}
	return found, nil
}

func newDistillEvalCorpusCommand(opts Options) *cobra.Command {
	var (
		out       string
		sessionsN int
		goldenN   int
	)
	cmd := &cobra.Command{
		Use:   "corpus",
		Short: "Build a fixed eval corpus from this repo's sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			if out == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				out = filepath.Join(home, ".entire-brain-eval", filepath.Base(opts.Env.RepoRoot))
			}
			sandbox := filepath.Join(out, "sandbox")
			if err := os.MkdirAll(sandbox, 0o700); err != nil {
				return err
			}
			env := distillEvalChildEnv(sandbox, nil)
			if _, err := distillEvalExec(cmd.Context(), opts.Env.RepoRoot, env, "refresh", "sessions"); err != nil {
				return fmt.Errorf("export sessions into sandbox: %w", err)
			}
			brainDir, err := findSandboxBrainDir(sandbox)
			if err != nil {
				return err
			}
			manifest, err := loadBrainManifest(brainDir)
			if err != nil {
				return err
			}
			if manifest.Sources == nil || manifest.Sources.Sessions == nil {
				return errors.New("sandbox manifest has no sessions")
			}
			var entries []distillEvalCorpusEntry
			for _, s := range manifest.Sources.Sessions.Sessions {
				info, err := os.Stat(filepath.Join(brainDir, filepath.FromSlash(s.TranscriptPath)))
				if err != nil || info.Size() == 0 {
					continue
				}
				entries = append(entries, distillEvalCorpusEntry{
					SessionID:      s.SessionID,
					Branch:         resolveDistillBranch(manifest, s),
					TranscriptPath: s.TranscriptPath,
					RawBytes:       int(info.Size()),
					// ponytail: chunk estimate assumes preprocessing is
					// roughly size-preserving; exact counts come from the raw
					// run's chunk lines.
					Chunks: int((info.Size() + defaultDistillChunkSize - 1) / defaultDistillChunkSize),
				})
			}
			if len(entries) == 0 {
				return errors.New("no non-empty session transcripts in sandbox export")
			}
			sort.Slice(entries, func(i, j int) bool {
				if entries[i].RawBytes != entries[j].RawBytes {
					return entries[i].RawBytes < entries[j].RawBytes
				}
				return entries[i].SessionID < entries[j].SessionID
			})
			if sessionsN > 0 && len(entries) > sessionsN {
				entries = entries[:sessionsN]
			}
			for i := 0; i < goldenN && i < len(entries); i++ {
				entries[i].Golden = true
			}
			ids := make([]string, len(entries))
			for i, e := range entries {
				ids[i] = e.SessionID
			}
			corpus := distillEvalCorpus{
				RepoKey:      brainRepoKeyBestEffort(brainDir),
				CreatedAt:    opts.Now(),
				CorpusSHA256: distillEvalCorpusSHA(ids),
				Sessions:     entries,
			}
			data, err := json.MarshalIndent(corpus, "", "  ")
			if err != nil {
				return err
			}
			path := filepath.Join(out, "corpus.json")
			if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "corpus: %d sessions (%d golden) → %s\n", len(entries), min(goldenN, len(entries)), path)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "Corpus directory (default ~/.entire-brain-eval/<repo>)")
	cmd.Flags().IntVar(&sessionsN, "sessions", 60, "How many sessions to select (smallest first)")
	cmd.Flags().IntVar(&goldenN, "golden", 12, "How many of the smallest selected sessions to mark golden")
	return cmd
}

// brainRepoKeyBestEffort derives the repo key from the brain path's repos/<key>
// suffix when present; empty otherwise. Informational field only.
func brainRepoKeyBestEffort(brainDir string) string {
	parts := filepath.ToSlash(brainDir)
	if i := indexOfSegment(parts, "repos"); i >= 0 {
		return parts[i+len("repos/"):]
	}
	return ""
}

func indexOfSegment(path, seg string) int {
	marker := "/" + seg + "/"
	for i := 0; i+len(marker) <= len(path); i++ {
		if path[i:i+len(marker)] == marker {
			return i + 1
		}
	}
	return -1
}

func newDistillEvalRunCommand(opts Options) *cobra.Command {
	var (
		corpusPath    string
		name          string
		templatePath  string
		cap           int
		agent         string
		model         string
		maxChunkBytes int
		goldenOnly    bool
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run one distillation variant over the corpus in a sandbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return errors.New("--name is required")
			}
			data, err := os.ReadFile(corpusPath)
			if err != nil {
				return fmt.Errorf("read corpus %s: %w", corpusPath, err)
			}
			var corpus distillEvalCorpus
			if err := json.Unmarshal(data, &corpus); err != nil {
				return fmt.Errorf("decode corpus %s: %w", corpusPath, err)
			}
			runDir := filepath.Join(filepath.Dir(corpusPath), "runs", name)
			sandbox := filepath.Join(runDir, "sandbox")
			rawDir := filepath.Join(runDir, "raw")
			for _, d := range []string{sandbox, rawDir} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					return err
				}
			}

			templateSHA, err := distillEvalTemplateSHA(templatePath)
			if err != nil {
				return err
			}
			extra := map[string]string{}
			if templatePath != "" {
				abs, err := filepath.Abs(templatePath)
				if err != nil {
					return err
				}
				extra[envDistillTemplate] = abs
			}
			if cap > 0 {
				extra[envFactsMaxPerChunk] = strconv.Itoa(cap)
			}
			env := distillEvalChildEnv(sandbox, extra)

			started := opts.Now()
			if _, err := distillEvalExec(cmd.Context(), opts.Env.RepoRoot, env, "refresh", "sessions"); err != nil {
				return fmt.Errorf("export sessions into run sandbox: %w", err)
			}
			var sessionFiles, traceFiles []string
			for _, s := range corpus.Sessions {
				if goldenOnly && !s.Golden {
					continue
				}
				rawOut := filepath.Join(rawDir, s.SessionID+".jsonl")
				trace := filepath.Join(rawDir, s.SessionID+".trace.jsonl")
				args := []string{"distill",
					"--session", s.SessionID,
					"--raw-facts-out", rawOut,
					"--call-trace", trace,
					"--agent", agent,
					"--max-chunk-bytes", strconv.Itoa(maxChunkBytes),
				}
				if model != "" {
					args = append(args, "--model", model)
				}
				if _, err := distillEvalExec(cmd.Context(), opts.Env.RepoRoot, env, args...); err != nil {
					return fmt.Errorf("distill session %s: %w", s.SessionID, err)
				}
				sessionFiles = append(sessionFiles, rawOut)
				traceFiles = append(traceFiles, trace)
				fmt.Fprintf(cmd.OutOrStdout(), "distilled %s\n", s.SessionID)
			}
			if err := concatFiles(filepath.Join(runDir, "raw-facts.jsonl"), sessionFiles); err != nil {
				return err
			}
			if err := concatFiles(filepath.Join(runDir, "call-trace.jsonl"), traceFiles); err != nil {
				return err
			}

			effectiveCap := cap
			if effectiveCap == 0 {
				effectiveCap = factsMaxPerChunk
			}
			meta := distillEvalRunMeta{
				Variant: distillEvalVariant{
					Name: name, TemplateSHA256: templateSHA,
					Agent: agent, Model: model,
					MaxChunkBytes: maxChunkBytes, CapPerChunk: effectiveCap,
				},
				CorpusSHA256: corpus.CorpusSHA256,
				StartedAt:    started,
				WallSeconds:  time.Since(started).Seconds(),
			}
			metaData, err := json.MarshalIndent(meta, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(runDir, "run.json"), append(metaData, '\n'), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "run %q complete → %s\n", name, runDir)
			return nil
		},
	}
	cmd.Flags().StringVar(&corpusPath, "corpus", "", "Path to corpus.json (required)")
	cmd.Flags().StringVar(&name, "name", "", "Variant name; artifacts land in <corpus-dir>/runs/<name>/")
	cmd.Flags().StringVar(&templatePath, "template", "", "Alternate distill template file for this variant")
	cmd.Flags().IntVar(&cap, "cap", 0, "Per-chunk fact cap override (0 = compiled default)")
	cmd.Flags().StringVar(&agent, "agent", "claude-code", "Distillation agent")
	cmd.Flags().StringVar(&model, "model", "claude-haiku-4-5-20251001", "Distillation model")
	cmd.Flags().IntVar(&maxChunkBytes, "max-chunk-bytes", defaultDistillChunkSize, "Transcript chunk size for this variant")
	cmd.Flags().BoolVar(&goldenOnly, "golden-only", false, "Only distill the corpus's golden sessions")
	_ = cmd.MarkFlagRequired("corpus")
	return cmd
}

// distillEvalTemplateSHA hashes the template BODY the run will use (override
// file when given, embedded default otherwise), frontmatter stripped — the
// same bytes distillTemplate() hands the agent path.
func distillEvalTemplateSHA(templatePath string) (string, error) {
	var body string
	if templatePath != "" {
		data, err := os.ReadFile(templatePath)
		if err != nil {
			return "", fmt.Errorf("read template %s: %w", templatePath, err)
		}
		body = stripTemplateFrontmatter(string(data))
	} else {
		var err error
		body, err = distillTemplate()
		if err != nil {
			return "", err
		}
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:]), nil
}

func concatFiles(dst string, srcs []string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	for _, src := range srcs {
		in, err := os.Open(src)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // a session the child produced no file for
			}
			return err
		}
		_, err = io.Copy(out, in)
		in.Close()
		if err != nil {
			return err
		}
	}
	return out.Close()
}
