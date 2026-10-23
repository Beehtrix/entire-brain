package tui

import (
	"fmt"
	"io"
)

// Warnf writes a "warning:"-prefixed line to w, painting the prefix in the
// theme's warning colour when w is a colour-capable terminal. It exists for
// code paths with no cobra command in scope (env parsing, hooks, servers)
// that would otherwise write raw unstyled lines straight to stderr. On a
// non-terminal destination the output is byte-identical to a plain
// fmt.Fprintf("warning: ...\n").
func Warnf(w io.Writer, format string, args ...any) {
	r := NewRenderer(w)
	fmt.Fprintf(w, "%s %s\n", r.Paint(r.Theme().Flag, "warning:"), fmt.Sprintf(format, args...))
}
