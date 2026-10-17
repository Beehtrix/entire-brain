package cli

import (
	"strings"

	"github.com/entireio/entire-brain/internal/setupui"
)

// setup_terse.go maps the setup report onto the short form.
//
// It is the only place that decides which of the short form's states a run
// landed in, so the states stay mutually exclusive and testable without
// standing up a brain.

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
		Agent:     report.Facts.Agent,
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

	out.Distill = terseDistillOutcome(report, opts)
	if out.Distill == setupui.DistillFailed {
		out.DistillReason = setupui.FlattenText(report.Backfill.Detail)
	}
	out.Watcher, out.WatcherReason = terseWatcherOutcome(report, opts)

	// A re-run that changed nothing collapses to one line. "Nothing to do"
	// means: the brain is usable, every session has been distilled, and the
	// watcher is up. Anything else still owes the reader an explanation.
	out.UpToDate = queryable &&
		len(out.Unavailable) == 0 &&
		report.Facts.Sessions > 0 &&
		report.Facts.Pending() == 0 &&
		!report.Facts.Running &&
		out.Watcher == setupui.WatcherAlreadyRunning
	return out
}

func terseDistillOutcome(report setupReport, opts setupCommandOptions) setupui.DistillOutcome {
	switch {
	case report.Facts.Sessions == 0:
		return setupui.DistillNoSessions
	case report.Backfill.State == "failed":
		// Attempted and broke. Reporting this as "no agent CLI" below would
		// send the reader to install a tool they already have.
		return setupui.DistillFailed
	case report.Facts.Running:
		return setupui.DistillStarted
	case report.Facts.Pending() == 0:
		return setupui.DistillAlreadyDone
	case opts.noBackfill:
		return setupui.DistillSkippedFlag
	case strings.TrimSpace(report.Facts.Agent) == "":
		// Sessions exist and nothing can read them. Named as a missing tool,
		// because the sessions are fine and nothing is lost by fixing it later.
		return setupui.DistillNoAgent
	default:
		// Sessions remain, an agent exists, and the backfill is not running:
		// the prompt was declined, or declined for us by a non-interactive
		// stdin. Either way the reader needs the same sentence.
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
