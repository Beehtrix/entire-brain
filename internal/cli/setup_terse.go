package cli

import (
	"github.com/entireio/entire-brain/internal/setupui"
)

// setup_terse.go maps the setup report onto the short form.
//
// It is the only place that decides which short-form state a run landed in,
// so the states stay mutually exclusive and testable without a brain.

// terseCapabilityLabels turns internal component ids into words a person
// recognises. A component missing from this map is deliberately NOT reported
// in the short form: an id like "entities" tells a reader nothing they can act
// on, and `doctor` already lists every component by name.
var terseCapabilityLabels = map[string]string{
	brainComponentSemantic: "semantic search",
	brainComponentHistory:  "history search",
	brainComponentDocs:     "docs search",
}

// terseCapabilityRemedies maps a missing capability to the one command that
// restores it, when a single command does.
var terseCapabilityRemedies = map[string]string{
	brainComponentSemantic: "run `entire plugin install graph`",
}

func terseReportFor(report setupReport, timings *setupTimings, queryable bool, opts setupCommandOptions, brainCmd string) setupui.TerseReport {
	out := setupui.TerseReport{
		Queryable: queryable,
		Sessions:  report.Facts.Sessions,
		Distilled: report.Facts.Distilled,
		BrainCmd:  setupBrainCommand(brainCmd),
	}
	if timings != nil {
		out.Elapsed = timings.total()
	}

	_, failed := partitionSetupComponents(report.Instant.Components)
	for _, component := range failed {
		if label, named := terseCapabilityLabels[component.Name]; named {
			out.Unavailable = append(out.Unavailable, label)
			if remedy, ok := terseCapabilityRemedies[component.Name]; ok && out.Remedy == "" {
				out.Remedy = remedy
			}
		}
	}

	out.Distill = terseDistillOutcome(report)
	if out.Distill == setupui.DistillFailed {
		out.DistillReason = setupui.FlattenText(report.Backfill.Detail)
	}
	out.Watcher, out.WatcherReason = terseWatcherOutcome(report, opts)

	// A re-run that changed nothing collapses to one line. "Nothing to do"
	// means: usable, nothing left to distill, watcher up. Anything else still
	// owes the reader an explanation.
	out.UpToDate = queryable &&
		len(out.Unavailable) == 0 &&
		report.Facts.Sessions > 0 &&
		report.Facts.Pending() == 0 &&
		out.Watcher == setupui.WatcherAlreadyRunning
	return out
}

// terseDistillOutcome reads the FACTS PHASE, not the fact counts.
//
// It used to branch on report.Facts.Running and report.Facts.Agent. Neither is
// ever set: factsBackfillStatusForBrain fills only Sessions and Distilled from
// the manifest and the distill cache. So "started" and "declined" were
// unreachable and every run with pending sessions reported "no agent CLI to
// distill them" -- telling people to install a tool they had just watched the
// same command use. The phase result is the only thing that knows what
// actually happened.
func terseDistillOutcome(report setupReport) setupui.DistillOutcome {
	switch report.Backfill.State {
	case "ok":
		return setupui.DistillStarted
	case "failed":
		// Attempted and broke. Reporting this as "no agent CLI" would send
		// the reader to install a tool they already have.
		return setupui.DistillFailed
	}

	// Skipped. The typed consent reason says why; matching English against
	// Detail would break the first time a sentence is reworded.
	switch report.Backfill.consentWhy {
	case setupui.ConsentReasonNoSessions:
		return setupui.DistillNoSessions
	case setupui.ConsentReasonNoAgent:
		return setupui.DistillNoAgent
	case setupui.ConsentReasonNoBackfill:
		return setupui.DistillSkippedFlag
	case setupui.ConsentReasonDeclined, setupui.ConsentReasonNoTTY:
		return setupui.DistillDeclined
	}

	// Skipped before consent ran at all: paused, already distilled, a
	// backfill already in flight, or an unresolvable executable. Counts
	// decide, because no consent decision exists to read.
	switch {
	case report.Facts.Sessions == 0:
		return setupui.DistillNoSessions
	case report.Facts.Pending() == 0:
		return setupui.DistillAlreadyDone
	default:
		return setupui.DistillDeclined
	}
}

func terseWatcherOutcome(report setupReport, opts setupCommandOptions) (setupui.WatcherOutcome, string) {
	switch {
	case opts.noDaemon:
		return setupui.WatcherSkippedFlag, ""
	case report.Daemon.Installed && report.Daemon.Running:
		return setupui.WatcherAlreadyRunning, ""
	case report.Daemon.Installed:
		return setupui.WatcherInstalled, ""
	default:
		return setupui.WatcherUnavailable, setupui.FlattenText(report.Daemon.Detail)
	}
}
