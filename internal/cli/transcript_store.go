package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// transcript_store.go is COR-2128's single transcript read path. Transcript
// content lives in git checkpoint refs — immutable, already stored — so the
// manifest's transcript paths are VIRTUAL identifiers resolved to checkpoint
// blob reads on demand rather than files duplicated under the brain dir.
//
// A materialized file still wins when present: exported bundles (--output)
// are standalone artifacts that carry their transcripts as files, and brains
// written before this change keep working until their cache is swept.
//
// Every consumer must read through this store, because the store is where two
// invariants are enforced:
//
//   - A purged session stays purged. The tombstone gate used to be implicit —
//     the purge deleted the cached file — and with on-demand git reads the
//     gate must live on the read path itself.
//   - Reads never touch the network. Resolution and blob reads run through the
//     same no-egress policy the snapshot export uses; a promisor-absent blob
//     is a read error the caller skips with a warning, never a fetch.

// transcriptLocation addresses one session's transcript inside the snapshot.
type transcriptLocation struct {
	sourceKey  string
	sourcePath string
}

// transcriptStore resolves in two lazy stages: the manifest and tombstones
// load on first read (cheap, and the gate must precede every read), while the
// checkpoint snapshot resolves only when a read actually needs git — a
// bundle with materialized files never pays for checkpoint discovery. The
// snapshot scan is amortized by the persisted checkpoint metadata cache (kept
// warm by export); the store loads that cache read-only and never writes it.
type transcriptStore struct {
	runner   CommandRunner
	brainDir string

	snapOnce sync.Once
	snapErr  error
	snapshot *checkpointSnapshot
	byRel    map[string]transcriptLocation
}

// newTranscriptStore builds a store for one brain. The repository the brain
// belongs to resolves lazily from the manifest's recorded repo root; a
// standalone exported bundle has materialized files and never needs it.
func newTranscriptStore(runner CommandRunner, brainDir string) *transcriptStore {
	return &transcriptStore{runner: runner, brainDir: brainDir}
}

// transcriptStores memoizes one store per brain dir for the process, so the
// checkpoint snapshot resolves at most once no matter how many reads a command
// performs. ponytail: a snapshot's temp dir (when a backend extracts one) is
// only reclaimed at process exit for registry stores; commands are short-lived
// and the dir lives under os.TempDir.
var transcriptStores sync.Map

func transcriptStoreFor(brainDir string) *transcriptStore {
	key := filepath.Clean(brainDir)
	if store, ok := transcriptStores.Load(key); ok {
		return store.(*transcriptStore)
	}
	store, _ := transcriptStores.LoadOrStore(key, newTranscriptStore(ExecRunner{}, brainDir))
	return store.(*transcriptStore)
}

// Close releases the snapshot's temp dir, if resolution created one.
func (s *transcriptStore) Close() {
	if s == nil {
		return
	}
	s.snapshot.Cleanup()
}

// excludedNow loads the tombstone gate fresh on every read: a purge that
// tombstones a session mid-process must bind the very next read, so the gate
// is deliberately NOT memoized (both inputs are small local files).
func (s *transcriptStore) excludedNow() (map[string]string, error) {
	stones, _, stonesErr := loadSessionTombstonesChecked(s.brainDir)
	if stonesErr != nil {
		// Fail closed: an unreadable tombstone file must not open purged
		// transcripts back up.
		return nil, fmt.Errorf("transcript store: %w", stonesErr)
	}
	manifest, err := loadBrainManifest(s.brainDir)
	if err != nil {
		// A brain without a manifest has nothing tombstoned to map; refuse
		// only when tombstones exist and cannot be resolved to paths.
		if len(stones.Excluded) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("transcript store: tombstones present but manifest unreadable: %w", err)
	}
	excluded, err := excludedTranscriptPathsChecked(s.brainDir, manifest, stones)
	if err != nil {
		return nil, fmt.Errorf("transcript store: %w", err)
	}
	return excluded, nil
}

func (s *transcriptStore) initSnapshot(ctx context.Context) error {
	s.snapOnce.Do(func() { s.snapErr = s.resolveSnapshot(ctx) })
	return s.snapErr
}

