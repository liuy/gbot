package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// AnthropicProvider implements Provider for the Anthropic Messages API.
// Source: services/api/client.ts — 1:1 port of the SDK client.
type AnthropicProvider struct {
	BaseProvider
	apiKey  string
	baseURL string
	model   string
}

// AnthropicConfig configures the Anthropic provider.
type AnthropicConfig struct {
	APIKey      string
	BaseURL     string
	Model       string
	Timeout     time.Duration
	RetryConfig *RetryConfig
}

// NewAnthropicProvider creates a new Anthropic provider.
func NewAnthropicProvider(cfg *AnthropicConfig) *AnthropicProvider {
	if cfg.Timeout == 0 {
		cfg.Timeout = 300 * time.Second
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.anthropic.com"
	}

	return &AnthropicProvider{
		BaseProvider: BaseProvider{
			httpClient:  &http.Client{Timeout: cfg.Timeout},
			retryConfig: cfg.RetryConfig,
		},
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		model:   cfg.Model,
	}
}

// Complete sends a non-streaming request.
func (p *AnthropicProvider) Complete(ctx context.Context, req *Request) (*Response, error) {
	// Apply cache control to system blocks if configured.
	// Source: claude.ts:358-374
	applyCacheControlToSystem(req)

	req.Stream = false
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Record prompt state for cache break detection (pre-call, Phase 1).
	// Source: promptCacheBreakDetection.ts:247-430
	if req.PromptStateKey != nil {
		system := RequestToSystemMaps(req)
		tools := RequestToToolMaps(req)
		RecordPromptState(system, tools, *req.PromptStateKey, req.Model, nil, "", false, false, false, false, "", 0)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	p.setHeaders(httpReq)
	// Prompt caching beta header — only when cache control is configured.
	if req.CacheControl != nil {
		httpReq.Header.Set("anthropic-beta", "prompt-caching-2024-10-22")
	}

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, p.ParseAPIError(respBody, httpResp.StatusCode)
	}

	var resp Response
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	// Check for cache break (post-call, Phase 2).
	// Source: promptCacheBreakDetection.ts:437-666
	if req.PromptStateKey != nil {
		CheckResponseForCacheBreak(*req.PromptStateKey,
			resp.Usage.CacheReadInputTokens,
			resp.Usage.CacheCreationInputTokens,
			nil)
	}

	return &resp, nil
}

// Stream sends a streaming request and returns a channel of events.
// Source: Anthropic SSE streaming — 1:1 port of TS SDK streaming.
func (p *AnthropicProvider) Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error) {
	// Apply cache control to system blocks if configured.
	applyCacheControlToSystem(req)

	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Record prompt state for cache break detection (pre-call, Phase 1).
	if req.PromptStateKey != nil {
		system := RequestToSystemMaps(req)
		tools := RequestToToolMaps(req)
		RecordPromptState(system, tools, *req.PromptStateKey, req.Model, nil, "", false, false, false, false, "", 0)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	p.setHeaders(httpReq)
	// Prompt caching beta header — only when cache control is configured.
	if req.CacheControl != nil {
		httpReq.Header.Set("anthropic-beta", "prompt-caching-2024-10-22")
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	// Apply retry with backoff
	var httpResp *http.Response
	var lastErr error
	retryCfg := p.retryConfig
	if retryCfg == nil {
		retryCfg = DefaultRetryConfig()
	}

	for attempt := 0; attempt <= retryCfg.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := CalculateBackoff(attempt, retryCfg)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		// Rewind body for retry
		httpReq.Body = io.NopCloser(bytes.NewReader(body))
		httpReq.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}

		httpResp, lastErr = p.httpClient.Do(httpReq)
		if lastErr != nil {
			if IsConnectionError(lastErr) {
				continue
			}
			return nil, fmt.Errorf("send request: %w", lastErr)
		}

		if httpResp.StatusCode == http.StatusOK {
			break
		}

		// Read error body
		errBody, _ := io.ReadAll(httpResp.Body)
		_ = httpResp.Body.Close()
		apiErr := p.ParseAPIError(errBody, httpResp.StatusCode)

		if !IsRetryableStatus(httpResp.StatusCode) {
			return nil, apiErr
		}
		lastErr = apiErr
		httpResp = nil
	}

	if httpResp == nil {
		return nil, fmt.Errorf("max retries exceeded: %w", lastErr)
	}

	eventCh := make(chan StreamEvent, 64)

	go func() {
		defer close(eventCh)
		defer func() { _ = httpResp.Body.Close() }()

		// Use a wrapper channel to intercept events for cache token tracking.
		// After streaming completes, accumulated tokens feed into CheckResponseForCacheBreak.
		sseCh := make(chan StreamEvent, 64)
		done := make(chan struct{})
		go func() {
			defer close(sseCh)
			p.ParseSSE(ctx, httpResp.Body, sseCh)
			close(done)
		}()

		var cacheRead, cacheCreation int
		for evt := range sseCh {
			// Accumulate cache tokens from message_start and message_delta events.
			if evt.Message != nil {
				cacheRead += evt.Message.Usage.CacheReadInputTokens
				cacheCreation += evt.Message.Usage.CacheCreationInputTokens
			}
			if evt.Usage != nil {
				cacheRead += evt.Usage.CacheReadInputTokens
				cacheCreation += evt.Usage.CacheCreationInputTokens
			}
			// Forward to caller
			select {
			case eventCh <- evt:
			case <-ctx.Done():
				// Wait for ParseSSE to finish before returning to avoid goroutine leak.
				<-done
				return
			}
		}

		// Stream complete — check for cache break (Phase 2, post-call).
		// Source: promptCacheBreakDetection.ts:437-666
		if req.PromptStateKey != nil {
			CheckResponseForCacheBreak(*req.PromptStateKey, cacheRead, cacheCreation, nil)
		}
	}()

	return eventCh, nil
}

