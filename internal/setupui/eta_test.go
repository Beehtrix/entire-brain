package setupui

import (
	"strings"
	"testing"
	"time"
)

// TestETAAppearsOnlyWhenItIsAnEstimate guards both directions: a rate needs at
// least two completions, and a bar that CAN say how much longer should.
func TestETAAppearsOnlyWhenItIsAnEstimate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		done, total  int
		elapsed      time.Duration
		wantEstimate bool
	}{
		{"no progress yet", 0, 8, 2 * time.Second, false},
		{"one completion is not a rate", 1, 8, 2 * time.Second, false},
		{"two is the earliest honest estimate", 2, 8, 2 * time.Second, true},
		{"mid run", 3, 8, 3 * time.Second, true},
		{"finished", 8, 8, 8 * time.Second, false},
		{"no clock", 3, 8, 0, false},
		{"indeterminate total", 3, 0, 3 * time.Second, false},
	} {
		got := etaText(tc.done, tc.total, tc.elapsed)
		if tc.wantEstimate && got == "" {
			t.Errorf("%s: wanted an estimate, got none", tc.name)
		}
		if !tc.wantEstimate && got != "" {
			t.Errorf("%s: wanted no estimate, got %q", tc.name, got)
		}
	}
}

func TestETAScalesItsUnit(t *testing.T) {
	t.Parallel()
	// 2 done in 4s => 2s each; 6 remaining => ~12s
	if got := etaText(2, 8, 4*time.Second); !strings.Contains(got, "s left") {
		t.Fatalf("seconds-scale estimate expected, got %q", got)
	}
	// 2 done in 400s => 200s each; 6 remaining => ~20m
	if got := etaText(2, 8, 400*time.Second); !strings.Contains(got, "m left") {
		t.Fatalf("minutes-scale estimate expected, got %q", got)
	}
	// 2 done in 4000s => 6 remaining => ~3h
	if got := etaText(2, 8, 4000*time.Second); !strings.Contains(got, "h") {
		t.Fatalf("hours-scale estimate expected, got %q", got)
	}
}

// TestBarColourTracksProgress pins that the bar changes hue as it fills, and
// that the change is redundant rather than load-bearing: with colour off the
// bar is still a bar.
// The bar's colour-by-fill lives in tui.Renderer.Bar (rampColor), not here.
// A guard in this package could only have tested a wrapper, which is exactly
// how an ineffective one passed review once already.
