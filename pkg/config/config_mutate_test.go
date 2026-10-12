package config

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// A single-element Input is still an override, not a miss.
func TestResolveInput_SingleModality(t *testing.T) {
	t.Parallel()
	p := &Provider{Models: NewModelsFromMap(map[string]ModelConfig{
		"vision": {Input: []string{"image"}},
	})}
	got := p.ResolveInput("vision")
	if len(got) != 1 || got[0] != "image" {
		t.Errorf("ResolveInput(vision) = %v, want [image]", got)
	}
}

// No proxy means the shared default client, not a fresh one.
func TestProxyHTTPClient_DefaultIdentity(t *testing.T) {
	t.Parallel()
	c := &Config{}
	if got := c.ProxyHTTPClient(); got != http.DefaultClient {
		t.Error("ProxyHTTPClient with no proxy must return http.DefaultClient")
	}
	c2 := &Config{Proxy: "http://127.0.0.1:8080"}
	if got := c2.ProxyHTTPClient(); got == http.DefaultClient {
		t.Error("ProxyHTTPClient with a proxy must return a dedicated client")
	}
}

// Empty model names inherit; unqualified names keep the model string.
func TestResolveModelByName_Shape(t *testing.T) {
	t.Parallel()
	c := &Config{Providers: []Provider{{Name: "anthropic", Models: NewModelsFromMap(map[string]ModelConfig{
		"claude-opus": {},
	})}}}
	p, name, err := c.ResolveModelByName("")
	if p != nil || name != "" || err != nil {
		t.Errorf("ResolveModelByName(\"\") = (%v, %q, %v), want (nil, \"\", nil)", p, name, err)
	}
	_, name, err = c.ResolveModelByName("claude-opus")
	if err != nil {
		t.Fatalf("ResolveModelByName(claude-opus): %v", err)
	}
	if name != "claude-opus" {
		t.Errorf("resolved name = %q, want claude-opus", name)
	}
}

// The rank sentinel for "no match" is -1.
func TestFindClosestMatchRank_NoMatchSentinel(t *testing.T) {
	t.Parallel()
	if model, dist := FindClosestMatchRank("", []string{"claude-opus"}); model != "" || dist != -1 {
		t.Errorf("empty input = (%q, %d), want (\"\", -1)", model, dist)
	}
	if model, dist := FindClosestMatchRank("claude", nil); model != "" || dist != -1 {
		t.Errorf("empty candidates = (%q, %d), want (\"\", -1)", model, dist)
	}
	model, dist := FindClosestMatchRank("claude-opus", []string{"claude-opus", "gpt-4"})
	if model != "claude-opus" || dist < 0 {
		t.Errorf("exact match = (%q, %d), want (claude-opus, >=0)", model, dist)
	}
}

// Non-empty input with non-empty candidates that match nothing must still
// reach the len(ranks)==0 sentinel — the earlier tests only exercise the
// empty-input and empty-candidates guards.
func TestFindClosestMatchRank_NoFuzzyMatchSentinel(t *testing.T) {
	t.Parallel()
	if model, dist := FindClosestMatchRank("zzzzz", []string{"MiniMax-M3", "glm-5.2"}); model != "" || dist != -1 {
		t.Errorf("no-fuzzy-match = (%q, %d), want (\"\", -1)", model, dist)
	}
}

// The distance is the real edit distance, not just "non-negative".
func TestFindClosestMatchRank_ExactDistance(t *testing.T) {
	t.Parallel()
	if model, dist := FindClosestMatchRank("glm5", []string{"glm-5.2"}); model != "glm-5.2" || dist != 1 {
		t.Errorf("glm5 = (%q, %d), want (glm-5.2, 1)", model, dist)
	}
}

