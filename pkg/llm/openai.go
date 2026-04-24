// Package llm provides the OpenAI Chat Completions provider.
//
// This file implements Provider for OpenAI's API, translating between
// Anthropic-shaped internal types and OpenAI's request/response formats.
// The engine sees identical StreamEvents regardless of which provider is used.
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
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Provider struct + config
// ---------------------------------------------------------------------------

// OpenAIProvider implements Provider for the OpenAI Chat Completions API.
type OpenAIProvider struct {
	BaseProvider
	apiKey  string
	baseURL string
	model   string
}

// OpenAIConfig configures the OpenAI provider.
type OpenAIConfig struct {
	APIKey  string
	BaseURL string // defaults to https://api.openai.com/v1
	Model   string
	Timeout time.Duration
}

// NewOpenAIProvider creates a new OpenAI provider.
func NewOpenAIProvider(cfg *OpenAIConfig) *OpenAIProvider {
	if cfg.Timeout == 0 {
		cfg.Timeout = 300 * time.Second
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	return &OpenAIProvider{
		BaseProvider: BaseProvider{
			httpClient: &http.Client{
				Timeout: cfg.Timeout,
				Transport: &http.Transport{
					MaxIdleConnsPerHost: 10,
					IdleConnTimeout:     90 * time.Second,
					TLSHandshakeTimeout: 10 * time.Second,
				},
			},
			retryConfig: DefaultRetryConfig(),
			idleTimeout: 60 * time.Second,
		},
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		model:   cfg.Model,
	}
}

// ---------------------------------------------------------------------------
// Internal OpenAI JSON types
// ---------------------------------------------------------------------------

type openaiChatRequest struct {
	Model       string          `json:"model"`
	Messages    []openaiMessage `json:"messages"`
	Tools       []openaiTool    `json:"tools,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	Stop        []string        `json:"stop,omitempty"`
	User        string          `json:"user,omitempty"`
}

type openaiMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"` // string, nil, or omitted
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openaiToolCall struct {
	Index    int            `json:"index,omitempty"` // streaming deltas include index
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"` // "function"
	Function openaiFunction `json:"function"`
}

type openaiFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openaiTool struct {
	Type     string        `json:"type"` // "function"
	Function openaiFuncDef `json:"function"`
}

type openaiFuncDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Streaming chunk (SSE data payload)
type openaiStreamChunk struct {
	ID      string         `json:"id"`
	Model   string         `json:"model,omitempty"`
	Choices []openaiChoice `json:"choices"`
	Usage   *openaiUsage   `json:"usage,omitempty"`
}

type openaiChoice struct {
	Index        int         `json:"index"`
	Delta        openaiDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type openaiDelta struct {
	Role             string           `json:"role,omitempty"`
	Content          *string          `json:"content,omitempty"` // pointer: null vs empty
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"` // GLM extended field
}

type openaiPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
	AudioTokens  int `json:"audio_tokens,omitempty"`
}

type openaiCompletionTokensDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens,omitempty"`
	AudioTokens              int `json:"audio_tokens,omitempty"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens,omitempty"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens,omitempty"`
}

type openaiUsage struct {
	PromptTokens            int                           `json:"prompt_tokens"`
	CompletionTokens        int                           `json:"completion_tokens"`
	PromptTokensDetails     openaiPromptTokensDetails     `json:"prompt_tokens_details"`
	CompletionTokensDetails openaiCompletionTokensDetails `json:"completion_tokens_details"`
}

// Non-streaming response
type openaiChatResponse struct {
	ID      string             `json:"id"`
	Model   string             `json:"model"`
	Choices []openaiRespChoice `json:"choices"`
	Usage   openaiUsage        `json:"usage"`
}

type openaiRespChoice struct {
	Index        int               `json:"index"`
	Message      openaiRespMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

type openaiRespMessage struct {
	Role             string           `json:"role"`
	Content          *string          `json:"content"` // null when tool_calls present
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"` // GLM extended field
}

// toolCallAccumulator tracks per-index state for fragmented tool calls.
type toolCallAccumulator struct {
	id           string // from first chunk
	name         string // from function.name chunk
	arguments    strings.Builder
	contentIndex int  // Anthropic content block index
	started      bool // content_block_start sent
}

// ---------------------------------------------------------------------------
// Complete — non-streaming
// ---------------------------------------------------------------------------

func (p *OpenAIProvider) Complete(ctx context.Context, req *Request) (*Response, error) {
	body, err := p.translateRequest(req, false)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	p.setHeaders(httpReq)

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(httpResp.Body, 50<<20)) // 50MB safety cap
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, p.ParseAPIError(respBody, httpResp.StatusCode)
	}

	return p.translateResponse(respBody)
}

