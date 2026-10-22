package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// graphTaskContext is the response of `entire graph context`, the graph
// plugin's delegation surface for task context (COR-2217). The graph owns the
// queryable store; the brief calls this instead of maintaining its own copy of
// the snapshot. Field names mirror the provider's contextResponse contract.
type graphTaskContext struct {
	SchemaVersion string                 `json:"schema_version"`
	Commit        string                 `json:"commit"`
	Tree          string                 `json:"tree"`
	Symbols       []graphContextSymbol   `json:"symbols"`
	Relations     []graphContextRelation `json:"relations"`
	Tests         []graphContextSymbol   `json:"tests"`
	LatencyMS     int64                  `json:"latency_ms"`
	// StoreUnavailable means the provider ran but has no store for the
	// committed tree. The brief degrades to its internal semantic path (or a
	// warning), exactly as it would with the plugin absent.
	StoreUnavailable bool   `json:"store_unavailable"`
	Detail           string `json:"detail"`
}

type graphContextSymbol struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	QualifiedName string  `json:"qualified_name"`
	Kind          string  `json:"kind"`
	FilePath      string  `json:"file_path"`
	StartLine     int     `json:"start_line"`
	EndLine       int     `json:"end_line"`
	Signature     string  `json:"signature"`
	Language      string  `json:"language"`
	Score         float64 `json:"score"`
}

type graphContextRelation struct {
	FromID     string  `json:"from_id"`
	ToID       string  `json:"to_id"`
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence"`
}

// graphTaskContextTimeout bounds the delegation subprocess. The command
// answers from an indexed store in well under a second; the margin covers a
// cold binary start and a loaded machine, not a build.
const graphTaskContextTimeout = 15 * time.Second

// fetchGraphTaskContext asks the graph plugin for task context. Any error —
// binary missing, a plugin version without the `context` verb, malformed
// output — reads as "delegation unavailable" and the caller falls back; it is
// never surfaced as a brief failure.
func fetchGraphTaskContext(ctx context.Context, runner CommandRunner, repoDir, graphBinary, task string, limit, testsLimit int) (graphTaskContext, error) {
	if runner == nil {
		return graphTaskContext{}, fmt.Errorf("graph context requires a command runner")
	}
	binary := strings.TrimSpace(graphBinary)
	if binary == "" {
		binary = "entire"
	}
	runCtx, cancel := context.WithTimeout(ctx, graphTaskContextTimeout)
	defer cancel()
	stdout, stderr, err := runner.Run(runCtx, repoDir, binary,
		"graph", "context", "--repo", repoDir, "--task", task,
		"--limit", fmt.Sprint(limit), "--tests-limit", fmt.Sprint(testsLimit))
	if err != nil {
		detail := strings.TrimSpace(string(stderr))
		if detail == "" {
			detail = strings.TrimSpace(string(stdout))
		}
		if detail != "" {
			return graphTaskContext{}, fmt.Errorf("graph context: %w: %s", err, firstLine(detail))
		}
		return graphTaskContext{}, fmt.Errorf("graph context: %w", err)
	}
	var response graphTaskContext
	if err := json.Unmarshal(stdout, &response); err != nil {
		return graphTaskContext{}, fmt.Errorf("graph context returned unparseable output: %w", err)
	}
	return response, nil
}

func firstLine(s string) string {
	if index := strings.IndexByte(s, '\n'); index >= 0 {
		return s[:index]
	}
	return s
}

// graphContextSymbolRecords projects provider symbols into the brief's
// semanticRecord shape so every downstream consumer (departed-file filtering,
// caps, likely-file synthesis, rendering) works unchanged.
func graphContextSymbolRecords(symbols []graphContextSymbol) []semanticRecord {
	records := make([]semanticRecord, 0, len(symbols))
	for _, symbol := range symbols {
		records = append(records, semanticRecord{
			RecordType:    "symbol",
			ID:            symbol.ID,
			Kind:          symbol.Kind,
			Name:          symbol.Name,
			QualifiedName: symbol.QualifiedName,
			FilePath:      symbol.FilePath,
			StartLine:     symbol.StartLine,
			EndLine:       symbol.EndLine,
			Signature:     symbol.Signature,
			Language:      symbol.Language,
		})
	}
	return records
}

// graphContextRelationRecords keeps only relations whose endpoints are both
// among the returned context symbols (or external graph endpoints): that is
// the brief packet's relation contract, enforced again by the semantic cap and
// departed-file filtering, and an edge to a symbol the packet does not show is
// an opaque ID the agent cannot use. Test edges are conveyed by the tests
// section instead.
func graphContextRelationRecords(symbols []graphContextSymbol, relations []graphContextRelation) []semanticRecord {
	known := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		known[symbol.ID] = struct{}{}
	}
	endpointKnown := func(id string) bool {
		if _, ok := known[id]; ok {
			return true
		}
		return strings.HasPrefix(id, "external:")
	}
	records := make([]semanticRecord, 0, len(relations))
	for _, relation := range relations {
		if !endpointKnown(relation.FromID) || !endpointKnown(relation.ToID) {
			continue
		}
		records = append(records, semanticRecord{
			RecordType: "relation",
			FromID:     relation.FromID,
			ToID:       relation.ToID,
			Type:       relation.Type,
			Confidence: relation.Confidence,
		})
	}
	return records
}

func graphContextTestsResult(tests []graphContextSymbol) semanticTestsResult {
	suggestions := make([]semanticTestSuggestion, 0, len(tests))
	for _, record := range graphContextSymbolRecords(tests) {
		suggestions = append(suggestions, semanticTestSuggestion{Symbol: record, Reason: "task match (graph store)"})
	}
	return semanticTestsResult{Roots: []semanticRecord{}, Suggestions: suggestions}
}
