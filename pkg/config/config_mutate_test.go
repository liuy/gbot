package config

import (
	"net/http"
	"testing"
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
