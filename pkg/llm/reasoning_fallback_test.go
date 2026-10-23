package llm

import (
	"errors"
	"fmt"
	"maps"
	"testing"
)

// ---------------------------------------------------------------------------
// omp message constructors — verbatim from oh-my-pi
// packages/ai/test/openai-reasoning-effort-fallback.test.ts:73-201. The exact
// wording is load-bearing: the negotiation gates are regex verdicts over these
// strings. Do not rephrase.
// ---------------------------------------------------------------------------

func invalidReasoningBody(param, value string) []byte {
	return fmt.Appendf(nil, `{"error":{"message":"invalid reasoning value: '%s' (must be \"high\", \"medium\", \"low\", \"max\", or \"none\")","type":"invalid_request_error","param":"%s"}}`, value, param)
}

func camelCaseReasoningEffortBody() []byte {
	return []byte(`{"error":{"message":"field ReasoningEffort invalid, should be one of: low, medium, high, xhigh, none","type":"invalid_request_error"}}`)
}

func invalidMediumReasoningBody() []byte {
	return []byte(`{"error":{"message":"reasoning.effort: Invalid option: expected one of \"high\"|\"low\"|\"minimal\"|\"none\"","type":"invalid_request_error","param":"reasoning.effort"}}`)
}

func pipeDelimitedReasoningEffortBody() []byte {
	return []byte(`{"error":{"message":"reasoning.effort: Invalid option: expected one of \"xhigh\"|\"high\"|\"medium\"|\"low\"|\"minimal\"|\"none\"","type":"invalid_request_error","param":"reasoning.effort"}}`)
}

func unsupportedLevelBody(value string) []byte {
	return fmt.Appendf(nil, `{"error":{"message":"level \"%s\" not supported, valid levels: low, medium, high, xhigh, max","type":"invalid_request_error"}}`, value)
}

func copilotUnsupportedValueBody(value, modelID string) []byte {
	return fmt.Appendf(nil, `{"error":{"message":"Unsupported value: '%s' is not supported with the '%s' model. Supported values are: 'low', 'medium', 'high', 'xhigh', and 'max'.","type":"invalid_request_body"}}`, value, modelID)
}

func summaryReasoningBody() []byte {
	return []byte(`{"error":{"message":"invalid reasoning.summary value: 'verbose'","type":"invalid_request_error","param":"reasoning.summary"}}`)
}

func unsupportedVerbosityBody() []byte {
	return []byte(`{"error":{"message":"Unsupported value: 'high' for text verbosity. Supported values are: 'low', 'medium'.","type":"invalid_request_body"}}`)
}

func templateKwargRejectionBody() []byte {
	return []byte(`{"error":{"message":"chat_template_kwargs.ReasoningEffort is not supported","type":"invalid_request_error","code":"unknown_parameter","param":"chat_template_kwargs.ReasoningEffort"}}`)
}

func templateKwargValueRejectionBody() []byte {
	return []byte(`{"error":{"message":"chat_template_kwargs.reasoning_effort: 'xhigh' is not supported, valid levels: low, medium, high","type":"invalid_request_error","param":"chat_template_kwargs.reasoning_effort"}}`)
}

func toolChoiceOtherFieldBody() []byte {
	return []byte(`{"error":{"message":"Unsupported value: 'none' is not supported. Supported values are: 'auto', 'required'.","param":"tool_choice","type":"invalid_request_error"}}`)
}

// noCueReasoningBody is the omp invalidReasoningResponse with the must-be
// clause removed (plan B4's omp-derived variant, wording fixed by the plan):
// quoted scan collects only the current value, no cue → null conclusion.
func noCueReasoningBody() []byte {
	return []byte(`{"error":{"message":"invalid reasoning value: 'high'","type":"invalid_request_error","param":"reasoning_effort"}}`)
}

// toolChoiceMessageWithoutParam reuses the omp :602 message verbatim in the
// copilot constructor shape (message-only, no param): a cue whose only ladder
// word is the deleted current value, pinning the empty-but-defined allowed set.
func toolChoiceMessageWithoutParam() []byte {
	return []byte(`{"error":{"message":"Unsupported value: 'none' is not supported. Supported values are: 'auto', 'required'.","type":"invalid_request_body"}}`)
}

// reasoningContentDecoyBody is the one authorized non-omp construction: omp's
// test file has no reasoning_content rejection. Constraints (orchestrator):
// the message must not contain any effort-field vocabulary, status 400, params
// without the chat_template_kwargs spelling.
func reasoningContentDecoyBody() []byte {
	return []byte(`{"error":{"message":"Invalid parameter: reasoning_content is not supported in this context","type":"invalid_request_error","param":"messages.1.reasoning_content"}}`)
}

// resolveFallback mirrors the production wiring: the provider parses the error
// body through ParseAPIError and the negotiation layer sees the same body via
// newCapturedHTTPError.
func resolveFallback(status int, body []byte, params map[string]any, explicitDisable bool) *reasoningEffortFallback {
	apiErr := (&OpenAIProvider{}).ParseAPIError(body, status)
	captured := newCapturedHTTPError(status, body)
	return resolveOpenAIReasoningEffortFallback(apiErr, captured, params, explicitDisable)
}

func assertFallback(t *testing.T, name string, got, want *reasoningEffortFallback) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("%s: got %v, want %v", name, got, want)
	case *got != *want:
		t.Errorf("%s: got %+v, want %+v", name, *got, *want)
	}
}

func effortSet(values ...string) map[string]bool {
	s := make(map[string]bool, len(values))
	for _, v := range values {
		s[v] = true
	}
	return s
}

// ---------------------------------------------------------------------------
// A1. readOpenAIReasoningEffort
// ---------------------------------------------------------------------------

