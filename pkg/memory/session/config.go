// Package sessionmemory implements background session memory extraction
// and session-memory-based compact (SM-compact).
//
// Source: services/SessionMemory/sessionMemory.ts (496 lines)
// Source: services/SessionMemory/sessionMemoryUtils.ts (208 lines)
// Source: services/SessionMemory/prompts.ts (325 lines)
package session

// Config controls session memory extraction thresholds.
// TS source: sessionMemoryUtils.ts:18-36 — SessionMemoryConfig + DEFAULT_SESSION_MEMORY_CONFIG.
type Config struct {
	// MinTokensToInit is the minimum token count before first extraction.
	// TS: minimumMessageTokensToInit (default: 10000).
	MinTokensToInit int

	// MinTokensBetweenUpdate is the minimum token growth since last extraction
	// to trigger an update. ALWAYS required — extraction never fires without it.
	// TS: minimumTokensBetweenUpdate (default: 5000).
	MinTokensBetweenUpdate int

	// ToolCallsBetweenUpdates is the minimum tool call count since last extraction.
	// Alternative trigger: extraction also fires at natural conversation breaks
	// (last turn has no tool calls).
	// TS: toolCallsBetweenUpdates (default: 3).
	ToolCallsBetweenUpdates int

	// MaxSectionTokens is the per-section token budget for warnings.
	// TS: MAX_SECTION_LENGTH in prompts.ts (default: 2000).
	MaxSectionTokens int

	// MaxTotalTokens is the total session memory token budget for warnings.
	// TS: MAX_TOTAL_SESSION_MEMORY_TOKENS in prompts.ts (default: 12000).
	MaxTotalTokens int

	// ExtractionTimeoutMs is the maximum time to wait for a single extraction.
	// TS: EXTRACTION_WAIT_TIMEOUT_MS (default: 15000 = 15s).
	ExtractionTimeoutMs int

	// ExtractionStaleMs is the time after which a stuck extraction is considered stale.
	// TS: EXTRACTION_STALE_THRESHOLD_MS (default: 60000 = 1 min).
	ExtractionStaleMs int
}

// DefaultConfig returns the default session memory configuration.
// TS source: sessionMemoryUtils.ts:32-36 — DEFAULT_SESSION_MEMORY_CONFIG.
func DefaultConfig() Config {
	return Config{
		MinTokensToInit:         10000,
		MinTokensBetweenUpdate:  5000,
		ToolCallsBetweenUpdates: 3,
		MaxSectionTokens:        2000,
		MaxTotalTokens:          12000,
		ExtractionTimeoutMs:     15000,
		ExtractionStaleMs:       60000,
	}
}
