package llm

import "fmt"

// Frozen thinking-effort mapping. The internal axis is Effort
// (none|auto|low|medium|high|max, empty = auto); each provider translates it
// to its own wire shape at request assembly — the engine never sees a dialect:
//
//              none                      auto                    low/medium/high/max
// anthropic    thinking{type:disabled}   thinking{type:adaptive} output_config{effort:"..."}, no thinking field
// chat (GLM)   thinking{type:disabled}   omit thinking field     thinking{type:enabled}
// responses    reasoning{effort:"none"}  omit reasoning field    reasoning{effort:"..."}
//
// anthropic effort rides the nested output_config object, not a top-level
// string — https://docs.anthropic.com/en/docs/build-with-claude/effort also
// forbids passing "adaptive" as an effort value, which is why auto keeps the
// adaptive thinking type instead. responses effort "none" is the GLM-verified
// hard off (reasoning_tokens=0); the word "disabled" is not recognized there.
// chat's axis is coarse: every non-none, non-auto level collapses to the
// enabled toggle because GLM's chat protocol exposes nothing finer.

// Effort is the protocol-agnostic thinking-effort axis carried on
// Request.Thinking. Empty means "no preference" and resolves to auto at the
// provider boundary, so it differs from EffortAuto only in that an empty
// config baseline lets the provider omit the field where auto cannot.
type Effort string

const (
	EffortNone   Effort = "none"
	EffortAuto   Effort = "auto"
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortMax    Effort = "max"
)

// Valid reports whether e is one of the six axis values or empty (empty = auto).
func (e Effort) Valid() bool {
	switch e {
	case "", EffortNone, EffortAuto, EffortLow, EffortMedium, EffortHigh, EffortMax:
		return true
	}
	return false
}

// ParseEffort parses UI/user input. Only the six axis values are accepted —
// the empty string is an error too, because every caller treats missing input
// distinctly from an explicit auto choice.
func ParseEffort(s string) (Effort, error) {
	if e := Effort(s); e != "" && e.Valid() {
		return e, nil
	}
	return "", fmt.Errorf("invalid thinking effort %q (none|auto|low|medium|high|max)", s)
}

// ThinkingMode is the legacy settings.json thinking value. The field type
// stays so old configs keep deserializing; values are migrated onto the
// Effort axis by NormalizeThinkingMode at engine construction.
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

// NormalizeThinkingMode migrates a config-side thinking value onto the Effort
// axis: disabled→none, enabled/adaptive→auto (both legacy values meant "let
// the model think its default way"), axis values map to themselves, ""→"".
// Unknown values return ok=false so the caller can warn and skip the model.
func NormalizeThinkingMode(m ThinkingMode) (Effort, bool) {
	switch m {
	case ThinkingDisabled:
		return EffortNone, true
	case ThinkingEnabled, ThinkingAdaptive:
		return EffortAuto, true
	}
	if e := Effort(m); e.Valid() {
		return e, true
	}
	return "", false
}

// translateAnthropicThinking maps the axis to the two anthropic wire fields.
// low/medium/high/max omit thinking entirely and carry output_config instead;
// "adaptive" must never appear inside output_config, so auto keeps the
// thinking type. Doc: https://docs.anthropic.com/en/docs/build-with-claude/effort
func translateAnthropicThinking(e Effort) (thinking *ThinkingConfig, out *outputConfig) {
	switch e {
	case EffortNone:
		return &ThinkingConfig{Type: "disabled"}, nil
	case EffortLow, EffortMedium, EffortHigh, EffortMax:
		return nil, &outputConfig{Effort: string(e)}
	}
	// "", EffortAuto — and unreachable non-axis garbage, which callers filter
	// through Valid() — all mean "model decides".
	return &ThinkingConfig{Type: "adaptive"}, nil
}

// translateChatThinking maps the axis onto GLM/DeepSeek's two-state chat
// toggle: every concrete level lands on enabled because the protocol has no
// finer granularity.
func translateChatThinking(e Effort) *ThinkingConfig {
	switch e {
	case EffortNone:
		return &ThinkingConfig{Type: "disabled"}
	case EffortLow, EffortMedium, EffortHigh, EffortMax:
		return &ThinkingConfig{Type: "enabled"}
	}
	return nil
}
