package context

import "github.com/liuy/gbot/pkg/lsp"

// BuildSystemPrompt constructs the full system prompt from all context sources.
// toolPrompts are the system prompt contributions collected from each tool's Prompt() method.
// On build failure, returns a minimal fallback prompt so the application can still start.
func BuildSystemPrompt(workingDir string, toolPrompts []string, skillListing string, lspReg *lsp.Registry) string {
	builder := NewBuilder(workingDir)
	builder.GitStatus = LoadGitStatus(workingDir)
	builder.MemoryFiles = LoadMemoryFiles(workingDir)
	builder.SkillListing = skillListing
	builder.ToolPrompts = toolPrompts
	builder.LSPReg = lspReg

	prompt, err := builder.Build()
	if err != nil {
		return "You are gbot, an interactive AI coding assistant. Use tools to accomplish tasks."
	}
	return prompt
}
