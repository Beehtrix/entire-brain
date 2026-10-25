package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// capabilityRunner answers the capability probe and records every argv, so a
// test can assert both the probe and whether --allow-ancestor was passed.
type capabilityRunner struct {
	capabilitiesJSON string
	capabilitiesErr  error
	calls            [][]string
}

func (r *capabilityRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if len(args) >= 2 && args[0] == "graph" && args[1] == "capabilities" {
		if r.capabilitiesErr != nil {
			return nil, nil, r.capabilitiesErr
		}
		return []byte(r.capabilitiesJSON), nil, nil
	}
	// Any context call: a minimal exact answer.
	return []byte(`{"schema_version":"1.1","commit":"c","tree":"t","symbols":[],"relations":[],"tests":[]}`), nil, nil
}

func (r *capabilityRunner) sawAllowAncestor() bool {
	for _, call := range r.calls {
		for i, a := range call {
			if a == "--allow-ancestor" && i+1 < len(call) {
				return true
			}
		}
	}
	return false
}

// The capability gate: the flag goes out only when the provider advertises it.
// Passing a flag an older provider does not know makes the subprocess fail,
// which reads as "delegation unavailable" — so a wrong answer here would turn
// every brief on an older graph into a silent fallback.
func TestAllowAncestorIsPassedOnlyWhenAdvertised(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		json string
		err  error
		want bool
	}{
		"advertised":       {json: `{"optional_local_only_features":{"context_allow_ancestor":true}}`, want: true},
		"absent key":       {json: `{"optional_local_only_features":{"durable_preindex":true}}`, want: false},
		"explicitly false": {json: `{"optional_local_only_features":{"context_allow_ancestor":false}}`, want: false},
		"no feature map":   {json: `{"schema_version":"1.4"}`, want: false},
		"malformed report": {json: `not json at all`, want: false},
		"probe fails":      {err: errors.New("unknown command"), want: false},
	} {
		testCase := testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runner := &capabilityRunner{capabilitiesJSON: testCase.json, capabilitiesErr: testCase.err}
			// A distinct binary name per case: the support probe is memoized
			// per binary across this process.
			binary := "eg-" + strings.ReplaceAll(name, " ", "-")
			if _, err := fetchGraphTaskContext(context.Background(), runner, "/repo", binary, "task", 8, 8); err != nil {
				t.Fatalf("context fetch failed: %v", err)
			}
			if got := runner.sawAllowAncestor(); got != testCase.want {
				t.Fatalf("--allow-ancestor passed = %v, want %v (argv: %v)", got, testCase.want, runner.calls)
			}
		})
	}
}

// The budget actually sent has to be the one that was reasoned about: the
// provider's retention keeps K=3 stores, so at most two ancestors exist.
func TestAllowAncestorSendsTheReasonedBudget(t *testing.T) {
	t.Parallel()
	runner := &capabilityRunner{capabilitiesJSON: `{"optional_local_only_features":{"context_allow_ancestor":true}}`}
	if _, err := fetchGraphTaskContext(context.Background(), runner, "/repo", "eg-budget", "task", 8, 8); err != nil {
		t.Fatalf("context fetch failed: %v", err)
	}
	var sent string
	for _, call := range runner.calls {
		for i, a := range call {
			if a == "--allow-ancestor" && i+1 < len(call) {
				sent = call[i+1]
			}
		}
	}
	if sent != "3" {
		t.Fatalf("--allow-ancestor %q, want 3 (the provider keeps K=3 stores, so at most two ancestors exist)", sent)
	}
	if graphBriefAncestorBudget < 1 || graphBriefAncestorBudget > 5 {
		t.Fatalf("budget %d is outside the provider's accepted 0-5 range", graphBriefAncestorBudget)
	}
}

