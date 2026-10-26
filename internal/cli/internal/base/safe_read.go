package base

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
)

// Size ceilings for reading untrusted inputs fully into memory. A file larger
// than its ceiling is overwhelmingly more likely to be corrupt or hostile than a
// legitimate input, so we fail loudly instead of letting a single read exhaust
// process memory. Callers needing a different bound pass it explicitly.

// Vars, not consts, so oversized-fixture tests can lower a bound instead of
// materializing the full-size condition under the race detector. Production
// never changes them; TestOverridableProductionBoundsKeepTheirDefaults pins
// the values.
var (
	// MaxDocumentTranscriptBytes bounds a single document-form session
	// transcript slurped into memory. Long sessions never approach this.
	MaxDocumentTranscriptBytes int64 = 256 << 20 // 256 MiB

	// DefaultMaxReadBytes is the fallback ceiling when a caller passes max <= 0.
	DefaultMaxReadBytes int64 = 64 << 20 // 64 MiB

	// MaxManifestBytes bounds auxiliary JSON state/index/cursor files.
	// Canonical Brain manifests use the dedicated, uncapped manifest reader.
	MaxManifestBytes int64 = 16 << 20 // 16 MiB
)

const (
	// MaxSeedDocBytes bounds a single seed/markdown document read for chunking.
	MaxSeedDocBytes = 32 << 20 // 32 MiB

	// DefaultMaxSemanticSnapshotBytes bounds a full entire-graph snapshot read. It is
	// generous because a large monorepo's snapshot is legitimately big; operators
	// with even larger repos can raise it via ENTIRE_BRAIN_MAX_SNAPSHOT_BYTES.
	DefaultMaxSemanticSnapshotBytes = 2 << 30 // 2 GiB
)

