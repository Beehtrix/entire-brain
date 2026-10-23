package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDistillTraceWriterRecordsCallWithProviderUsage(t *testing.T) {
	var buf bytes.Buffer
	trace := &distillTraceWriter{w: &buf}
	chunk := transcriptChunk{StartLine: 7, Text: "chunk body"}
	out, err := trace.call(context.Background(), "sess-1", chunk, 3, 250*time.Millisecond, func(ctx context.Context) (string, error) {
		// Simulate the agent exec path: the structured-output decoder reports
		// this call's usage through the same context.
		recordDistillProviderUsage(ctx, distillProviderUsage{
			Source:        distillUsageSourceClaude,
			Reported:      true,
			InputReported: true,
			InputTokens:   11,
			OutputTokens:  5,
			DurationMS:    1200,
			DurationAPIMS: 900,
		})
		return "ok", nil
	})
	if err != nil || out != "ok" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	var rec distillCallRecord
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("trace line is not JSON: %v (%q)", err, buf.String())
	}
	if rec.Session != "sess-1" || rec.Chunk != 3 || rec.StartLine != 7 || rec.ChunkBytes != len(chunk.Text) {
		t.Fatalf("identity fields wrong: %+v", rec)
	}
	if rec.SlotWaitS != 0.25 || rec.CallS < 0 || rec.StartedAt.IsZero() {
		t.Fatalf("timing fields wrong: %+v", rec)
	}
	if rec.AgentMS != 1200 || rec.AgentAPIMS != 900 || rec.InputTokens != 11 || rec.OutputTokens != 5 {
		t.Fatalf("provider fields not attached: %+v", rec)
	}
	if rec.Err != "" {
		t.Fatalf("unexpected err field: %+v", rec)
	}
}

func TestDistillTraceWriterRecordsFailedCallAndNilIsNoop(t *testing.T) {
	var buf bytes.Buffer
	trace := &distillTraceWriter{w: &buf}
	_, err := trace.call(context.Background(), "sess-2", transcriptChunk{Text: "x"}, 0, 0, func(context.Context) (string, error) {
		return "", errors.New("agent timed out after 10m0s")
	})
	if err == nil {
		t.Fatal("expected error passthrough")
	}
	var rec distillCallRecord
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("trace line is not JSON: %v", err)
	}
	if !strings.Contains(rec.Err, "timed out") {
		t.Fatalf("err not recorded: %+v", rec)
	}

	var nilTrace *distillTraceWriter
	out, err := nilTrace.call(context.Background(), "s", transcriptChunk{}, 0, 0, func(context.Context) (string, error) {
		return "passthrough", nil
	})
	if err != nil || out != "passthrough" {
		t.Fatalf("nil trace must be a pure passthrough: out=%q err=%v", out, err)
	}
}

func TestParseClaudeDistillOutputCapturesDurations(t *testing.T) {
	raw := `{"type":"result","subtype":"success","is_error":false,"duration_ms":4321,"duration_api_ms":4000,"result":"ok","usage":{"input_tokens":1,"output_tokens":2}}`
	_, usage, err := parseClaudeDistillOutput(raw)
	if err != nil {
		t.Fatal(err)
	}
	if usage.DurationMS != 4321 || usage.DurationAPIMS != 4000 {
		t.Fatalf("durations not parsed: %+v", usage)
	}
}
