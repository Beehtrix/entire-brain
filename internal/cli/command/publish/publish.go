package publish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/entireio/entire-brain/internal/cli/internal/semstore"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/entireio/entire-brain/factmerge"
	"github.com/entireio/entire-brain/internal/apiurl"
	"github.com/entireio/entire-brain/internal/brainwire"
	clibase "github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
	"github.com/entireio/entire-brain/internal/cli/internal/factstore"
	bmanifest "github.com/entireio/entire-brain/internal/cli/internal/manifest"
	"github.com/entireio/entire-brain/internal/factsync"
	"github.com/entireio/entire-brain/internal/httpx"
	"github.com/entireio/entire-brain/internal/repoid"
	"github.com/spf13/cobra"
)

// Hosted brain publish (P1.M1.5, client half). This command is the ONLY path in
// the plugin allowed to egress brain data, and only behind an explicit opt-in.
//
// No implicit egress (ADR-P1-A, amended by COR-2111): a network call happens
// only when the user explicitly runs `entire brain publish` AND supplies the
// full target (repo id, API URL, token) — there are no target defaults, so a
// bare invocation refuses BEFORE any resolution, disk read, or HTTP request.
// The ENTIRE_BRAIN_ALLOW_HOSTED env gate this command originally required was
// retired once `entire brain connect` made hosted consent an explicit, durable
// state. The master kill switch (ENTIRE_BRAIN_NO_EGRESS) always wins.
//
// Where the bundle may go is gated too: the resolved base URL must clear the shared
// scheme floor (internal/apiurl) — https, or http only to a loopback host — checked
// on the same egress-free side of the disk read as the two gates above, so an
// unsafe or malformed target never gets the brain built for it.
const (
	// Artifact kinds, matching the server's brainstore.Kind* constants exactly.
	KindSnapshot = "snapshot"
	KindOverlay  = "overlay"
	KindFacts    = "facts"
	KindManifest = "manifest"

	mediaTypeJSON   = "application/json"
	mediaTypeNDJSON = "application/x-ndjson"

	// publishAPIPath is the route on entire-api: POST
	// {base}/api/v1/repos/{repo_id}/brain/artifacts.
	APIPathPrefix = "/api/v1/repos/"
	APIPathSuffix = "/brain/artifacts"

	RequestTimeout          = 5 * time.Minute
	maxPublishResponseBytes = 1 << 20 // 1 MiB is ample for the small JSON result.

	// serverMaxBrainBodyBytes mirrors entire-api's maxBrainArtifactBytes: the
	// publish endpoint caps the request body at 256 MiB and 413s anything larger (a
	// brain bundle inlines all its blobs as base64 in one JSON body). The client
	// preflights against this so an oversized bundle fails locally with a precise
	// message before a multi-GB buffer is ever built.
	serverMaxBrainBodyBytes = 256 << 20 // 256 MiB

	// publishBodyHeadroom keeps the client ceiling below the server cap so the
	// projection's rounding can never let through a body the server would 413.
	publishBodyHeadroom = 8 << 20 // 8 MiB

	// publishArtifactEnvelopeBytes over-estimates one artifact's JSON structural
	// overhead ({"kind":..,"ref":..,"digest":..,"data":..}) so the projected body
	// size stays an upper bound on the real marshaled body.
	publishArtifactEnvelopeBytes = 64
)

// MaxBodyBytes is the client-side ceiling on the projected publish request
// body: the server cap less headroom for the projection's rounding. It is a var
// solely so tests can shrink it to exercise the preflight without a
// hundreds-of-MiB fixture; production never reassigns it.
var MaxBodyBytes int64 = serverMaxBrainBodyBytes - publishBodyHeadroom

// Artifact is one artifact in the publish bundle. Its JSON tags mirror the
// server's httpapi.BrainArtifactInput exactly: kind, ref, digest (omitempty), and
// data. Go marshals a []byte field as base64, which is exactly what the server's
// []byte Data field expects on the wire.
type Artifact struct {
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	Digest string `json:"digest,omitempty"`
	Data   []byte `json:"data"`
}

// RequestBody is the POST body: {"artifacts":[...]}, matching the server's
// httpapi.BrainPublishInput.Body.
type RequestBody struct {
	Artifacts []Artifact `json:"artifacts"`
}

// Result mirrors the server's httpapi.BrainPublishOutput body.
type Result struct {
	Status string `json:"status"`
	Stored []struct {
		Kind string `json:"kind"`
		Ref  string `json:"ref"`
	} `json:"stored"`
}