func TestReadOpenAIReasoningEffort(t *testing.T) {
	tests := []struct {
		name      string
		params    map[string]any
		want      string
		wantFound bool
	}{
		{"top-level spelling", map[string]any{"reasoning_effort": "high"}, "high", true},
		{"nested reasoning.effort", map[string]any{"reasoning": map[string]any{"effort": "medium"}}, "medium", true},
		{"template kwarg spelling", map[string]any{"chat_template_kwargs": map[string]any{"reasoning_effort": "low"}}, "low", true},
		{"all three: top-level wins", map[string]any{
			"reasoning_effort":     "high",
			"reasoning":            map[string]any{"effort": "medium"},
			"chat_template_kwargs": map[string]any{"reasoning_effort": "low"},
		}, "high", true},
		{"reasoning beats kwargs", map[string]any{
			"reasoning":            map[string]any{"effort": "medium"},
			"chat_template_kwargs": map[string]any{"reasoning_effort": "low"},
		}, "medium", true},
		{"no effort anywhere", map[string]any{"max_tokens": 100}, "", false},
		{"top-level numeric is not a string", map[string]any{"reasoning_effort": 3}, "", false},
		{"reasoning non-object is skipped", map[string]any{"reasoning": "high"}, "", false},
		{"numeric reasoning.effort falls through to kwargs", map[string]any{
			"reasoning":            map[string]any{"effort": 3},
			"chat_template_kwargs": map[string]any{"reasoning_effort": "low"},
		}, "low", true},
		{"nil params", nil, "", false},
	}
	if got, ok := readTemplateKwargReasoningEffort(nil); ok || got != "" {
		t.Errorf("readTemplateKwarg(nil) = (%q, %v), want (\"\", false)", got, ok)
	}
	for _, tt := range tests {
		got, ok := readOpenAIReasoningEffort(tt.params)
		if ok != tt.wantFound {
			t.Errorf("%s: ok = %v, want %v", tt.name, ok, tt.wantFound)
		}
		if got != tt.want {
			t.Errorf("%s: effort = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// A2. applyOpenAIReasoningEffortFallback
// ---------------------------------------------------------------------------

func TestApplyOpenAIReasoningEffortFallback(t *testing.T) {
	t.Run("string conclusion rewrites every spelling", func(t *testing.T) {
		params := map[string]any{
			"reasoning_effort":     "medium",
			"reasoning":            map[string]any{"effort": "medium"},
			"chat_template_kwargs": map[string]any{"reasoning_effort": "medium"},
		}
		if !applyOpenAIReasoningEffortFallback(params, &reasoningEffortFallback{value: "high"}) {
			t.Fatal("changed = false, want true (three spellings present)")
		}
		if v, ok := params["reasoning_effort"].(string); !ok || v != "high" {
			t.Errorf("reasoning_effort = %v, want high", params["reasoning_effort"])
		}
		reasoning := params["reasoning"].(map[string]any)
		if v, ok := reasoning["effort"].(string); !ok || v != "high" {
			t.Errorf("reasoning.effort = %v, want high", reasoning["effort"])
		}
		kwargs := params["chat_template_kwargs"].(map[string]any)
		if v, ok := kwargs["reasoning_effort"].(string); !ok || v != "high" {
			t.Errorf("kwargs.reasoning_effort = %v, want high", kwargs["reasoning_effort"])
		}
	})

	t.Run("each spelling alone is rewritten without touching the others", func(t *testing.T) {
		only := map[string]any{"reasoning_effort": "medium"}
		if !applyOpenAIReasoningEffortFallback(only, &reasoningEffortFallback{value: "high"}) {
			t.Fatal("top-level only: changed = false, want true")
		}
		if v, ok := only["reasoning_effort"].(string); !ok || v != "high" {
			t.Errorf("top-level only: reasoning_effort = %v, want high", only["reasoning_effort"])
		}

		only = map[string]any{"reasoning": map[string]any{"effort": "medium"}}
		if !applyOpenAIReasoningEffortFallback(only, &reasoningEffortFallback{value: "high"}) {
			t.Fatal("reasoning only: changed = false, want true")
		}
		if _, present := only["reasoning_effort"]; present {
			t.Errorf("reasoning only: reasoning_effort = %v, want key absent", only["reasoning_effort"])
		}
		reasoning := only["reasoning"].(map[string]any)
		if v, ok := reasoning["effort"].(string); !ok || v != "high" {
			t.Errorf("reasoning only: reasoning.effort = %v, want high", reasoning["effort"])
		}

		only = map[string]any{"chat_template_kwargs": map[string]any{"reasoning_effort": "medium"}}
		if !applyOpenAIReasoningEffortFallback(only, &reasoningEffortFallback{value: "high"}) {
			t.Fatal("kwargs only: changed = false, want true")
		}
		if _, present := only["reasoning_effort"]; present {
			t.Errorf("kwargs only: reasoning_effort = %v, want key absent", only["reasoning_effort"])
		}
		kwargs := only["chat_template_kwargs"].(map[string]any)
		if v, ok := kwargs["reasoning_effort"].(string); !ok || v != "high" {
			t.Errorf("kwargs only: kwargs.reasoning_effort = %v, want high", kwargs["reasoning_effort"])
		}
	})

	t.Run("string conclusion ignores a reasoning object without effort", func(t *testing.T) {
		params := map[string]any{"reasoning": map[string]any{"max_tokens": 512}}
		if applyOpenAIReasoningEffortFallback(params, &reasoningEffortFallback{value: "high"}) {
			t.Error("changed = true, want false (no effort spelling)")
		}
		reasoning := params["reasoning"].(map[string]any)
		if _, present := reasoning["effort"]; present {
			t.Errorf("reasoning.effort = %v, want untouched (absent)", reasoning["effort"])
		}
	})

	t.Run("null conclusion deletes all spellings and emptied containers", func(t *testing.T) {
		params := map[string]any{
			"reasoning_effort":     "high",
			"reasoning":            map[string]any{"effort": "high"},
			"chat_template_kwargs": map[string]any{"reasoning_effort": "high"},
		}
		if !applyOpenAIReasoningEffortFallback(params, &reasoningEffortFallback{}) {
			t.Fatal("changed = false, want true")
		}
		for _, key := range []string{"reasoning_effort", "reasoning", "chat_template_kwargs"} {
			if _, present := params[key]; present {
				t.Errorf("%s = %v, want key dropped after emptying", key, params[key])
			}
		}
	})

	t.Run("null conclusion keeps reasoning object with sibling keys", func(t *testing.T) {
		params := map[string]any{"reasoning": map[string]any{"effort": "high", "max_tokens": 512}}
		if !applyOpenAIReasoningEffortFallback(params, &reasoningEffortFallback{}) {
			t.Fatal("changed = false, want true")
		}
		reasoning, ok := params["reasoning"].(map[string]any)
		if !ok {
			t.Fatalf("reasoning = %v, want object kept (max_tokens sibling)", params["reasoning"])
		}
		if _, present := reasoning["effort"]; present {
			t.Errorf("reasoning.effort = %v, want deleted", reasoning["effort"])
		}
		if reasoning["max_tokens"] != 512 {
			t.Errorf("reasoning.max_tokens = %v, want 512", reasoning["max_tokens"])
		}
	})

	t.Run("null conclusion keeps kwargs object with sibling keys", func(t *testing.T) {
		params := map[string]any{"chat_template_kwargs": map[string]any{"reasoning_effort": "high", "enable_thinking": true}}
		if !applyOpenAIReasoningEffortFallback(params, &reasoningEffortFallback{}) {
			t.Fatal("changed = false, want true")
		}
		kwargs, ok := params["chat_template_kwargs"].(map[string]any)
		if !ok {
			t.Fatalf("chat_template_kwargs = %v, want object kept (enable_thinking sibling)", params["chat_template_kwargs"])
		}
		if _, present := kwargs["reasoning_effort"]; present {
			t.Errorf("kwargs.reasoning_effort = %v, want deleted", kwargs["reasoning_effort"])
		}
		if kwargs["enable_thinking"] != true {
			t.Errorf("kwargs.enable_thinking = %v, want true", kwargs["enable_thinking"])
		}
	})

	t.Run("strip hoists kwarg value onto top-level field", func(t *testing.T) {
		params := map[string]any{"chat_template_kwargs": map[string]any{"reasoning_effort": "medium"}}
		if !applyOpenAIReasoningEffortFallback(params, &reasoningEffortFallback{stripTemplateKwarg: true}) {
			t.Fatal("changed = false, want true")
		}
		if v, ok := params["reasoning_effort"].(string); !ok || v != "medium" {
			t.Errorf("reasoning_effort = %v, want medium hoisted", params["reasoning_effort"])
		}
		if _, present := params["chat_template_kwargs"]; present {
			t.Errorf("chat_template_kwargs = %v, want dropped after emptying", params["chat_template_kwargs"])
		}
	})

	t.Run("strip never overwrites an existing top-level field", func(t *testing.T) {
		params := map[string]any{
			"reasoning_effort":     "high",
			"chat_template_kwargs": map[string]any{"reasoning_effort": "medium"},
		}
		if !applyOpenAIReasoningEffortFallback(params, &reasoningEffortFallback{stripTemplateKwarg: true}) {
			t.Fatal("changed = false, want true")
		}
		if v, ok := params["reasoning_effort"].(string); !ok || v != "high" {
			t.Errorf("reasoning_effort = %v, want high kept (no overwrite)", params["reasoning_effort"])
		}
		if _, present := params["chat_template_kwargs"]; present {
			t.Errorf("chat_template_kwargs = %v, want dropped", params["chat_template_kwargs"])
		}
	})

	t.Run("strip without kwarg effort is a no-op", func(t *testing.T) {
		params := map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": true}}
		if applyOpenAIReasoningEffortFallback(params, &reasoningEffortFallback{stripTemplateKwarg: true}) {
			t.Error("changed = true, want false (no kwargs effort to strip)")
		}
		params = map[string]any{"chat_template_kwargs": "not-an-object"}
		if applyOpenAIReasoningEffortFallback(params, &reasoningEffortFallback{stripTemplateKwarg: true}) {
			t.Error("changed = true, want false (kwargs not an object)")
		}
	})

	t.Run("no spelling present is a no-op", func(t *testing.T) {
		params := map[string]any{"max_tokens": 100}
		if applyOpenAIReasoningEffortFallback(params, &reasoningEffortFallback{value: "high"}) {
			t.Error("changed = true, want false (nothing to rewrite)")
		}
	})

	t.Run("nil params is a no-op", func(t *testing.T) {
		if applyOpenAIReasoningEffortFallback(nil, &reasoningEffortFallback{value: "high"}) {
			t.Error("value conclusion: changed = true, want false")
		}
		if applyOpenAIReasoningEffortFallback(nil, &reasoningEffortFallback{stripTemplateKwarg: true}) {
			t.Error("strip conclusion: changed = true, want false")
		}
	})
}

// ---------------------------------------------------------------------------
// A3. nearestEnabledReasoningFallback
// ---------------------------------------------------------------------------

func TestNearestEnabledReasoningFallback(t *testing.T) {
	tests := []struct {
		name    string
		current string
		allowed map[string]bool
		want    string
	}{
		{"minimal special-cases to low", "minimal", effortSet("low", "xhigh"), "low"},
		{"xhigh prefers max", "xhigh", effortSet("low", "max"), "max"},
		{"xhigh falls to high without max", "xhigh", effortSet("low", "high"), "high"},
		{"max falls to xhigh", "max", effortSet("low", "xhigh"), "xhigh"},
		{"equidistant tie prefers higher rank", "medium", effortSet("low", "high"), "high"},
		{"current value excluded from candidates", "high", effortSet("high", "medium"), "medium"},
		{"only disabled values in allowed", "high", effortSet("none"), ""},
		{"unknown current value", "turbo", effortSet("low", "high"), ""},
		{"empty current string", "", effortSet("low"), ""},
		{"empty current string with high allowed", "", effortSet("high"), ""},
		{"empty current string with xhigh allowed", "", effortSet("xhigh"), ""},
		{"empty allowed set", "high", map[string]bool{}, ""},
		// Rank-table pinners: each row's outcome flips if a single rank entry
		// shifts by one, so number mutants cannot survive.
		{"minimal without low falls to rank path", "minimal", effortSet("high"), "high"},
		{"xhigh without max or high", "xhigh", effortSet("low", "medium"), "medium"},
		{"xhigh with only low", "xhigh", effortSet("low"), "low"},
		{"max without xhigh", "max", effortSet("low", "high"), "high"},
		{"low keeps uniquely nearer minimal", "low", effortSet("minimal", "high"), "minimal"},
		{"xhigh prefers nearer low", "xhigh", effortSet("minimal", "low"), "low"},
		{"medium prefers low over xhigh", "medium", effortSet("low", "xhigh"), "low"},
		{"high resolves distance tie toward max", "high", effortSet("low", "max"), "max"},
		{"high prefers nearer medium", "high", effortSet("medium", "max"), "medium"},
	}
	for _, tt := range tests {
		if got := nearestEnabledReasoningFallback(tt.current, tt.allowed); got != tt.want {
			t.Errorf("%s: nearest(%q, %v) = %q, want %q", tt.name, tt.current, tt.allowed, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// A4. parseKnownReasoningValues
// ---------------------------------------------------------------------------

func TestParseKnownReasoningValues(t *testing.T) {
	tests := []struct {
		name string
		text string
		want map[string]bool
	}{
		{"quoted ladder words", `must be "high", "medium", "low"`, effortSet("high", "medium", "low")},
		{"quoted ladder word outside any cue", "prefix 'high' suffix", effortSet("high")},
		{"bare words inside allowed-cue segment", "value must be one of high or low", effortSet("high", "low")},
		{"bare words without cue are not collected", "pick from high and low", map[string]bool{}},
		{"non-ladder quoted words", "'balanced', 'creative'", map[string]bool{}},
	}
	for _, tt := range tests {
		if got := parseKnownReasoningValues(tt.text); !maps.Equal(got, tt.want) {
			t.Errorf("%s: parseKnown(%q) = %v, want %v", tt.name, tt.text, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// A5. parseAllowedReasoningValues — the three-state pin. ok=true means the
// omp Set is defined; a cue with no collectable words must return a non-nil
// empty map, not (nil, false).
// ---------------------------------------------------------------------------

func TestParseAllowedReasoningValues(t *testing.T) {
	t.Run("no cue and no values yields no conclusion", func(t *testing.T) {
		allowed, ok := parseAllowedReasoningValues("chat_template_kwargs.ReasoningEffort is not supported", "high")
		if ok {
			t.Error("ok = true, want false")
		}
		if allowed != nil {
			t.Errorf("allowed = %v, want nil (maps.Equal(nil, empty) would pass, identity must not)", allowed)
		}
	})

	t.Run("cue plus values", func(t *testing.T) {
		allowed, ok := parseAllowedReasoningValues(`reasoning.effort: Invalid option: expected one of "high"|"low"|"minimal"|"none"`, "medium")
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if want := effortSet("high", "low", "minimal", "none"); !maps.Equal(allowed, want) {
			t.Errorf("allowed = %v, want %v", allowed, want)
		}
	})

	t.Run("no cue but a quoted value still counts", func(t *testing.T) {
		allowed, ok := parseAllowedReasoningValues("invalid: 'high'", "medium")
		if !ok {
			t.Fatal("ok = false, want true")
		}
		if want := effortSet("high"); !maps.Equal(allowed, want) {
			t.Errorf("allowed = %v, want %v", allowed, want)
		}
	})

	t.Run("cue quoting only the current value yields non-nil empty set", func(t *testing.T) {
		allowed, ok := parseAllowedReasoningValues("Unsupported value: 'high' is not supported. Supported values are: 'balanced', 'creative'.", "high")
		if !ok {
			t.Fatal("ok = false, want true (cue present, so the set is defined)")
		}
		if allowed == nil {
			t.Fatal("allowed = nil, want non-nil empty map (len(nil)==0 would let the fold bug pass)")
		}
		if len(allowed) != 0 {
			t.Errorf("allowed = %v, want empty", allowed)
		}
	})
}

// ---------------------------------------------------------------------------
// A6. resolveOpenAIReasoningEffortFallback — table inputs are the omp message
// constructors above; params are hand-built wire shapes (wire values may use
// tiers like xhigh that the internal Effort axis does not expose).
// ---------------------------------------------------------------------------

func TestResolveOpenAIReasoningEffortFallback(t *testing.T) {
	tests := []struct {
		name            string
		status          int
		body            []byte
		params          map[string]any
		explicitDisable bool
		want            *reasoningEffortFallback
	}{
		{"must-be list: xhigh downgrades to max", 400, invalidReasoningBody("reasoning_effort", "xhigh"), map[string]any{"reasoning_effort": "xhigh"}, false, &reasoningEffortFallback{value: "max"}},
		{"camelCase: equidistant tie prefers higher rank", 400, camelCaseReasoningEffortBody(), map[string]any{"reasoning_effort": "high"}, false, &reasoningEffortFallback{value: "xhigh"}},
		{"invalid medium upgrades to high", 400, invalidMediumReasoningBody(), map[string]any{"reasoning_effort": "medium"}, false, &reasoningEffortFallback{value: "high"}},
		{"pipe-delimited medium upgrades to high", 400, pipeDelimitedReasoningEffortBody(), map[string]any{"reasoning_effort": "medium"}, false, &reasoningEffortFallback{value: "high"}},
		{"fieldless levels rejection of none: drop the field", 400, unsupportedLevelBody("none"), map[string]any{"reasoning_effort": "none"}, false, &reasoningEffortFallback{}},
		{"copilot supported-values rejection of none: drop the field", 400, copilotUnsupportedValueBody("none", "gpt-6-astra"), map[string]any{"reasoning_effort": "none"}, false, &reasoningEffortFallback{}},
		{"fieldless levels rejection of medium: nearest tier", 400, unsupportedLevelBody("medium"), map[string]any{"reasoning_effort": "medium"}, false, &reasoningEffortFallback{value: "high"}},
		{"sibling reasoning.summary param: no negotiation", 400, summaryReasoningBody(), map[string]any{"reasoning_effort": "high"}, false, nil},
		{"verbosity sibling field: no negotiation", 400, unsupportedVerbosityBody(), map[string]any{"reasoning_effort": "high"}, false, nil},
		{"reasoning_content decoy: no negotiation", 400, reasoningContentDecoyBody(), map[string]any{"reasoning_effort": "high"}, false, nil},
		{"foreign param names another field: no negotiation", 400, toolChoiceOtherFieldBody(), map[string]any{"reasoning_effort": "none"}, true, nil},
		{"kwargs spelling rejection: strip", 400, templateKwargRejectionBody(), map[string]any{"chat_template_kwargs": map[string]any{"reasoning_effort": "high"}}, false, &reasoningEffortFallback{stripTemplateKwarg: true}},
		{"kwargs value rejection with levels list: remap both spellings", 400, templateKwargValueRejectionBody(), map[string]any{
			"reasoning_effort":     "xhigh",
			"chat_template_kwargs": map[string]any{"reasoning_effort": "xhigh"},
		}, false, &reasoningEffortFallback{value: "high"}},
		{"explicit disable clamps to lowest allowed", 400, copilotUnsupportedValueBody("none", "gpt-6-astra"), map[string]any{"reasoning_effort": "none"}, true, &reasoningEffortFallback{value: "low"}},
		{"explicit disable clamps levels-list rejection too", 400, unsupportedLevelBody("none"), map[string]any{"reasoning_effort": "none"}, true, &reasoningEffortFallback{value: "low"}},
		{"status 500: no negotiation", 500, invalidReasoningBody("reasoning_effort", "xhigh"), map[string]any{"reasoning_effort": "xhigh"}, false, nil},
		{"status 200: no negotiation", 200, invalidReasoningBody("reasoning_effort", "xhigh"), map[string]any{"reasoning_effort": "xhigh"}, false, nil},
		{"status 422 negotiates like 400", 422, invalidReasoningBody("reasoning_effort", "xhigh"), map[string]any{"reasoning_effort": "xhigh"}, false, &reasoningEffortFallback{value: "max"}},
		{"no effort in params: no negotiation", 400, invalidReasoningBody("reasoning_effort", "xhigh"), map[string]any{"max_tokens": 100}, false, nil},
		{"empty-string current effort: no negotiation", 400, invalidReasoningBody("reasoning_effort", "xhigh"), map[string]any{"reasoning_effort": ""}, false, nil},
		{"unknown current effort: no negotiation", 400, invalidReasoningBody("reasoning_effort", "xhigh"), map[string]any{"reasoning_effort": "turbo"}, false, nil},
		{"no-cue rejection: drop the field", 400, noCueReasoningBody(), map[string]any{"reasoning_effort": "high"}, false, &reasoningEffortFallback{}},
		{"cue with only non-ladder values, current none: drop the field", 400, toolChoiceMessageWithoutParam(), map[string]any{"reasoning_effort": "none"}, false, &reasoningEffortFallback{}},
		{"same shape under explicit disable: no enabled tier to clamp to", 400, toolChoiceMessageWithoutParam(), map[string]any{"reasoning_effort": "none"}, true, &reasoningEffortFallback{}},
		{"explicit disable prefers none when the list has it", 400, invalidReasoningBody("reasoning_effort", "xhigh"), map[string]any{"reasoning_effort": "xhigh"}, true, &reasoningEffortFallback{value: "none"}},
		{"low current negotiates", 400, invalidReasoningBody("reasoning_effort", "low"), map[string]any{"reasoning_effort": "low"}, false, &reasoningEffortFallback{value: "medium"}},
		{"minimal current negotiates", 400, invalidReasoningBody("reasoning_effort", "minimal"), map[string]any{"reasoning_effort": "minimal"}, false, &reasoningEffortFallback{value: "low"}},
		{"max current negotiates", 400, invalidReasoningBody("reasoning_effort", "max"), map[string]any{"reasoning_effort": "max"}, false, &reasoningEffortFallback{value: "high"}},
		{"kwargs rejection at non-400 status: no negotiation", 500, templateKwargRejectionBody(), map[string]any{"chat_template_kwargs": map[string]any{"reasoning_effort": "high"}}, false, nil},
	}
	for _, tt := range tests {
		assertFallback(t, tt.name, resolveFallback(tt.status, tt.body, tt.params, tt.explicitDisable), tt.want)
	}

	t.Run("apiErr alone without captured body", func(t *testing.T) {
		apiErr := &APIError{Status: 400, Message: `invalid reasoning value: 'high' (must be "low")`}
		got := resolveOpenAIReasoningEffortFallback(apiErr, nil, map[string]any{"reasoning_effort": "high"}, false)
		assertFallback(t, "apiErr only", got, &reasoningEffortFallback{value: "low"})
	})

	t.Run("allowed set without enabled tiers drops the field", func(t *testing.T) {
		// omp must-be template listing only the off value: nearest() has no
		// enabled candidate, so the conclusion collapses to "drop the field".
		apiErr := &APIError{Status: 400, Message: `invalid reasoning value: 'high' (must be "none")`}
		got := resolveOpenAIReasoningEffortFallback(apiErr, nil, map[string]any{"reasoning_effort": "high"}, false)
		assertFallback(t, "none-only list", got, &reasoningEffortFallback{})
	})

	t.Run("no apiErr, status from captured body", func(t *testing.T) {
		captured := newCapturedHTTPError(400, camelCaseReasoningEffortBody())
		got := resolveOpenAIReasoningEffortFallback(nil, captured, map[string]any{"reasoning_effort": "high"}, false)
		assertFallback(t, "captured only", got, &reasoningEffortFallback{value: "xhigh"})
	})

	t.Run("neither apiErr nor captured body", func(t *testing.T) {
		got := resolveOpenAIReasoningEffortFallback(nil, nil, map[string]any{"reasoning_effort": "high"}, false)
		assertFallback(t, "no error context", got, nil)
	})

	t.Run("apiErr status wins over captured body", func(t *testing.T) {
		apiErr := &APIError{Status: 400, Message: `invalid reasoning value: 'high' (must be "low")`}
		captured := newCapturedHTTPError(500, []byte(`{"error":{"message":"server hiccup"}}`))
		got := resolveOpenAIReasoningEffortFallback(apiErr, captured, map[string]any{"reasoning_effort": "high"}, false)
		assertFallback(t, "apiErr status precedence", got, &reasoningEffortFallback{value: "low"})
	})

	t.Run("field mention and rejection verdict on different parts", func(t *testing.T) {
		// The verdict patterns cannot cross the newline between joined parts:
		// the field word lives in the apiErr message, the quoted-value verdict
		// in the captured body. isInvalid still qualifies via rejectedMatches,
		// and with no allowed cue the conclusion is "drop the field".
		apiErr := &APIError{Status: 400, Message: "note about reasoning_effort"}
		captured := newCapturedHTTPError(400, []byte(`{"error":{"message":"Unsupported value: 'high'"}}`))
		got := resolveOpenAIReasoningEffortFallback(apiErr, captured, map[string]any{"reasoning_effort": "high"}, false)
		assertFallback(t, "split parts", got, &reasoningEffortFallback{})
	})

	t.Run("dual-spelling params apply remaps both", func(t *testing.T) {
		params := map[string]any{
			"reasoning_effort":     "xhigh",
			"chat_template_kwargs": map[string]any{"reasoning_effort": "xhigh"},
		}
		fb := resolveFallback(400, templateKwargValueRejectionBody(), params, false)
		assertFallback(t, "resolve", fb, &reasoningEffortFallback{value: "high"})
		if !applyOpenAIReasoningEffortFallback(params, fb) {
			t.Fatal("apply changed = false, want true")
		}
		if v, ok := params["reasoning_effort"].(string); !ok || v != "high" {
			t.Errorf("reasoning_effort = %v, want high", params["reasoning_effort"])
		}
		kwargs := params["chat_template_kwargs"].(map[string]any)
		if v, ok := kwargs["reasoning_effort"].(string); !ok || v != "high" {
			t.Errorf("kwargs.reasoning_effort = %v, want high (stale xhigh must not survive)", kwargs["reasoning_effort"])
		}
	})
}

// ---------------------------------------------------------------------------
// A7. reasoningFallbackKey
// ---------------------------------------------------------------------------

func TestReasoningFallbackKey(t *testing.T) {
	if got := reasoningFallbackKey("chat-completions", "https://x/v1", "m"); got != "chat-completions:https://x/v1:m" {
		t.Errorf("key = %q, want %q", got, "chat-completions:https://x/v1:m")
	}
	if got := reasoningFallbackKey("chat-completions", "", "m"); got != "chat-completions::m" {
		t.Errorf("empty baseUrl: key = %q, want %q", got, "chat-completions::m")
	}
	if got := reasoningFallbackKey("chat-completions", "https://x/v1", ""); got != "chat-completions:https://x/v1:" {
		t.Errorf("empty model: key = %q, want %q", got, "chat-completions:https://x/v1:")
	}
}

// ---------------------------------------------------------------------------
// A8. reasoningFallbackState nil-safety and lifecycle
// ---------------------------------------------------------------------------

func TestReasoningFallbackStateNilSafety(t *testing.T) {
	var nilState *reasoningFallbackState
	if fb := nilState.get("k"); fb != nil {
		t.Errorf("nil state get = %v, want nil", fb)
	}
	nilState.remember("k", &reasoningEffortFallback{value: "high"})
	nilState.clear()

	state := newReasoningFallbackState()
	fb := &reasoningEffortFallback{value: "high"}
	state.remember("chat-completions:b:m", fb)
	if got := state.get("chat-completions:b:m"); got != fb {
		t.Errorf("get after remember = %v, want %v", got, fb)
	}
	if got := state.get("chat-completions:other:m"); got != nil {
		t.Errorf("get unknown key = %v, want nil", got)
	}
	state.clear()
	if got := state.get("chat-completions:b:m"); got != nil {
		t.Errorf("get after clear = %v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// Internal predicate pins — minimal crafted strings (not server messages) that
// fix the branch behavior mutation testing must see.
// ---------------------------------------------------------------------------

func TestNewCapturedHTTPError(t *testing.T) {
	c := newCapturedHTTPError(502, []byte("bad gateway"))
	if c.status != 502 {
		t.Errorf("status = %d, want 502", c.status)
	}
	if c.bodyText != "bad gateway" {
		t.Errorf("bodyText = %q, want %q", c.bodyText, "bad gateway")
	}
	if c.bodyJson != nil {
		t.Errorf("bodyJson = %v, want nil for non-JSON body", c.bodyJson)
	}

	body := []byte(`{"error":{"message":"m"}}`)
	c = newCapturedHTTPError(400, body)
	if c.bodyText != string(body) {
		t.Errorf("bodyText = %q, want raw body", c.bodyText)
	}
	msg, ok := c.bodyJson["error"].(map[string]any)["message"].(string)
	if !ok || msg != "m" {
		t.Errorf("bodyJson.error.message = %v, want m", c.bodyJson["error"])
	}
}

func TestCollectMessageParts(t *testing.T) {
	body := []byte(`{"error":{"message":"error message","param":"reasoning_effort","code":"unknown_parameter","type":"invalid_request_error"}}`)
	apiErr := &APIError{Status: 400, Message: "api message"}
	captured := newCapturedHTTPError(400, body)
	want := "api message\nerror message\nreasoning_effort\nunknown_parameter\ninvalid_request_error\n" + string(body)
	if got := collectMessageParts(apiErr, captured); got != want {
		t.Errorf("collectMessageParts = %q, want %q", got, want)
	}

	// Blank parts are dropped; a nil apiErr contributes nothing.
	if got := collectMessageParts(&APIError{Status: 400}, captured); got != "error message\nreasoning_effort\nunknown_parameter\ninvalid_request_error\n"+string(body) {
		t.Errorf("blank filtering: got %q", got)
	}

	// Top-level body fields are the fallback when the error object lacks one.
	flatBody := []byte(`{"error":{"type":"invalid_request_error"},"message":"reasoning_effort is not supported"}`)
	captured = newCapturedHTTPError(400, flatBody)
	want = "reasoning_effort is not supported\ninvalid_request_error\n" + string(flatBody)
	if got := collectMessageParts(&APIError{Status: 400}, captured); got != want {
		t.Errorf("top-level fallback: got %q, want %q", got, want)
	}

	// No captured body: only the apiErr message survives.
	if got := collectMessageParts(apiErr, nil); got != "api message" {
		t.Errorf("nil captured: got %q, want %q", got, "api message")
	}
}

func TestMentionsReasoningEffort(t *testing.T) {
	withField := func(field, value string) *capturedHTTPError {
		return &capturedHTTPError{status: 400, bodyJson: map[string]any{"error": map[string]any{field: value}}}
	}
	// Each of the four channels alone identifies the field (omp :192-203).
	cases := []struct {
		name     string
		apiErr   *APIError
		captured *capturedHTTPError
	}{
		{"param", nil, withField("param", "reasoning_effort")},
		{"code", nil, withField("code", "reasoning_effort_invalid")},
		{"type", nil, withField("type", "invalid reasoning value")},
		{"message", &APIError{Status: 400, Message: "valid levels: low, medium"}, nil},
	}
	for _, tt := range cases {
		if !mentionsReasoningEffort(tt.apiErr, tt.captured) {
			t.Errorf("%s: mentions = false, want true", tt.name)
		}
	}
	if mentionsReasoningEffort(&APIError{Status: 400, Message: "nothing relevant"}, nil) {
		t.Error("no signal: mentions = true, want false")
	}
}

func TestMessageCarriesEffortVerdict(t *testing.T) {
	positive := []string{
		"invalid option for reasoning_effort",     // verdict before field
		"reasoning_effort must be one of: low",    // field before verdict
		"unsupported parameter: reasoning_effort", // verdict-before-field word order
		"reasoning_effort is not permitted",
	}
	for _, msg := range positive {
		if !messageCarriesEffortVerdict(msg) {
			t.Errorf("verdict(%q) = false, want true", msg)
		}
	}
	negative := []string{"reasoning_effort", "totally unrelated message", ""}
	for _, msg := range negative {
		if messageCarriesEffortVerdict(msg) {
			t.Errorf("verdict(%q) = true, want false", msg)
		}
	}
}

func TestParseEffortRejectionSignal(t *testing.T) {
	// Param names the field while the message does not.
	c := &capturedHTTPError{status: 400, bodyJson: map[string]any{"error": map[string]any{"param": "reasoning_effort"}}}
	sig := parseEffortRejectionSignal("bad input", c, "high")
	if sig.field != "reasoning-effort" || sig.namesFieldInMessage || sig.messageVerdict || sig.listsLevels || sig.rejectedMatches {
		t.Errorf("param-naming signal = %+v", sig)
	}

	// Message names the field with no structured param at all.
	sig = parseEffortRejectionSignal("reasoning_effort is invalid", nil, "high")
	if sig.field != "reasoning-effort" || !sig.namesFieldInMessage || !sig.messageVerdict {
		t.Errorf("message-naming signal = %+v", sig)
	}

	// A structured foreign param beats the heuristic.
	c = &capturedHTTPError{status: 400, bodyJson: map[string]any{"error": map[string]any{"param": "tool_choice"}}}
	sig = parseEffortRejectionSignal("Unsupported value: 'none' is not supported. Supported values are: 'auto', 'required'.", c, "none")
	if sig.field != "other" {
		t.Errorf("foreign param signal.field = %q, want other", sig.field)
	}

	// No param, no field word: unknown.
	sig = parseEffortRejectionSignal("level \"none\" not supported, valid levels: low", nil, "none")
	if sig.field != "unknown" || !sig.listsLevels || !sig.rejectedMatches {
		t.Errorf("fieldless signal = %+v", sig)
	}

	// rejectedMatches requires the current effort in quotes next to a verdict.
	if sig := parseEffortRejectionSignal("invalid value 'high'", nil, "high"); !sig.rejectedMatches {
		t.Error("verdict-then-value: rejectedMatches = false, want true")
	}
	if sig := parseEffortRejectionSignal("'high' is not supported", nil, "high"); !sig.rejectedMatches {
		t.Error("value-then-verdict: rejectedMatches = false, want true")
	}
	if sig := parseEffortRejectionSignal("invalid value 'low'", nil, "high"); sig.rejectedMatches {
		t.Error("other value quoted: rejectedMatches = true, want false")
	}
}

func TestResolveStripTemplateKwargFallback(t *testing.T) {
	kwargsParams := map[string]any{"chat_template_kwargs": map[string]any{"reasoning_effort": "high"}}
	rejection := newCapturedHTTPError(400, templateKwargRejectionBody())
	apiErr400 := (&OpenAIProvider{}).ParseAPIError(templateKwargRejectionBody(), 400)

	tests := []struct {
		name   string
		apiErr *APIError
		c      *capturedHTTPError
		params map[string]any
		want   bool
	}{
		{"nil params", apiErr400, rejection, nil, false},
		{"no kwargs spelling", apiErr400, rejection, map[string]any{"reasoning_effort": "high"}, false},
		{"non-400 status from apiErr", &APIError{Status: 500}, rejection, kwargsParams, false},
		{"non-400 status from captured", nil, newCapturedHTTPError(500, templateKwargRejectionBody()), kwargsParams, false},
		{"message without kwargs naming", &APIError{Status: 400, Message: "reasoning_effort is not supported"}, nil, kwargsParams, false},
		{"kwargs named but no rejection verdict", &APIError{Status: 400, Message: "chat_template_kwargs.reasoning_effort is expected"}, nil, kwargsParams, false},
		{"kwargs named, no verdict, no cue: not a strip", &APIError{Status: 400, Message: "chat_template_kwargs.reasoning_effort is present"}, nil, kwargsParams, false},
		{"cue present routes to value remap", &APIError{Status: 400, Message: "chat_template_kwargs.reasoning_effort: 'high' is not supported, valid levels: low"}, nil, kwargsParams, false},
		{"full strip match", apiErr400, rejection, kwargsParams, true},
		{"captured body alone drives strip", nil, rejection, kwargsParams, true},
		{"422 rejection still strips", &APIError{Status: 422, Message: "chat_template_kwargs.reasoning_effort is not supported"}, nil, kwargsParams, true},
		{"no error context at all", nil, nil, kwargsParams, false},
	}
	for _, tt := range tests {
		if got := resolveStripTemplateKwargFallback(tt.apiErr, tt.c, tt.params); got != tt.want {
			t.Errorf("%s: strip = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestLowestEnabledAllowedValue(t *testing.T) {
	if got := lowestEnabledAllowedValue(effortSet("none", "medium", "max")); got != "medium" {
		t.Errorf("lowest(none,medium,max) = %q, want medium (skips none, not enabled)", got)
	}
	if got := lowestEnabledAllowedValue(effortSet("low", "xhigh")); got != "low" {
		t.Errorf("lowest(low,xhigh) = %q, want low", got)
	}
	if got := lowestEnabledAllowedValue(effortSet("none")); got != "" {
		t.Errorf("lowest(none) = %q, want empty", got)
	}
	if got := lowestEnabledAllowedValue(map[string]bool{}); got != "" {
		t.Errorf("lowest(empty) = %q, want empty", got)
	}
}

func TestDeleteReasoningEffort(t *testing.T) {
	// Non-string effort is a no-op (defensive branch, omp :105).
	parent := map[string]any{"reasoning": map[string]any{"max_tokens": 512}}
	if deleteReasoningEffort(parent["reasoning"].(map[string]any), parent) {
		t.Error("non-string effort: deleted = true, want false")
	}

	// Emptied reasoning object is dropped from the parent.
	parent = map[string]any{"reasoning": map[string]any{"effort": "high"}}
	if !deleteReasoningEffort(parent["reasoning"].(map[string]any), parent) {
		t.Fatal("deleted = false, want true")
	}
	if _, present := parent["reasoning"]; present {
		t.Errorf("reasoning = %v, want dropped", parent["reasoning"])
	}

	// Sibling keys keep the reasoning object alive.
	parent = map[string]any{"reasoning": map[string]any{"effort": "high", "max_tokens": 512}}
	if !deleteReasoningEffort(parent["reasoning"].(map[string]any), parent) {
		t.Fatal("deleted = false, want true")
	}
	if _, present := parent["reasoning"]; !present {
		t.Error("reasoning dropped, want kept with max_tokens")
	}
}

func TestReasoningFallbackWiringGlue(t *testing.T) {
	p := &OpenAIProvider{}

	// Only APIError-carried failures negotiate, even when a body was captured.
	captured := newCapturedHTTPError(400, invalidReasoningBody("reasoning_effort", "xhigh"))
	if fb := p.resolveReasoningFallback(errors.New("transport boom"), captured, []byte(`{"reasoning_effort":"xhigh"}`), false); fb != nil {
		t.Errorf("non-APIError error: fb = %v, want nil", fb)
	}
	// Without a captured body there is nothing to negotiate on — even when
	// the error itself carries a fully negotiable rejection message.
	apiErr := (&OpenAIProvider{}).ParseAPIError(invalidReasoningBody("reasoning_effort", "xhigh"), 400)
	if fb := p.resolveReasoningFallback(apiErr, nil, []byte(`{"reasoning_effort":"xhigh"}`), false); fb != nil {
		t.Errorf("nil captured: fb = %v, want nil", fb)
	}
	// A body that is not a JSON object cannot carry an effort spelling.
	if fb := p.resolveReasoningFallback(&APIError{Status: 400, Message: "m"}, captured, []byte("not json"), false); fb != nil {
		t.Errorf("invalid body: fb = %v, want nil", fb)
	}

	// Bodies that are not JSON objects pass through byte-identical.
	if got := applyReasoningFallbackToBody([]byte("not json"), &reasoningEffortFallback{value: "high"}); string(got) != "not json" {
		t.Errorf("invalid body rewritten to %q, want unchanged", got)
	}
	// A conclusion that changes nothing must return the original bytes, not
	// a re-serialization (key order and formatting stay provider-authored).
	untouched := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if got := applyReasoningFallbackToBody(untouched, &reasoningEffortFallback{value: "high"}); string(got) != string(untouched) {
		t.Errorf("no-op conclusion re-serialized body to %s, want byte-identical passthrough", got)
	}
	if got := chatBodyParams([]byte("not json")); got != nil {
		t.Errorf("chatBodyParams(invalid) = %v, want nil", got)
	}
	if got := chatBodyParams([]byte("null")); got != nil {
		t.Errorf("chatBodyParams(null) = %v, want nil", got)
	}
	if got := chatBodyParams([]byte(`{"model":"m"}`)); got == nil || got["model"] != "m" {
		t.Errorf("chatBodyParams(valid) = %v, want parsed map", got)
	}
}
