package cli

import (
	"strings"
	"testing"
)

// Fact-anchor verification left status because it walks git history per anchor
// commit (24s to 2.4s on a 961-fact brain). Moving it is right; letting the
// line vanish without a word is not. A reader who saw "verification: ..."
// yesterday and sees nothing today has been told that verification passed,
// which is the exact silent-degradation class this change set exists to
// remove.
//
// This test used to require the line to say "--details", because that was
// where verification went first. main settled it elsewhere -- `verify` and
// `doctor` -- and TestBrainStatusDetailsDoesNotVerifyFacts now pins that
// --details runs no walk at all. The hint went on saying --details anyway,
// which is worse than the missing line it replaced: it costs the reader a run
// to disprove and leaves them with no answer. Flipped to guard the decision
// that was actually made.
func TestStatusSaysWhereVerificationWentWhenItIsNotRun(t *testing.T) {
	t.Parallel()

	report := statusFixtureReport(t, 44)
	report.Facts = &brainStatusFacts{Facts: 12, Verification: nil}

	got := renderStatus(t, report, true)
	if !strings.Contains(got, "verification:") {
		t.Fatalf("status with facts and no verification must still name verification; got:\n%s", got)
	}
	if !strings.Contains(got, "verify") {
		t.Fatalf("the line must point at a command that actually verifies; got:\n%s", got)
	}
	if strings.Contains(got, "--details") {
		t.Fatalf("--details runs no walk; pointing at it sends the reader nowhere; got:\n%s", got)
	}
}

// TestTheVerificationHintNamesSomethingThatVerifies ties the sentence to the
// behaviour instead of to a spelling.
//
// Asserting "the hint does not say --details" is a string check that goes
// stale the moment the decision moves again -- which is exactly how the hint
// came to be wrong. This measures the flag first: if --details ever starts
// running the walk again, the first assertion fails and the hint is free to
// name it. While it does not, the hint must not.
func TestTheVerificationHintNamesSomethingThatVerifies(t *testing.T) {
	t.Parallel()
	opts, _, runner := statusVerificationFixture(t)

	if _, err := execute(t, NewRootCommand(opts), "status", "--json", "--details"); err != nil {
		t.Fatalf("status --json --details: %v", err)
	}
	detailsVerifies := reachabilityWalks(runner) > 0

	report := statusFixtureReport(t, 0)
	report.Facts = &brainStatusFacts{Facts: 12}
	hint := renderStatus(t, report, true)

	if detailsVerifies {
		if !strings.Contains(hint, "--details") {
			t.Fatalf("--details runs the walk again, so the hint should name it; got:\n%s", hint)
		}
		return
	}
	if strings.Contains(hint, "--details") {
		t.Fatalf("--details ran no reachability walk, so the hint must not name it; got:\n%s", hint)
	}
	if !strings.Contains(hint, "verify") && !strings.Contains(hint, "doctor") {
		t.Fatalf("the hint names no command that verifies; got:\n%s", hint)
	}
}

// With no facts there is nothing to verify, so the hint would be noise. A
// notice everyone learns to ignore is worse than no notice.
func TestStatusStaysQuietAboutVerificationWithNoFacts(t *testing.T) {
	t.Parallel()

	report := statusFixtureReport(t, 44)
	report.Facts = &brainStatusFacts{Facts: 0}

	if got := renderStatus(t, report, true); strings.Contains(got, "verification:") {
		t.Fatalf("a brain with no facts must not advertise verification; got:\n%s", got)
	}
}