// The probe is memoized per binary, so a brief plus a status in one process
// does not pay for it twice — and the memo must not answer for a different
// binary.
func TestAncestorCapabilityIsProbedOncePerBinary(t *testing.T) {
	t.Parallel()
	runner := &capabilityRunner{capabilitiesJSON: `{"optional_local_only_features":{"context_allow_ancestor":true}}`}
	for i := 0; i < 3; i++ {
		if _, err := fetchGraphTaskContext(context.Background(), runner, "/repo", "eg-memo", "task", 8, 8); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}
	probes := 0
	for _, call := range runner.calls {
		if len(call) >= 3 && call[1] == "graph" && call[2] == "capabilities" {
			probes++
		}
	}
	if probes != 1 {
		t.Fatalf("capability probed %d times across 3 fetches, want 1", probes)
	}

	// A different binary must be probed on its own account.
	other := &capabilityRunner{capabilitiesJSON: `{"optional_local_only_features":{}}`}
	if _, err := fetchGraphTaskContext(context.Background(), other, "/repo", "eg-memo-other", "task", 8, 8); err != nil {
		t.Fatal(err)
	}
	if other.sawAllowAncestor() {
		t.Fatal("a second binary inherited the first binary's capability from the memo")
	}
}

// GOLDEN: the real feature map emitted by the provider build that added the
// key (graph #380 at edbdacb6). Captured from `<provider> capabilities --json`
// on a live run, trimmed to the map this code reads. As with the stale block,
// my own fixtures only prove my parsing; this proves the contract.
func TestRealProviderCapabilityReportAdvertisesAncestorSupport(t *testing.T) {
	t.Parallel()
	const live = `{"optional_local_only_features":{"compact_snapshot_ndjson_v1":true,` +
		`"context_allow_ancestor":true,"durable_preindex":true,"focused_neighbors":true,` +
		`"git_cochange_edges":true,"hybrid_source_search":true,"ndjson_snapshot":true,` +
		`"near_clone_detection":true,"scip_snapshot_experimental":true,"semantic_diff":true,` +
		`"stable_symbol_ids":true}}`

	runner := &capabilityRunner{capabilitiesJSON: live}
	if _, err := fetchGraphTaskContext(context.Background(), runner, "/repo", "eg-real-caps", "task", 8, 8); err != nil {
		t.Fatalf("context fetch failed: %v", err)
	}
	if !runner.sawAllowAncestor() {
		t.Fatalf("the provider's real capability report did not enable the flag; the key moved or the parse is wrong (argv: %v)", runner.calls)
	}

	// And the shape BEFORE the key existed must read as unsupported, which is
	// how an older provider degrades without any version arithmetic.
	const before = `{"optional_local_only_features":{"compact_snapshot_ndjson_v1":true,` +
		`"durable_preindex":true,"focused_neighbors":true,"git_cochange_edges":true,` +
		`"hybrid_source_search":true,"ndjson_snapshot":true,"near_clone_detection":true,` +
		`"scip_snapshot_experimental":true,"semantic_diff":true,"stable_symbol_ids":true}}`
	older := &capabilityRunner{capabilitiesJSON: before}
	if _, err := fetchGraphTaskContext(context.Background(), older, "/repo", "eg-real-caps-older", "task", 8, 8); err != nil {
		t.Fatal(err)
	}
	if older.sawAllowAncestor() {
		t.Fatal("a provider report without the key enabled the flag, which would make every brief on an older graph fall back")
	}
}

// The invocation form this probe assumes, stated as a test so it is not folded
// away. Brain addresses the provider through the HOST verb (`<binary> graph
// capabilities`), the same form as its existing doctor and version probes. A
// bare plugin binary answers `capabilities` directly and rejects the `graph`
// prefix with `unknown command "graph"` — so passing one to --graph-binary
// makes this probe fail, which it must read as unsupported rather than as an
// error.
func TestAncestorProbeUsesTheHostVerbAndFailsClosedOnAPluginBinary(t *testing.T) {
	t.Parallel()
	runner := &capabilityRunner{capabilitiesErr: errors.New(`unknown command "graph"; run "entire graph help" to list commands`)}
	if _, err := fetchGraphTaskContext(context.Background(), runner, "/repo", "eg-plugin-form", "task", 8, 8); err != nil {
		t.Fatalf("a provider that rejects the host verb must not fail the fetch: %v", err)
	}
	if runner.sawAllowAncestor() {
		t.Fatal("the flag was sent although the capability probe was rejected")
	}
	var probed bool
	for _, call := range runner.calls {
		if len(call) >= 3 && call[1] == "graph" && call[2] == "capabilities" {
			probed = true
		}
	}
	if !probed {
		t.Fatalf("the probe did not use the host verb `<binary> graph capabilities`: %v", runner.calls)
	}
}