// Config supplies the shared command options and the assembly package's
// storage, hosted-target, guard, and repo-remote operations. Dependencies are
// per invocation, never global hooks.
type Config struct {
	clibase.Options
	Deps Deps
}

// Deps keeps publish independent of command assembly, repo identity
// resolution, the hosted-target resolver (shared with facts sync), and the
// session privacy guard composition.
type Deps struct {
	RepoStoragePaths func(context.Context, clibase.CommandRunner, clibase.EntireEnv, string) (clibase.RepoStorage, error)
	// ResolveManualHostedTarget resolves where this command may send data, per
	// field: explicit flag > env override > the connected binding. It refuses
	// egress-free when consent or a required field is missing.
	ResolveManualHostedTarget func(ctx context.Context, opts clibase.Options, brainDir, repoDir, command, flagRepoID, flagAPIURL, flagToken string) (repoID, baseURL, token string, err error)
	// LoadSessionReadGuard builds the privacy guard over the brain's excluded
	// sessions; GuardFactRecords filters a fact slice through it.
	LoadSessionReadGuard func(brainDir string, manifest *bmanifest.Manifest) (brainstore.SessionReadGuard, error)
	GuardFactRecords     func(guard brainstore.SessionReadGuard, facts []factmerge.Record) []factmerge.Record
	// ParseRepoRemote and NormalizeRepoHost are the same remote parsing the
	// on-disk store key uses, so the wire key stays byte-identical to it.
	ParseRepoRemote   func(remote string) (host string, components []string, ok bool)
	NormalizeRepoHost func(host string) string
}

// Flags carries the explicit-target flag values.
type Flags struct {
	RepoID string
	APIURL string
	Token  string
}