// SemanticSnapshotMaxBytes returns the ceiling for reading a full semantic
// snapshot, honoring an ENTIRE_BRAIN_MAX_SNAPSHOT_BYTES override so unusually
// large repositories are not artificially blocked.
func SemanticSnapshotMaxBytes() int64 {
	if v := os.Getenv("ENTIRE_BRAIN_MAX_SNAPSHOT_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return DefaultMaxSemanticSnapshotBytes
}

// SafeReadOpenFile is a variable so adversarial tests can drive the
// lstat-to-open TOCTOU window deterministically, matching the existing
// memoryStateOpenFile and privacyOpen seams.
var SafeReadOpenFile = os.OpenFile

// SafeOpenRegularFile opens path for reading and refuses anything that is not a
// regular file.
//
// A size ceiling alone does not bound a read. open(2) on a FIFO with no writer
// blocks inside the syscall before a single byte is read, and a character device
// yields bytes forever, exhausting the ceiling and then reporting the wrong
// reason. Both are reachable wherever the path is caller supplied, and several
// SafeReadFile call sites take an arbitrary path by design
// (brain_ingest_traces, loadEvalTasks).
//
// Two mechanisms, and only two:
//
//   - O_NONBLOCK on Unix (MemoryStateReadOpenFlags) so opening a FIFO returns
//     immediately instead of parking the calling thread until a writer appears;
//   - fstat on the RESULTING DESCRIPTOR, which must be a regular file.
//
// Checking the descriptor rather than the path is what makes this airtight:
// there is no window between the check and the use, because the thing checked
// IS the thing opened. A path-based lstat would need a post-open recheck to
// close its own TOCTOU gap; this has no gap to close.
//
// It deliberately does NOT refuse symlinks. An earlier revision paired an lstat
// with O_NOFOLLOW, which rejected every symlink at all 13 call sites. That is
// collateral damage, not a control: this helper defends against a read that
// never ends, and a symlink to an ordinary file ends exactly like the ordinary
// file does. Symlinked files are unremarkable on a developer machine -- a
// checkout under a symlinked parent, a fixture linked into a test tree, macOS's
// own /tmp -> /private/tmp -- and refusing them broke user-supplied paths with
// an error that named a property the user had no reason to think mattered. A
// symlink cannot smuggle a FIFO through either, because the fstat above
// describes what was actually opened, not what the path looked like.
//
// Write-side symlink escapes are a different problem with a different control:
// see rejectOpenFileAlias for lock and privacy artifacts, which brain owns and
// writes, and where following a link really would be an escape. Callers of
// SafeReadFile only read.
func SafeOpenRegularFile(path string) (*os.File, error) {
	// O_NONBLOCK must be on the open itself; there is no way to add it later.
	f, err := SafeReadOpenFile(path, os.O_RDONLY|MemoryStateReadOpenFlags(), 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("%s must be a regular file", path)
	}
	// This is a file-type and allocation boundary, not a wall-clock deadline:
	// regular-file reads can still wait on the backing filesystem. Trace ingestion
	// therefore reads before acquiring the semantic index lock.
	return f, nil
}

// SafeReadFile reads up to max bytes from path. If the file is larger than max it
// returns an error naming the path rather than allocating unbounded memory. A
// max <= 0 falls back to DefaultMaxReadBytes. The path must be a regular file:
// see SafeOpenRegularFile for why the size ceiling alone is not a bound.
func SafeReadFile(path string, max int64) ([]byte, error) {
	if max <= 0 {
		max = DefaultMaxReadBytes
	}
	f, err := SafeOpenRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// A regular file states its size before any of it is read, and that already
	// decides the answer: refuse here so an oversized input costs nothing. Reading
	// up to the ceiling first and only then rejecting still pays for the ceiling —
	// and pays several times over, because the growing read buffer churns every
	// intermediate size through the allocator on the way up. SafeOpenRegularFile
	// has already established that this descriptor IS a regular file, so the size
	// it reports is meaningful; the streaming bound in SafeReadAll stays the
	// authority for a file that grows between this stat and the read.
	if fi, statErr := f.Stat(); statErr == nil && fi.Size() > max {
		return nil, &ReadBoundExceededError{Source: path, Max: max}
	}
	return SafeReadAll(f, max, path)
}

// SafeReadAll reads up to max bytes from r. If r yields more than max bytes it
// returns an error naming source, so an oversized untrusted stream fails loudly
// instead of being silently truncated or driving the process out of memory.
// ReadBoundExceededError marks the ONE failure that is genuinely about size,
// so a caller can tell it apart from an I/O error or a cancelled context that
// io.ReadAll surfaces through the same return. The message is unchanged from
// the plain error it replaces.
type ReadBoundExceededError struct {
	Source string
	Max    int64
}

func (e *ReadBoundExceededError) Error() string {
	return fmt.Sprintf("%s exceeds maximum size of %d bytes", e.Source, e.Max)
}

func SafeReadAll(r io.Reader, max int64, source string) ([]byte, error) {
	if max <= 0 {
		max = DefaultMaxReadBytes
	}
	// Read one byte past the limit to detect overflow, guarding against max+1
	// wrapping negative when max is near MaxInt64 (which would make LimitReader
	// read nothing and silently return empty).
	limit := max
	if max < math.MaxInt64 {
		limit = max + 1
	}
	data, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, &ReadBoundExceededError{Source: source, Max: max}
	}
	return data, nil
}

// ReadFilePrefix reads at most max bytes from path and reports whether the file has
// more content beyond that. It is the truncating counterpart to SafeReadFile: where
// SafeReadFile refuses an oversized input, callers that only ever keep a bounded
// prefix (the seed document copier) can take that prefix without the file's full
// size ever entering memory.
func ReadFilePrefix(path string, max int) ([]byte, bool, error) {
	// SafeOpenRegularFile, not os.Open: this takes a repository file path, which is
	// as caller-supplied as SafeReadFile's, so it needs the same refusal of a FIFO
	// or device that would park or never end.
	f, err := SafeOpenRegularFile(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	// One byte past max distinguishes "exactly max bytes" from "longer than max".
	data, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > max {
		return data[:max], true, nil
	}
	return data, false, nil
}

// HashFileDetectingNUL streams path through sha256 and reports whether any NUL byte
// was seen, returning the "sha256:"-prefixed digest in the same form as the callers'
// previous sha256.Sum256 over the whole file. Streaming keeps peak memory at one
// buffer regardless of file size while leaving the digest byte-identical.
func HashFileDetectingNUL(path string) (digest string, binary bool, err error) {
	// SafeOpenRegularFile, not os.Open: hashing a FIFO or a character device would
	// never terminate, and this runs over whatever files a repository contains.
	f, err := SafeOpenRegularFile(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	hasher := sha256.New()
	buf := make([]byte, 32*1024)
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if !binary && bytes.IndexByte(chunk, 0) >= 0 {
				binary = true
			}
			hasher.Write(chunk)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", false, readErr
		}
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), binary, nil
}
