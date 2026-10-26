package cli

import (
	"github.com/entireio/entire-brain/internal/cli/internal/base"
)

// Redaction covers recognized credential formats and home-directory usernames.

// redactCandidate returns a copy of a task candidate with every text-bearing
// evidence field redacted, for safe display/JSON egress.
func redactCandidate(c taskCandidate) taskCandidate {
	c.Label = base.RedactText(c.Label)
	c.Commands = base.RedactStrings(c.Commands)
	c.SampleIntents = base.RedactStrings(c.SampleIntents)
	c.MatchingFacts = base.RedactStrings(c.MatchingFacts)
	procs := make([]taskProcedure, len(c.Procedures))
	for i, p := range c.Procedures {
		p.Commands = base.RedactStrings(p.Commands)
		procs[i] = p
	}
	c.Procedures = procs
	return c
}
