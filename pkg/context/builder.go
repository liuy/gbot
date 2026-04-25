// Package context assembles the system prompt context for each LLM call.
//
// Source reference: context.ts, utils/claudemd.ts
package context

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Builder assembles the system prompt context.
// Source: context.ts — builds the full system prompt from components.
type Builder struct {
	// WorkingDir is the current working directory.
	WorkingDir string

	// GBOTMDContent is the loaded GBOT.md instruction content.
	GBOTMDContent string

	// GitStatus is the injected git status information.
	GitStatus *GitStatusInfo

	// ToolPrompts are system prompt contributions from tools.
	ToolPrompts []string

	// SkillListing is the formatted skill listing within context window budget.
	SkillListing string

	// MemoryFiles are loaded memory files for the system prompt.
	MemoryFiles []MemoryFile

	// MaxTokens is the token budget for the system prompt.
	MaxTokens int
}

// NewBuilder creates a new context builder.
func NewBuilder(workingDir string) *Builder {
	return &Builder{
		WorkingDir: workingDir,
		MaxTokens:  100000, // Will be dynamically calculated
	}
}

// Build assembles the full system prompt.
// Source: context.ts — the complete context assembly algorithm.
func (b *Builder) Build() (json.RawMessage, error) {
	var buf bytes.Buffer

	// 1. Base system prompt template
	buf.WriteString(b.BaseSystemPrompt())

	// 2. Platform info
	buf.WriteString(b.PlatformInfo())

	// 3. Git status
	if b.GitStatus != nil {
		buf.WriteString(b.GitStatusSection())
	}

	// 4. GBOT.md instructions
	if b.GBOTMDContent != "" {
		buf.WriteString("\n\n## Instructions\n\n")
		buf.WriteString(b.GBOTMDContent)
	}

	// 5. Memory files
	if len(b.MemoryFiles) > 0 {
		buf.WriteString(FormatMemorySection(b.MemoryFiles))
	}

	// 6. Tool prompts
	for _, prompt := range b.ToolPrompts {
		if prompt != "" {
			buf.WriteString("\n\n")
			buf.WriteString(prompt)
		}
	}

	// 7. Skill listing
	if b.SkillListing != "" {
		buf.WriteString("\n\n## Available Skills\n\n")
		buf.WriteString(b.SkillListing)
	}

	encoded, err := json.Marshal(buf.String())
	if err != nil {
		return nil, fmt.Errorf("encode system prompt: %w", err)
	}
	return json.RawMessage(encoded), nil
}
