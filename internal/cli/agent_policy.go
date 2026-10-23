package cli

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/entireio/entire-brain/internal/tui"
)

// securityToggleWarned dedups the unrecognized-value warning so a garbage env
// value cannot flood stderr (brainNoEgressMode runs on every agent/export check).
var securityToggleWarned sync.Map

// brainNoEgressMode is the master egress kill switch. ENTIRE_BRAIN_NO_EGRESS is
// the canonical name; ENTIRE_BRAIN_LOCAL_ONLY is a deprecated alias kept so
// existing CI and policy configs never fail open on upgrade (COR-2111/COR-2116).
func brainNoEgressMode() bool {
	if securityToggleEnabled("ENTIRE_BRAIN_NO_EGRESS") {
		return true
	}
	if securityToggleEnabled("ENTIRE_BRAIN_LOCAL_ONLY") {
		if _, seen := securityToggleWarned.LoadOrStore("ENTIRE_BRAIN_LOCAL_ONLY:deprecated", struct{}{}); !seen {
			tui.Warnf(os.Stderr, "ENTIRE_BRAIN_LOCAL_ONLY is a deprecated alias; set ENTIRE_BRAIN_NO_EGRESS instead")
		}
		return true
	}
	return false
}

// noEgressToggleName names the variable that enabled no-egress mode, so a
// user-facing refusal blames the variable actually set. Canonical name wins
// when both are set; it is also the fallback so callers can use this
// unconditionally in advice strings.
func noEgressToggleName() string {
	if !securityToggleEnabled("ENTIRE_BRAIN_NO_EGRESS") && securityToggleEnabled("ENTIRE_BRAIN_LOCAL_ONLY") {
		return "ENTIRE_BRAIN_LOCAL_ONLY"
	}
	return "ENTIRE_BRAIN_NO_EGRESS"
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// securityToggleEnabled reads a fail-closed security toggle. Canonical
// true/false values are honored and an unset/empty value is off (these guards are
// opt-in). Any other, unrecognized value is treated as ENABLED and a warning is
// emitted — so a typo'd "enabled"/"ture" can never silently leave a no-egress
// guard switched off, which would fail open to egress.
func securityToggleEnabled(name string) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	switch strings.ToLower(raw) {
	case "", "0", "false", "no", "off", "disable", "disabled":
		return false
	case "1", "true", "yes", "on", "enable", "enabled":
		return true
	default:
		if _, seen := securityToggleWarned.LoadOrStore(name, struct{}{}); !seen {
			tui.Warnf(os.Stderr, "%s is not a recognized boolean; treating as enabled (fail-closed)", name)
		}
		return true
	}
}

// rejectAgentForNoEgress fails closed: under no-egress mode only agents that are
// known to stay on local loopback (ollama / none) are permitted. Any other value
// — including a newly-added or unknown agent name — is denied, so the guard
// cannot be bypassed by an agent the allowlist has not yet been taught about.
func rejectAgentForNoEgress(agent string) error {
	if !brainNoEgressMode() {
		return nil
	}
	switch strings.TrimSpace(agent) {
	case "ollama", "none", "":
		return nil
	default:
		return fmt.Errorf("no_egress: --agent %s can send selected brain context outside local loopback; use --agent ollama for local loopback models, --agent none where supported, or unset ENTIRE_BRAIN_NO_EGRESS", agent)
	}
}
