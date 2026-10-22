package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// semanticStreamLeanHeader is a minimal streaming header: identity fields only,
// no aggregate metadata (that lives in the trailing summary).
const semanticStreamLeanHeader = `{"schema_version":"1.0","provider":"entire-graph","provider_version":"0.2.0","repo_key":"gh/example/repo","commit":"aaa111","tree":"tree111"}`

func semanticStreamSummaryFixture() string {
	lines := []string{
		semanticStreamLeanHeader,
		`{"record_type":"file","file_path":"internal/auth/token.go","blob":"abc123","language":"Go"}`,
		`{"record_type":"symbol","id":"gh/example/repo:go:internal/auth/token.go:function:auth.ValidateToken","kind":"function","name":"ValidateToken","qualified_name":"auth.ValidateToken","file_path":"internal/auth/token.go","start_line":10,"end_line":20,"signature":"func ValidateToken(token string) error","language":"Go","stable_id_version":"1"}`,
		`{"record_type":"relation","from_id":"caller","to_id":"gh/example/repo:go:internal/auth/token.go:function:auth.ValidateToken","type":"CALLS","confidence":1}`,
		`{"record_type":"external","id":"external:package:fmt","kind":"package","name":"fmt"}`,
		`{"record_type":"future_thing","id":"x","note":"forward compatible payload"}`,
		`{"record_type":"summary","languages":["Go"],"capabilities":["go"],"profile":"default","relation_set":["CALLS"],"skipped_relation_families":["routes"],"completeness":{"languages":{"Go":{"files":1,"symbols":1}},"relations":{"CALLS":1}},"profile_limits":{"max_files":1000},"stats":{"files":1,"symbols":1,"relations":1,"externals":1,"completeness_level":"ok"},"warnings":[{"code":"summary_warning","severity":"warning","detail":"from summary"}],"partial_failures":[]}`,
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestScanSemanticStreamCapturesSummaryAndCounts(t *testing.T) {
	out := &bytes.Buffer{}
	res, err := scanSemanticStream(strings.NewReader(semanticStreamSummaryFixture()), out, semanticStreamScanConfig{})
	if err != nil {
		t.Fatalf("scan stream: %v", err)
	}
	if !res.haveHeader {
		t.Fatal("expected header to be parsed")
	}
	if res.summary == nil {
		t.Fatal("expected trailing summary to be captured")
	}
	if res.counts.Symbols != 1 || res.counts.Relations != 1 || res.counts.Files != 1 {
		t.Fatalf("unexpected counts: %+v", res.counts)
	}
	if res.stream.Externals != 1 {
		t.Fatalf("expected 1 external, got %d", res.stream.Externals)
	}
	if res.stream.Unknown != 1 {
		t.Fatalf("expected 1 unknown record, got %d", res.stream.Unknown)
	}
	if len(res.extraWarnings) != 1 || res.extraWarnings[0].Code != "provider_unknown_record_type" {
		t.Fatalf("expected unknown-record warning, got %+v", res.extraWarnings)
	}
	if !bytes.HasPrefix(bytes.TrimSpace(res.summary.Completeness), []byte("{")) {
		t.Fatalf("summary completeness should be a JSON object, got %s", res.summary.Completeness)
	}
}

func TestScanSemanticStreamPreservesUnknownAndExternalVerbatim(t *testing.T) {
	out := &bytes.Buffer{}
	if _, err := scanSemanticStream(strings.NewReader(semanticStreamSummaryFixture()), out, semanticStreamScanConfig{}); err != nil {
		t.Fatalf("scan stream: %v", err)
	}
	if !strings.Contains(out.String(), `"record_type":"future_thing"`) {
		t.Fatalf("unknown record was not preserved verbatim:\n%s", out.String())
	}
	if !strings.Contains(out.String(), `"forward compatible payload"`) {
		t.Fatalf("unknown record fields were dropped:\n%s", out.String())
	}
	if !strings.Contains(out.String(), `"record_type":"external"`) {
		t.Fatalf("external record was not preserved:\n%s", out.String())
	}
}

func TestScanSemanticStreamFilteredArtifactIsValidNDJSON(t *testing.T) {
	out := &bytes.Buffer{}
	if _, err := scanSemanticStream(strings.NewReader(semanticStreamSummaryFixture()), out, semanticStreamScanConfig{}); err != nil {
		t.Fatalf("scan stream: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected multiple NDJSON lines, got %d", len(lines))
	}
	for i, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("line %d is not valid JSON: %v\n%s", i+1, err, line)
		}
		if _, ok := obj["record_type"]; i == 0 && ok {
			t.Fatalf("first line should be the header, got %s", line)
		}
		if i == len(lines)-1 && obj["record_type"] != "summary" {
			t.Fatalf("last line should be the summary, got %s", line)
		}
	}
}