func (s *transcriptStore) resolveSnapshot(ctx context.Context) error {
	manifest, err := loadBrainManifest(s.brainDir)
	if err != nil {
		return fmt.Errorf("transcript store: %w", err)
	}
	if manifest.Sources == nil || manifest.Sources.Sessions == nil {
		return fmt.Errorf("transcript store: brain has no session source manifest")
	}
	repoDir := strings.TrimSpace(manifest.RepoRoot)
	if repoDir == "" {
		return fmt.Errorf("transcript store: brain manifest records no repo root to resolve checkpoints from")
	}
	defaultBranch, _ := detectDefaultBranch(ctx, s.runner, repoDir)
	branchDestinations, _ := buildCheckpointBranchDestinations(ctx, s.runner, repoDir, defaultBranch)
	cache := loadCheckpointMetadataCache(filepath.Join(s.brainDir, filepath.FromSlash(checkpointMetadataCachePath)))
	raw := manifest.Sources.Sessions.TranscriptMode == "raw"
	snapshot, _, err := loadConfiguredCheckpointSnapshot(ctx, s.runner, repoDir, raw, 0, branchDestinations, nil, cache)
	if err != nil {
		return fmt.Errorf("transcript store: resolve checkpoint snapshot: %w", err)
	}
	s.snapshot = snapshot

	// Index the snapshot's sessions by identity so manifest paths resolve by
	// (session, checkpoint, index) rather than by the key format either side
	// uses internally.
	type sessionIdentity struct {
		sessionID    string
		checkpointID string
		sessionIndex int
	}
	byIdentity := make(map[sessionIdentity]selectedSession, len(snapshot.Selected))
	for _, selected := range snapshot.Selected {
		byIdentity[sessionIdentity{selected.SessionID, selected.CheckpointID, selected.SessionIndex}] = selected
	}

	s.byRel = make(map[string]transcriptLocation, len(manifest.Sources.Sessions.Sessions))
	for _, session := range manifest.Sources.Sessions.Sessions {
		rel := cleanTranscriptRel(session.TranscriptPath)
		if rel == "" {
			continue
		}
		selected, ok := byIdentity[sessionIdentity{session.SessionID, session.LatestCheckpoint, session.SessionIndex}]
		if !ok {
			// Checkpoint no longer resolvable locally: the session stays listed
			// but unreadable, and Read reports it when asked.
			continue
		}
		sourcePath := selected.SourceTranscriptPath
		if sourcePath == "" {
			sourcePath = snapshotTranscriptPath(selected.CheckpointID, selected.SessionIndex, snapshot.TranscriptFileName)
		}
		s.byRel[rel] = transcriptLocation{sourceKey: selected.SourceKey, sourcePath: sourcePath}
	}
	return nil
}

func cleanTranscriptRel(rel string) string {
	rel = path.Clean(strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/")))
	if rel == "" || rel == "." {
		return ""
	}
	return rel
}

// Read returns one session transcript's verbatim bytes by its manifest path.
// A materialized file wins when present; otherwise the content reads from the
// session's checkpoint blob. Both branches sit behind the tombstone gate.
func (s *transcriptStore) Read(ctx context.Context, rel string) ([]byte, error) {
	reader, finish, err := s.Open(ctx, rel)
	if err != nil {
		return nil, err
	}
	data, readErr := safeReadAll(contextCheckingReader{ctx: ctx, r: reader}, maxDocumentTranscriptBytes, "canonical transcript "+rel)
	finishErr := finish()
	if readErr != nil || finishErr != nil {
		combined := errors.Join(readErr, finishErr)
		if readErr != nil && strings.Contains(readErr.Error(), "exceeds maximum size") {
			return nil, fmt.Errorf("%s: %w", memoryErrInputTooLarge, combined)
		}
		return nil, combined
	}
	return data, nil
}

// Open is the streaming variant of Read, for consumers that scan line by line
// without retaining the whole transcript. finish must be called after the
// complete read; for a materialized file it revalidates the descriptor-bound
// leaf exactly as the direct read contract always has.
func (s *transcriptStore) Open(ctx context.Context, rel string) (io.Reader, func() error, error) {
	if s == nil {
		return nil, nil, fmt.Errorf("transcript store is nil")
	}
	excluded, err := s.excludedNow()
	if err != nil {
		return nil, nil, err
	}
	rel = cleanTranscriptRel(rel)
	if reason, purged := excluded[rel]; purged {
		return nil, nil, fmt.Errorf("%s: transcript %s is tombstoned (%s)", memoryErrStateUnsafe, rel, reason)
	}

	file, finish, fileErr := openCanonicalHistoryTranscript(ctx, s.brainDir, rel)
	if fileErr == nil {
		return file, finish, nil
	}
	if !errors.Is(fileErr, fs.ErrNotExist) {
		// A present-but-unsafe file (symlink games, swapped leaf) is a refusal,
		// not an invitation to read the same path from somewhere else.
		return nil, nil, fileErr
	}

	if err := s.initSnapshot(ctx); err != nil {
		return nil, nil, err
	}
	location, ok := s.byRel[rel]
	if !ok {
		return nil, nil, fmt.Errorf("transcript %s has no readable checkpoint source", rel)
	}
	data, err := readSnapshotTranscriptFromSource(ctx, s.runner, s.snapshot, location.sourceKey, location.sourcePath)
	if err != nil {
		return nil, nil, fmt.Errorf("read transcript %s: %w", rel, err)
	}
	return bytes.NewReader(data), func() error { return nil }, nil
}
