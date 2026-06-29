package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// gbotArt is the blocky "GBOT" ASCII art (blocky font, 7 lines).
var gbotArt = strings.Join([]string{
	` ██████   ████████   ███████  ████████ `,
	`██    ██  ██     ██ ██     ██    ██    `,
	`██        ██     ██ ██     ██    ██    `,
	`██   ████ ████████  ██     ██    ██    `,
	`██    ██  ██     ██ ██     ██    ██    `,
	`██    ██  ██     ██ ██     ██    ██    `,
	` ██████   ████████   ███████     ██    `,
}, "\n")

// renderLogo returns the bold GBOT blocky ASCII art.
func renderLogo() string {
	bold := lipgloss.NewStyle().Bold(true)
	return bold.Render(gbotArt) + "\n"
}