func TestScanSemanticStreamReportsMalformedLineInStrictMode(t *testing.T) {
	t.Setenv("ENTIRE_BRAIN_STRICT_INGEST", "1")
	input := semanticStreamLeanHeader + "\n" + `{"record_type":"symbol" BROKEN` + "\n"
	_, err := scanSemanticStream(strings.NewReader(input), io.Discard, semanticStreamScanConfig{})
	if err == nil {
		t.Fatal("expected error for malformed line in strict mode")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error should identify the malformed line: %v", err)
	}
}

func TestScanSemanticStreamTolerantlySkipsMalformedLine(t *testing.T) {
	// Default (tolerant) ingest skips a stray malformed record, counts it, and
	// surfaces a warning rather than failing the whole index. The following valid
	// records still make it through.
	input := semanticStreamLeanHeader + "\n" +
		`{"record_type":"symbol" BROKEN` + "\n" +
		`{"record_type":"file","file_path":"a.go","language":"Go"}` + "\n" +
		`{"record_type":"summary","languages":["Go"]}` + "\n"
	res, err := scanSemanticStream(strings.NewReader(input), io.Discard, semanticStreamScanConfig{})
	if err != nil {
		t.Fatalf("tolerant ingest should not fail on a stray malformed line: %v", err)
	}
	if res.stream.Dropped != 1 {
		t.Fatalf("expected 1 dropped record, got %d", res.stream.Dropped)
	}
	if res.stream.Files != 1 {
		t.Fatalf("valid file record after the bad line should be processed, files=%d", res.stream.Files)
	}
	foundWarning := false
	for _, w := range res.extraWarnings {
		if w.Code == "provider_malformed_record_dropped" {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("expected a provider_malformed_record_dropped warning, got %+v", res.extraWarnings)
	}
}

func TestScanSemanticStreamProcessesRecordsIncrementally(t *testing.T) {
	records := []string{
		`{"record_type":"file","file_path":"a.go","language":"Go"}`,
		`{"record_type":"symbol","id":"s1","kind":"function","name":"A","file_path":"a.go","stable_id_version":"1"}`,
		`{"record_type":"relation","from_id":"caller","to_id":"s1","type":"CALLS","confidence":1}`,
		`{"record_type":"summary","languages":["Go"]}`,
	}
	pr, pw := io.Pipe()
	proceed := make(chan struct{})
	cfg := semanticStreamScanConfig{
		onRecord: func(string) {
			// Block until the producer acknowledges, proving the consumer reaches
			// this record before the next one is even written. If the scanner
			// buffered the whole stream first, the producer would never write
			// past record 1 and this would not complete.
			proceed <- struct{}{}
		},
	}
	go func() {
		fmt.Fprintln(pw, semanticStreamLeanHeader)
		for _, rec := range records {
			fmt.Fprintln(pw, rec)
			<-proceed
		}
		_ = pw.Close()
	}()

	done := make(chan semanticStreamResult, 1)
	go func() {
		res, err := scanSemanticStream(pr, io.Discard, cfg)
		if err != nil {
			t.Errorf("scan stream: %v", err)
		}
		done <- res
	}()

	select {
	case res := <-done:
		if res.counts.Symbols != 1 || res.counts.Relations != 1 {
			t.Fatalf("unexpected counts: %+v", res.counts)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("streaming did not complete; records were not processed incrementally")
	}
}

// generatedSemanticStream lazily emits a header, n relation records, and a
// trailing summary without ever materializing the whole stream in memory, so
// the test can prove the scanner does not buffer proportionally.
type generatedSemanticStream struct {
	n       int
	i       int
	pending []byte
	bytes   int64
}

func (g *generatedSemanticStream) line(i int) (string, bool) {
	switch {
	case i == 0:
		return semanticStreamLeanHeader, true
	case i <= g.n:
		return fmt.Sprintf(`{"record_type":"relation","from_id":"caller-%d","to_id":"callee-%d","type":"CALLS","confidence":1}`, i, i), true
	case i == g.n+1:
		return `{"record_type":"summary","languages":["Go"]}`, true
	default:
		return "", false
	}
}

func (g *generatedSemanticStream) Read(p []byte) (int, error) {
	for len(g.pending) == 0 {
		line, ok := g.line(g.i)
		if !ok {
			return 0, io.EOF
		}
		g.i++
		g.pending = []byte(line + "\n")
	}
	n := copy(p, g.pending)
	g.pending = g.pending[n:]
	g.bytes += int64(n)
	return n, nil
}

func TestScanSemanticStreamDoesNotBufferProportionally(t *testing.T) {
	if raceEnabled {
		// The race detector instruments allocations, which both distorts heap
		// measurements and makes a large synthetic stream slow; the bounded-memory
		// property this asserts is independent of data races.
		t.Skip("heap-bounds test is unreliable and slow under -race")
	}
	const records = 50_000
	gen := &generatedSemanticStream{n: records}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	res, err := scanSemanticStream(gen, io.Discard, semanticStreamScanConfig{})
	if err != nil {
		t.Fatalf("scan stream: %v", err)
	}

	runtime.GC()
	runtime.ReadMemStats(&after)

	if res.counts.Relations != records {
		t.Fatalf("relation count = %d, want %d", res.counts.Relations, records)
	}
	heapDelta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if heapDelta < 0 {
		heapDelta = 0
	}
	// The stream is tens of megabytes; retained heap must be a tiny fraction of
	// it (relations are not retained in any map, so growth should be ~flat).
	if heapDelta*4 > gen.bytes {
		t.Fatalf("retained heap %d bytes is not bounded vs %d streamed bytes", heapDelta, gen.bytes)
	}
}

func TestStreamSemanticSnapshotReportsProviderFailureAfterPartialOutput(t *testing.T) {
	repoDir := t.TempDir()
	partial := semanticStreamLeanHeader + "\n" +
		`{"record_type":"symbol","id":"s1","kind":"function","name":"A","file_path":"a.go","stable_id_version":"1"}` + "\n"
	runner := &fakeCommandRunner{responses: map[string]fakeCommandResponse{
		fakeCommandKey("entire", "graph", "snapshot", "--repo", repoDir, "--format", "ndjson", "--no-network"): {
			stdout: partial,
			stderr: "provider crashed mid-stream",
			err:    fmt.Errorf("exit status 1"),
		},
	}}
	out := &bytes.Buffer{}
	res, err := streamSemanticSnapshot(context.Background(), runner, repoDir, "", semanticIndexOptions{graphBinary: "entire"}, nil, brainIgnore{}, out)
	if err == nil {
		t.Fatal("expected provider failure error")
	}
	if !strings.Contains(err.Error(), "semantic provider snapshot failed") {
		t.Fatalf("error should report provider failure: %v", err)
	}
	if !strings.Contains(err.Error(), "provider crashed mid-stream") {
		t.Fatalf("error should include provider stderr: %v", err)
	}
	// Partial records observed before the failure are still counted, but the
	// caller (runSemanticIndex) aborts on the error and never persists them.
	if res.counts.Symbols != 1 {
		t.Fatalf("expected partial symbol count, got %+v", res.counts)
	}
}

// TestStreamSemanticSnapshotFailsOnTruncatedEntireGraphStream proves the
// confirmed truncation bug: a real entire-graph snapshot always ends with a
// terminating "summary" record, emitted unconditionally right before the
// provider process exits 0. A provider bug, short write, wrapper/shim that
// swallows the real child's status, or a caught panic that still exits 0 can
// produce header + records with NO summary, and exit 0. Before the fix,
// streamSemanticSnapshot gated truncation detection solely on the exit code
// and silently accepted this as a complete snapshot. The absence of the
// terminating record is itself the truncation signal and must fail loudly for
// the default/entire-graph provider.
func TestStreamSemanticSnapshotFailsOnTruncatedEntireGraphStream(t *testing.T) {
	repoDir := t.TempDir()
	truncated := semanticStreamLeanHeader + "\n" +
		`{"record_type":"symbol","id":"s1","kind":"function","name":"A","file_path":"a.go","stable_id_version":"1"}` + "\n" +
		`{"record_type":"relation","from_id":"caller","to_id":"s1","type":"CALLS","confidence":1}` + "\n"
	runner := &fakeCommandRunner{responses: map[string]fakeCommandResponse{
		fakeCommandKey("entire", "graph", "snapshot", "--repo", repoDir, "--format", "ndjson", "--no-network"): {
			stdout: truncated,
			// No err: the child exits 0 despite never emitting its summary record.
		},
	}}
	out := &bytes.Buffer{}
	res, err := streamSemanticSnapshot(context.Background(), runner, repoDir, "", semanticIndexOptions{graphBinary: "entire"}, nil, brainIgnore{}, out)
	if err == nil {
		t.Fatalf("expected truncation error for a summary-less entire-graph stream, got success: %+v", res)
	}
	for _, want := range []string{"truncat", "summary"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Fatalf("error should mention %q, got: %v", want, err)
		}
	}
	// Record counts observed before the (missing) summary must be reported so
	// the failure is actionable.
	if !strings.Contains(err.Error(), "1") {
		t.Fatalf("error should report the records observed, got: %v", err)
	}
	if res.stream.Symbols != 1 || res.stream.Relations != 1 {
		t.Fatalf("expected partial counts to still be tracked: %+v", res.stream)
	}
}

// TestStreamSemanticSnapshotFailsOnTruncatedStreamWithEmptyProvider confirms
// the same truncation failure applies when the header omits "provider"
// entirely: Brain defaults an empty provider to entire-graph, so a missing
// summary must be treated as truncation there too.
func TestStreamSemanticSnapshotFailsOnTruncatedStreamWithEmptyProvider(t *testing.T) {
	repoDir := t.TempDir()
	header := `{"schema_version":"1.0","repo_key":"gh/example/repo","commit":"aaa111","tree":"tree111"}`
	truncated := header + "\n" +
		`{"record_type":"symbol","id":"s1","kind":"function","name":"A","file_path":"a.go","stable_id_version":"1"}` + "\n"
	runner := &fakeCommandRunner{responses: map[string]fakeCommandResponse{
		fakeCommandKey("entire", "graph", "snapshot", "--repo", repoDir, "--format", "ndjson", "--no-network"): {
			stdout: truncated,
		},
	}}
	out := &bytes.Buffer{}
	_, err := streamSemanticSnapshot(context.Background(), runner, repoDir, "", semanticIndexOptions{graphBinary: "entire"}, nil, brainIgnore{}, out)
	if err == nil {
		t.Fatal("expected truncation error when provider is empty (defaults to entire-graph)")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "truncat") {
		t.Fatalf("error should mention truncation, got: %v", err)
	}
}

// TestStreamSemanticSnapshotWarnsOnMissingSummaryForThirdPartyProvider proves
// the fix is safe in both version-skew directions: a third-party/unknown
// provider that legitimately never implements the trailing summary record
// must not be hard-failed. It succeeds, and the caller (runSemanticIndex)
// surfaces a warning instead.
func TestStreamSemanticSnapshotWarnsOnMissingSummaryForThirdPartyProvider(t *testing.T) {
	repoDir := t.TempDir()
	header := `{"schema_version":"1.0","provider":"other-sem-tool","provider_version":"9.0","repo_key":"gh/example/repo","commit":"aaa111","tree":"tree111"}`
	noSummary := header + "\n" +
		`{"record_type":"symbol","id":"s1","kind":"function","name":"A","file_path":"a.go","stable_id_version":"1"}` + "\n"
	runner := &fakeCommandRunner{responses: map[string]fakeCommandResponse{
		fakeCommandKey("entire", "graph", "snapshot", "--repo", repoDir, "--format", "ndjson", "--no-network"): {
			stdout: noSummary,
		},
	}}
	out := &bytes.Buffer{}
	res, err := streamSemanticSnapshot(context.Background(), runner, repoDir, "", semanticIndexOptions{graphBinary: "entire"}, nil, brainIgnore{}, out)
	if err != nil {
		t.Fatalf("third-party provider without a summary record should not hard-fail: %v", err)
	}
	found := false
	for _, w := range res.extraWarnings {
		if w.Code == "provider_summary_missing" {
			found = true
			if w.Severity != "warning" {
				t.Fatalf("expected warning severity, got %q", w.Severity)
			}
		}
	}
	if !found {
		t.Fatalf("expected a provider_summary_missing warning, got %+v", res.extraWarnings)
	}
}

// TestStreamSemanticSnapshotRequiresStreamingRunner confirms semantic indexing
// fails fast (rather than buffering) when given a runner that does not implement
// CommandStreamer.
func TestStreamSemanticSnapshotRequiresStreamingRunner(t *testing.T) {
	_, err := streamSemanticSnapshot(context.Background(), nonStreamingRunner{}, t.TempDir(), "", semanticIndexOptions{graphBinary: "entire"}, nil, brainIgnore{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "streaming command runner") {
		t.Fatalf("expected streaming-required error, got %v", err)
	}
}

// nonStreamingRunner implements CommandRunner but not CommandStreamer.
type nonStreamingRunner struct{}

func (nonStreamingRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
	return nil, nil, nil
}

// TestMergeSemanticSummaryCarriesLanguageTiers verifies the per-language tier
// map from the authoritative summary fills the lean header (the same path
// languages/completeness take), so it reaches the persisted manifest.
func TestMergeSemanticSummaryCarriesLanguageTiers(t *testing.T) {
	var header semanticHeader
	mergeSemanticSummary(&header, &semanticSummary{
		LanguageTiers: map[string]string{"Go": "semantic", "Dockerfile": "inventory-only"},
	})
	if header.LanguageTiers["Go"] != "semantic" || header.LanguageTiers["Dockerfile"] != "inventory-only" {
		t.Fatalf("merged language tiers = %#v", header.LanguageTiers)
	}
}

// TestScanSemanticStreamRealProviderOutput ingests a real entire-graph snapshot
// (schema 1.1, streaming). It fails before Fix 1 because completeness is an
// object, not a string, and the lean header cannot be parsed.
func TestScanSemanticStreamRealProviderOutput(t *testing.T) {
	data, err := os.ReadFile("testdata/graph_snapshot_stream.ndjson")
	if err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	res, err := scanSemanticStream(bytes.NewReader(data), out, semanticStreamScanConfig{})
	if err != nil {
		t.Fatalf("scan real provider stream: %v", err)
	}
	if !res.haveHeader || res.summary == nil {
		t.Fatalf("expected header and summary (haveHeader=%v summary=%v)", res.haveHeader, res.summary != nil)
	}

	// completeness is a JSON object on the summary (the breakdown), not a level.
	if !bytes.HasPrefix(bytes.TrimSpace(res.summary.Completeness), []byte("{")) {
		t.Fatalf("summary completeness should be a JSON object, got %s", res.summary.Completeness)
	}
	var comp struct {
		Relations map[string]int `json:"relations"`
	}
	if err := json.Unmarshal(res.summary.Completeness, &comp); err != nil {
		t.Fatalf("parse completeness object: %v", err)
	}
	if comp.Relations["DEFINES"] != 5 {
		t.Fatalf("completeness.relations DEFINES = %d, want 5", comp.Relations["DEFINES"])
	}

	// The level string lives in stats.completeness_level.
	var stats struct {
		Symbols           int    `json:"symbols"`
		CompletenessLevel string `json:"completeness_level"`
	}
	if err := json.Unmarshal(res.summary.Stats, &stats); err != nil {
		t.Fatalf("parse summary stats: %v", err)
	}
	if stats.CompletenessLevel != "ok" || stats.Symbols != 5 {
		t.Fatalf("summary stats = %+v, want completeness_level=ok symbols=5", stats)
	}

	// The summary is authoritative: merging it fills the lean header.
	header := res.header
	mergeSemanticSummary(&header, res.summary)
	if len(header.Languages) != 1 || header.Languages[0] != "Go" {
		t.Fatalf("merged languages = %v, want [Go]", header.Languages)
	}
	found := false
	for _, w := range header.Warnings {
		if w.Code == "W_WORKTREE_SNAPSHOT" {
			found = true
		}
	}
	if !found {
		t.Fatalf("merged header missing summary warning: %+v", header.Warnings)
	}
}

// TestScanSemanticStreamPreservesSchema11Fields enforces Fix 2: the filtered
// stream must not drop the schema-1.1 symbol/relation fields.
func TestScanSemanticStreamPreservesSchema11Fields(t *testing.T) {
	data, err := os.ReadFile("testdata/graph_snapshot_stream.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if _, err := scanSemanticStream(bytes.NewReader(data), out, semanticStreamScanConfig{}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		`"container_id":"local/bank:Go:account.go:type:Account"`,
		`"resolution":"type_inferred"`,
		`"relation_scope":"file"`,
		`"target_kind":"symbol"`,
		`"evidence":[`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("filtered stream dropped %s", want)
		}
	}
}

func TestScanSemanticStreamPreservesEvidenceDropped(t *testing.T) {
	input := strings.Join([]string{
		semanticStreamLeanHeader,
		`{"record_type":"relation","from_id":"caller","to_id":"callee","type":"DATA_FLOWS","confidence":1,"warning_codes":["EVIDENCE_TRUNCATED"],"evidence":[{"kind":"read","file_path":"internal/a.go","start_line":7,"end_line":7}],"evidence_dropped":3}`,
	}, "\n") + "\n"

	out := &bytes.Buffer{}
	if _, err := scanSemanticStream(strings.NewReader(input), out, semanticStreamScanConfig{}); err != nil {
		t.Fatalf("scan: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("filtered stream has %d lines, want 2:\n%s", len(lines), out.String())
	}
	var relation struct {
		EvidenceDropped int `json:"evidence_dropped"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &relation); err != nil {
		t.Fatalf("decode filtered relation: %v", err)
	}
	if relation.EvidenceDropped != 3 {
		t.Fatalf("evidence_dropped = %d, want 3; filtered relation: %s", relation.EvidenceDropped, lines[1])
	}
}
