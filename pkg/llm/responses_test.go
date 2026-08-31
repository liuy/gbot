package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// newResponsesTestProvider returns a provider with defaults suitable for
// direct translateRequest assertions.
func newResponsesTestProvider() *ResponsesProvider {
	return NewResponsesProvider(&ResponsesConfig{
		APIKey: "test-key",
		Model:  "glm-4.6",
	})
}

// translateToMap runs translateRequest and decodes the JSON body for assertions.
func translateToMap(t *testing.T, p *ResponsesProvider, req *Request, stream bool) map[string]any {
	t.Helper()
	body, err := p.translateRequest(req, stream)
	if err != nil {
		t.Fatalf("translateRequest: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal request body %s: %v", body, err)
	}
	return m
}

func userTextMessage(text string) types.Message {
	return types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(text)}}
}

// ---------------------------------------------------------------------------
// Step 1 — request shape
// ---------------------------------------------------------------------------

func TestResponsesStreamRetryExhausted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	p := NewResponsesProvider(&ResponsesConfig{APIKey: "k", BaseURL: server.URL, Model: "glm-4.6"})
	// Shrink retries: default 10 with exponential backoff would sleep for
	// minutes — this test only needs the terminal-failure path.
	p.retryConfig = &RetryConfig{MaxRetries: 1, BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}
	_, err := p.Stream(context.Background(), &Request{
		Model:    "glm-4.6",
		Messages: []types.Message{userTextMessage("hi")},
	})
	if err == nil {
		t.Fatal("exhausted retries must surface an error")
	}
}

func TestResponsesParseHTTPErrorStatusBranches(t *testing.T) {
	// 429 and 403 map to their typed codes — the Retryable flag decides the
	// stream retry loop, so a mis-typed status silently changes retry behavior.
	cases := []struct {
		status int
		code   string
	}{
		{429, "rate_limit_error"},
		{403, "permission_error"},
	}
	for _, c := range cases {
		e := (&ResponsesProvider{}).parseHTTPError([]byte(`{"error":{"message":"boom"}}`), c.status)
		if e.ErrorCode != c.code || e.Type != c.code {
			t.Errorf("status %d: code/type = %q/%q, want %q", c.status, e.ErrorCode, e.Type, c.code)
		}
	}
}

func TestResponsesTranslateResponseStatusFailedNoError(t *testing.T) {
	// failed without an error object must degrade to the raw body, not panic.
	_, err := (&ResponsesProvider{}).translateResponse([]byte(`{"id":"resp_1","status":"failed"}`))
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Message == "" {
		t.Error("message must fall back to the raw body")
	}
}

