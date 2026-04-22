package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/llm"

	tea "github.com/charmbracelet/bubbletea"
)

// ModelItem represents a single provider/tier/model entry in the picker.
type ModelItem struct {
	Provider string
	Tier     config.Tier
	Model    string
	Current  bool
}

// ModelPicker is a Bubble Tea model for selecting a provider/tier combination.
type ModelPicker struct {
	items    []ModelItem
	cursor   int
	selected *ModelItem
	aborted  bool
}

// NewModelPicker creates a model picker with all available provider/tier combos.
func NewModelPicker(providers map[string]llm.Provider, providerConfigs map[string]*config.Provider, currentProvider string, currentTier config.Tier) *ModelPicker {
	var items []ModelItem

	// Deterministic order: sort provider names
	names := make([]string, 0, len(providerConfigs))
	for n := range providerConfigs {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		cfg := providerConfigs[name]
		if _, ok := providers[name]; !ok {
			continue
		}
		for _, tier := range []config.Tier{config.TierLite, config.TierPro, config.TierMax} {
			if model := cfg.Models[tier]; model != "" {
				items = append(items, ModelItem{
					Provider: name,
					Tier:     tier,
					Model:    model,
					Current:  name == currentProvider && tier == currentTier,
				})
			}
		}
	}

	p := &ModelPicker{items: items}

	// Set cursor to current item
	for i, item := range items {
		if item.Current {
			p.cursor = i
			break
		}
	}

	return p
}

// Init implements tea.Model.
func (p *ModelPicker) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (p *ModelPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyUp:
			if len(p.items) == 0 {
				return p, nil
			}
			p.cursor--
			if p.cursor < 0 {
				p.cursor = len(p.items) - 1
			}
		case tea.KeyDown:
			if len(p.items) == 0 {
				return p, nil
			}
			p.cursor++
			if p.cursor >= len(p.items) {
				p.cursor = 0
			}
		case tea.KeyEnter:
			if len(p.items) == 0 {
				p.aborted = true
				return p, tea.Quit
			}
			item := p.items[p.cursor]
			p.selected = &item
			return p, tea.Quit
		case tea.KeyEsc:
			p.aborted = true
			return p, tea.Quit
		}
	}
	return p, nil
}

// View implements tea.Model.
func (p *ModelPicker) View() string {
	if len(p.items) == 0 {
		return "\n  No models available\n\n  Press Esc to cancel\n"
	}

	var b strings.Builder
	b.WriteString("\n  Select model:\n\n")

	for i, item := range p.items {
		cursor := " "
		if i == p.cursor {
			cursor = "▸"
		}

		current := ""
		if item.Current {
			current = " *"
		}

		fmt.Fprintf(&b, "  %s %s / %-4s %s%s\n",
			cursor, item.Provider, item.Tier, item.Model, current)
	}

	b.WriteString("\n  ↑/↓ navigate · Enter select · Esc cancel\n")
	return b.String()
}
