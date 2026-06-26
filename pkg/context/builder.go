// Package context assembles the system prompt context for each LLM call.
//
// Source reference: context.ts, utils/claudemd.ts
package context

import (
	"bytes"

	"github.com/liuy/gbot/pkg/lsp"
)

// Builder assembles the system prompt context.
// Source: context.ts — builds the full system prompt from components.
type Builder struct {
	// WorkingDir is the current working directory.
	WorkingDir string

	// GitStatus is the injected git status information.
	GitStatus *GitStatusInfo

	// ToolPrompts are system prompt contributions from tools.
	ToolPrompts []string

	// SkillListing is the formatted skill listing within context window budget.
	SkillListing string

	// MemoryFiles are loaded memory files for the system prompt.
	MemoryFiles []MemoryFile

	// MemoryDirOverride, when non-empty, replaces the workingDir-derived memory path.
	MemoryDirOverride string

	// MaxTokens is the token budget for the system prompt.
	MaxTokens int

	// LSPReg, when non-nil and non-empty, causes RuntimeInfo() to list available LSP servers.
	LSPReg *lsp.Registry
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
func (b *Builder) Build() (string, error) {
	var buf bytes.Buffer

	// 1. Base system prompt template
	buf.WriteString(b.BaseSystemPrompt())

	// 2. SOUL placeholder — right after SYSTEM, before operational context
	buf.WriteString("\n\n{{SOUL}}")

	// 3. Platform info
	buf.WriteString(b.RuntimeInfo())

	// 4. Git status
	if b.GitStatus != nil {
		buf.WriteString(b.GitStatusSection())
	}

	// 5. Memory — typed-memory prompt with full instructions
	if memPrompt := FormatMemoryPrompt(b.WorkingDir, b.MemoryDirOverride); memPrompt != "" {
		buf.WriteString("\n\n")
		buf.WriteString(memPrompt)
	} else if len(b.MemoryFiles) > 0 {
		buf.WriteString(FormatMemorySection(b.MemoryFiles))
	}

	// 6. Skill listing
	if b.SkillListing != "" {
		buf.WriteString("\n\n## Available Skills\n\n")
		buf.WriteString(b.SkillListing)
	}

	return buf.String(), nil
}