// The Current flag must be evaluated inside the free-provider loop too,
// with BOTH operands discriminated: the current model name ("zhipu") is
// also a regular provider's NAME, and the current provider name
// ("orfree") is also a model name — so dropping either operand of the
// match marks the wrong item Current.
func TestBuildModelItems_CurrentInFreeProvider(t *testing.T) {
	t.Parallel()
	zhipu := &Provider{Name: "zhipu"}
	zhipu.Models.Set("glm-5.2", ModelConfig{})
	free := &Provider{Name: "orfree", FreeFetched: []string{"glm-5.2", "zhipu"}}
	free.Models.Set("glm-5.2", ModelConfig{})
	free.Models.Set("zhipu", ModelConfig{})

	items := BuildModelItems(
		map[string]*Provider{"zhipu": zhipu, "orfree": free},
		func(string) bool { return true },
		"orfree", "zhipu",
	)
	type item struct{ provider, model string }
	want := []item{
		{"zhipu", "glm-5.2"},
		{"orfree", "glm-5.2"},
		{"orfree", "zhipu"},
	}
	if len(items) != len(want) {
		t.Fatalf("items length = %d, want %d: %+v", len(items), len(want), items)
	}
	for i, w := range want {
		if items[i].Provider != w.provider || items[i].Model != w.model {
			t.Errorf("items[%d] = %s/%s, want %s/%s", i, items[i].Provider, items[i].Model, w.provider, w.model)
		}
	}
	if !items[2].Current {
		t.Error("items[2] (orfree/zhipu) must be Current")
	}
	if items[0].Current || items[1].Current {
		t.Errorf("only items[2] may be Current, got %v", []bool{items[0].Current, items[1].Current})
	}

	// Mirror image: the current MODEL name lives in the regular provider
	// while the current PROVIDER is the free one — dropping the provider
	// operand from the regular loop marks zhipu/glm-5.2 Current.
	items = BuildModelItems(
		map[string]*Provider{"zhipu": zhipu, "orfree": free},
		func(string) bool { return true },
		"orfree", "glm-5.2",
	)
	if len(items) != len(want) {
		t.Fatalf("second pass items length = %d, want %d: %+v", len(items), len(want), items)
	}
	if !items[1].Current {
		t.Error("second pass: items[1] (orfree/glm-5.2) must be Current")
	}
	if items[0].Current || items[2].Current {
		t.Errorf("second pass: only items[1] may be Current, got %v", []bool{items[0].Current, items[2].Current})
	}

	// Third pass: current provider is the REGULAR one but the free loop
	// holds a model with the same name — dropping the provider operand
	// from the free loop marks orfree/glm-5.2 Current.
	items = BuildModelItems(
		map[string]*Provider{"zhipu": zhipu, "orfree": free},
		func(string) bool { return true },
		"zhipu", "glm-5.2",
	)
	if len(items) != len(want) {
		t.Fatalf("third pass items length = %d, want %d: %+v", len(items), len(want), items)
	}
	if !items[0].Current {
		t.Error("third pass: items[0] (zhipu/glm-5.2) must be Current")
	}
	if items[1].Current || items[2].Current {
		t.Errorf("third pass: only items[0] may be Current, got %v", []bool{items[1].Current, items[2].Current})
	}
}

// The free loop must SKIP an absent provider, not stop: a present free
// provider listed after the absent one still contributes its models.
func TestBuildModelItems_SkipsAbsentFreeProvider(t *testing.T) {
	t.Parallel()
	zhipu := &Provider{Name: "zhipu"}
	zhipu.Models.Set("glm-5.2", ModelConfig{})
	absentFree := &Provider{Name: "afree", FreeFetched: []string{"llama-free"}}
	absentFree.Models.Set("llama-free", ModelConfig{})
	presentFree := &Provider{Name: "pfree", FreeFetched: []string{"qwen-free"}}
	presentFree.Models.Set("qwen-free", ModelConfig{})

	items := BuildModelItems(
		map[string]*Provider{"zhipu": zhipu, "afree": absentFree, "pfree": presentFree},
		func(name string) bool { return name != "afree" },
		"zhipu", "glm-5.2",
	)
	if len(items) != 2 {
		t.Fatalf("items length = %d, want 2 (absent free skipped, present free kept): %+v", len(items), items)
	}
	if items[0].Provider != "zhipu" || items[0].Model != "glm-5.2" || !items[0].Current {
		t.Errorf("items[0] = %s/%s current=%v, want zhipu/glm-5.2 current=true", items[0].Provider, items[0].Model, items[0].Current)
	}
	if items[1].Provider != "pfree" || items[1].Model != "qwen-free" || items[1].Current {
		t.Errorf("items[1] = %s/%s current=%v, want pfree/qwen-free current=false", items[1].Provider, items[1].Model, items[1].Current)
	}
}

