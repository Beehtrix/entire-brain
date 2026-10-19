package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// legacyDistillConversationText is the map[string]any implementation the typed
// rawConversationText replaced, kept verbatim as the differential reference.
// Byte-identical preprocessing output is a hard requirement: session
// fingerprints hash it, so any divergence silently re-distills cached sessions.
func legacyDistillConversationText(obj map[string]any) string {
	switch jsonString(obj["type"]) {
	case "agent_message":
		return jsonString(obj["message"])
	case "event_msg":
		payload := jsonMap(obj["payload"])
		switch jsonString(payload["type"]) {
		case "agent_message", "user_message":
			return jsonString(payload["message"])
		case "task_complete":
			return jsonString(payload["last_agent_message"])
		}
		return ""
	case "response_item":
		payload := jsonMap(obj["payload"])
		if jsonString(payload["type"]) != "message" {
			return ""
		}
		if role := jsonString(payload["role"]); role != "assistant" && role != "user" {
			return ""
		}
		return distillTextBlocks(payload["content"])
	case "assistant", "user":
		if message, ok := obj["message"]; ok {
			return distillTextBlocks(jsonMap(message)["content"])
		}
		return distillTextBlocks(obj["content"])
	case "message":
		payload := jsonMap(obj["message"])
		if role := jsonString(payload["role"]); role != "assistant" && role != "user" {
			return ""
		}
		return distillTextBlocks(payload["content"])
	case "session_meta", "turn_context", "permission-mode", "progress":
		return ""
	default:
		return jsonString(obj["message"])
	}
}

// legacyPreprocessTranscriptForDistill is the whole-content reference: the
// exact Split/Unmarshal/Join implementation the streaming core replaced.
func legacyPreprocessTranscriptForDistill(content string) string {
	if doc, ok := distillDocumentConversation(content); ok {
		return doc
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			lines[i] = ""
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		lines[i] = strings.Join(strings.Fields(legacyDistillConversationText(obj)), " ")
	}
	return strings.Join(lines, "\n")
}

// preprocessDifferentialLines covers every dialect branch and the parity traps:
// null-vs-absent message, non-object payloads, wrong-typed fields, duplicate
// keys, empty blocks, non-JSON lines, and whitespace variants.
var preprocessDifferentialLines = []string{
	`{"type":"agent_message","message":"decided to use testify"}`,
	`{"type":"agent_message","message":42}`,
	`{"type":"event_msg","payload":{"type":"user_message","message":"please use tabs"}}`,
	`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"done"}}`,
	`{"type":"event_msg","payload":{"type":"exec_command_end","exit":0}}`,
	`{"type":"event_msg","payload":"not-an-object"}`,
	`{"type":"event_msg","payload":null}`,
	`{"type":"event_msg","payload":[1,2]}`,
	`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"text","text":"hello  world"},{"type":"tool_use","name":"bash"}]}}`,
	`{"type":"response_item","payload":{"type":"message","role":"tool","content":"x"}}`,
	`{"type":"response_item","payload":{"type":"function_call","name":"grep"}}`,
	`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"nested shape"}]}}`,
	`{"type":"assistant","message":null}`,
	`{"type":"assistant","message":"just a string"}`,
	`{"type":"assistant","content":[{"type":"text","text":"flat shape"},{"type":"tool_result","content":"noise"}]}`,
	`{"type":"assistant","content":"bare string content"}`,
	`{"type":"assistant","content":{"not":"an array"}}`,
	`{"type":"assistant","content":[{},null,"loose",{"type":"text"},{"type":"text","input_text":"it"},{"type":"text","output_text":"ot"},{"type":"text","content":"ct"},{"type":"text","text":"   "},{"type":"text","text":"  padded  "}]}`,
	`{"type":"message","message":{"role":"user","content":"pi shape"}}`,
	`{"type":"message","message":{"role":"toolResult","content":"dropped"}}`,
	`{"type":"message","message":"not an object"}`,
	`{"type":"session_meta","message":"meta"}`,
	`{"type":"turn_context"}`,
	`{"type":123,"message":"numeric type"}`,
	`{"type":null,"message":"null type"}`,
	`{"message":"typeless record"}`,
	`{"message":{"role":"user"}}`,
	`{"type":"assistant","type":"user","content":"duplicate key keeps last"}`,
	`{"type":"agent_message","message":"first","message":"duplicate message"}`,
	`{"type":"agent_message","message":"unicode \u00e9\u0009tab\nnewline"}`,
	`not json at all`,
	`[1,2,3]`,
	`"a bare string"`,
	`12345`,
	`null`,
	`true`,
	`{broken json`,
	`   `,
	"\t",
	"\r",
	`{"type":"user","content":[{"type":"text","text":"line with trailing cr"}]}` + "\r",
	`{"type":"agent_message","message":"bad surrogate \ud800 kept"}`,
	"{\"type\":\"agent_message\",\"message\":\"raw invalid utf8 \xff\xfe\"}",
	`{"type":"agent_message","message":"a"} trailing garbage`,
	`{"type":"agent_message","message":"a"}{"second":"object"}`,
	`{"type":"assistant","content":[[1,2],{"type":"text","text":"after nested array"}]}`,
	`{"type":"response_item","payload":{"content":[{"type":"text","text":"early content"}],"type":"message","role":"user"}}`,
	`{"type":"assistant","message":{"content":[{"text":"late type","type":"tool_result"}]}}`,
}

func TestPreprocessStreamingMatchesLegacyPerLine(t *testing.T) {
	extractor := newLineExtractor()
	for _, line := range preprocessDifferentialLines {
		var obj map[string]any
		var want string
		if strings.TrimSpace(line) == "" {
			want = ""
		} else if err := json.Unmarshal([]byte(line), &obj); err != nil {
			want = line
		} else {
			want = strings.Join(strings.Fields(legacyDistillConversationText(obj)), " ")
		}
		if got := extractor.preprocessLine([]byte(line)); got != want {
			t.Errorf("line %q:\n got %q\nwant %q", line, got, want)
		}
	}
}

func TestPreprocessStreamingMatchesLegacyWholeContent(t *testing.T) {
	joined := strings.Join(preprocessDifferentialLines, "\n")
	contents := []string{
		joined,
		joined + "\n",
		"\n\n" + joined + "\n\n",
		"",
		"\n",
		"single line no newline",
		// Document-form (opencode) transcript: exercises the probe + document path.
		"{\n  \"info\": {},\n  \"messages\": [\n    {\"info\": {\"role\": \"user\"}, \"parts\": [{\"type\": \"text\", \"text\": \"doc form text\"}]}\n  ]\n}",
		// Document-lookalike that must fall through to the JSONL path.
		"{\n  broken document",
	}
	for i, content := range contents {
		want := legacyPreprocessTranscriptForDistill(content)
		got := preprocessTranscriptForDistill(content)
		if got != want {
			t.Errorf("content %d: streaming output diverges from legacy\n got %q\nwant %q", i, got, want)
		}
		if strings.Count(got, "\n") != strings.Count(content, "\n") {
			t.Errorf("content %d: line count changed: %d -> %d", i, strings.Count(content, "\n"), strings.Count(got, "\n"))
		}
	}
}