func TestResponsesTranslateResponseStatusFailed(t *testing.T) {
	// A 200 body with status:"failed" must surface as an error — the empty
	// end_turn reply it replaced silently poisoned the next turn's history.
	body := []byte(`{"id":"resp_1","status":"failed","error":{"type":"server_error","message":"upstream exploded"}}`)
	_, err := (&ResponsesProvider{}).translateResponse(body)
	if err == nil {
		t.Fatal("status:failed must return an error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Message != "upstream exploded" {
		t.Errorf("message = %q, want upstream exploded", apiErr.Message)
	}
}

func TestResponsesTranslateRequest_Defaults(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	m := translateToMap(t, p, &Request{Messages: []types.Message{userTextMessage("hi")}}, false)

	if m["model"] != "glm-4.6" {
		t.Errorf("model = %v, want glm-4.6", m["model"])
	}
	if s, ok := m["store"].(bool); !ok || s {
		t.Errorf("store = %v, want false", m["store"])
	}
	if s, ok := m["stream"].(bool); !ok || s {
		t.Errorf("stream = %v, want false", m["stream"])
	}
	if n := m["max_output_tokens"]; n != float64(32768) {
		t.Errorf("max_output_tokens = %v, want 32768 default", n)
	}
	if _, present := m["reasoning"]; present {
		t.Error("reasoning key must be absent when Thinking is nil")
	}
	if _, present := m["prompt_cache_key"]; present {
		t.Error("prompt_cache_key must be absent when PromptStateKey is nil")
	}
	if _, present := m["tools"]; present {
		t.Error("tools key must be absent when req.Tools is empty")
	}
}

func TestResponsesTranslateRequest_MaxTokensPassthrough(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	m := translateToMap(t, p, &Request{MaxTokens: 4096, Messages: []types.Message{userTextMessage("hi")}}, true)

	if n := m["max_output_tokens"]; n != float64(4096) {
		t.Errorf("max_output_tokens = %v, want 4096", n)
	}
	if s, ok := m["stream"].(bool); !ok || !s {
		t.Errorf("stream = %v, want true", m["stream"])
	}
}

func TestResponsesTranslateRequest_Instructions(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	m := translateToMap(t, p, &Request{
		System:   json.RawMessage(`"be brief"`),
		Messages: []types.Message{userTextMessage("hi")},
	}, false)

	if m["instructions"] != "be brief" {
		t.Errorf("instructions = %v, want be brief", m["instructions"])
	}
}

func TestResponsesTranslateRequest_ThinkingStates(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()

	enabled := translateToMap(t, p, &Request{
		Thinking: &ThinkingConfig{Type: "enabled"},
		Messages: []types.Message{userTextMessage("hi")},
	}, false)
	// Effort is deliberately unset — gbot has no numeric thinking tier, and
	// GLM keeps thinking on regardless of the field. extra_params is the
	// manual override channel.
	if _, present := enabled["reasoning"]; present {
		t.Errorf("reasoning must stay unset, got %v", enabled["reasoning"])
	}
	adaptive := translateToMap(t, p, &Request{
		Thinking: &ThinkingConfig{Type: "adaptive"},
		Messages: []types.Message{userTextMessage("hi")},
	}, false)
	if _, present := adaptive["reasoning"]; present {
		t.Errorf("adaptive must stay unset too, got %v", adaptive["reasoning"])
	}

	disabled := translateToMap(t, p, &Request{
		Thinking: &ThinkingConfig{Type: "disabled"},
		Messages: []types.Message{userTextMessage("hi")},
	}, false)
	if _, present := disabled["reasoning"]; present {
		t.Error("disabled thinking must omit the reasoning key")
	}
}

func TestResponsesTranslateRequest_PromptCacheKey(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	key := &PromptStateKey{QuerySource: "repl_main_thread"}
	m := translateToMap(t, p, &Request{
		PromptStateKey: key,
		Messages:       []types.Message{userTextMessage("hi")},
	}, false)

	if m["prompt_cache_key"] != key.String() {
		t.Errorf("prompt_cache_key = %v, want %q", m["prompt_cache_key"], key.String())
	}
}

func TestResponsesTranslateRequest_TemperaturePassthrough(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	temp := 0.7
	m := translateToMap(t, p, &Request{
		Temperature: &temp,
		Messages:    []types.Message{userTextMessage("hi")},
	}, false)

	if v, ok := m["temperature"].(float64); !ok || v != 0.7 {
		t.Errorf("temperature = %v, want 0.7", m["temperature"])
	}
}

func TestResponsesTranslateRequest_Tools(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	m := translateToMap(t, p, &Request{
		Tools: []ToolDef{{
			Name:        "bash",
			Description: "run a command",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}}}`),
		}},
		Messages: []types.Message{userTextMessage("hi")},
	}, false)

	tools, ok := m["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v, want one entry", m["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tools[0] = %T, want map", tools[0])
	}
	if tool["type"] != "function" {
		t.Errorf("tools[0].type = %v, want function", tool["type"])
	}
	if tool["name"] != "bash" {
		t.Errorf("tools[0].name = %v, want bash", tool["name"])
	}
	if tool["description"] != "run a command" {
		t.Errorf("tools[0].description = %v, want 'run a command'", tool["description"])
	}
	params, ok := tool["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("tools[0].parameters = %T, want object", tool["parameters"])
	}
	if params["type"] != "object" {
		t.Errorf("tools[0].parameters.type = %v, want object", params["type"])
	}
	if _, present := tool["strict"]; present {
		t.Error("tools[0] must not carry a strict key")
	}
}

func TestResponsesTranslateRequest_SingleUserText(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	m := translateToMap(t, p, &Request{Messages: []types.Message{userTextMessage("hello")}}, false)

	input, ok := m["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input = %v, want one item", m["input"])
	}
	item, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("input[0] = %T, want map", input[0])
	}
	if item["type"] != "message" {
		t.Errorf("input[0].type = %v, want message", item["type"])
	}
	if item["role"] != "user" {
		t.Errorf("input[0].role = %v, want user", item["role"])
	}
	content, ok := item["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("input[0].content = %v, want one part", item["content"])
	}
	part, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] = %T, want map", content[0])
	}
	if part["type"] != "input_text" || part["text"] != "hello" {
		t.Errorf("content[0] = %v, want input_text/hello", part)
	}
}

// TestResponsesTranslateRequest_MultiTurnReplay asserts the exact input
// sequence for a tool-loop turn: [reasoning, message(output_text),
// function_call, function_call_output, message(user)].
func TestResponsesTranslateRequest_MultiTurnReplay(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	messages := []types.Message{
		{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeThinking, Thinking: "let me look"},
				types.NewTextBlock("I'll check."),
				{Type: types.ContentTypeToolUse, ID: "call_1", Name: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)},
			},
		},
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{
					Type:      types.ContentTypeToolResult,
					ToolUseID: "call_1",
					Content:   json.RawMessage(`[{"type":"text","text":"file_a\nfile_b"}]`),
				},
				types.NewTextBlock("what did you find?"),
			},
		},
	}

	m := translateToMap(t, p, &Request{Messages: messages}, false)

	input, ok := m["input"].([]any)
	if !ok {
		t.Fatalf("input = %T, want array", m["input"])
	}
	if len(input) != 5 {
		t.Fatalf("input has %d items, want 5: %v", len(input), input)
	}

	wantKinds := []struct{ typ, subkey, subval string }{
		{"reasoning", "", ""},
		{"message", "role", "assistant"},
		{"function_call", "", ""},
		{"function_call_output", "", ""},
		{"message", "role", "user"},
	}
	for i, w := range wantKinds {
		item, ok := input[i].(map[string]any)
		if !ok {
			t.Fatalf("input[%d] = %T, want map", i, input[i])
		}
		if item["type"] != w.typ {
			t.Errorf("input[%d].type = %v, want %s (full: %v)", i, item["type"], w.typ, input)
		}
		if w.subkey != "" && item[w.subkey] != w.subval {
			t.Errorf("input[%d].%s = %v, want %v", i, w.subkey, item[w.subkey], w.subval)
		}
	}

	reasoning := input[0].(map[string]any)
	rContent := reasoning["content"].([]any)
	rPart := rContent[0].(map[string]any)
	if rPart["type"] != "reasoning_text" || rPart["text"] != "let me look" {
		t.Errorf("reasoning content = %v, want reasoning_text/let me look", rPart)
	}

	assistantMsg := input[1].(map[string]any)
	aContent := assistantMsg["content"].([]any)
	aPart := aContent[0].(map[string]any)
	if aPart["type"] != "output_text" || aPart["text"] != "I'll check." {
		t.Errorf("assistant message content = %v, want output_text", aPart)
	}

	fc := input[2].(map[string]any)
	if fc["call_id"] != "call_1" || fc["name"] != "bash" || fc["arguments"] != `{"cmd":"ls"}` {
		t.Errorf("function_call = %v, want call_1/bash/{\"cmd\":\"ls\"}", fc)
	}
	if _, present := fc["id"]; present {
		t.Error("function_call must not carry an id key on send")
	}

	fco := input[3].(map[string]any)
	if fco["call_id"] != "call_1" || fco["output"] != "file_a\nfile_b" {
		t.Errorf("function_call_output = %v, want call_1/file listing", fco)
	}

	userMsg := input[4].(map[string]any)
	uContent := userMsg["content"].([]any)
	if len(uContent) != 1 {
		t.Fatalf("user message content has %d parts, want 1", len(uContent))
	}
	uPart := uContent[0].(map[string]any)
	if uPart["type"] != "input_text" || uPart["text"] != "what did you find?" {
		t.Errorf("user content = %v, want input_text", uPart)
	}
}

// TestResponsesTranslateRequest_ImageAndEmptyToolResult covers user image
// blocks (data URL) and empty tool_result content (output "").
func TestResponsesTranslateRequest_ImageAndEmptyToolResult(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "call_9", Name: "screenshot", Input: json.RawMessage(`{}`)},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "call_9"},
			types.NewTextBlock("describe this"),
			{Type: types.ContentTypeImage, Source: &types.ImageSource{
				Type:      "base64",
				MediaType: "image/png",
				Data:      "aGVsbG8=",
			}},
		}},
	}

	m := translateToMap(t, p, &Request{Messages: messages}, false)
	input := m["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input has %d items, want 3: %v", len(input), input)
	}

	fc := input[0].(map[string]any)
	if fc["type"] != "function_call" || fc["call_id"] != "call_9" || fc["arguments"] != "{}" {
		t.Errorf("input[0] = %v, want function_call call_9/{}", fc)
	}

	fco := input[1].(map[string]any)
	if fco["type"] != "function_call_output" {
		t.Errorf("input[1].type = %v, want function_call_output", fco["type"])
	}
	if out, present := fco["output"]; !present || out != "" {
		t.Errorf("function_call_output.output = %v (present=%v), want empty string present", out, present)
	}

	userMsg := input[2].(map[string]any)
	content := userMsg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("user message content has %d parts, want 2 (text + image)", len(content))
	}
	if content[0].(map[string]any)["type"] != "input_text" {
		t.Errorf("content[0].type = %v, want input_text (text before image)", content[0])
	}
	img := content[1].(map[string]any)
	if img["type"] != "input_image" {
		t.Errorf("content[1].type = %v, want input_image", img["type"])
	}
	if img["image_url"] != "data:image/png;base64,aGVsbG8=" {
		t.Errorf("image_url = %v, want data URL", img["image_url"])
	}
}

// TestResponsesTranslateRequest_RedactedThinkingDropped verifies that
// redacted_thinking produces no item carrying encrypted content.
func TestResponsesTranslateRequest_RedactedThinkingDropped(t *testing.T) {
	t.Parallel()

	p := newResponsesTestProvider()
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeRedacted, Data: "opaque-blob"},
			types.NewTextBlock("answer"),
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("next")}},
	}

	m := translateToMap(t, p, &Request{Messages: messages}, false)
	raw, err := json.Marshal(m["input"])
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	if strings.Contains(string(raw), "encrypted") {
		t.Errorf("input must not contain encrypted content: %s", raw)
	}

	input := m["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("input has %d items, want 2 (redacted dropped): %v", len(input), input)
	}
	if input[0].(map[string]any)["type"] != "message" {
		t.Errorf("input[0].type = %v, want message", input[0].(map[string]any)["type"])
	}
}

// TestResponsesTranslateRequest_ExtraParamsMerge verifies extra_params can
// override the reasoning object wholesale.
func TestResponsesTranslateRequest_ExtraParamsMerge(t *testing.T) {
	t.Parallel()

	p := NewResponsesProvider(&ResponsesConfig{
		APIKey:      "test-key",
		Model:       "glm-4.6",
		ExtraParams: map[string]any{"reasoning": map[string]any{"effort": "high"}},
	})
	m := translateToMap(t, p, &Request{Messages: []types.Message{userTextMessage("hi")}}, false)

	reasoning, ok := m["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning = %v, want object from extra_params", m["reasoning"])
	}
	if reasoning["effort"] != "high" {
		t.Errorf("reasoning.effort = %v, want high (extra_params override)", reasoning["effort"])
	}
}

// ---------------------------------------------------------------------------
// Step 2 — Complete (httptest)
// ---------------------------------------------------------------------------

func newResponsesProviderWithServer(server *httptest.Server) *ResponsesProvider {
	return NewResponsesProvider(&ResponsesConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "glm-4.6",
	})
}

func defaultResponsesRequest() *Request {
	return &Request{
		Model:     "glm-4.6",
		MaxTokens: 1024,
		System:    json.RawMessage(`"be brief"`),
		Messages:  []types.Message{userTextMessage("hello")},
	}
}

func TestResponsesComplete_TextResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q, want /responses", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", r.Header.Get("Authorization"))
		}

		var reqBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if s, ok := reqBody["stream"].(bool); !ok || s {
			t.Errorf("stream = %v, want false", reqBody["stream"])
		}
		if s, ok := reqBody["store"].(bool); !ok || s {
			t.Errorf("store = %v, want false", reqBody["store"])
		}
		if reqBody["instructions"] != "be brief" {
			t.Errorf("instructions = %v, want be brief", reqBody["instructions"])
		}
		input := reqBody["input"].([]any)
		if len(input) != 1 {
			t.Fatalf("input has %d items, want 1", len(input))
		}
		item := input[0].(map[string]any)
		if item["type"] != "message" || item["role"] != "user" {
			t.Errorf("input[0] = %v, want user message", item)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"id": "resp_123",
			"model": "glm-4.6",
			"status": "completed",
			"output": [
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello!"}]}
			],
			"usage": {
				"input_tokens": 100,
				"input_tokens_details": {"cached_tokens": 40},
				"output_tokens": 12,
				"total_tokens": 112
			}
		}`)
	}))
	defer server.Close()

	resp, err := newResponsesProviderWithServer(server).Complete(context.Background(), defaultResponsesRequest())
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	if resp.ID != "resp_123" {
		t.Errorf("ID = %q, want resp_123", resp.ID)
	}
	if resp.Model != "glm-4.6" {
		t.Errorf("Model = %q, want glm-4.6", resp.Model)
	}
	if resp.Role != "assistant" || resp.Type != "message" {
		t.Errorf("Role/Type = %q/%q, want assistant/message", resp.Role, resp.Type)
	}
	if len(resp.Content) != 1 {
		t.Fatalf("Content length = %d, want 1", len(resp.Content))
	}
	if resp.Content[0].Type != types.ContentTypeText || resp.Content[0].Text != "Hello!" {
		t.Errorf("Content[0] = %+v, want text Hello!", resp.Content[0])
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", resp.StopReason)
	}
	if resp.Usage.InputTokens != 60 {
		t.Errorf("Usage.InputTokens = %d, want 60 (100 minus 40 cached)", resp.Usage.InputTokens)
	}
	if resp.Usage.CacheReadInputTokens != 40 {
		t.Errorf("Usage.CacheReadInputTokens = %d, want 40", resp.Usage.CacheReadInputTokens)
	}
	if resp.Usage.OutputTokens != 12 {
		t.Errorf("Usage.OutputTokens = %d, want 12", resp.Usage.OutputTokens)
	}
}

