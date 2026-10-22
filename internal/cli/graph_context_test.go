package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func graphContextFixtureResponse() string {
	return `{"schema_version":"1.1","commit":"aaa111","tree":"tree111",` +
		`"symbols":[{"id":"gc-root","name":"NormalizeQueryLimit","qualified_name":"b.NormalizeQueryLimit","kind":"function","file_path":"packages/store/query.ts","start_line":1,"end_line":8,"signature":"function NormalizeQueryLimit(limit: number)","language":"TypeScript","score":12.5},` +
		`{"id":"gc-helper","name":"clampLimit","qualified_name":"b.clampLimit","kind":"function","file_path":"packages/store/query.ts","start_line":10,"end_line":14,"score":4.2}],` +
		`"relations":[{"from_id":"gc-root","to_id":"gc-helper","type":"CALLS","confidence":1},` +
		`{"from_id":"gc-test","to_id":"gc-root","type":"TESTS","confidence":1}],` +
		`"tests":[{"id":"gc-test","name":"TestNormalizeQueryLimit","qualified_name":"d.TestNormalizeQueryLimit","kind":"test","file_path":"packages/store/query.test.ts","start_line":1,"end_line":8,"score":9.1}],` +
		`"latency_ms":42}`
}

func runGraphContextBriefJSON(t *testing.T, fixture brainBriefSemanticFreshnessFixtureEnv) brainBriefJSONReport {
	t.Helper()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	err := runBrainBrief(context.Background(), cmd, fixture.opts, brainBriefOptions{
		json:        true,
		limit:       8,
		graphBinary: "entire",
	}, "normalize query limit")
	if err != nil {
		t.Fatalf("brief: %v\n%s", err, out.String())
	}
	var report brainBriefJSONReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("parse brief JSON: %v\n%s", err, out.String())
	}
	return report
}

func graphContextCommandKey(repoDir string) string {
	return fakeCommandKey("entire", "graph", "context", "--repo", repoDir,
		"--task", "normalize query limit", "--limit", "8", "--tests-limit", "8")
}

// TestBrainBriefDelegatesSemanticContextToGraphStore pins COR-2217's
// delegation contract: when `entire graph context` answers, the brief's
// semantic section is served from the graph's store (source=graph-context)
// and the internal semantic index is not consulted for context or tests.
func TestBrainBriefDelegatesSemanticContextToGraphStore(t *testing.T) {
	fixture := newBrainBriefSemanticFreshnessFixture(t)
	runner := fixture.opts.Runner.(*fakeCommandRunner)
	runner.responses[graphContextCommandKey(fixture.repoDir)] = fakeCommandResponse{stdout: graphContextFixtureResponse()}

	report := runGraphContextBriefJSON(t, fixture)
	if report.Semantic.Source != "graph-context" {
		t.Fatalf("semantic source = %q, want graph-context (warnings: %v)", report.Semantic.Source, report.Warnings)
	}
	if len(report.Semantic.Context.Symbols) != 2 || report.Semantic.Context.Symbols[0].Name != "NormalizeQueryLimit" {
		t.Fatalf("delegated symbols = %+v", report.Semantic.Context.Symbols)
	}
	// The CALLS edge joins two returned symbols and survives; the TESTS edge's
	// test endpoint is not a context symbol, so it is conveyed only by the
	// tests section (the packet's relation contract).
	if len(report.Semantic.Context.Relations) != 1 || report.Semantic.Context.Relations[0].Type != "CALLS" {
		t.Fatalf("delegated relations = %+v", report.Semantic.Context.Relations)
	}
	if len(report.Semantic.Tests.Suggestions) != 1 || report.Semantic.Tests.Suggestions[0].Symbol.Name != "TestNormalizeQueryLimit" {
		t.Fatalf("delegated test suggestions = %+v", report.Semantic.Tests.Suggestions)
	}
	// The internal index would have surfaced current-root from the fixture
	// snapshot; its absence proves the internal path was skipped.
	for _, symbol := range report.Semantic.Context.Symbols {
		if symbol.ID == "current-root" {
			t.Fatalf("internal semantic record leaked into delegated context: %+v", symbol)
		}
	}
}

// TestBrainBriefFallsBackWhenGraphStoreUnavailable pins the degradation
// contract: store_unavailable (and equally a missing or too-old plugin) must
// leave the brief exactly as it was before delegation existed — internal
// semantic section, no source marker, no new warnings.
func TestBrainBriefFallsBackWhenGraphStoreUnavailable(t *testing.T) {
	fixture := newBrainBriefSemanticFreshnessFixture(t)
	runner := fixture.opts.Runner.(*fakeCommandRunner)
	runner.responses[graphContextCommandKey(fixture.repoDir)] = fakeCommandResponse{
		stdout: `{"schema_version":"1.1","symbols":[],"relations":[],"tests":[],"store_unavailable":true,"detail":"search store not built for this commit"}`,
	}

	report := runGraphContextBriefJSON(t, fixture)
	if report.Semantic.Source != "" {
		t.Fatalf("semantic source = %q, want internal (empty)", report.Semantic.Source)
	}
	assertInternalSemanticServed(t, report)
}

// TestBrainBriefFallsBackWhenGraphStoreEmpty pins that a successful but
// empty provider response does not displace the internal answer: zero
// symbols means the store had nothing for the task, not that the task has no
// context.
func TestBrainBriefFallsBackWhenGraphStoreEmpty(t *testing.T) {
	fixture := newBrainBriefSemanticFreshnessFixture(t)
	runner := fixture.opts.Runner.(*fakeCommandRunner)
	runner.responses[graphContextCommandKey(fixture.repoDir)] = fakeCommandResponse{
		stdout: `{"schema_version":"1.1","commit":"aaa111","tree":"tree111","symbols":[],"relations":[],"tests":[],"latency_ms":3}`,
	}

	report := runGraphContextBriefJSON(t, fixture)
	if report.Semantic.Source != "" {
		t.Fatalf("semantic source = %q, want internal (empty)", report.Semantic.Source)
	}
	assertInternalSemanticServed(t, report)
}

// TestBrainBriefFallsBackWhenGraphPluginAbsent covers the user-required
// no-graph degradation: the unscripted command errors like a machine without
// the plugin, and the brief serves the internal path without complaint.
func TestBrainBriefFallsBackWhenGraphPluginAbsent(t *testing.T) {
	fixture := newBrainBriefSemanticFreshnessFixture(t)

	report := runGraphContextBriefJSON(t, fixture)
	if report.Semantic.Source != "" {
		t.Fatalf("semantic source = %q, want internal (empty)", report.Semantic.Source)
	}
	assertInternalSemanticServed(t, report)
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "graph context") {
			t.Fatalf("plugin absence must degrade silently, got warning %q", warning)
		}
	}
}

func assertInternalSemanticServed(t *testing.T, report brainBriefJSONReport) {
	t.Helper()
	found := false
	for _, symbol := range report.Semantic.Context.Symbols {
		if symbol.Name == "NormalizeQueryLimit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("internal semantic context missing expected symbol: %+v (warnings: %v)", report.Semantic.Context.Symbols, report.Warnings)
	}
}
