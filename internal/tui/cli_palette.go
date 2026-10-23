package tui

import "github.com/charmbracelet/lipgloss"

// CLI colors match github.com/entireio/cli/cmd/entire/cli/palette, also used
// by Graph's impact progress. Base16 slots respect the user's terminal theme.
// Body text stays unstyled so it works on both light and dark backgrounds.
// The "default" Theme in styles.go carries the same palette; keep the two in
// sync when re-copying from the CLI repo.
const (
	CLIAccent  lipgloss.Color = "5"
	CLIMuted   lipgloss.Color = "8"
	CLISuccess lipgloss.Color = "2"
	CLIError   lipgloss.Color = "1"
)