func TestResponsesComplete_ThinkingAndToolCallResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"id": "resp_456",
			"model": "glm-4.6",
			"status": "completed",
			"output": [
				{"type":"reasoning","content":[{"type":"reasoning_text","text":"pondering"}]},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Checking."}]},
				{"type":"function_call","call_id":"call_1","name":"bash","arguments":"{\"cmd\":\"ls\"}"}
			],
			"usage": {"input_tokens": 10, "output_tokens": 20, "total_tokens": 30}
		}`)
	}))
	defer server.Close()

	resp, err := newResponsesProviderWithServer(server).Complete(context.Background(), defaultResponsesRequest())
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	if len(resp.Content) != 3 {
		t.Fatalf("Content length = %d, want 3", len(resp.Content))
	}
	if resp.Content[0].Type != types.ContentTypeThinking || resp.Content[0].Thinking != "pondering" {
		t.Errorf("Content[0] = %+v, want thinking/pondering", resp.Content[0])
	}
	if resp.Content[1].Type != types.ContentTypeText || resp.Content[1].Text != "Checking." {
		t.Errorf("Content[1] = %+v, want text Checking.", resp.Content[1])
	}
	tool := resp.Content[2]
	if tool.Type != types.ContentTypeToolUse {
		t.Errorf("Content[2].Type = %q, want tool_use", tool.Type)
	}
	if tool.ID != "call_1" || tool.Name != "bash" {
		t.Errorf("Content[2] ID/Name = %q/%q, want call_1/bash", tool.ID, tool.Name)
	}
	if string(tool.Input) != `{"cmd":"ls"}` {
		t.Errorf("Content[2].Input = %q, want arguments verbatim", string(tool.Input))
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use", resp.StopReason)
	}
}

func TestResponsesComplete_IncompleteMaxTokens(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"id": "resp_789",
			"model": "glm-4.6",
			"status": "incomplete",
			"incomplete_details": {"reason": "max_output_tokens"},
			"output": [
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"trunc"}]}
			],
			"usage": {"input_tokens": 5, "output_tokens": 9, "total_tokens": 14}
		}`)
	}))
	defer server.Close()

	resp, err := newResponsesProviderWithServer(server).Complete(context.Background(), defaultResponsesRequest())
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	if resp.StopReason != "max_tokens" {
		t.Errorf("StopReason = %q, want max_tokens (engine continuation trigger)", resp.StopReason)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "trunc" {
		t.Errorf("Content = %+v, want one text block", resp.Content)
	}
}

