package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/types"
)

// -----------------------------------------------------------------------

const (
	largeToolResultPercent = 15
	largeToolResultTokens  = 10_000
	readBloatPercent       = 5
	nearCapacityPercent    = 80
	memoryHighPercent      = 5
	memoryHighTokens       = 5_000
)

type contextSuggestion struct {
	Severity string // "info" or "warning"
	Title    string
	Detail   string
}

// -----------------------------------------------------------------------
// /context handler
// -----------------------------------------------------------------------

func (a *App) handleContext(commitCmd tea.Cmd) tea.Cmd {
	if a.repl.IsStreaming() {
		return a.showInfo("Cannot show context while streaming")
	}
	bd := a.engine.ContextBreakdown()
	if bd == nil || bd.TotalTokens == 0 {
		return a.showInfo("Send a message first to see context usage.")
	}
	a.infoOverlay = renderContextView(bd, a.width)
	a.infoOverlayScroll = 0
	return commitCmd
}

// renderContextView builds the full /context rendering as plain text with
// lipgloss ANSI styling. Layout:
//
//	Context Usage
//	─────
//	[grid 10×10]   model-name
//	[grid ...]     XX.Xk / XXXk tokens (XX.X%)
//	[grid ...]     (empty)
//	               Estimated usage by category
//	               ⛁ System prompt: XX.Xk tokens (XX.X%)
//	               ⛁ ...
//
//	MCP tools
//	  Loaded
//	    └ name: XX tokens
//	  Available
//	    └ name
//
//	Custom agents
//	  └ name: XX tokens
//
//	Memory files
//	  └ path: XX tokens
//
//	Skills
//	  └ name: XX tokens
//
//	Message breakdown
//	  Tool calls: XX.Xk
//	  ...
func renderContextView(bd *engine.ContextBreakdown, width int) string {
	var sb strings.Builder

	// Header.
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	sb.WriteString(titleStyle.Render("Context Usage"))
	sb.WriteString("\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(
		strings.Repeat("─", min(width-1, 60))))
	sb.WriteString("\n")

	// Grid + legend side by side.
	gridLines := renderGrid(bd)
	legendLines := renderLegend(bd)
	sb.WriteString(joinGridLegend(gridLines, legendLines))
	sb.WriteString("\n")

	// Detail sections.
	if section := renderMCPSection(bd); section != "" {
		sb.WriteString("\n")
		sb.WriteString(section)
	}
	if section := renderAgentsSection(bd); section != "" {
		sb.WriteString("\n")
		sb.WriteString(section)
	}
	if section := renderMemorySection(bd); section != "" {
		sb.WriteString("\n")
		sb.WriteString(section)
	}
	if section := renderSkillsSection(bd); section != "" {
		sb.WriteString("\n")
		sb.WriteString(section)
	}
	if section := renderMessageBreakdownSection(bd); section != "" {
		sb.WriteString("\n")
		sb.WriteString(section)
	}
	if section := renderSuggestionsSection(bd); section != "" {
		sb.WriteString("\n")
		sb.WriteString(section)
	}
	return sb.String()
}

func renderGrid(bd *engine.ContextBreakdown) []string {
	if len(bd.GridRows) == 0 {
		return nil
	}
	lines := make([]string, 0, len(bd.GridRows))
	for _, row := range bd.GridRows {
		var rowSb strings.Builder
		for _, sq := range row {
			sym := engine.SymFreeSpace
			if sq.IsFilled {
				sym = engine.SymFilledFull
			} else if sq.CategoryName == "Autocompact buffer" {
				sym = engine.SymReserved
			} else if sq.SquareFullness > 0 {
				sym = engine.SymFilledPart
			}
			style := lipgloss.NewStyle().Foreground(lipgloss.Color(sq.Color))
			rowSb.WriteString(style.Render(sym))
		}
		lines = append(lines, rowSb.String())
	}
	return lines
}

func renderLegend(bd *engine.ContextBreakdown) []string {
	used := types.FormatTokenCount(bd.TotalTokens)
	window := types.FormatTokenCount(bd.ContextWindow)
	pct := bd.Percentage

	modelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("255"))
	headerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	categoryStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("255"))
	freeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	lines := []string{
		modelStyle.Render(bd.Model),
		fmt.Sprintf("%s / %s tokens (%.1f%%)", used, window, pct),
		"",
		headerStyle.Render("Estimated usage by category"),
	}

	for _, c := range bd.Categories {
		var prefix string
		var style lipgloss.Style
		switch {
		case c.IsFree:
			prefix = engine.SymFreeSpace + " "
			style = freeStyle
		case c.IsReserved:
			prefix = engine.SymReserved + " "
			style = lipgloss.NewStyle().Foreground(lipgloss.Color(c.Color))
		default:
			sym := engine.SymFilledPart
			if c.Percentage >= 70 {
				sym = engine.SymFilledFull
			}
			prefix = sym + " "
			style = categoryStyle
		}
		// Apply the category's own color when non-free/reserved.
		if !c.IsFree {
			style = lipgloss.NewStyle().Foreground(lipgloss.Color(c.Color))
		}
		lines = append(lines, style.Render(fmt.Sprintf(
			"%s%s: %s tokens (%.1f%%)",
			prefix,
			c.Name,
			types.FormatTokenCount(c.Tokens),
			c.Percentage,
		)))
	}
	return lines
}

