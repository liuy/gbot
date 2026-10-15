package context

// BuildSystemPrompt constructs the full system prompt from all context sources.
//
// toolPrompts are the system prompt contributions collected from each tool's
// Prompt() method. They deliberately do NOT go into the system prompt — the
// tool descriptions ride on the API's tools field instead. The parameter stays
// because the Env.ToolPrompts harvest chain and its production call sites still
// pass it; dropping it would touch 8 production and 14 test call sites, which is
// outside this cleanup.
func BuildSystemPrompt(workingDir, projectDir string, toolPrompts []string, skillListing string, memoryDirOverride string) string {
	builder := NewBuilder(workingDir)
	builder.ProjectDir = projectDir
	builder.MemoryFiles = LoadMemoryFiles(workingDir, memoryDirOverride)
	builder.MemoryDirOverride = memoryDirOverride
	builder.SkillListing = skillListing

	return builder.Build()
}