func TestResponsesComplete_ContextLengthExceeded(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"code":"context_length_exceeded","message":"too long","type":"invalid_request_error"}}`)
	}))
	defer server.Close()

	resp, err := newResponsesProviderWithServer(server).Complete(context.Background(), defaultResponsesRequest())
	if resp != nil {
		t.Errorf("expected nil response, got %+v", resp)
	}
	if !IsContextOverflow(err) {
		t.Fatalf("IsContextOverflow(%v) = false, want true", err)
	}
}

func TestResponsesComplete_AuthErrorNotRetryable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"code":"invalid_api_key","message":"Invalid API key","type":"invalid_request_error"}}`)
	}))
	defer server.Close()

	resp, err := newResponsesProviderWithServer(server).Complete(context.Background(), defaultResponsesRequest())
	if resp != nil {
		t.Errorf("expected nil response, got %+v", resp)
	}
	if IsRetryable(err) {
		t.Errorf("401 must not be retryable, IsRetryable(%v) = true", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Type != "authentication_error" {
		t.Errorf("Type = %q, want authentication_error", apiErr.Type)
	}
	if apiErr.Status != 401 {
		t.Errorf("Status = %d, want 401", apiErr.Status)
	}
}

func TestResponsesComplete_ServerErrorRetryableFields(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error":{"code":"internal","message":"boom","type":"server_error"}}`)
	}))
	defer server.Close()

	resp, err := newResponsesProviderWithServer(server).Complete(context.Background(), defaultResponsesRequest())
	if resp != nil {
		t.Errorf("expected nil response, got %+v", resp)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if !apiErr.Retryable {
		t.Error("500 must be retryable")
	}
	if apiErr.Type != "api_error" || apiErr.ErrorCode != "api_error" {
		t.Errorf("Type/ErrorCode = %q/%q, want api_error/api_error", apiErr.Type, apiErr.ErrorCode)
	}
	if apiErr.Message != "boom" {
		t.Errorf("Message = %q, want boom", apiErr.Message)
	}
}