// ParseSSE parses the SSE stream into events.
// Source: Anthropic SSE protocol — event types match exactly.
func (p *AnthropicProvider) ParseSSE(ctx context.Context, body io.Reader, eventCh chan<- StreamEvent) {
	scanner := bufio.NewScanner(body)
	// Increase buffer for large responses
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	var eventType string
	var eventData strings.Builder

	for scanner.Scan() {
		line := scanner.Text()

		// Empty line = end of event
		if line == "" {
			if eventType != "" && eventData.Len() > 0 {
				event := p.ParseEvent(eventType, eventData.String())
				select {
				case eventCh <- event:
				case <-ctx.Done():
					return
				}
			}
			eventType = ""
			eventData.Reset()
			continue
		}

		// Event type line
		if after, ok := strings.CutPrefix(line, "event: "); ok {
			eventType = after
			continue
		}

		// Data line
		if after, ok := strings.CutPrefix(line, "data: "); ok {
			eventData.WriteString(after)
			continue
		}

		// Comment (starts with :)
		if len(line) > 0 && line[0] == ':' {
			continue
		}
	}

	// Handle last event if no trailing empty line
	if eventType != "" && eventData.Len() > 0 {
		event := p.ParseEvent(eventType, eventData.String())
		select {
		case eventCh <- event:
		case <-ctx.Done():
		}
	}
}

// ParseEvent converts an SSE event type + data into a StreamEvent.
func (p *AnthropicProvider) ParseEvent(eventType, data string) StreamEvent {
	event := StreamEvent{
		Type: eventType,
		Raw:  json.RawMessage(data),
	}

	switch eventType {
	case "message_start":
		var msg struct {
			Message MessageStart `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &msg); err == nil {
			event.Message = &msg.Message
			slog.Info("llm:message_start_raw", "id", msg.Message.ID, "usage", msg.Message.Usage)
		} else {
			slog.Warn("parse message_start failed", "error", err, "data", truncateForLog(data, 200))
		}

	case "content_block_start":
		var block struct {
			Index        int                `json:"index"`
			ContentBlock types.ContentBlock `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(data), &block); err == nil {
			event.Index = block.Index
			event.ContentBlock = &block.ContentBlock
		} else {
			slog.Warn("parse content_block_start failed", "error", err, "data", truncateForLog(data, 200))
		}

	case "content_block_delta":
		var delta struct {
			Index int         `json:"index"`
			Delta StreamDelta `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &delta); err == nil {
			event.Index = delta.Index
			event.Delta = &delta.Delta
		} else {
			slog.Warn("parse content_block_delta failed", "error", err, "data", truncateForLog(data, 200))
		}

	case "content_block_stop":
		var stop struct {
			Index int `json:"index"`
		}
		if err := json.Unmarshal([]byte(data), &stop); err == nil {
			event.Index = stop.Index
		} else {
			slog.Warn("parse content_block_stop failed", "error", err, "data", truncateForLog(data, 200))
		}

	case "message_delta":
		var delta struct {
			Delta MessageDelta `json:"delta"`
			Usage UsageDelta   `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &delta); err == nil {
			event.DeltaMsg = &delta.Delta
			event.Usage = &delta.Usage
			slog.Info("llm:message_delta_raw", "output_tokens", delta.Usage.OutputTokens,
				"cache_read", delta.Usage.CacheReadInputTokens,
				"cache_creation", delta.Usage.CacheCreationInputTokens,
				"input_tokens", delta.Usage.InputTokens)
		} else {
			slog.Warn("parse message_delta failed", "error", err, "data", truncateForLog(data, 200))
		}

	case "message_stop":
		// Terminal event — no payload

	case "ping":
		// Keepalive — no action

	case "error":
		var errData struct {
			Error APIError `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &errData); err == nil {
			event.Error = &errData.Error
		} else {
			slog.Warn("parse error event failed", "error", err, "data", truncateForLog(data, 200))
		}
	}

	return event
}

// setHeaders sets the required Anthropic API headers.
func (p *AnthropicProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	// Set both header styles — Anthropic uses x-api-key, but compatible
	// endpoints (like minimax) require Authorization: Bearer.
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
}

// applyCacheControlToSystem injects cache_control markers into system blocks.
// Source: claude.ts:358-374 — when CacheControl is non-nil, system blocks get cache_control markers.
func applyCacheControlToSystem(req *Request) {
	if req.CacheControl == nil {
		return
	}
	if len(req.SystemBlocks) > 0 {
		// Inject cache_control into the last system block.
		// Anthropic recommends placing cache_control on the last block for maximum cache hit rate.
		last := &req.SystemBlocks[len(req.SystemBlocks)-1]
		if last.CacheControl == nil {
			last.CacheControl = req.CacheControl
		}
		// Serialize SystemBlocks as the system field for the API call.
		if b, err := json.Marshal(req.SystemBlocks); err == nil {
			req.System = b
		}
	}
}

// ParseAPIError parses an error response body.
func (p *AnthropicProvider) ParseAPIError(body []byte, statusCode int) *APIError {
	apiErr := &APIError{
		Status:    statusCode,
		Retryable: IsRetryableStatus(statusCode),
	}

	var errResp struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err == nil {
		apiErr.Type = errResp.Type
		apiErr.Message = errResp.Error.Message
		if apiErr.Message == "" {
			apiErr.Message = string(body)
		}
	} else {
		slog.Warn("parse API error response failed", "error", err, "status", statusCode)
		apiErr.Message = string(body)
	}

	return apiErr
}
