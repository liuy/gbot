package llm

// ThinkingMode controls the Anthropic "thinking" request field.
//
// Anthropic supports three modes for the `thinking` request parameter:
//   - "disabled" — explicit no-thinking (only accepted by models that support it)
//   - "enabled"  — extended thinking with budget_tokens (legacy/old models)
//   - "adaptive" — model decides when/how much to think (newer models, e.g. Opus 4.7+)
//
// gbot does NOT translate between levels. Users pick the value their model
// accepts. An empty level means the field is omitted entirely (default behavior).
//
// Source reference: Anthropic Messages API "thinking" parameter.
// M3 (minimax) uses the Anthropic API format and accepts all three values.
type ThinkingMode string

const (
	// ThinkingDisabled sends {"type": "disabled"}. Some models reject this.
	ThinkingDisabled ThinkingMode = "disabled"
	// ThinkingEnabled sends {"type": "enabled"}. Requires budget_tokens on most models.
	ThinkingEnabled ThinkingMode = "enabled"
	// ThinkingAdaptive sends {"type": "adaptive"}. Model decides thinking depth.
	ThinkingAdaptive ThinkingMode = "adaptive"
)

// Valid reports whether the level is one of the three known values or empty.
// Empty is valid — it means "don't send the thinking field at all".
func (l ThinkingMode) Valid() bool {
	switch l {
	case "", ThinkingDisabled, ThinkingEnabled, ThinkingAdaptive:
		return true
	}
	return false
}

// TranslateThinking converts a ThinkingLevel into a ThinkingConfig for the
// request body. Returns nil when level is empty (caller omits the field).
//
// gbot does NOT synthesize budget_tokens — the user picks "enabled" knowing
// their model accepts it, and the API is responsible for any default budget.
// If a model rejects the bare {"type":"enabled"} without a budget, the API
// returns 400 and the user adjusts their config.
func TranslateThinking(level ThinkingMode) *ThinkingConfig {
	if level == "" {
		return nil
	}
	return &ThinkingConfig{Type: string(level)}
}
