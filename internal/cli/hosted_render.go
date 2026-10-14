package cli

import (
	"fmt"
	"io"

	"github.com/entireio/entire-brain/internal/tui"
)

// renderHostedBanner is the one shape every hosted connect/disconnect message
// uses: a blank line, the done mark with a bold headline, then each paragraph
// dimmed and blank-line separated. Everything is left-aligned — no indentation.
func renderHostedBanner(out io.Writer, headline string, paragraphs ...string) {
	render := tui.NewRenderer(out)
	fmt.Fprintf(out, "\n%s %s\n", render.Mark(tui.MarkDone), render.Bold(headline))
	for _, paragraph := range paragraphs {
		fmt.Fprintf(out, "\n%s\n", render.Dim(paragraph))
	}
}
