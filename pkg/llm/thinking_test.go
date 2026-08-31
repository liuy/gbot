package llm

import (
	"strings"
	"testing"
)

func TestThinkingLevel_Valid(t *testing.T) {
	tests := []struct {
		level ThinkingMode
		want  bool
	}{
		{"", true},
		{ThinkingDisabled, true},
		{ThinkingEnabled, true},
		{ThinkingAdaptive, true},
		{"bogus", false},
		{"DISABLED", false}, // case-sensitive on purpose — wire format is strict
	}
	for _, tt := range tests {
		if got := tt.level.Valid(); got != tt.want {
			t.Errorf("Valid(%q) = %v, want %v", tt.level, got, tt.want)
		}
	}
}

func TestEffortValid(t *testing.T) {
	tests := []struct {
		effort Effort
		want   bool
	}{
		{"", true}, // empty = auto
		{EffortNone, true},
		{EffortAuto, true},
		{EffortLow, true},
		{EffortMedium, true},
		{EffortHigh, true},
		{EffortMax, true},
		{"adaptive", false}, // legacy word, not an axis value
		{"enabled", false},  // legacy word, not an axis value
		{"xhigh", false},    // intentionally excluded from the frozen axis
		{"HIGH", false},     // case-sensitive on purpose — wire format is strict
		{"bogus", false},
	}
	for _, tt := range tests {
		if got := tt.effort.Valid(); got != tt.want {
			t.Errorf("Valid(%q) = %v, want %v", tt.effort, got, tt.want)
		}
	}
}

func TestParseEffort(t *testing.T) {
	for _, v := range []Effort{EffortNone, EffortAuto, EffortLow, EffortMedium, EffortHigh, EffortMax} {
		got, err := ParseEffort(string(v))
		if err != nil {
			t.Errorf("ParseEffort(%q) error: %v", v, err)
			continue
		}
		if got != v {
			t.Errorf("ParseEffort(%q) = %q, want %q", v, got, v)
		}
	}
	for _, s := range []string{"", "adaptive", "xhigh", "HIGH"} {
		got, err := ParseEffort(s)
		if err == nil {
			t.Errorf("ParseEffort(%q) = %q, want error", s, got)
			continue
		}
		if !strings.Contains(err.Error(), "none|auto|low|medium|high|max") {
			t.Errorf("ParseEffort(%q) error = %q, want it to list the six values", s, err.Error())
		}
		if got != "" {
			t.Errorf("ParseEffort(%q) = %q, want empty effort on error", s, got)
		}
	}
}

func TestNormalizeThinkingMode(t *testing.T) {
	tests := []struct {
		in   ThinkingMode
		want Effort
		ok   bool
	}{
		{ThinkingDisabled, EffortNone, true},
		{ThinkingEnabled, EffortAuto, true},
		{ThinkingAdaptive, EffortAuto, true},
		{"none", EffortNone, true},
		{"auto", EffortAuto, true},
		{"low", EffortLow, true},
		{"medium", EffortMedium, true},
		{"high", EffortHigh, true},
		{"max", EffortMax, true},
		{"", "", true},
		{"bogus", "", false},
		{"xhigh", "", false},
	}
	for _, tt := range tests {
		got, ok := NormalizeThinkingMode(tt.in)
		if ok != tt.ok {
			t.Errorf("NormalizeThinkingMode(%q) ok = %v, want %v", tt.in, ok, tt.ok)
		}
		if got != tt.want {
			t.Errorf("NormalizeThinkingMode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTranslateAnthropicThinking(t *testing.T) {
	th, oc := translateAnthropicThinking(EffortNone)
	if th == nil || th.Type != "disabled" {
		t.Errorf("none: thinking = %+v, want Type=disabled", th)
	}
	if oc != nil {
		t.Errorf("none: outputConfig = %+v, want nil", oc)
	}

	for _, e := range []Effort{EffortAuto, ""} {
		th, oc := translateAnthropicThinking(e)
		if th == nil || th.Type != "adaptive" {
			t.Errorf("%q: thinking = %+v, want Type=adaptive", e, th)
		}
		if oc != nil {
			t.Errorf("%q: outputConfig = %+v, want nil", e, oc)
		}
	}

	for _, e := range []Effort{EffortLow, EffortMedium, EffortHigh, EffortMax} {
		th, oc := translateAnthropicThinking(e)
		if th != nil {
			t.Errorf("%q: thinking = %+v, want nil (effort rides output_config)", e, th)
		}
		if oc == nil || oc.Effort != string(e) {
			t.Errorf("%q: outputConfig = %+v, want Effort=%q", e, oc, string(e))
		}
	}
}

func TestTranslateChatThinking(t *testing.T) {
	got := translateChatThinking(EffortNone)
	if got == nil || got.Type != "disabled" {
		t.Errorf("none = %+v, want Type=disabled", got)
	}
	for _, e := range []Effort{EffortAuto, ""} {
		if got := translateChatThinking(e); got != nil {
			t.Errorf("%q = %+v, want nil (field omitted)", e, got)
		}
	}
	for _, e := range []Effort{EffortLow, EffortMedium, EffortHigh, EffortMax} {
		got := translateChatThinking(e)
		if got == nil || got.Type != "enabled" {
			t.Errorf("%q = %+v, want Type=enabled (coarse GLM toggle)", e, got)
		}
	}
}

func TestTranslateResponsesReasoning(t *testing.T) {
	got := translateResponsesReasoning(EffortNone)
	if got == nil || got.Effort != "none" {
		t.Errorf("none = %+v, want Effort=none", got)
	}
	for _, e := range []Effort{EffortAuto, ""} {
		if got := translateResponsesReasoning(e); got != nil {
			t.Errorf("%q = %+v, want nil (field omitted)", e, got)
		}
	}
	for _, e := range []Effort{EffortLow, EffortMedium, EffortHigh, EffortMax} {
		got := translateResponsesReasoning(e)
		if got == nil || got.Effort != string(e) {
			t.Errorf("%q = %+v, want Effort=%q", e, got, string(e))
		}
	}
}