func joinGridLegend(gridLines, legendLines []string) string {
	gap := "  "
	n := max(len(legendLines), len(gridLines))
	var sb strings.Builder
	for i := range n {
		var g, l string
		if i < len(gridLines) {
			g = gridLines[i]
		}
		if i < len(legendLines) {
			l = legendLines[i]
		}
		sb.WriteString(g)
		sb.WriteString(gap)
		sb.WriteString(l)
		if i < n-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// -----------------------------------------------------------------------
// Detail section renderers
// -----------------------------------------------------------------------

func sectionHeader(title string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Render(title)
}

func renderMCPSection(bd *engine.ContextBreakdown) string {
	if len(bd.MCPToolsLoaded) == 0 && len(bd.MCPToolsDeferred) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(sectionHeader("MCP tools"))
	sb.WriteString("\n")
	if len(bd.MCPToolsLoaded) > 0 {
		sb.WriteString("  " + lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Render("Loaded"))
		sb.WriteString("\n")
		for _, t := range bd.MCPToolsLoaded {
			sb.WriteString("    └ ")
			sb.WriteString(t.Name)
			sb.WriteString(": ")
			sb.WriteString(types.FormatTokenCount(t.Tokens))
			sb.WriteString(" tokens\n")
		}
	}
	if len(bd.MCPToolsDeferred) > 0 {
		sb.WriteString("  " + lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Render("Available"))
		sb.WriteString("\n")
		for _, t := range bd.MCPToolsDeferred {
			sb.WriteString("    └ ")
			sb.WriteString(t.Name)
			sb.WriteString("\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func renderAgentsSection(bd *engine.ContextBreakdown) string {
	if len(bd.Agents) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(sectionHeader("Custom agents"))
	sb.WriteString("\n")
	for _, a := range bd.Agents {
		sb.WriteString("  └ ")
		sb.WriteString(a.AgentType)
		sb.WriteString(": ")
		sb.WriteString(types.FormatTokenCount(a.Tokens))
		sb.WriteString(" tokens\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func renderMemorySection(bd *engine.ContextBreakdown) string {
	if len(bd.MemoryFiles) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(sectionHeader("Memory files"))
	sb.WriteString("\n")
	for _, f := range bd.MemoryFiles {
		sb.WriteString("  └ ")
		sb.WriteString(shortenPath(f.Path))
		sb.WriteString(": ")
		sb.WriteString(types.FormatTokenCount(f.Tokens))
		sb.WriteString(" tokens\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func renderSkillsSection(bd *engine.ContextBreakdown) string {
	if len(bd.Skills) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(sectionHeader("Skills"))
	sb.WriteString("\n")
	for _, s := range bd.Skills {
		sb.WriteString("  └ ")
		sb.WriteString(s.Name)
		if s.Tokens > 0 {
			sb.WriteString(": ")
			sb.WriteString(types.FormatTokenCount(s.Tokens))
			sb.WriteString(" tokens")
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func renderMessageBreakdownSection(bd *engine.ContextBreakdown) string {
	mb := bd.MessageBreakdown
	if mb == nil {
		return ""
	}
	if mb.ToolCallTokens == 0 && mb.ToolResultTokens == 0 &&
		mb.AssistantTextTokens == 0 && mb.UserTextTokens == 0 &&
		mb.AttachmentTokens == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(sectionHeader("Message breakdown"))
	sb.WriteString("\n")
	if mb.ToolCallTokens > 0 {
		sb.WriteString("  Tool calls: ")
		sb.WriteString(types.FormatTokenCount(mb.ToolCallTokens))
		sb.WriteString("\n")
	}
	if mb.ToolResultTokens > 0 {
		sb.WriteString("  Tool results: ")
		sb.WriteString(types.FormatTokenCount(mb.ToolResultTokens))
		sb.WriteString("\n")
	}
	if mb.AttachmentTokens > 0 {
		sb.WriteString("  Attachments: ")
		sb.WriteString(types.FormatTokenCount(mb.AttachmentTokens))
		sb.WriteString("\n")
	}
	if mb.AssistantTextTokens > 0 {
		sb.WriteString("  Assistant text: ")
		sb.WriteString(types.FormatTokenCount(mb.AssistantTextTokens))
		sb.WriteString("\n")
	}
	if mb.UserTextTokens > 0 {
		sb.WriteString("  User text: ")
		sb.WriteString(types.FormatTokenCount(mb.UserTextTokens))
		sb.WriteString("\n")
	}
	if len(mb.ToolCallsByType) > 0 {
		sb.WriteString("  Top tools:\n")
		for _, t := range mb.ToolCallsByType {
			sb.WriteString("    └ ")
			sb.WriteString(t.Name)
			sb.WriteString(": calls ")
			sb.WriteString(types.FormatTokenCount(t.CallTokens))
			if t.ResultTokens > 0 {
				sb.WriteString(", results ")
				sb.WriteString(types.FormatTokenCount(t.ResultTokens))
			}
			sb.WriteString("\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// -----------------------------------------------------------------------
// Suggestions — source: contextSuggestions.ts
// -----------------------------------------------------------------------

func generateSuggestions(bd *engine.ContextBreakdown) []contextSuggestion {
	var out []contextSuggestion

	pctFull := bd.Percentage
	if pctFull >= nearCapacityPercent {
		detail := fmt.Sprintf("%.1f%% of %.0fk tokens used — consider /compact to free space.",
			pctFull, float64(bd.ContextWindow)/1000.0)
		out = append(out, contextSuggestion{
			Severity: "warning",
			Title:    fmt.Sprintf("Context is %.0f%% full", pctFull),
			Detail:   detail,
		})
	}

	if mb := bd.MessageBreakdown; mb != nil {
		// Tool result bloat.
		toolResultPct := 0.0
		if bd.TotalTokens > 0 {
			toolResultPct = float64(mb.ToolResultTokens) * 100.0 / float64(bd.TotalTokens)
		}
		if toolResultPct >= largeToolResultPercent || mb.ToolResultTokens >= largeToolResultTokens {
			out = append(out, contextSuggestion{
				Severity: "info",
				Title:    "Tool results dominate",
				Detail: fmt.Sprintf(
					"Tool results: %s (%.1f%%). Older tool results will be auto-cleared at high context.",
					types.FormatTokenCount(mb.ToolResultTokens), toolResultPct),
			})
		}

		// Read tool bloat.
		for _, t := range mb.ToolCallsByType {
			if (t.Name == "Read" || t.Name == "file_read") && t.ResultTokens > 0 {
				readPct := float64(t.ResultTokens) * 100.0 / float64(bd.TotalTokens)
				if readPct >= readBloatPercent {
					out = append(out, contextSuggestion{
						Severity: "info",
						Title:    "Read tool results accumulate",
						Detail: fmt.Sprintf(
							"Read tool: %s (%.1f%%). Consider summarizing file contents.",
							types.FormatTokenCount(t.ResultTokens), readPct),
					})
				}
			}
		}
	}

	// Memory file bloat.
	if len(bd.MemoryFiles) > 0 {
		memTokens := 0
		for _, f := range bd.MemoryFiles {
			memTokens += f.Tokens
		}
		memPct := 0.0
		if bd.TotalTokens > 0 {
			memPct = float64(memTokens) * 100.0 / float64(bd.TotalTokens)
		}
		if memPct >= memoryHighPercent || memTokens >= memoryHighTokens {
			out = append(out, contextSuggestion{
				Severity: "info",
				Title:    "Memory files are large",
				Detail: fmt.Sprintf(
					"Memory files: %s (%.1f%%). Consider trimming.",
					types.FormatTokenCount(memTokens), memPct),
			})
		}
	}

	if !bd.IsAutoCompact && bd.ContextWindow > 0 {
		out = append(out, contextSuggestion{
			Severity: "info",
			Title:    "Auto-compact disabled",
			Detail:   "Auto-compact is disabled; manual /compact required at capacity.",
		})
	}

	// Sort: warnings first, then by token impact.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == "warning"
		}
		return false
	})
	return out
}

func renderSuggestionsSection(bd *engine.ContextBreakdown) string {
	suggs := generateSuggestions(bd)
	if len(suggs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(sectionHeader("Suggestions"))
	sb.WriteString("\n")
	for _, s := range suggs {
		icon := engine.SymFilledPart
		iconColor := "220"
		if s.Severity == "warning" {
			icon = "!"
			iconColor = "196"
		}
		styled := lipgloss.NewStyle().Foreground(lipgloss.Color(iconColor)).Render(icon)
		sb.WriteString("  ")
		sb.WriteString(styled)
		sb.WriteString(" ")
		sb.WriteString(lipgloss.NewStyle().Bold(true).Render(s.Title))
		sb.WriteString("\n    ")
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Render(s.Detail))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// -----------------------------------------------------------------------
// Utilities
// -----------------------------------------------------------------------

func shortenPath(p string) string {
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		if idx2 := strings.LastIndex(p[:idx], "/"); idx2 >= 0 {
			return "..." + p[idx2:]
		}
		return p[idx+1:]
	}
	return p
}
