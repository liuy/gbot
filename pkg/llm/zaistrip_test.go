package llm

import (
	"strings"
	"testing"
)

func TestIsZAIHost(t *testing.T) {
	t.Parallel()
	cases := []struct {
		baseURL string
		want    bool
	}{
		{"https://api.anthropic.com", false},
		{"https://api.z.ai", true},
		{"https://api.z.ai/v1/messages", true},
		{"https://open.bigmodel.cn", true},
		{"https://open.bigmodel.cn/api/coding/paas/v4", true},
		{"https://api.openai.com", false},
	}
	for _, c := range cases {
		if got := isZAIHost(c.baseURL); got != c.want {
			t.Errorf("isZAIHost(%q) = %v, want %v", c.baseURL, got, c.want)
		}
	}
}

func TestDeepRemoveCacheControl_Nested(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"system": [
			{"type": "text", "text": "a", "cache_control": {"type": "ephemeral"}},
			{"type": "text", "text": "b"}
		],
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "hi", "cache_control": {"type": "ephemeral"}}
			]}
		],
		"tools": [
			{"name": "X", "cache_control": {"type": "ephemeral"}}
		]
	}`)
	got := stripCacheControlForZAI("https://api.z.ai", body)
	s := string(got)
	if strings.Contains(s, "cache_control") {
		t.Errorf("expected all cache_control removed, got:\n%s", s)
	}
	// Non-control fields preserved.
	if !strings.Contains(s, `"text":"a"`) {
		t.Error("text fields should be preserved")
	}
	if !strings.Contains(s, `"name":"X"`) {
		t.Error("tool name should be preserved")
	}
}

func TestStripCacheControlForZAI_OtherHostUntouched(t *testing.T) {
	t.Parallel()
	body := []byte(`{"system":[{"cache_control":{"type":"ephemeral"},"text":"a"}]}`)
	got := stripCacheControlForZAI("https://api.anthropic.com", body)
	if !strings.Contains(string(got), "cache_control") {
		t.Error("anthropic host should keep cache_control")
	}
}

func TestStripCacheControlForZAI_MalformedPassesThrough(t *testing.T) {
	t.Parallel()
	body := []byte(`not json {{{`)
	got := stripCacheControlForZAI("https://api.z.ai", body)
	if string(got) != string(body) {
		t.Error("malformed JSON should pass through unchanged")
	}
}
