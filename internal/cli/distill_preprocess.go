package cli

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"io"
	"strings"
)

// distill_preprocess.go is the streaming transcript preprocessor. It walks each
// JSONL record's tokens with jsontext, capturing only the handful of candidate
// strings the conversation extractor can route to and skipping everything else
// — the megabytes of tool input/output per record are never materialized as Go
// values. Output is BYTE-IDENTICAL to the legacy map[string]any implementation:
// session fingerprints hash this text, so any drift would silently re-distill
// every cached session (see distillPreprocessingVersion for the escape hatch
// when a semantic change is intended). The legacy implementation lives in
// distill_preprocess_test.go and a differential test holds the two equal across
// every dialect and parity trap (null-vs-absent keys, duplicate keys, wrong
// types, invalid UTF-8, trailing garbage).

// errNotConversationJSON reports a line the extractor must pass through
// unchanged — exactly the lines the legacy json.Unmarshal rejected: malformed
// JSON, a non-object top level, or trailing content after the object.
var errNotConversationJSON = errors.New("not a JSON object line")

// lineExtractor owns the reusable decoder state for one preprocessing pass.
// Not safe for concurrent use; each caller builds its own.
type lineExtractor struct {
	reader bytes.Reader
	dec    *jsontext.Decoder
}

func newLineExtractor() *lineExtractor {
	e := &lineExtractor{}
	// AllowDuplicateNames and AllowInvalidUTF8 mirror encoding/json v1, which
	// the legacy path used: duplicates keep the last value, and invalid UTF-8
	// coerces to U+FFFD instead of rejecting the line.
	e.dec = jsontext.NewDecoder(&e.reader, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	return e
}

// envCaptures holds the candidate fields of one nested envelope (a "payload"
// or an object-valued "message"). Every field is "" unless the LAST occurrence
// of its key held that shape, mirroring v1 last-wins duplicate handling plus
// jsonString/jsonMap's non-matching-type behavior.
type envCaptures struct {
	typeStr          string
	role             string
	messageStr       string
	lastAgentMessage string
	contentText      string
}

// lineCaptures holds the top-level candidates of one record.
type lineCaptures struct {
	typeStr     string
	msgPresent  bool
	msgString   string
	msgEnv      envCaptures
	payloadEnv  envCaptures
	contentText string
}

// readStringValue consumes the next value: a JSON string yields its unquoted
// content, anything else is skipped and yields "" (jsonString semantics).
func readStringValue(dec *jsontext.Decoder) (string, error) {
	if dec.PeekKind() == '"' {
		tok, err := dec.ReadToken()
		if err != nil {
			return "", err
		}
		return tok.String(), nil
	}
	return "", dec.SkipValue()
}

// readContentValue consumes a "content" value with distillTextBlocks
// semantics: a string passes through, an array keeps the text of non-tool
// blocks joined by one space, anything else is "".
func readContentValue(dec *jsontext.Decoder) (string, error) {
	switch dec.PeekKind() {
	case '"':
		return readStringValue(dec)
	case '[':
		if _, err := dec.ReadToken(); err != nil {
			return "", err
		}
		var parts []string
		for dec.PeekKind() != ']' {
			if dec.PeekKind() != '{' {
				// Mirrors jsonMap(item) == nil → skip the element.
				if err := dec.SkipValue(); err != nil {
					return "", err
				}
				continue
			}
			text, err := readTextBlock(dec)
			if err != nil {
				return "", err
			}
			if text != "" {
				parts = append(parts, text)
			}
		}
		if _, err := dec.ReadToken(); err != nil { // consume ']'
			return "", err
		}
		return strings.Join(parts, " "), nil
	default:
		return "", dec.SkipValue()
	}
}

// readTextBlock consumes one content-array block object and returns its kept
// text: "" for tool_use/tool_result blocks and for blocks with no non-blank
// text/input_text/output_text/content string (firstNonEmptyString semantics).
func readTextBlock(dec *jsontext.Decoder) (string, error) {
	if _, err := dec.ReadToken(); err != nil { // consume '{'
		return "", err
	}
	var blockType, text, inputText, outputText, content string
	for dec.PeekKind() != '}' {
		keyTok, err := dec.ReadToken()
		if err != nil {
			return "", err
		}
		var dst *string
		switch keyTok.String() {
		case "type":
			dst = &blockType
		case "text":
			dst = &text
		case "input_text":
			dst = &inputText
		case "output_text":
			dst = &outputText
		case "content":
			dst = &content
		default:
			if err := dec.SkipValue(); err != nil {
				return "", err
			}
			continue
		}
		if *dst, err = readStringValue(dec); err != nil {
			return "", err
		}
	}
	if _, err := dec.ReadToken(); err != nil { // consume '}'
		return "", err
	}
	switch blockType {
	case "tool_use", "tool_result":
		return "", nil
	}
	for _, candidate := range []string{text, inputText, outputText, content} {
		if strings.TrimSpace(candidate) != "" {
			return candidate, nil
		}
	}
	return "", nil
}

// readEnvelope consumes an object-valued "payload"/"message" and captures its
// candidate fields.
func readEnvelope(dec *jsontext.Decoder) (envCaptures, error) {
	var env envCaptures
	if _, err := dec.ReadToken(); err != nil { // consume '{'
		return env, err
	}
	for dec.PeekKind() != '}' {
		keyTok, err := dec.ReadToken()
		if err != nil {
			return env, err
		}
		switch keyTok.String() {
		case "type":
			env.typeStr, err = readStringValue(dec)
		case "role":
			env.role, err = readStringValue(dec)
		case "message":
			env.messageStr, err = readStringValue(dec)
		case "last_agent_message":
			env.lastAgentMessage, err = readStringValue(dec)
		case "content":
			env.contentText, err = readContentValue(dec)
		default:
			err = dec.SkipValue()
		}
		if err != nil {
			return env, err
		}
	}
	_, err := dec.ReadToken() // consume '}'
	return env, err
}

// extractLine parses one JSONL record and returns its conversation text.
// errNotConversationJSON (or any decode error) means the caller must pass the
// line through unchanged.
func (e *lineExtractor) extractLine(line []byte) (string, error) {
	e.reader.Reset(line)
	e.dec.Reset(&e.reader, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	tok, err := e.dec.ReadToken()
	if err != nil {
		return "", errNotConversationJSON
	}
	if tok.Kind() == 'n' {
		// A bare `null` line: v1 Unmarshal into a map succeeds with a nil map,
		// which extracts to "". Mirror that (still rejecting trailing content).
		if _, err := e.dec.ReadToken(); err != io.EOF {
			return "", errNotConversationJSON
		}
		return "", nil
	}
	if tok.Kind() != '{' {
		return "", errNotConversationJSON
	}
	var c lineCaptures
	for e.dec.PeekKind() != '}' {
		keyTok, err := e.dec.ReadToken()
		if err != nil {
			return "", err
		}
		switch keyTok.String() {
		case "type":
			c.typeStr, err = readStringValue(e.dec)
		case "message":
			// Last occurrence wins, including its shape: reset the captures a
			// previous occurrence may have set.
			c.msgPresent, c.msgString, c.msgEnv = true, "", envCaptures{}
			if e.dec.PeekKind() == '{' {
				c.msgEnv, err = readEnvelope(e.dec)
			} else {
				c.msgString, err = readStringValue(e.dec)
			}
		case "payload":
			c.payloadEnv = envCaptures{}
			if e.dec.PeekKind() == '{' {
				c.payloadEnv, err = readEnvelope(e.dec)
			} else {
				err = e.dec.SkipValue()
			}
		case "content":
			c.contentText, err = readContentValue(e.dec)
		default:
			err = e.dec.SkipValue()
		}
		if err != nil {
			return "", err
		}
	}
	if _, err := e.dec.ReadToken(); err != nil { // consume '}'
		return "", err
	}
	// v1 Unmarshal rejects trailing content after the object; mirror it.
	if _, err := e.dec.ReadToken(); err != io.EOF {
		return "", errNotConversationJSON
	}
	return routeConversationText(c), nil
}

// routeConversationText mirrors the legacy distillConversationText switch over
// the captured candidates (reference copy in distill_preprocess_test.go).
func routeConversationText(c lineCaptures) string {
	switch c.typeStr {
	case "agent_message":
		return c.msgString
	case "event_msg":
		switch c.payloadEnv.typeStr {
		case "agent_message", "user_message":
			return c.payloadEnv.messageStr
		case "task_complete":
			return c.payloadEnv.lastAgentMessage
		}
		return "" // task_started, exec_command_end, patch_apply_end, ...
	case "response_item":
		if c.payloadEnv.typeStr != "message" {
			return "" // function_call, custom_tool_call, function_call_output, reasoning
		}
		if role := c.payloadEnv.role; role != "assistant" && role != "user" {
			return ""
		}
		return c.payloadEnv.contentText
	case "assistant", "user":
		// Presence of "message" (even null or non-object) selects the nested
		// shape, exactly like the map path's two-value key lookup did.
		if c.msgPresent {
			return c.msgEnv.contentText
		}
		return c.contentText
	case "message":
		if role := c.msgEnv.role; role != "assistant" && role != "user" {
			return "" // toolResult, ...
		}
		return c.msgEnv.contentText
	case "session_meta", "turn_context", "permission-mode", "progress":
		return ""
	default:
		return c.msgString
	}
}

// preprocessLine transforms one JSONL line exactly as the legacy per-line
// logic did: blank stays blank, non-JSON-object lines pass through unchanged,
// and a decoded record collapses to its whitespace-normalized conversation text.
func (e *lineExtractor) preprocessLine(line []byte) string {
	if len(bytes.TrimSpace(line)) == 0 {
		return ""
	}
	text, err := e.extractLine(line)
	if err != nil {
		return string(line) // not a JSON object; leave the line as-is
	}
	return strings.Join(strings.Fields(text), " ")
}

// isDocumentFormTranscript mirrors distillDocumentConversation's probe without
// converting the whole input to a string: a document-form transcript starts
// with "{" (after trimming) and its first line is NOT complete JSON (a JSONL
// transcript's first line is).
func isDocumentFormTranscript(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if !bytes.HasPrefix(trimmed, []byte("{")) {
		return false
	}
	firstLine, _, _ := bytes.Cut(trimmed, []byte("\n"))
	return !json.Valid(firstLine)
}

// preprocessTranscriptForDistillBytes is the streaming core behind
// preprocessTranscriptForDistill: one pass over the raw bytes, line slices
// taken zero-copy, no whole-file string or line-slice materialization. Only a
// document-form transcript (the opencode shape, a small minority) pays a full
// string conversion, because its parser needs the whole document.
func preprocessTranscriptForDistillBytes(data []byte) string {
	if isDocumentFormTranscript(data) {
		if doc, ok := distillDocumentConversation(string(data)); ok {
			return doc
		}
	}
	extractor := newLineExtractor()
	var b strings.Builder
	// Stripped output is a fraction of the input; start small and let the
	// builder grow instead of reserving input-sized memory.
	b.Grow(min(len(data)/16+64, 1<<20))
	rest := data
	for first := true; ; first = false {
		if !first {
			b.WriteByte('\n')
		}
		idx := bytes.IndexByte(rest, '\n')
		line := rest
		if idx >= 0 {
			line = rest[:idx]
			rest = rest[idx+1:]
		}
		b.WriteString(extractor.preprocessLine(line))
		if idx < 0 {
			return b.String()
		}
	}
}
