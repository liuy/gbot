package wui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
)

// stubLLMProvider is a minimal mock that satisfies llm.Provider for config tests.
type stubLLMProvider struct {
	name string
}

func (m *stubLLMProvider) Name() string { return m.name }
func (m *stubLLMProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, nil
}
func (m *stubLLMProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	return nil, nil
}

// buildTestProviderConfigs creates provider configs with 2 regular + 1 free provider.
func buildTestProviderConfigs() map[string]*config.Provider {
	zhipu := &config.Provider{Name: "zhipu"}
	zhipu.Models.Set("glm-5.2", config.ModelConfig{})
	zhipu.Models.Set("glm-4.6", config.ModelConfig{})

	openai := &config.Provider{Name: "openai"}
	openai.Models.Set("gpt-5", config.ModelConfig{})
	openai.Models.Set("gpt-4.1", config.ModelConfig{})

	free := &config.Provider{Name: "openrouter-free", Free: true}
	free.Models.Set("llama-free", config.ModelConfig{})
	free.Models.Set("qwen-free", config.ModelConfig{})

	return map[string]*config.Provider{
		"zhipu":           zhipu,
		"openai":          openai,
		"openrouter-free": free,
	}
}

// buildTestProviders creates llm.Provider stubs matching the config names.
func buildTestProviders() map[string]llm.Provider {
	return map[string]llm.Provider{
		"zhipu":           &stubLLMProvider{name: "zhipu"},
		"openai":          &stubLLMProvider{name: "openai"},
		"openrouter-free": &stubLLMProvider{name: "openrouter-free"},
	}
}

