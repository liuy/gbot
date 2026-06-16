package engine

import (
	"testing"

	"github.com/liuy/gbot/pkg/llm"
)

func TestResolveThinking(t *testing.T) {
	t.Parallel()

	mk := func(perModel map[string]llm.ThinkingMode) *Engine {
		return &Engine{modelThinking: perModel}
	}

	tests := []struct {
		name     string
		engine   *Engine
		model    string
		wantType string // expected ThinkingConfig.Type; "" means nil config
		wantNil  bool
	}{
		{"nil map", mk(nil), "any-model", "", true},
		{"empty map", mk(map[string]llm.ThinkingMode{}), "any-model", "", true},
		{"model not in map", mk(map[string]llm.ThinkingMode{
			"opus": llm.ThinkingAdaptive,
		}), "haiku", "", true},
		{"model with empty value", mk(map[string]llm.ThinkingMode{
			"opus": "",
		}), "opus", "", true},
		{"adaptive", mk(map[string]llm.ThinkingMode{
			"opus": llm.ThinkingAdaptive,
		}), "opus", "adaptive", false},
		{"disabled", mk(map[string]llm.ThinkingMode{
			"opus": llm.ThinkingDisabled,
		}), "opus", "disabled", false},
		{"enabled", mk(map[string]llm.ThinkingMode{
			"opus": llm.ThinkingEnabled,
		}), "opus", "enabled", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.engine.resolveThinking(tt.model)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("want nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("want non-nil with Type=%q, got nil", tt.wantType)
			}
			if got.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", got.Type, tt.wantType)
			}
		})
	}
}
