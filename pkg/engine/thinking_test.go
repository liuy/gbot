package engine

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/llm"
)

func TestResolveThinking(t *testing.T) {
	t.Parallel()

	mk := func(perModel map[string]llm.Effort) *Engine {
		return &Engine{modelThinking: perModel}
	}

	// Cold start: no override and no config anywhere resolves to "" (=auto).
	for name, eng := range map[string]*Engine{
		"nil map":          mk(nil),
		"empty map":        mk(map[string]llm.Effort{}),
		"model not set":    mk(map[string]llm.Effort{"opus": llm.EffortNone}),
		"model value \"\"": mk(map[string]llm.Effort{"opus": ""}),
	} {
		if got := eng.resolveThinking("any-model"); got != "" {
			t.Errorf("%s: resolveThinking = %q, want \"\"", name, got)
		}
		if got := eng.Thinking(); got != llm.EffortAuto {
			t.Errorf("%s: Thinking() = %q, want auto (getter normalizes empty)", name, got)
		}
	}

	// Hot path: config baseline, then override precedence — including an
	// explicit auto override beating a none baseline.
	eng := mk(map[string]llm.Effort{"opus": llm.EffortNone})
	if got := eng.resolveThinking("opus"); got != llm.EffortNone {
		t.Fatalf("baseline: resolveThinking = %q, want none", got)
	}
	if err := eng.SetThinking(llm.EffortHigh); err != nil {
		t.Fatalf("SetThinking(high): %v", err)
	}
	if got := eng.resolveThinking("opus"); got != llm.EffortHigh {
		t.Fatalf("override: resolveThinking = %q, want high", got)
	}
	if err := eng.SetThinking(llm.EffortAuto); err != nil {
		t.Fatalf("SetThinking(auto): %v", err)
	}
	if got := eng.resolveThinking("opus"); got != llm.EffortAuto {
		t.Fatalf("explicit auto: resolveThinking = %q, want auto over none baseline", got)
	}
	if got := eng.Thinking(); got != llm.EffortAuto {
		t.Fatalf("Thinking() = %q, want auto", got)
	}

	// Override stickiness across model switches, and baseline re-resolution
	// when no override exists.
	eng.SetModel("haiku")
	if got := eng.resolveThinking("haiku"); got != llm.EffortAuto {
		t.Fatalf("override must survive model switch: resolveThinking = %q, want auto (override)", got)
	}
	noOverride := mk(map[string]llm.Effort{"opus": llm.EffortNone, "haiku": llm.EffortMax})
	noOverride.SetModel("haiku")
	if got := noOverride.resolveThinking("haiku"); got != llm.EffortMax {
		t.Fatalf("no override: resolveThinking = %q, want max (new model's baseline)", got)
	}
	noOverride.SetModel("opus")
	if got := noOverride.resolveThinking("opus"); got != llm.EffortNone {
		t.Fatalf("no override: resolveThinking = %q, want none (new model's baseline)", got)
	}
}

func TestSetThinking_RejectsIllegalValues(t *testing.T) {
	t.Parallel()

	eng := &Engine{}
	if err := eng.SetThinking("bogus"); err == nil {
		t.Fatal("SetThinking(bogus) = nil error, want rejection")
	} else if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("SetThinking(bogus) error = %q, want it to name the offending value", err.Error())
	}
	// "" passes Valid() but would silently clear the override — it must be
	// rejected so an explicit auto choice goes through EffortAuto.
	if err := eng.SetThinking(""); err == nil {
		t.Fatal("SetThinking(\"\") = nil error, want rejection")
	} else if !strings.Contains(err.Error(), "auto") {
		t.Errorf("SetThinking(\"\") error = %q, want it to point at auto", err.Error())
	}
	if got := eng.Thinking(); got != llm.EffortAuto {
		t.Errorf("after rejections Thinking() = %q, want auto (state untouched)", got)
	}
}

func TestResolveThinking_ParamsConstruction(t *testing.T) {
	t.Parallel()

	eng := New(&Params{ModelThinking: map[string]llm.Effort{"opus": llm.EffortMax}, Model: "opus"})
	if got := eng.resolveThinking("opus"); got != llm.EffortMax {
		t.Errorf("resolveThinking = %q, want max (baseline via Params constructor)", got)
	}
	if got := eng.Thinking(); got != llm.EffortMax {
		t.Errorf("Thinking() = %q, want max", got)
	}
}