// translateResponse converts an OpenAI Chat Completions response to our Response.
func (p *OpenAIProvider) translateResponse(body []byte) (*Response, error) {
	var oResp openaiChatResponse
	if err := json.Unmarshal(body, &oResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if len(oResp.Choices) == 0 {
		return nil, fmt.Errorf("response has no choices")
	}

	choice := oResp.Choices[0]
	var content []types.ContentBlock

	// GLM extended: reasoning_content → thinking block
	if choice.Message.ReasoningContent != nil && *choice.Message.ReasoningContent != "" {
		content = append(content, types.ContentBlock{
			Type: types.ContentTypeThinking,
			Text: *choice.Message.ReasoningContent,
		})
	}

	// Text content
	if choice.Message.Content != nil && *choice.Message.Content != "" {
		content = append(content, types.NewTextBlock(*choice.Message.Content))
	}

	// Tool calls
	for _, tc := range choice.Message.ToolCalls {
		content = append(content, types.ContentBlock{
			Type:  types.ContentTypeToolUse,
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: json.RawMessage(tc.Function.Arguments),
		})
	}

	return &Response{
		ID:         oResp.ID,
		Type:       "message",
		Role:       "assistant",
		Content:    content,
		Model:      oResp.Model,
		StopReason: mapFinishReason(choice.FinishReason),
		Usage: types.Usage{
			InputTokens:              oResp.Usage.PromptTokens - oResp.Usage.PromptTokensDetails.CachedTokens,
			OutputTokens:             oResp.Usage.CompletionTokens,
			CacheReadInputTokens:     oResp.Usage.PromptTokensDetails.CachedTokens,
			CacheCreationInputTokens: 0, // OpenAI API does not report cache creation
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Maximum accumulated tool call arguments size (10MB).
var maxToolArgumentsSize = 10 * 1024 * 1024

// Stream — streaming
// ---------------------------------------------------------------------------

func (p *OpenAIProvider) Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error) {
	body, err := p.translateRequest(req, true)
	if err != nil {
		return nil, err
	}

	retryCfg := p.retryConfig
	if retryCfg == nil {
		retryCfg = DefaultRetryConfig()
	}

	var httpResp *http.Response
	var lastErr error

	for attempt := 0; attempt <= retryCfg.MaxRetries; attempt++ {
		if attempt > 0 {
			retryAfter := time.Duration(0)
			if httpResp != nil {
				if ra := httpResp.Header.Get("Retry-After"); ra != "" {
					if sec, e := strconv.Atoi(ra); e == nil && sec > 0 {
						retryAfter = time.Duration(sec) * time.Second
					}
				}
			}
			backoff := CalculateBackoffWithRetryAfter(attempt, retryCfg, retryAfter)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		p.setHeaders(httpReq)

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

		errBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20)) // 1MB cap for error bodies
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
		defer httpResp.Body.Close()
		p.parseOpenAISSE(ctx, req, httpResp.Body, eventCh)
	}()

	return eventCh, nil
}

// parseOpenAISSE parses the OpenAI SSE stream and emits Anthropic-shaped StreamEvents.
func (p *OpenAIProvider) parseOpenAISSE(ctx context.Context, req *Request, body io.Reader, eventCh chan<- StreamEvent) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	// Synthesize message_start
	send(ctx, eventCh, StreamEvent{
		Type: "message_start",
		Message: &MessageStart{
			ID:    "msg_" + randomID(),
			Role:  string(types.RoleAssistant),
			Model: req.Model,
			Usage: types.Usage{},
		},
	})

	accumulated := map[int]*toolCallAccumulator{} // OpenAI tool index → accumulator
	nextContentIndex := 0
	textBlockOpen := false
	textContentIndex := 0
	thinkingBlockOpen := false
	thinkingContentIndex := 0
	lastData := time.Now()

	for scanner.Scan() {
		line := scanner.Text()

		// Idle timeout check (fires between lines when data trickles slowly)
		if p.idleTimeout > 0 && !lastData.IsZero() && time.Since(lastData) > p.idleTimeout {
			slog.Warn("openai sse: idle timeout exceeded", "timeout", p.idleTimeout)
			return
		}

		// Empty line — skip
		if line == "" {
			continue
		}

		// SSE comment — skip, but don't reset idle timer
		if strings.HasPrefix(line, ":") {
			continue
		}

		// Line length guard
		if len(line) > 100_000 {
			slog.Warn("openai sse: line too long, skipping", "length", len(line))
			continue
		}

		// Must be a data line
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}

		// [DONE] marker
		if strings.TrimSpace(data) == "[DONE]" {
			// Close text block if open
			if textBlockOpen {
				send(ctx, eventCh, StreamEvent{Type: "content_block_stop", Index: textContentIndex})
			}
			// Drain pending content_block_stop for all tool calls
			for _, acc := range accumulated {
				if acc.started {
					send(ctx, eventCh, StreamEvent{Type: "content_block_stop", Index: acc.contentIndex})
				}
			}
			send(ctx, eventCh, StreamEvent{Type: "message_stop"})
			return
		}

		lastData = time.Now()

		var chunk openaiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			slog.Warn("openai sse: failed to parse chunk", "error", err, "data", truncateForLog(data, 200))
			continue
		}

		for _, choice := range chunk.Choices {
			delta := choice.Delta

			// GLM extended: reasoning_content delta → emit as thinking block
			if delta.ReasoningContent != nil && *delta.ReasoningContent != "" {
				if !thinkingBlockOpen {
					thinkingContentIndex = nextContentIndex
					nextContentIndex++
					send(ctx, eventCh, StreamEvent{
						Type:         "content_block_start",
						Index:        thinkingContentIndex,
						ContentBlock: &types.ContentBlock{Type: types.ContentTypeThinking, Text: ""},
					})
					thinkingBlockOpen = true
				}
				send(ctx, eventCh, StreamEvent{
					Type:  "content_block_delta",
					Index: thinkingContentIndex,
					Delta: &StreamDelta{Type: "thinking_delta", Thinking: *delta.ReasoningContent},
				})
			}

			// Close thinking block when actual content starts (GLM sends reasoning_content first, then content)
			if delta.Content != nil && *delta.Content != "" && thinkingBlockOpen {
				send(ctx, eventCh, StreamEvent{Type: "content_block_stop", Index: thinkingContentIndex})
				thinkingBlockOpen = false
			}
			// Process text content
			if delta.Content != nil && *delta.Content != "" {
				if !textBlockOpen {
					textContentIndex = nextContentIndex
					nextContentIndex++
					send(ctx, eventCh, StreamEvent{
						Type:         "content_block_start",
						Index:        textContentIndex,
						ContentBlock: &types.ContentBlock{Type: types.ContentTypeText},
					})
					textBlockOpen = true
				}
				send(ctx, eventCh, StreamEvent{
					Type:  "content_block_delta",
					Index: textContentIndex,
					Delta: &StreamDelta{Type: "text_delta", Text: *delta.Content},
				})
			}

			// Process tool calls
			for _, tc := range delta.ToolCalls {
				acc, exists := accumulated[tc.Index]
				if !exists {
					acc = &toolCallAccumulator{
						contentIndex: nextContentIndex,
					}
					nextContentIndex++
					accumulated[tc.Index] = acc
				}

				// Store ID (arrives in first chunk, before function.name)
				if tc.ID != "" {
					acc.id = tc.ID
				}

				// Function name → emit content_block_start
				if tc.Function.Name != "" {
					// Close text block if still open
					if textBlockOpen {
						send(ctx, eventCh, StreamEvent{Type: "content_block_stop", Index: textContentIndex})
						textBlockOpen = false
					}
					acc.name = tc.Function.Name
					acc.started = true
					send(ctx, eventCh, StreamEvent{
						Type:  "content_block_start",
						Index: acc.contentIndex,
						ContentBlock: &types.ContentBlock{
							Type: types.ContentTypeToolUse,
							ID:   acc.id,
							Name: acc.name,
						},
					})
				}

				// Arguments → emit input_json_delta
				if tc.Function.Arguments != "" {
					if !acc.started {
						// Edge case: arguments arrived before name.
						// Start the block now with whatever we have.
						if textBlockOpen {
							send(ctx, eventCh, StreamEvent{Type: "content_block_stop", Index: textContentIndex})
							textBlockOpen = false
						}
						acc.started = true
						send(ctx, eventCh, StreamEvent{
							Type:  "content_block_start",
							Index: acc.contentIndex,
							ContentBlock: &types.ContentBlock{
								Type: types.ContentTypeToolUse,
								ID:   acc.id,
								Name: acc.name,
							},
						})
					}
					if acc.arguments.Len()+len(tc.Function.Arguments) > maxToolArgumentsSize {
						slog.Warn("openai sse: tool arguments exceed size limit",
							"index", acc.contentIndex, "size", acc.arguments.Len())
						return
					}
					acc.arguments.WriteString(tc.Function.Arguments)
					send(ctx, eventCh, StreamEvent{
						Type:  "content_block_delta",
						Index: acc.contentIndex,
						Delta: &StreamDelta{Type: "input_json_delta", PartialJSON: tc.Function.Arguments},
					})
				}
			}

			// Process finish_reason
			if choice.FinishReason != nil {
				stopReason := mapFinishReason(*choice.FinishReason)

				// Close text block if open
				if textBlockOpen && stopReason != "tool_use" {
					send(ctx, eventCh, StreamEvent{Type: "content_block_stop", Index: textContentIndex})
					textBlockOpen = false
				}

				// Emit message_delta with stop_reason + usage
				usage := &UsageDelta{}
				if chunk.Usage != nil {
					usage.InputTokens = chunk.Usage.PromptTokens - chunk.Usage.PromptTokensDetails.CachedTokens
					usage.OutputTokens = chunk.Usage.CompletionTokens
					usage.CacheReadInputTokens = chunk.Usage.PromptTokensDetails.CachedTokens
					slog.Info("llm:message_delta_raw",
						"output_tokens", chunk.Usage.CompletionTokens,
						"cache_read", chunk.Usage.PromptTokensDetails.CachedTokens,
						"cache_creation", 0,
						"input_tokens", usage.InputTokens,
						"reasoning_tokens", chunk.Usage.CompletionTokensDetails.ReasoningTokens,
						"prompt_tokens", chunk.Usage.PromptTokens,
					)
				}
				send(ctx, eventCh, StreamEvent{
					Type:     "message_delta",
					DeltaMsg: &MessageDelta{StopReason: stopReason},
					Usage:    usage,
				})
			}
		}
	}

	// Stream ended without [DONE] — close gracefully
	if textBlockOpen {
		send(ctx, eventCh, StreamEvent{Type: "content_block_stop", Index: textContentIndex})
	}
	for _, acc := range accumulated {
		if acc.started {
			send(ctx, eventCh, StreamEvent{Type: "content_block_stop", Index: acc.contentIndex})
		}
	}
	send(ctx, eventCh, StreamEvent{Type: "message_stop"})
}

