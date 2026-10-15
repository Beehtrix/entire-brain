package providerdetail

import (
	"strings"
	"testing"
)

// realDiskFullFailure is a verbatim component detail from a run where the disk
// filled. Two of the three such lines rendered as "...: no..." because the
// budget cut the tail, which is where a Go error keeps its cause.
const realDiskFullFailure = "stage history index: write /Users/suhaan/brain-demo-sandbox/plugin/data/repos/local/entire-graph-854644bc3816/history/staging/fd30a011c1075a840f15a2d623540962bc4ac6b6/.index.json.tmp-1814321680: no space left on device"

func TestSummaryKeepsBothTheOperationAndTheCause(t *testing.T) {
	t.Parallel()
	// Non-vacuity: the fixture must actually exceed the budget, or this
	// proves nothing about truncation.
	if len([]rune(realDiskFullFailure)) <= MaxSummary {
		t.Fatalf("fixture is %d runes, not over the %d budget", len([]rune(realDiskFullFailure)), MaxSummary)
	}
	got := Summary(realDiskFullFailure)
	if n := len([]rune(got)); n > MaxSummary {
		t.Fatalf("summary is over budget at %d runes: %q", n, got)
	}
	if !strings.Contains(got, "no space left on device") {
		t.Fatalf("the CAUSE is the only actionable part and must survive: %q", got)
	}
	if !strings.Contains(got, "stage history index") {
		t.Fatalf("the operation must survive too, or the reader cannot tell what failed: %q", got)
	}
}

func TestSummaryLeavesAShortDetailWhole(t *testing.T) {
	t.Parallel()
	short := "semantic provider is not installed"
	if got := Summary(short); got != short {
		t.Fatalf("a detail inside the budget must pass through unchanged, got %q", got)
	}
}

func TestElideIsRuneSafe(t *testing.T) {
	t.Parallel()
	// Multi-byte either side of both cuts: a byte-slice would corrupt these.
	s := strings.Repeat("Ω", 300) + ": причина"
	got := elide(s, MaxSummary)
	if n := len([]rune(got)); n > MaxSummary {
		t.Fatalf("over budget at %d runes", n)
	}
	if !strings.Contains(got, "причина") {
		t.Fatalf("the tail must survive intact: %q", got)
	}
	if strings.Contains(got, "�") {
		t.Fatalf("truncation corrupted a rune: %q", got)
	}
}