// The empty-object guard must cover a Models whose store was allocated and
// then emptied by Delete — without it MarshalJSON emits a bare "}".
func TestModels_MarshalJSON_EmptyAfterDelete(t *testing.T) {
	t.Parallel()
	var m Models
	m.Set("a", ModelConfig{})
	m.Delete("a")
	b, err := m.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if string(b) != "{}" {
		t.Errorf("MarshalJSON after deleting the only key = %q, want {}", string(b))
	}
}

// Delete must splice exactly one key out of the order slice: bounds off by
// one drop neighbours, a missing break corrupts the tail.
func TestModels_Delete_OrderSplice(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		del  string
		want []string
	}{
		{"first", "a", []string{"b", "c"}},
		{"middle", "b", []string{"a", "c"}},
		{"last", "c", []string{"a", "b"}},
	} {
		var m Models
		m.Set("a", ModelConfig{})
		m.Set("b", ModelConfig{})
		m.Set("c", ModelConfig{})
		m.Delete(tc.del)
		if got := m.Ordered(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Delete(%q) Ordered = %v, want %v", tc.del, got, tc.want)
		}
		if m.Len() != 2 {
			t.Errorf("Delete(%q) Len = %d, want 2", tc.del, m.Len())
		}
		if m.Has(tc.del) {
			t.Errorf("Delete(%q): still present in store", tc.del)
		}
	}
}

// A truncated object must fail at the closing-delimiter check — swallowing
// that error would accept malformed settings.
func TestModels_UnmarshalJSON_TruncatedObject(t *testing.T) {
	t.Parallel()
	var m Models
	err := m.UnmarshalJSON([]byte(`{"a":{}`))
	if err == nil {
		t.Fatalf("UnmarshalJSON truncated object: got nil error, want decode error")
	}
	if err.Error() != "unexpected end of JSON input" {
		t.Errorf("UnmarshalJSON truncated object error = %q, want %q", err.Error(), "unexpected end of JSON input")
	}
}

// A valid entry followed by an invalid one must still return the error —
// an error-guard removal would keep the valid entry and report success.
func TestModels_UnmarshalJSON_InvalidValueAfterValid(t *testing.T) {
	t.Parallel()
	var m Models
	err := m.UnmarshalJSON([]byte(`{"a":{},"b":123}`))
	if err == nil {
		t.Fatalf("UnmarshalJSON invalid value: got nil error, want type error")
	}
	if !m.Has("a") || m.Has("b") || m.Len() != 1 {
		t.Errorf("failed UnmarshalJSON must keep only the entries parsed before the error, got Len=%d Ordered=%v", m.Len(), m.Ordered())
	}
}

// Defaults' error paths pin the exact wrapped message, the zeroed
// durations (a bare `return` would leak the successfully parsed idle), and
// the error chain through to time.ParseDuration's error.
func TestDreamConfig_Defaults_ExactErrors(t *testing.T) {
	t.Parallel()
	_, perr := time.ParseDuration("notaduration")
	_, cerr := time.ParseDuration("bad")

	enabled, idle, cooldown, err := DreamConfig{IdleThreshold: "notaduration"}.Defaults()
	if want := fmt.Sprintf("dream idle_threshold %q: %v", "notaduration", perr); err == nil || err.Error() != want {
		t.Errorf("idle error = %v, want %q", err, want)
	}
	if unwrapped := errors.Unwrap(err); unwrapped == nil || unwrapped.Error() != perr.Error() {
		t.Errorf("idle error must %%w-wrap the ParseDuration error, unwrapped=%v", unwrapped)
	}
	if enabled != true || idle != 0 || cooldown != 0 {
		t.Errorf("idle-error return = (%v, %v, %v), want (true, 0, 0)", enabled, idle, cooldown)
	}

	// idle parses fine; the cooldown failure must still zero BOTH durations.
	enabled, idle, cooldown, err = DreamConfig{Disabled: true, IdleThreshold: "30m", Cooldown: "bad"}.Defaults()
	if want := fmt.Sprintf("dream cooldown %q: %v", "bad", cerr); err == nil || err.Error() != want {
		t.Errorf("cooldown error = %v, want %q", err, want)
	}
	if unwrapped := errors.Unwrap(err); unwrapped == nil || unwrapped.Error() != cerr.Error() {
		t.Errorf("cooldown error must %%w-wrap the ParseDuration error, unwrapped=%v", unwrapped)
	}
	if enabled != false || idle != 0 || cooldown != 0 {
		t.Errorf("cooldown-error return = (%v, %v, %v), want (false, 0, 0)", enabled, idle, cooldown)
	}
}
