// Section extraction for the wui context panel accordion. The raw system
// prompt is in stub form ({{SYSTEM}}/{{SOUL}}/{{MODEL}}/{{MODALITIES}}); the
// anchors below are the exact spellings Build()/AppendVoiceBlock write, so
// locating them is a layout scan, not a heading heuristic.
package engine

import (
	"slices"
	"strings"

	"github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/memory/long"
)

// stripMemSection removes the typed-memory section's single Build()-written
// copy from a raw prompt. Shared by the base estimate and the section
// builder so the strip has one spelling of record.
func stripMemSection(raw, memPrompt string) string {
	if memPrompt == "" {
		return raw
	}
	return strings.Replace(raw, "\n\n"+memPrompt, "", 1)
}

// buildSystemPromptSections splits the memory-stripped raw prompt into its
// Build()-assembled sections and expands each to the display text the request
// path sends (engine.go callLLM, count=1 expansions). The soul span consumes
// its bytes even when no SOUL.md exists — the stub expands to "" there, so
// the literal "{{SOUL}}" must not leak into an "other" gap.
func (e *Engine) buildSystemPromptSections(
	raw, model string,
	modalities []string,
	memPrompt string,
) []PromptSection {
	stripped := stripMemSection(raw, memPrompt)
	if stripped == "" {
		return nil
	}
	if len(modalities) == 0 {
		modalities = []string{"text"}
	}

	type anchor struct {
		id    string
		start int
	}
	var anchors []anchor
	if strings.HasPrefix(stripped, "{{SYSTEM}}") {
		anchors = append(anchors, anchor{"base", 0})
	}
	if i := strings.Index(stripped, "\n\n{{SOUL}}"); i >= 0 {
		anchors = append(anchors, anchor{"soul", i})
	}
	if i := strings.Index(stripped, "\n\n# Environment\n\nRuntime: "); i >= 0 {
		anchors = append(anchors, anchor{"runtime", i})
	}
	if i := strings.Index(stripped, "\n\n## Available Skills\n\n"); i >= 0 {
		anchors = append(anchors, anchor{"skills", i})
	}
	if voiceSep := "\n\n" + context.VoicePromptBlock; strings.HasSuffix(stripped, voiceSep) {
		anchors = append(anchors, anchor{"voice", len(stripped) - len(voiceSep)})
	}
	slices.SortStableFunc(anchors, func(a, b anchor) int { return a.start - b.start })

	// Build writes each section's separator as a leading "\n\n"; stripping it
	// from the span start yields the section's own bytes. Trailing separators
	// never occur inside a span — each span ends where the next span's
	// separator begins.
	expand := func(text string) string {
		text = strings.Replace(text, "{{MODEL}}", model, 1)
		return strings.Replace(text, "{{MODALITIES}}", strings.Join(modalities, "+"), 1)
	}

	type sectionOut struct{ id, text, source string }
	var outs, others []sectionOut
	pos := 0
	for i, a := range anchors {
		// Gaps only occur before the first anchor (spans run to the next
		// anchor's start); whitespace-only gaps are not sections.
		if gap := strings.TrimSpace(stripped[pos:a.start]); gap != "" {
			others = append(others, sectionOut{"other", expand(stripped[pos:a.start]), ""})
		}
		end := len(stripped)
		if i+1 < len(anchors) {
			end = anchors[i+1].start
		}
		switch a.id {
		case "base":
			systemText, _ := context.LoadSystemFile()
			source := "system_md"
			if systemText == "" {
				systemText = context.DefaultBasePrompt()
				source = ""
			}
			outs = append(outs, sectionOut{"base", strings.Replace(stripped[a.start:end], "{{SYSTEM}}", systemText, 1), source})
		case "soul":
			// The expansion value carries its own leading "\n\n" on top of the
			// span's; both must go for the persona header to lead the display.
			// Badge mirrors the base section: SOUL.md present → file badge,
			// embedded default → none.
			soul, _ := context.LoadSoulFile()
			source := "soul_md"
			if soul == "" {
				source = ""
			}
			outs = append(outs, sectionOut{"soul", strings.TrimPrefix(context.SoulExpansion(), "\n\n"), source})
		case "runtime":
			outs = append(outs, sectionOut{"runtime", expand(strings.TrimPrefix(stripped[a.start:end], "\n\n")), ""})
		case "skills":
			outs = append(outs, sectionOut{"skills", strings.TrimPrefix(stripped[a.start:end], "\n\n"), ""})
		case "voice":
			outs = append(outs, sectionOut{"voice", context.VoicePromptBlock, ""})
		}
		pos = end
	}
	if tail := strings.TrimSpace(stripped[pos:]); tail != "" {
		others = append(others, sectionOut{"other", expand(stripped[pos:]), ""})
	}
	outs = append(outs, others...)

	sections := make([]PromptSection, 0, len(outs))
	for _, o := range outs {
		sections = append(sections, PromptSection{
			ID:     o.id,
			Text:   o.text,
			Tokens: e.EstimateTokensCalibrated(o.text),
			Source: o.source,
		})
	}
	return sections
}

// buildMemorySections splits the typed-memory prompt at the MEMORY.md
// entrypoint marker: behavioral instructions above, the index below. A
// missing marker (unreachable through BuildMemoryPrompt) degrades to a
// single instructions section so no bytes are dropped.
func (e *Engine) buildMemorySections(memPrompt string) []PromptSection {
	if memPrompt == "" {
		return nil
	}
	marker := "\n## " + long.EntrypointName + "\n\n"
	idx := strings.Index(memPrompt, marker)
	if idx < 0 {
		return []PromptSection{{
			ID:     "instructions",
			Text:   memPrompt,
			Tokens: e.EstimateTokensCalibrated(memPrompt),
		}}
	}
	instructions := memPrompt[:idx]
	index := memPrompt[idx+1:]
	return []PromptSection{
		{
			ID:     "instructions",
			Text:   instructions,
			Tokens: e.EstimateTokensCalibrated(instructions),
		},
		{
			ID:     "index",
			Text:   index,
			Tokens: e.EstimateTokensCalibrated(index),
			Source: "memory_md",
		},
	}
}
