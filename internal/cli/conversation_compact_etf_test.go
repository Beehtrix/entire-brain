package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Entire CLI's compact transcript (Entire Transcript Format) carries the
// content blocks at the RECORD's top level, with no "message" wrapper:
//
//	{"v":1,"agent":"claude-code","type":"user","content":[{"id":"u1","text":"..."}]}
//	{"v":1,"agent":"claude-code","type":"assistant","content":[{"type":"text",...},{"type":"tool_use",...}]}
//
// It is the DEFAULT export flavor, so the conversation and history dialect
// switches must parse it (customer report: compact exports produced zero
// searchable exchanges; COR-2289).
const (
	etfUserLine      = `{"v":1,"agent":"claude-code","cli_version":"0.12.2","type":"user","ts":"2026-10-26T02:30:20.496Z","content":[{"id":"u1","text":"please add a healthcheck endpoint"}]}`
	etfToolLine      = `{"v":1,"agent":"claude-code","cli_version":"0.12.2","type":"assistant","ts":"2026-10-26T02:30:22.000Z","content":[{"type":"text","text":"Adding the endpoint now."},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./..."},"result":{"output":"ok"}}]}`
	etfAssistantLine = `{"v":1,"agent":"claude-code","cli_version":"0.12.2","type":"assistant","ts":"2026-10-26T02:30:25.000Z","content":[{"type":"text","text":"Added /healthz returning 200."}]}`
)

func etfTranscript() string {
	return etfUserLine + "\n" + etfToolLine + "\n" + etfAssistantLine + "\n"
}

func TestScanConversationTranscriptParsesCompactETF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(etfTranscript()), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := scanConversationTranscript(path)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(result.Exchanges) != 1 {
		t.Fatalf("exchanges = %d, want 1 (%+v)", len(result.Exchanges), result)
	}
	exchange := result.Exchanges[0]
	if !strings.Contains(exchange.Request, "healthcheck endpoint") {
		t.Errorf("request = %q, want the ETF user text", exchange.Request)
	}
	if !strings.Contains(exchange.Response, "Added /healthz") {
		t.Errorf("response = %q, want the ETF assistant narrative", exchange.Response)
	}
	if len(exchange.ToolNames) != 1 || exchange.ToolNames[0] != "Bash" {
		t.Errorf("tool names = %v, want [Bash]", exchange.ToolNames)
	}
}

func TestExtractHistoryJSONFragmentsCompactETF(t *testing.T) {
	var user, assistant map[string]any
	if err := json.Unmarshal([]byte(etfUserLine), &user); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(etfToolLine), &assistant); err != nil {
		t.Fatal(err)
	}

	userFragments := extractHistoryJSONFragments(user)
	if len(userFragments) != 1 || userFragments[0].Source != "user_prompt" {
		t.Fatalf("user fragments = %+v, want one user_prompt", userFragments)
	}

	sources := map[string]bool{}
	for _, fragment := range extractHistoryJSONFragments(assistant) {
		sources[fragment.Source] = true
	}
	if !sources["assistant_message"] {
		t.Errorf("assistant fragments missing assistant_message: %v", sources)
	}
	if !sources[historyToolCallSource("Bash")] {
		t.Errorf("assistant fragments missing tool call: %v", sources)
	}
}
