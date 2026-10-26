package cli

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"
)

// distillCallRecord is one extraction agent call in the --call-trace JSONL
// stream. Wall-clock fields come from the dispatching worker; token counts and
// agent-reported durations are attached by recordDistillProviderUsage via the
// context. started_at/call_s pairs across records reconstruct the pool's
// in-flight concurrency over time, which aggregate extraction_wait_seconds
// cannot (COR-2214).
type distillCallRecord struct {
	Session    string    `json:"session"`
	Chunk      int       `json:"chunk"`
	StartLine  int       `json:"start_line"`
	ChunkBytes int       `json:"chunk_bytes"`
	SlotWaitS  float64   `json:"slot_wait_s"`
	StartedAt  time.Time `json:"started_at"`
	CallS      float64   `json:"call_s"`
	// AgentMS/AgentAPIMS are the agent CLI's self-reported total and API
	// durations (claude --output-format json); their gap is process overhead
	// outside the API call. 0 = not reported by the agent.
	AgentMS                  int64 `json:"agent_ms,omitempty"`
	AgentAPIMS               int64 `json:"agent_api_ms,omitempty"`
	InputTokens              int64 `json:"input_tokens,omitempty"`
	OutputTokens             int64 `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
	// ResultBytes is the decoded visible result text; output_tokens minus its
	// token equivalent is thinking/overhead the extraction never sees.
	ResultBytes int    `json:"result_bytes"`
	Err         string `json:"err,omitempty"`
}

type distillCallRecordKey struct{}

// distillTraceWriter streams one JSON line per completed extraction call, so a
// killed run keeps the records for every call it already paid for. A nil
// writer disables tracing at every call site.
type distillTraceWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// call wraps one agent call with trace capture. The record rides the context
// so the structured-output decoder can attach provider-reported usage and
// durations to the exact call that produced them.
func (t *distillTraceWriter) call(ctx context.Context, session string, chunk transcriptChunk, chunkIdx int, slotWait time.Duration, run func(context.Context) (string, error)) (string, error) {
	if t == nil {
		return run(ctx)
	}
	rec := &distillCallRecord{
		Session:    session,
		Chunk:      chunkIdx,
		StartLine:  chunk.StartLine,
		ChunkBytes: len(chunk.Text),
		SlotWaitS:  slotWait.Seconds(),
	}
	start := time.Now()
	rec.StartedAt = start.UTC()
	out, err := run(context.WithValue(ctx, distillCallRecordKey{}, rec))
	rec.CallS = time.Since(start).Seconds()
	rec.ResultBytes = len(out)
	if err != nil {
		rec.Err = truncateAgentWarning(err.Error())
	}
	t.write(rec)
	return out, err
}

func (t *distillTraceWriter) write(rec *distillCallRecord) {
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.w.Write(append(line, '\n'))
}