func New(cfg Config) *cobra.Command {
	publishOpts := Flags{}

	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Publish the local brain to hosted Entire (opt-in)",
		Long: `Publish serializes the current on-disk brain into the versioned brain wire
artifacts (manifest, branch overlays, and durable facts) and
uploads them to hosted Entire.

Hosted publishing is opt-in and off by default: the brain stays entirely local
unless you run this command with a target — the connected binding by default
(see 'entire brain connect'), or an explicit one. No brain data ever leaves
your machine implicitly, and ENTIRE_BRAIN_NO_EGRESS always wins.

The target repo, API base URL, and bearer token resolve flag > env > connected
binding: --repo-id / ENTIRE_REPO_ID, --api-url / ENTIRE_API_URL, --token /
ENTIRE_API_TOKEN. On a connected repo the token is minted via the host CLI. The
base URL must be https:// — brain content and the API token are refused over
plaintext http:// unless the host is loopback (or
ENTIRE_BRAIN_ALLOW_INSECURE_API_URL is explicitly set).
Re-running publish is safe: the server overwrites each artifact at its
coordinate, so identical bytes produce an identical result.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return Run(cmd.Context(), cmd, cfg, publishOpts)
		},
	}

	cmd.Flags().StringVar(&publishOpts.RepoID, "repo-id", "", "Target repo ULID (overrides ENTIRE_REPO_ID)")
	cmd.Flags().StringVar(&publishOpts.APIURL, "api-url", "", "Entire API base URL, https:// (overrides ENTIRE_API_URL)")
	cmd.Flags().StringVar(&publishOpts.Token, "token", "", "Entire API bearer token (overrides ENTIRE_API_TOKEN)")

	return cmd
}

func Run(ctx context.Context, cmd *cobra.Command, cfg Config, publishOpts Flags) error {
	// Every gate is checked BEFORE resolving any target, reading the brain, or
	// making a network call, so a refusal is guaranteed egress-free.

	// The master kill switch always wins over the hosted opt-in.
	if clibase.BrainNoEgressMode() {
		return fmt.Errorf("no_egress: hosted brain publish is disabled while %s is set; unset it to publish", clibase.NoEgressToggleName())
	}

	// Locate the on-disk brain for this repo; the connected binding under it is
	// the default target.
	repoDir := cfg.Env.RepoRoot
	if repoDir == "" {
		repoDir = "."
	}
	storage, err := cfg.Deps.RepoStoragePaths(ctx, cfg.Runner, cfg.Env, repoDir)
	if err != nil {
		return fmt.Errorf("publish: resolve brain: %w", err)
	}

	// Resolve the target: flag > env > connected binding. Validation (repo-id
	// path segment, URL scheme floor) happens inside, before any brain read or
	// network call, so a malformed or missing target refuses with the whole
	// brain still on disk — the same egress-free guarantee as the gate above.
	repoID, baseURL, token, err := cfg.Deps.ResolveManualHostedTarget(ctx, cfg.Options, storage.BrainDir, repoDir, "publish", publishOpts.RepoID, publishOpts.APIURL, publishOpts.Token)
	if err != nil {
		return err
	}
	if !bmanifest.Exists(storage.BrainDir) {
		return errors.New("publish: no brain found for this repo; run '" + clibase.BrainInvocation(os.LookupEnv, "refresh") + "' first")
	}

	// Exclusion and purge use the same boundary. Hold it from collection through
	// the completed HTTP exchange so a checked bundle cannot become private
	// between its policy read and its last outgoing byte.
	privacyUnlock, err := brainstore.AcquireBrainPrivacySideEffectLock(storage.BrainDir)
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	defer privacyUnlock()

	body, repoKey, err := buildPublishBundle(ctx, cfg, storage, repoDir)
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	result, err := PostBrainArtifacts(ctx, baseURL, repoID, token, body)
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "published %d artifact(s) for %s to %s\n",
		len(result.Stored), repoKey, strings.TrimRight(baseURL, "/"))
	return nil
}

// buildPublishBundle serializes the on-disk brain into the publish bundle: the
// manifest (kind=manifest, its data is the full brainwire.BrainArtifact JSON with
// content-addressed references) plus each snapshot/overlay/facts blob inline. It
// returns the bundle and the repo_key it declares.
func buildPublishBundle(ctx context.Context, cfg Config, storage clibase.RepoStorage, repoDir string) (RequestBody, string, error) {
	manifest, err := bmanifest.Load(storage.BrainDir)
	if err != nil {
		return RequestBody{}, "", fmt.Errorf("load brain manifest: %w", err)
	}
	// The wire manifest's repo_key MUST equal the slug entire-api resolves the
	// target repo_id to server-side ("gh/owner/repo"), or the server 422s the whole
	// bundle. Derive it from the origin remote in that canonical form rather than
	// forwarding the on-disk store key (whose per-host DomainSlugs prefix the
	// GitHub-sourced server can never reproduce). The on-disk RepoKey stays the
	// local store's identity and is intentionally not sent as the wire key.
	repoKey, err := CanonicalWireRepoKey(ctx, cfg, repoDir)
	if err != nil {
		return RequestBody{}, "", err
	}

	art := brainwire.NewBrainArtifact(repoKey, manifest.DefaultBranch, manifest.GeneratedAt)
	if sem := semanticSourceOf(manifest); sem != nil {
		art.SetProvider(sem.Provider, sem.ProviderVersion, sem.SchemaVersion)
	}

	// Collect the heavy blobs (each also registers a content-addressed reference on
	// the wire manifest). Order within each kind is stable so re-publishing an
	// unchanged brain produces a byte-identical request (idempotent).
	var blobs []Artifact
	overlays, err := cfg.Deps.CollectOverlayArtifacts(art, storage.BrainDir)
	if err != nil {
		return RequestBody{}, "", err
	}
	blobs = append(blobs, overlays...)
	facts, err := cfg.Deps.CollectFactArtifacts(art, storage.BrainDir)
	if err != nil {
		return RequestBody{}, "", err
	}
	blobs = append(blobs, facts...)

	// The manifest artifact is the fully-built wire artifact (manifest header +
	// all references), addressed by the repo's HEAD commit (the store addresses a
	// manifest by commit).
	manifestData, err := json.Marshal(art)
	if err != nil {
		return RequestBody{}, "", fmt.Errorf("encode manifest: %w", err)
	}
	manifestArtifact := Artifact{
		Kind:   KindManifest,
		Ref:    publishManifestRef(ctx, cfg.Options, repoDir, manifest),
		Digest: ContentDigest(manifestData),
		Data:   manifestData,
	}

	body := RequestBody{
		Artifacts: append([]Artifact{manifestArtifact}, blobs...),
	}
	// Preflight the total bundle size against the server's 256 MiB body cap BEFORE
	// json.Marshal base64-expands every blob into one buffer, so an oversized brain
	// fails with a precise local message instead of building a multi-GB body only to
	// be 413'd with an opaque error.
	if err := ensurePublishBodyWithinLimit(body); err != nil {
		return RequestBody{}, "", err
	}
	return body, repoKey, nil
}

// brainProviderPrefixGitHub is the wire-contract domain slug for github.com
// (entire-brain's knownRepoDomainSlugs maps github.com -> "gh"). entire-api's
// brain store is GitHub-sourced, so its server-side slug resolver ALWAYS renders a
// repo as "gh/owner/repo" from the stored github.com full name — it never sees the
// client's per-host DomainSlugs. Only a github.com remote can therefore produce a
// wire repo_key the server accepts.
const brainProviderPrefixGitHub = "gh"

// CanonicalWireRepoKey derives the wire manifest repo_key from the repo's origin
// remote in the exact form entire-api's brainSlugResolver emits:
// "gh/" + <normalized owner>/<normalized repo>. Owner/repo come from the same
// Deps.ParseRepoRemote the on-disk store key uses (each component lowercased and run
// through safeRepoPathComponent — byte-identical to the server's wireRepoPath).
//
// Only a github.com origin is accepted: entire-api's brain store is sourced from
// github_repo_meta (github.com repos) and its resolver UNCONDITIONALLY renders
// "gh/owner/repo" from the stored full name — it has no notion of the client's git
// host, so it can never reproduce a slug for a GitHub Enterprise or look-alike
// "github.*" host. Admitting those would send a key the server 422s (or, on an
// owner/repo collision with a user-supplied github.com repo_id, overwrite the wrong
// repo's brain), so any non-github.com host fails fast here. github.com's own
// naming rules forbid an owner or repo that is all punctuation, so every valid
// github.com full name normalizes to two non-empty components and matches the
// server's empty-preserving join exactly.
func CanonicalWireRepoKey(ctx context.Context, cfg Config, repoDir string) (string, error) {
	remote := strings.TrimSpace(string(clibase.GitOutput(ctx, cfg.Runner, repoDir, "remote", "get-url", "origin")))
	host, components, ok := cfg.Deps.ParseRepoRemote(remote)
	if !ok {
		return "", fmt.Errorf("could not resolve a git origin remote for this repo; hosted publish requires a github.com origin remote")
	}
	if !cfg.Deps.isGitHubDotComHost(host) {
		return "", fmt.Errorf("hosted publish currently supports github.com-hosted repos only; origin remote host %q is not github.com (the hosted brain store is GitHub-sourced)", host)
	}
	return brainProviderPrefixGitHub + "/" + strings.Join(components, "/"), nil
}

// isGitHubDotComHost reports whether host is exactly github.com — the only host
// whose repos entire-api's GitHub-sourced resolver serves under the "gh" provider
// prefix. GitHub Enterprise / self-hosted / look-alike "github.*" hosts are NOT
// accepted: the public github.com-sourced server cannot resolve them, so a wire key
// for such a host would 422 (or collide with an unrelated github.com repo).
func (d Deps) isGitHubDotComHost(host string) bool {
	return d.NormalizeRepoHost(host) == "github.com"
}

// ensurePublishBodyWithinLimit refuses a bundle whose projected request body would
// exceed the hosted publish size ceiling, BEFORE json.Marshal builds the
// base64-expanded body in memory. The error names the projected size and the limit
// so the failure is actionable rather than an opaque server 413.
func ensurePublishBodyWithinLimit(body RequestBody) error {
	if projected := ProjectedBodyBytes(body); projected > MaxBodyBytes {
		return fmt.Errorf("publish_too_large: the brain bundle is ~%d bytes, over the %d-byte hosted publish limit; refresh a smaller brain or publish fewer artifacts", projected, MaxBodyBytes)
	}
	return nil
}

// ProjectedBodyBytes upper-bounds the size of the marshaled JSON request
// body without marshaling it: each artifact's Data becomes base64 (4/3 expansion,
// rounded up) plus a generous fixed JSON envelope, summed with the array framing.
// It never undershoots the real body, so a bundle that clears this check also
// clears the server's body cap.
func ProjectedBodyBytes(body RequestBody) int64 {
	total := int64(len(`{"artifacts":[]}`))
	for i, a := range body.Artifacts {
		if i > 0 {
			total++ // comma between array elements
		}
		total += publishArtifactEnvelopeBytes
		total += int64(len(a.Kind) + len(a.Ref) + len(a.Digest))
		total += int64(base64.StdEncoding.EncodedLen(len(a.Data)))
	}
	return total
}

// CollectOverlayArtifacts reads semantic/overlays/<base>..<head>.json, keying
// each by "base..head", and registers an OverlayRef on art. The all-branches.json
// refresh report is not a (base,head) overlay and is skipped.
func (d Deps) CollectOverlayArtifacts(art *brainwire.BrainArtifact, brainDir string) ([]Artifact, error) {
	entries, _, err := brainstore.ReadPrivacyDirectory(brainDir, filepath.Join(semstore.DirName, "overlays"), "publish overlays")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read overlays: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ref := strings.TrimSuffix(name, ".json")
		// Only (base..head)-addressed overlays are shareable references; skip the
		// aggregate all-branches.json report and any non-.json file.
		if name == ref || !strings.Contains(ref, "..") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var out []Artifact
	for _, name := range names {
		ref := strings.TrimSuffix(name, ".json")
		base, head, _ := strings.Cut(ref, "..")
		rel := filepath.ToSlash(filepath.Join(semstore.DirName, "overlays", name))
		data, ok, err := ReadBrainBlob(brainDir, rel)
		if err != nil {
			return nil, fmt.Errorf("read overlay %s: %w", ref, err)
		}
		if !ok {
			continue
		}
		digest := ContentDigest(data)
		art.AddOverlay(brainwire.OverlayRef{
			BaseCommit: base,
			HeadCommit: head,
			Branch:     overlayBranchName(data),
			Content:    brainwire.ContentRef{Digest: digest, Size: int64(len(data)), MediaType: mediaTypeJSON, Path: rel},
		})
		out = append(out, Artifact{Kind: KindOverlay, Ref: ref, Digest: digest, Data: data})
	}
	return out, nil
}

// CollectFactArtifacts reads facts/<slug>/facts.ndjson, recovering each stream's
// logical branch (resolveFactStreamBranch), and registers a FactsRef on art.
// Streams are keyed by branch and emitted in branch order.
func (d Deps) CollectFactArtifacts(art *brainwire.BrainArtifact, brainDir string) ([]Artifact, error) {
	manifest, err := bmanifest.Load(brainDir)
	if err != nil {
		return nil, fmt.Errorf("load facts privacy manifest: %w", err)
	}
	guard, err := d.LoadSessionReadGuard(brainDir, manifest)
	if err != nil {
		return nil, fmt.Errorf("load facts privacy policy: %w", err)
	}
	entries, _, err := brainstore.ReadPrivacyDirectory(brainDir, factstore.DirName, "publish facts")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read facts: %w", err)
	}

	dirs := subdirNames(entries)
	sort.Strings(dirs)

	type factStream struct {
		branch string
		data   []byte
	}
	var streams []factStream
	seen := make(map[string]struct{})
	for _, dir := range dirs {
		data, ok, err := ReadBrainBlob(brainDir, filepath.Join(factstore.DirName, dir, factstore.FileName))
		if err != nil {
			return nil, fmt.Errorf("read facts %s: %w", dir, err)
		}
		if !ok {
			continue
		}
		branch, err := resolveFactStreamBranch(dir, data)
		if err != nil {
			return nil, fmt.Errorf("read facts %s: %w", dir, err)
		}
		if branch == "" {
			continue
		}
		if _, dup := seen[branch]; dup {
			return nil, fmt.Errorf("read facts %s: duplicate branch stream %q", dir, branch)
		}
		seen[branch] = struct{}{}
		streams = append(streams, factStream{branch: branch, data: data})
	}
	sort.Slice(streams, func(i, j int) bool { return streams[i].branch < streams[j].branch })

	var out []Artifact
	for _, stream := range streams {
		// REDACT before the bytes become an artifact. The digest and the manifest
		// ContentRef must describe what actually leaves, so the redaction has to
		// happen ahead of both.
		data, err := d.redactFactsForEgress(stream.data, guard)
		if err != nil {
			return nil, fmt.Errorf("read facts %s: %w", stream.branch, err)
		}
		rel := factstore.FileRelPath(stream.branch)
		digest := ContentDigest(data)
		art.AddFacts(brainwire.FactsRef{
			Branch:  stream.branch,
			Content: brainwire.ContentRef{Digest: digest, Size: int64(len(data)), MediaType: mediaTypeNDJSON, Path: rel},
		})
		out = append(out, Artifact{Kind: KindFacts, Ref: stream.branch, Digest: digest, Data: data})
	}
	return out, nil
}

// redactFactsForEgress re-renders a facts stream with the LOCAL-ONLY provenance
// coordinates stripped, which is what `facts sync` has always done and what publish
// did not.
//
// A distilled fact's anchor mixes two kinds of value (see factsync.SanitizeForEgress):
// opaque cross-member ids another member can carry but never resolve (session id,
// commit, checkpoint, turn), and the coordinates of a private file on THIS machine —
// Transcript, a brain-relative transcript path, and Line, a turn offset into it. The
// path is not an opaque handle: it is rendered as
//
//	sessions/main/20260530T174906Z_codex_<session-uuid>_<checkpoint>.jsonl
//
// so shipping it hands the other side a dated inventory of the member's private
// sessions — when each ran, which agent tool ran it — and, with Line, the exact turn
// a statement came from. None of it is needed to consume a fact: the reasoning layer
// treats a peer's anchor as opaque and never resolves one (ADR-P1).
//
// factsync.SanitizeForEgress is the SAME redaction the fact-set sync and the
// proposal push apply, deliberately rather than a second copy of the rule: the two
// egress paths carry the same records to the same service, so they must agree on
// what may leave, and one implementation is how that stays true.
//
// A stream that will not parse is an ERROR, not a stream to ship unredacted. It is
// also not a new class of failure: `facts sync` already refuses the same file, so a
// brain that cannot publish here could not sync either.
func (d Deps) redactFactsForEgress(data []byte, guard brainstore.SessionReadGuard) ([]byte, error) {
	records, err := factmerge.ParseNDJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("cannot redact local-only provenance before upload: %w", err)
	}
	// Filter before sanitizing: transcript coordinates are needed to recognize
	// excluded provenance when an anchor has no session ID.
	records = d.GuardFactRecords(guard, records)
	var buf bytes.Buffer
	if err := factmerge.WriteNDJSON(&buf, factsync.SanitizeForEgress(records)); err != nil {
		return nil, fmt.Errorf("cannot redact local-only provenance before upload: %w", err)
	}
	return buf.Bytes(), nil
}

// publishManifestRef addresses the manifest by the repo HEAD commit (the store
// addresses a manifest by commit), falling back to the default branch, then a
// stable literal, when HEAD is not resolvable.
func publishManifestRef(ctx context.Context, opts clibase.Options, repoDir string, manifest *bmanifest.Manifest) string {
	if head := strings.TrimSpace(string(clibase.GitOutput(ctx, opts.Runner, repoDir, "rev-parse", "HEAD"))); head != "" {
		return head
	}
	if manifest != nil {
		if branch := strings.TrimSpace(manifest.DefaultBranch); branch != "" {
			return branch
		}
	}
	return KindManifest
}

// HTTPClient retains dial/TLS bounds and the full five-minute budget for
// uploading and processing a bundle. It shares the upload connection pool, whose
// header bound permits server processing after a large request has been sent.
func HTTPClient() *http.Client {
	return httpx.UploadClient(RequestTimeout)
}

// PostBrainArtifacts POSTs the bundle to the hosted brain publish endpoint and
// maps the response to a clear error or result. It is the only network call in
// the command; it runs only after both opt-in gates have passed.
func PostBrainArtifacts(ctx context.Context, baseURL, repoID, token string, body RequestBody) (Result, error) {
	baseURL, err := apiurl.Validate(baseURL)
	if err != nil {
		return Result{}, fmt.Errorf("publish: %w", err)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Result{}, fmt.Errorf("publish: encode request: %w", err)
	}
	// The chokepoint check, not a duplicate of the one in Run: this is the
	// only function that turns a repo id into a network target, and it is reachable
	// from tests and any future caller that did not come through the command. An id
	// that is not one bare path segment re-addresses the request — url.PathEscape
	// cannot prevent that, since "." and ".." are unreserved and survive escaping, so
	// ".." here yields /api/v1/repos/../brain/artifacts, which normalizes to
	// /api/v1/brain/artifacts with the caller's bearer token and the whole brain
	// bundle attached. Refuse before http.NewRequest exists.
	if err := repoid.Validate(repoID); err != nil {
		return Result{}, fmt.Errorf("publish: %w", err)
	}
	// Validated ids equal their own escaped form, so the id is concatenated raw.
	endpoint := strings.TrimRight(baseURL, "/") + APIPathPrefix + repoID + APIPathSuffix

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return Result{}, fmt.Errorf("publish: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := apiurl.WithoutRedirects(HTTPClient()).Do(req)
	if err != nil {
		// The wrapped error is a *url.Error, whose own Error() renders the target
		// through url.URL.Redacted() — any userinfo password is printed as "xxxxx".
		// Interpolating `endpoint` here as well undid exactly that redaction and
		// printed the cleartext credential. apiurl.Validate now refuses a base URL
		// carrying userinfo at all, so this is the second of two guards; the target
		// is still named, by the half of the message that redacts it.
		return Result{}, fmt.Errorf("publish: request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxPublishResponseBytes))

	switch resp.StatusCode {
	case http.StatusOK:
		var result Result
		if err := json.Unmarshal(respBody, &result); err != nil {
			return Result{}, fmt.Errorf("publish: decode server response: %w", err)
		}
		// A 200 whose body acknowledges NOTHING is not a success, and reporting it as
		// one is the worst outcome on this path: the member is told the brain is
		// published, stops thinking about it, and the hosted brain holds nothing. The
		// endpoint answers with the coordinate of every artifact it stored, so an
		// empty `stored` against a non-empty upload means the bytes did not land —
		// a half-deployed server, a proxy answering `{}` in front of it, or a
		// response that never came from the endpoint at all. Refuse, and say which
		// of the two it is rather than guessing on the member's behalf.
		if len(body.Artifacts) > 0 && len(result.Stored) == 0 {
			return Result{}, fmt.Errorf("publish_unacknowledged: the server answered HTTP 200 but acknowledged storing 0 of the %d artifact(s) sent%s; the brain was NOT published — check that %s is the hosted Entire API and not a proxy in front of it",
				len(body.Artifacts), publishStatusDetail(result.Status, token), endpoint)
		}
		// Stored coordinates must cover exactly the submitted set, once each.
		expected := make(map[[2]string]bool, len(body.Artifacts))
		for _, artifact := range body.Artifacts {
			expected[[2]string{artifact.Kind, artifact.Ref}] = true
		}
		complete := len(result.Stored) == len(body.Artifacts)
		for _, stored := range result.Stored {
			key := [2]string{stored.Kind, stored.Ref}
			if !expected[key] {
				complete = false
			}
			delete(expected, key)
		}
		if !complete || len(expected) != 0 {
			return Result{}, fmt.Errorf("publish_unacknowledged: the server did not acknowledge every submitted artifact exactly once%s; check the hosted publish response before retrying", publishStatusDetail(result.Status, token))
		}
		return result, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return Result{}, fmt.Errorf("publish_auth: authentication or push authorization failed (HTTP %d)%s", resp.StatusCode, publishServerDetail(respBody, resp.StatusCode, token))
	case http.StatusUnprocessableEntity:
		return Result{}, fmt.Errorf("publish_rejected: server rejected the brain artifacts (HTTP 422)%s", publishServerDetail(respBody, resp.StatusCode, token))
	case http.StatusServiceUnavailable:
		// The server uses 503 for more than one reason (no hosted brain on this
		// deployment, publishing disabled for this jurisdiction, a store that is
		// temporarily read-only), so its own sentence is the only thing that says
		// WHICH — dropping the body here left the client guessing on the member's
		// behalf.
		return Result{}, fmt.Errorf("publish_unavailable: hosted brain publishing is not available on the server (HTTP 503)%s", publishServerDetail(respBody, resp.StatusCode, token))
	case http.StatusRequestEntityTooLarge:
		return Result{}, fmt.Errorf("publish_too_large: the server rejected the brain bundle as too large (HTTP 413); its request body exceeds the hosted publish size limit%s", publishServerDetail(respBody, resp.StatusCode, token))
	default:
		return Result{}, fmt.Errorf("publish_failed: unexpected server response (HTTP %d)%s", resp.StatusCode, publishServerDetail(respBody, resp.StatusCode, token))
	}
}

// publishStatusDetail renders the server's own `status` field for the
// acknowledged-nothing refusal, as " (status \"...\")", or "" when it said nothing.
// It goes through the same terminal-safety cleaning as every other server string
// this command prints.
func publishStatusDetail(status, token string) string {
	cleaned := httpx.Redact(httpx.ErrorDetailFromBody([]byte(status)), token)
	if cleaned == "" {
		return ""
	}
	return " (status " + strconv.Quote(cleaned) + ")"
}

// publishServerDetail renders a short human-readable reason from a server error body.
//
// It delegates to httpx, which is the shared renderer for every hosted-API error body
// in this repo. The local version this replaced handled the ONE shape the endpoint
// documents — huma's {"title","detail"} — and pasted everything else through as a
// raw 200-byte prefix. That is exactly the wrong answer for the two shapes that
// actually show up when the endpoint is not the thing answering: a proxy's HTML page
// (200 bytes of doctype and markup, with the useful part — its <title> — usually past
// the cut) and a body carrying control bytes, printed straight into the member's
// terminal. httpx renders both, and still prefers the envelope's own detail when the
// endpoint did answer.
//
// Every message this suffixes already prints the status ("(HTTP 404)"), so a body
// that merely echoes the status text would render doubled: "unexpected server
// response (HTTP 404): not found". The echo is stripped; a body saying more keeps
// the part beyond it.
func publishServerDetail(body []byte, statusCode int, token string) string {
	detail := httpx.StripStatusEcho(httpx.ErrorDetailFromBody(body),
		strconv.Itoa(statusCode)+" "+http.StatusText(statusCode))
	if detail == "" {
		return ""
	}
	// httpx.Redact strips this request's own bearer token from the rendered line: a
	// server that echoes the Authorization header back would otherwise put it in the
	// member's stderr, and from there into a CI log or a pasted bug report.
	return ": " + httpx.Redact(detail, token)
}

// ContentDigest returns the "sha256:"-prefixed lowercase-hex digest of data, the
// exact form the wire contract's ContentRef.Digest and the server's VerifyDigest
// expect.
func ContentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func semanticSourceOf(manifest *bmanifest.Manifest) *bmanifest.SemanticSource {
	if manifest == nil || manifest.Sources == nil {
		return nil
	}
	return manifest.Sources.Semantic
}

// ReadBrainBlob binds a payload to its brain-relative path, rejecting symlinks
// in every component and validating the opened regular-file descriptor. Missing
// files remain optional; unsafe files fail the bundle instead of being omitted.
func ReadBrainBlob(brainDir, rel string) ([]byte, bool, error) {
	f, present, err := brainstore.OpenMemoryStateFileExpected(brainDir, rel, "publish artifact", nil)
	if err != nil || !present {
		return nil, false, err
	}
	defer f.Close()
	max := clibase.SemanticSnapshotMaxBytes()
	path := f.Name()
	if info, err := f.Stat(); err != nil {
		return nil, false, err
	} else if info.Size() > max {
		return nil, false, &clibase.ReadBoundExceededError{Source: path, Max: max}
	}
	data, err := clibase.SafeReadAll(f, max, path)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func subdirNames(entries []os.DirEntry) []string {
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

// resolveFactStreamBranch validates a stream and recovers the branch that owns
// it. Records from other branches are expected content, not corruption: the
// hosted pull rebuilds a branch store from the repo-wide feed
// (pullHostedFacts), so after a sync on a feature branch the store also
// carries teammates' main facts. The owner is therefore not "the records'
// branch" but the branch that re-derives the directory name
// (factstore.BranchRelDir is deterministic: slug plus a hash of the full name).
// Candidates are every branch the records name, plus the directory's own slug
// for a store holding only pulled foreign records. A directory no candidate
// re-derives is refused — publishing it under a guessed branch would corrupt
// the artifact coordinate. (Residual gap: a store of only foreign records for
// a branch whose name sanitizes lossily, e.g. "evis/x"; such a stream fails
// publish until the branch distills or records a fact of its own.)
func resolveFactStreamBranch(dir string, data []byte) (string, error) {
	records, err := factmerge.ParseNDJSON(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("invalid fact stream: %w", err)
	}
	seen := make(map[string]struct{})
	var candidates []string
	for i, record := range records {
		branch := strings.TrimSpace(record.Branch)
		if branch == "" {
			return "", fmt.Errorf("fact record %d has no branch", i+1)
		}
		if _, dup := seen[branch]; !dup {
			seen[branch] = struct{}{}
			candidates = append(candidates, branch)
		}
	}
	if len(candidates) == 0 {
		return "", nil
	}
	recordBranches := strings.Join(candidates, ", ")
	if i := len(dir) - len("-01234567"); i > 0 {
		candidates = append(candidates, dir[:i])
	}
	for _, candidate := range candidates {
		if filepath.Base(factstore.BranchRelDir(candidate)) == dir {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("directory name matches no branch named by its records (%s); refusing to publish the stream under a guessed branch", recordBranches)
}

// overlayBranchName reads the advisory "branch" field from an overlay payload.
func overlayBranchName(data []byte) string {
	var payload struct {
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Branch)
}
