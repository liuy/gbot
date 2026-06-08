package context

import (
	"encoding/json"
)

// BuildSystemPrompt constructs the full system prompt from all context sources.
// toolPrompts are the system prompt contributions collected from each tool's Prompt() method.
// On build failure, returns a minimal fallback prompt so the application can still start.
func BuildSystemPrompt(workingDir string, toolPrompts []string, skillListing string) json.RawMessage {
	builder := NewBuilder(workingDir)
	builder.GitStatus = LoadGitStatus(workingDir)
	builder.MemoryFiles = LoadMemoryFiles(workingDir)
	builder.SkillListing = skillListing
	builder.ToolPrompts = toolPrompts

	if soul, _ := LoadSoulFile(); soul != "" {
		builder.SoulContent = soul
	}

	prompt, err := builder.Build()
	if err != nil {
		fallback := json.RawMessage(`"You are gbot, an interactive AI coding assistant. Use tools to accomplish tasks."`)
		return fallback
	}
	return prompt
}