func TestBuildConfigMessage_Ordering(t *testing.T) {
	c := newTestConnectorWithConfig(t, hub.NewHub(), buildTestProviders(), buildTestProviderConfigs())
	c.mock().providerFn = func() llm.Provider { return &stubLLMProvider{name: "zhipu"} }
	c.mock().modelFn = func() string { return "glm-5.2" }

	payload := c.buildConfig(c.activeSlotTest(t))
	var msg struct {
		Type   string `json:"type"`
		Models []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"models"`
		Current struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"current"`
		Thinking string `json:"thinking"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "config" {
		t.Fatalf("type = %q, want config", msg.Type)
	}
	if msg.Thinking != "auto" {
		t.Errorf("thinking = %q, want auto (mockEngine default)", msg.Thinking)
	}
	if len(msg.Models) != 6 {
		t.Fatalf("models length = %d, want 6", len(msg.Models))
	}
	// Regular providers first (alphabetical): openai, zhipu.
	// Free providers last: openrouter-free.
	if msg.Models[0].Provider != "openai" {
		t.Errorf("models[0].provider = %q, want openai", msg.Models[0].Provider)
	}
	if msg.Models[2].Provider != "zhipu" {
		t.Errorf("models[2].provider = %q, want zhipu", msg.Models[2].Provider)
	}
	if msg.Models[4].Provider != "openrouter-free" {
		t.Errorf("models[4].provider = %q, want openrouter-free", msg.Models[4].Provider)
	}
	if msg.Current.Provider != "zhipu" {
		t.Errorf("current.provider = %q, want zhipu", msg.Current.Provider)
	}
	if msg.Current.Model != "glm-5.2" {
		t.Errorf("current.model = %q, want glm-5.2", msg.Current.Model)
	}
}

func TestBuildConfigMessage_Empty(t *testing.T) {
	c := newTestConnectorWithConfig(t, hub.NewHub(), nil, nil)

	payload := c.buildConfig(c.activeSlotTest(t))
	var msg struct {
		Type   string `json:"type"`
		Models []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"models"`
		Current struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"current"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "config" {
		t.Fatalf("type = %q, want config", msg.Type)
	}
	if len(msg.Models) != 0 {
		t.Fatalf("models length = %d, want 0", len(msg.Models))
	}
	if msg.Current.Model != "glm-5.2" {
		t.Errorf("current.model = %q, want glm-5.2 (engine default)", msg.Current.Model)
	}
}

func TestTakeover_PushesConfigAfterConnectStatus(t *testing.T) {
	c := newTestConnectorWithConfig(t, hub.NewHub(), buildTestProviders(), buildTestProviderConfigs())
	c.mock().providerFn = func() llm.Provider { return &stubLLMProvider{name: "zhipu"} }
	c.mock().modelFn = func() string { return "glm-5.2" }
	c.mock().messagesFn = nil

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	defer ws.Close()

	// The server now sends a single metadata frame containing connect, config,
	// engines, history, stats as nested JSON fields.
	data := readWSMessage(t, ws)
	var meta struct {
		Type    string          `json:"type"`
		Connect json.RawMessage `json:"connect"`
		Config  json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if meta.Type != "metadata" {
		t.Fatalf("type = %q, want metadata", meta.Type)
	}

	// Verify connect field.
	var connect struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(meta.Connect, &connect); err != nil {
		t.Fatalf("unmarshal connect: %v", err)
	}
	if connect.Type != "connect_status" {
		t.Fatalf("connect.type = %q, want connect_status", connect.Type)
	}

	// Verify config field.
	var config struct {
		Type   string `json:"type"`
		Models []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"models"`
	}
	if err := json.Unmarshal(meta.Config, &config); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if config.Type != "config" {
		t.Fatalf("config.type = %q, want config", config.Type)
	}
	if len(config.Models) != 6 {
		t.Fatalf("config models length = %d, want 6", len(config.Models))
	}
}

func TestModelSwitch_CallsSetProviderAndSetModel(t *testing.T) {
	providers := buildTestProviders()
	configs := buildTestProviderConfigs()
	c := newTestConnectorWithConfig(t, hub.NewHub(), providers, configs)
	c.mock().providerFn = func() llm.Provider { return &stubLLMProvider{name: "zhipu"} }

	ws := dialAndStore(t, c)
	defer ws.Close()

	switchMsg, _ := json.Marshal(map[string]string{
		"type":     "model_switch",
		"provider": "openai",
		"model":    "gpt-5",
	})
	if err := ws.WriteMessage(websocket.TextMessage, switchMsg); err != nil {
		t.Fatalf("write model_switch: %v", err)
	}

	if !waitFor(10e9, func() bool {
		c.mock().mu.Lock()
		defer c.mock().mu.Unlock()
		return len(c.mock().setProviderCalls) > 0 && len(c.mock().setModelCalls) > 0
	}) {
		t.Fatal("timeout waiting for SetProvider/SetModel calls")
	}

	c.mock().mu.Lock()
	defer c.mock().mu.Unlock()
	if len(c.mock().setProviderCalls) != 1 || c.mock().setProviderCalls[0] != "openai" {
		t.Errorf("setProviderCalls = %v, want [openai]", c.mock().setProviderCalls)
	}
	if len(c.mock().setModelCalls) != 1 || c.mock().setModelCalls[0] != "gpt-5" {
		t.Errorf("setModelCalls = %v, want [gpt-5]", c.mock().setModelCalls)
	}

	// Model switch must NOT push connect_status — that would trigger
	// resetAllState on the client, wiping the chat. It SHOULD respond with
	// model_switched carrying contextUsed + contextTotal so the client
	// header updates immediately.
	data := readWSMessage(t, ws)
	var resp struct {
		Type         string `json:"type"`
		ContextUsed  int    `json:"contextUsed"`
		ContextTotal int    `json:"contextTotal"`
		Thinking     string `json:"thinking"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Type != "model_switched" {
		t.Fatalf("type = %q, want model_switched", resp.Type)
	}
	if resp.ContextTotal != 200000 {
		t.Errorf("contextTotal = %d, want 200000 (mockEngine default)", resp.ContextTotal)
	}
	if resp.Thinking != "auto" {
		t.Errorf("thinking = %q, want auto (effort re-resolved after the model switch)", resp.Thinking)
	}
}

func TestModelSwitch_UnknownProvider(t *testing.T) {
	c := newTestConnectorWithConfig(t, hub.NewHub(), buildTestProviders(), buildTestProviderConfigs())

	ws := dialAndStore(t, c)
	defer ws.Close()

	switchMsg, _ := json.Marshal(map[string]string{
		"type":     "model_switch",
		"provider": "nonexistent",
		"model":    "whatever",
	})
	if err := ws.WriteMessage(websocket.TextMessage, switchMsg); err != nil {
		t.Fatalf("write model_switch: %v", err)
	}

	data := readWSMessage(t, ws)
	var resp struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Type != "error" {
		t.Fatalf("type = %q, want error", resp.Type)
	}
	if !strings.Contains(resp.Message, "nonexistent") {
		t.Errorf("message = %q, want contains 'nonexistent'", resp.Message)
	}

	c.mock().mu.Lock()
	defer c.mock().mu.Unlock()
	if len(c.mock().setProviderCalls) != 0 {
		t.Errorf("setProviderCalls = %v, want empty", c.mock().setProviderCalls)
	}
}

func TestModelSwitch_UnknownModel(t *testing.T) {
	c := newTestConnectorWithConfig(t, hub.NewHub(), buildTestProviders(), buildTestProviderConfigs())

	ws := dialAndStore(t, c)
	defer ws.Close()

	switchMsg, _ := json.Marshal(map[string]string{
		"type":     "model_switch",
		"provider": "zhipu",
		"model":    "nonexistent-model",
	})
	if err := ws.WriteMessage(websocket.TextMessage, switchMsg); err != nil {
		t.Fatalf("write model_switch: %v", err)
	}

	data := readWSMessage(t, ws)
	var resp struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Type != "error" {
		t.Fatalf("type = %q, want error", resp.Type)
	}
	if !strings.Contains(resp.Message, "nonexistent-model") {
		t.Errorf("message = %q, want contains model name", resp.Message)
	}
}

func TestModelSwitch_SyncsCapabilities(t *testing.T) {
	providers := buildTestProviders()
	configs := buildTestProviderConfigs()
	c := newTestConnectorWithConfig(t, hub.NewHub(), providers, configs)
	c.mock().providerFn = func() llm.Provider { return &stubLLMProvider{name: "zhipu"} }

	ws := dialAndStore(t, c)
	defer ws.Close()

	switchMsg, _ := json.Marshal(map[string]string{
		"type":     "model_switch",
		"provider": "openai",
		"model":    "gpt-5",
	})
	if err := ws.WriteMessage(websocket.TextMessage, switchMsg); err != nil {
		t.Fatalf("write model_switch: %v", err)
	}

	if !waitFor(10e9, func() bool {
		c.mock().mu.Lock()
		defer c.mock().mu.Unlock()
		return c.mock().updateAutoCalls > 0 && len(c.mock().setMaxTokensCalls) > 0
	}) {
		t.Fatal("timeout waiting for capability sync calls")
	}

	c.mock().mu.Lock()
	defer c.mock().mu.Unlock()
	if len(c.mock().setMaxTokensCalls) != 1 {
		t.Errorf("setMaxTokensCalls = %v, want 1 call", c.mock().setMaxTokensCalls)
	}
	if c.mock().updateAutoCalls != 1 {
		t.Errorf("updateAutoCalls = %d, want 1", c.mock().updateAutoCalls)
	}
}
