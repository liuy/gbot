package llm

import "testing"

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

func TestTranslateThinking(t *testing.T) {
	tests := []struct {
		name  string
		level ThinkingMode
		want  *ThinkingConfig
	}{
		{"empty omits field", "", nil},
		{"disabled", ThinkingDisabled, &ThinkingConfig{Type: "disabled"}},
		{"enabled", ThinkingEnabled, &ThinkingConfig{Type: "enabled"}},
		{"adaptive", ThinkingAdaptive, &ThinkingConfig{Type: "adaptive"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TranslateThinking(tt.level)
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("nil mismatch: got %v, want %v", got, tt.want)
			}
			if got == nil {
				return
			}
			if got.Type != tt.want.Type {
				t.Errorf("Type = %q, want %q", got.Type, tt.want.Type)
			}
		})
	}
}