// ---------------------------------------------------------------------------
// Request translation (Anthropic → OpenAI)
// ---------------------------------------------------------------------------

func (p *OpenAIProvider) translateRequest(req *Request, stream bool) ([]byte, error) {
	oReq := openaiChatRequest{
		Model:       p.model,
		Stream:      stream,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stop:        req.StopSequences,
	}

	if req.Metadata != nil && req.Metadata.UserID != "" {
		oReq.User = req.Metadata.UserID
	}

	// System prompt
	if sys := extractSystemPrompt(req.System); sys != "" {
		oReq.Messages = append(oReq.Messages, openaiMessage{Role: "system", Content: sys})
	}

	// Messages
	oReq.Messages = append(oReq.Messages, translateMessages(req.Messages)...)

	// Tools
	if len(req.Tools) > 0 {
		oReq.Tools = translateTools(req.Tools)
	}

	body, err := json.Marshal(oReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	return body, nil
}

// translateMessages converts Anthropic-shaped messages to OpenAI format.
func translateMessages(messages []types.Message) []openaiMessage {
	var result []openaiMessage

	for _, msg := range messages {
		var assistantToolCalls []openaiToolCall
		var assistantText strings.Builder
		var toolResults []openaiMessage

		for _, cb := range msg.Content {
			switch cb.Type {
			case types.ContentTypeText:
				if msg.Role == types.RoleAssistant {
					assistantText.WriteString(cb.Text)
				} else {
					result = append(result, openaiMessage{
						Role:    string(msg.Role),
						Content: cb.Text,
					})
				}

			case types.ContentTypeToolUse:
				assistantToolCalls = append(assistantToolCalls, openaiToolCall{
					ID:   cb.ID,
					Type: "function",
					Function: openaiFunction{
						Name:      cb.Name,
						Arguments: string(cb.Input),
					},
				})

			case types.ContentTypeToolResult:
				content := extractToolResultText(cb.Content)
				toolResults = append(toolResults, openaiMessage{
					Role:       "tool",
					ToolCallID: cb.ToolUseID,
					Content:    content,
				})

			case types.ContentTypeThinking, types.ContentTypeRedacted:
				// Skip — OpenAI doesn't support thinking blocks
			}
		}

		// Build assistant message if it has content or tool_calls
		if msg.Role == types.RoleAssistant && (assistantText.String() != "" || len(assistantToolCalls) > 0) {
			om := openaiMessage{Role: "assistant"}
			if assistantText.String() != "" {
				om.Content = assistantText.String()
			} else {
				om.Content = nil
			}
			if len(assistantToolCalls) > 0 {
				om.ToolCalls = assistantToolCalls
			}
			result = append(result, om)
		}

		// Append tool result messages
		result = append(result, toolResults...)
	}

	return result
}

// translateTools converts Anthropic tool definitions to OpenAI format.
func translateTools(tools []ToolDef) []openaiTool {
	result := make([]openaiTool, len(tools))
	for i, t := range tools {
		result[i] = openaiTool{
			Type: "function",
			Function: openaiFuncDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		}
	}
	return result
}

// extractSystemPrompt extracts text from the System field.
// Handles: string, []ContentBlock with text fields, or empty.
func extractSystemPrompt(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try as string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	// Try as []ContentBlock
	var blocks []types.ContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var texts []string
		for _, b := range blocks {
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		return strings.Join(texts, "\n")
	}

	return ""
}

// extractToolResultText extracts text content from a tool result's Content field.
// Handles: string, []ContentBlock with text fields, or raw JSON fallback.
func extractToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try as string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	// Try as []ContentBlock
	var blocks []types.ContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var texts []string
		for _, b := range blocks {
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}

	// Fallback: raw JSON string
	return string(raw)
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

// ParseAPIError parses an OpenAI error response body.
func (p *OpenAIProvider) ParseAPIError(body []byte, statusCode int) *APIError {
	apiErr := &APIError{
		Status:    statusCode,
		Retryable: IsRetryableStatus(statusCode),
	}

	var errResp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &errResp); err == nil {
		apiErr.Type = errResp.Error.Type
		apiErr.Message = errResp.Error.Message
		if apiErr.Message == "" {
			apiErr.Message = string(body)
		}
	} else {
		slog.Warn("parse OpenAI error response failed", "error", err, "status", statusCode)
		apiErr.Message = string(body)
	}

	// OpenAI-specific error code mapping
	switch {
	case statusCode == 400 && errResp.Error.Code == "context_length_exceeded":
		apiErr.Retryable = false
		apiErr.ErrorCode = "prompt_too_long"
		apiErr.Type = "prompt_too_long"
	case statusCode == 429:
		apiErr.ErrorCode = "rate_limit_error"
		apiErr.Type = "rate_limit_error"
	case statusCode == 401:
		apiErr.Retryable = false
		apiErr.ErrorCode = "authentication_error"
		apiErr.Type = "authentication_error"
	case statusCode == 403:
		apiErr.Retryable = false
		apiErr.ErrorCode = "permission_error"
		apiErr.Type = "permission_error"
	case statusCode >= 500:
		apiErr.ErrorCode = "api_error"
		apiErr.Type = "api_error"
	}

	return apiErr
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (p *OpenAIProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
}

func mapFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "content_filter":
		return "content_filter"
	default:
		return reason
	}
}

// send sends an event on the channel, respecting context cancellation.
func send(ctx context.Context, ch chan<- StreamEvent, event StreamEvent) {
	select {
	case ch <- event:
	case <-ctx.Done():
	}
}

// randomID generates a short random string for synthesized message IDs.
func randomID() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 24)
	for i := range b {
		b[i] = charset[fastrand()%uint32(len(charset))]
	}
	return string(b)
}

// fastrandState holds persistent PRNG state for fastrand.
var fastrandState uint64

func init() {
	// Seed once at startup
	fastrandState = uint64(time.Now().UnixNano())
}

// fastrand returns a pseudo-random uint32 using a persistent xorshift state.
func fastrand() uint32 {
	for {
		old := atomic.LoadUint64(&fastrandState)
		x := old
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		if atomic.CompareAndSwapUint64(&fastrandState, old, x) {
			return uint32(x)
		}
	}
}
