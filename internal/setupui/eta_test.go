package setupui

import (
	"strings"
	"testing"
	"time"

	"github.com/entireio/entire-brain/internal/tui"
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
func TestBarColourTracksProgress(t *testing.T) {
	t.Parallel()
	r := NewRendererWidth(tui.NewRendererWith(tui.Theme{}, tui.Caps{Color: true, Unicode: true, Width: 100}), 100)

	// Compare the COLOUR, not the whole string. An earlier version of this
	// test compared barText output directly and passed even with the hue
	// pinned to a constant, because the three bars differ by FILL regardless.
	hue := func(done, total int) string {
		out := r.barText(done, total, 20)
		i := strings.Index(out, "\x1b[")
		if i < 0 {
			return "<none>"
		}
		j := strings.Index(out[i:], "m")
		if j < 0 {
			return "<malformed>"
		}
		return out[i : i+j+1]
	}

	early, mid, late := hue(1, 10), hue(5, 10), hue(9, 10)
	if early == "<none>" {
		t.Fatal("fixture: colour is off, this test cannot measure hue")
	}
	if early == mid || mid == late || early == late {
		t.Fatalf("the bar must change HUE as it fills: early=%q mid=%q late=%q", early, mid, late)
	}

	mono := NewRendererWidth(tui.NewRendererWith(tui.Theme{}, tui.Caps{Unicode: true, Width: 100}), 100)
	if strings.Contains(mono.barText(5, 10, 20), "\x1b[") {
		t.Fatal("with colour off the bar must carry no escapes")
	}
}
