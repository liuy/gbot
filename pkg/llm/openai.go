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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
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
	apiKey      string
	baseURL     string
	model       string
	extraParams map[string]any
	// inputModalities maps model → declared input modalities
	// (settings.json models "input"). Governs whether image_url parts may
	// reach the wire — see visionSupported.
	inputModalities map[string][]string
	// reasoningFallbacks freezes runtime effort-dialect negotiation
	// conclusions for this instance's lifetime (reasoning_fallback.go).
	reasoningFallbacks *reasoningFallbackState
}

// OpenAIConfig configures the OpenAI provider.
type OpenAIConfig struct {
	Name        string
	APIKey      string
	BaseURL     string // defaults to https://api.openai.com/v1
	Model       string
	Timeout     time.Duration
	ExtraParams map[string]any // Provider-specific params merged into request body (e.g. {"tool_stream": true} for Zhipu GLM)
	// InputModalities maps model name → declared input modalities
	// (["text","image","video"], from settings.json provider models).
	// Models absent from the map are treated as text-only — text-only
	// endpoints reject image_url with a sticky 400.
	InputModalities map[string][]string
}

// NewOpenAIProvider creates a new OpenAI provider.
func NewOpenAIProvider(cfg *OpenAIConfig) *OpenAIProvider {
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultHTTPTimeout
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	return &OpenAIProvider{
		name:               cfg.Name,
		httpClient:         newLLMHTTPClient(cfg.Timeout),
		retryConfig:        DefaultRetryConfig(),
		idleTimeout:        DefaultSSETimeout,
		apiKey:             cfg.APIKey,
		baseURL:            strings.TrimRight(cfg.BaseURL, "/"),
		model:              cfg.Model,
		extraParams:        cfg.ExtraParams,
		inputModalities:    cfg.InputModalities,
		reasoningFallbacks: newReasoningFallbackState(),
	}
}

// visionSupported reports whether the model accepts image_url parts on the
// wire. Only models whose settings declaration includes "image" count;
// undeclared models default to text-only, mirroring omp's defensive guard
// (misconfigured descriptors must not leak image_url to text-only endpoints).
func (p *OpenAIProvider) visionSupported(model string) bool {
	return slices.Contains(p.inputModalities[model], "image")
}

// ---------------------------------------------------------------------------
// Internal OpenAI JSON types
// ---------------------------------------------------------------------------

type openaiChatRequest struct {
	Model    string          `json:"model"`
	Messages []openaiMessage `json:"messages"`
	Tools    []openaiTool    `json:"tools,omitempty"`
	// ToolChoice is the OpenAI chat string form, sent only when tools are
	// present — the contract requires tool_choice to accompany tools, and a
	// bare field is rejected.
	ToolChoice  string   `json:"tool_choice,omitempty"`
	Stream      bool     `json:"stream,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	Stop        []string `json:"stop,omitempty"`
	User        string   `json:"user,omitempty"`
	// Native reasoning-effort tier. Omitted for auto; servers that reject a
	// tier are renegotiated at runtime (reasoning_fallback.go).
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type openaiMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"` // string, nil, []any (vision content array), or omitted
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	// ReasoningContent is a GLM-style top-level assistant-message field
	// (sibling of content/tool_calls, not inside the content array). GLM
	// Preserved Thinking requires verbatim replay of the prior turn's
	// reasoning; see https://docs.z.ai/guides/capabilities/thinking-mode.
	ReasoningContent *string `json:"reasoning_content,omitempty"`
}

// openaiTextContent is a text entry inside a vision content array.
type openaiTextContent struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

// openaiImageURL is an image entry inside a vision content array.
type openaiImageURL struct {
	Type     string             `json:"type"` // "image_url"
	ImageURL openaiImageURLData `json:"image_url"`
}

type openaiImageURLData struct {
	URL string `json:"url"` // "data:image/png;base64,..."
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
	if err := ValidateImagesForAPI(req.Messages); err != nil {
		return nil, err
	}

	body, err := p.translateRequest(req, false)
	if err != nil {
		return nil, err
	}

	body = p.applyFrozenReasoningFallback(req, body)

	httpResp, apiErr, captured, err := p.postChat(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	if httpResp == nil {
		// Non-200: one negotiation attempt, one retry. Complete deliberately
		// has no transport-level retry loop (omp's non-streaming path has
		// none either); the negotiation retry here is the single authorized
		// extension to omp, which wires it only on streaming paths.
		fb := p.resolveReasoningFallback(apiErr, captured, body, req.Thinking == EffortNone)
		if fb == nil {
			return nil, apiErr
		}
		body = applyReasoningFallbackToBody(body, fb)
		httpResp, apiErr, _, err = p.postChat(ctx, body)
		if err != nil {
			return nil, fmt.Errorf("send request: %w", err)
		}
		if httpResp == nil {
			return nil, apiErr
		}
		// Freeze only after the retry connected; explicit-disable conclusions
		// stay per-request (omp: a reasoning-off side request must not
		// downgrade later normal turns).
		if req.Thinking != EffortNone {
			p.rememberReasoningFallback(req, fb)
		}
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(httpResp.Body, 50<<20)) // 50MB safety cap
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
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
			Type:     types.ContentTypeThinking,
			Thinking: *choice.Message.ReasoningContent,
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
	if err := ValidateImagesForAPI(req.Messages); err != nil {
		return nil, err
	}

	body, err := p.translateRequest(req, true)
	if err != nil {
		return nil, err
	}

	body = p.applyFrozenReasoningFallback(req, body)

	httpResp, captured, err := p.connectChatStream(ctx, body)
	if err != nil {
		fb := p.resolveReasoningFallback(err, captured, body, req.Thinking == EffortNone)
		if fb == nil {
			return nil, err
		}
		body = applyReasoningFallbackToBody(body, fb)
		httpResp, _, err = p.connectChatStream(ctx, body)
		if err != nil {
			return nil, err
		}
		// Freeze only after the retry connected; explicit-disable conclusions
		// stay per-request (omp: a reasoning-off side request must not
		// downgrade later normal turns).
		if req.Thinking != EffortNone {
			p.rememberReasoningFallback(req, fb)
		}
	}

	eventCh := make(chan StreamEvent, 64)
	go func() {
		defer close(eventCh)
		defer httpResp.Body.Close()
		var body io.Reader = httpResp.Body
		var td TimeoutDisabler
		if p.idleTimeout > 0 {
			tr := &timeoutReader{reader: httpResp.Body, timeout: p.idleTimeout, ctx: ctx}
			body = tr
			td = tr
		}
		if err := p.parseOpenAISSE(ctx, req, body, td, eventCh); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("openai sse: scanner error", "error", err)
			send(ctx, eventCh, StreamEvent{
				Type: "error",
				Error: &APIError{
					Type:      "transport_error",
					Message:   err.Error(),
					Retryable: errors.Is(err, ErrIdleTimeout),
				},
			})
		}
	}()

	return eventCh, nil
}

// postChat issues one chat-completions POST. Non-200 responses are drained
// (1MB error-body cap, matching the stream path) and returned as a parsed
// APIError plus the captured body for reasoning negotiation; the caller owns
// draining a 200 response body. Transport failures return err unwrapped so
// the retry loop can classify them.
func (p *OpenAIProvider) postChat(ctx context.Context, body []byte) (*http.Response, *APIError, *capturedHTTPError, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, nil, nil, err
	}
	p.setHeaders(httpReq)

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, nil, nil, err
	}
	if httpResp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20)) // 1MB cap for error bodies
		_ = httpResp.Body.Close()
		apiErr := p.ParseAPIError(errBody, httpResp.StatusCode)
		// The body is drained and closed here, so the response headers are the
		// only place the server's Retry-After advice can be captured for the
		// retry loop.
		if ra := httpResp.Header.Get("Retry-After"); ra != "" {
			if sec, e := strconv.Atoi(ra); e == nil && sec > 0 {
				apiErr.RetryAfter = time.Duration(sec) * time.Second
			}
		}
		return nil, apiErr, newCapturedHTTPError(httpResp.StatusCode, errBody), nil
	}
	return httpResp, nil, nil, nil
}

// connectChatStream runs the transport retry loop around postChat:
// connection errors retry immediately, retryable statuses retry with
// backoff, and a non-retryable status returns the APIError directly with its
// captured body so reasoning negotiation can inspect it. Exhausting the
// budget wraps the last error.
func (p *OpenAIProvider) connectChatStream(ctx context.Context, body []byte) (*http.Response, *capturedHTTPError, error) {
	retryCfg := p.retryConfig
	if retryCfg == nil {
		retryCfg = DefaultRetryConfig()
	}

	var httpResp *http.Response
	var lastErr error

	for attempt := 0; attempt <= retryCfg.MaxRetries; attempt++ {
		if attempt > 0 {
			// httpResp is nil on every path that reaches a retry (both the
			// connection-error and drained-error branches clear it), so the
			// server's Retry-After travels on the last APIError instead.
			retryAfter := time.Duration(0)
			if apiErr, ok := errors.AsType[*APIError](lastErr); ok {
				retryAfter = apiErr.RetryAfter
			}
			backoff := CalculateBackoffWithRetryAfter(attempt, retryCfg, retryAfter)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}

		resp, apiErr, captured, err := p.postChat(ctx, body)
		if err != nil {
			httpResp = nil
			lastErr = err
			if IsConnectionError(err) {
				continue
			}
			return nil, nil, fmt.Errorf("send request: %w", err)
		}
		if resp != nil {
			httpResp = resp
			break
		}

		if !IsRetryableStatus(apiErr.Status) {
			return nil, captured, apiErr
		}
		lastErr = apiErr
		httpResp = nil
	}

	if httpResp == nil {
		return nil, nil, fmt.Errorf("max retries exceeded: %w", lastErr)
	}
	return httpResp, nil, nil
}

// parseOpenAISSE parses the OpenAI SSE stream and emits Anthropic-shaped StreamEvents.
// td, when non-nil, allows the parser to disable idle timeout during tool input phase.
// Returns the underlying scanner error if the stream failed mid-read; nil on clean EOF.
func (p *OpenAIProvider) parseOpenAISSE(ctx context.Context, req *Request, body io.Reader, td TimeoutDisabler, eventCh chan<- StreamEvent) error {
	// Safety net: if we return while still in tool input phase, re-enable timeout.
	if td != nil {
		defer td.SetTimeoutDisabled(false)
	}

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

	closeThinking := func() {
		if thinkingBlockOpen {
			send(ctx, eventCh, StreamEvent{Type: "content_block_stop", Index: thinkingContentIndex})
			thinkingBlockOpen = false
		}
	}
	closeText := func() {
		if textBlockOpen {
			send(ctx, eventCh, StreamEvent{Type: "content_block_stop", Index: textContentIndex})
			textBlockOpen = false
		}
	}
	// Normal-completion sweep: terminate every block this stream opened, so
	// pure-reasoning turns still emit content_block_stop for thinking. Error
	// exits call closeThinking/closeText only — a stop for a tool block makes
	// the engine execute it, and an error turn's arguments are knowingly
	// incomplete.
	closeOpenBlocks := func() {
		closeThinking()
		closeText()
		for _, acc := range accumulated {
			if acc.started {
				send(ctx, eventCh, StreamEvent{Type: "content_block_stop", Index: acc.contentIndex})
				acc.started = false
			}
		}
	}

	for scanner.Scan() {
		line := scanner.Text()

		// Empty line — skip
		if line == "" {
			continue
		}

		// SSE comment — skip, but don't reset idle timer
		if strings.HasPrefix(line, ":") {
			continue
		}

		// Must be a data line
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}

		// [DONE] marker
		if strings.TrimSpace(data) == "[DONE]" {
			closeOpenBlocks()
			send(ctx, eventCh, StreamEvent{Type: "message_stop"})
			return nil
		}

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
						ContentBlock: &types.ContentBlock{Type: types.ContentTypeThinking, Thinking: ""},
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
			if delta.Content != nil && *delta.Content != "" {
				closeThinking()
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
					closeThinking()
					closeText()
					acc.name = tc.Function.Name
					acc.started = true
					// Disable idle timeout during tool input phase — the LLM may
					// pause for extended periods while generating large parameters.
					if td != nil {
						td.SetTimeoutDisabled(true)
					}
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
						closeText()
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
						// Both blocks can dangle here: thinking never closes on
						// tool arguments without a name, and text can reopen after
						// the tool block started — content deltas interleave with
						// tool chunks and the text open-block logic has no
						// tool-phase guard.
						closeThinking()
						closeText()
						return nil
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

				// Pure-reasoning turns end here; Anthropic closes blocks
				// before message_delta.
				closeThinking()
				if stopReason != "tool_use" {
					closeText()
				}

				// Tool input phase is over — re-enable idle timeout. This
				// mirrors Anthropic's content_block_stop handling and keeps
				// the timeout disabled only during tool parameter generation.
				if td != nil && stopReason == "tool_use" {
					td.SetTimeoutDisabled(false)
				}

				// Emit message_delta with stop_reason + usage
				usage := &types.Usage{}
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

	// Stream ended without [DONE]
	if err := scanner.Err(); err != nil {
		closeThinking()
		closeText()
		return err
	}
	// Clean EOF without [DONE] — close gracefully.
	closeOpenBlocks()
	send(ctx, eventCh, StreamEvent{Type: "message_stop"})
	return nil
}

// ---------------------------------------------------------------------------
// Request translation (Anthropic → OpenAI)
// ---------------------------------------------------------------------------

func (p *OpenAIProvider) translateRequest(req *Request, stream bool) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	oReq := openaiChatRequest{
		Model:           model,
		Stream:          stream,
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		Stop:            req.StopSequences,
		ReasoningEffort: translateChatReasoningEffort(req.Thinking),
	}

	if req.Metadata != nil && req.Metadata.UserID != "" {
		oReq.User = req.Metadata.UserID
	}

	// System prompt
	if sys := extractSystemPrompt(req.System); sys != "" {
		oReq.Messages = append(oReq.Messages, openaiMessage{Role: "system", Content: sys})
	}

	// Messages
	oReq.Messages = append(oReq.Messages, translateMessagesVision(req.Messages, p.visionSupported(model))...)

	// Tools
	if len(req.Tools) > 0 {
		oReq.Tools = translateTools(req.Tools)
		oReq.ToolChoice = "auto"
	}

	body, err := json.Marshal(oReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	if len(p.extraParams) > 0 {
		body, err = mergeJSON(body, p.extraParams)
		if err != nil {
			return nil, fmt.Errorf("merge extra params: %w", err)
		}
	}
	return body, nil
}

// Wire texts for tool-result image assembly (omp-aligned).
const (
	// toolResultImageStandIn fills an image-only tool result — tool messages
	// cannot carry image_url parts.
	toolResultImageStandIn = "(see attached image)"
	// nonVisionImagePlaceholder replaces images stripped for text-only models.
	nonVisionImagePlaceholder = "[image omitted: model does not support vision]"
	// toolResultImagePrefixText introduces the trailing user message that
	// carries a turn's collected tool-result images.
	toolResultImagePrefixText = "Attached image(s) from tool result:"
)

// joinTextWithImagePlaceholder appends the non-vision omission note to tool
// text, so a text-only model still learns that an image existed (omp:
// joinTextWithImagePlaceholder).
func joinTextWithImagePlaceholder(text string) string {
	if text == "" {
		return nonVisionImagePlaceholder
	}
	return text + "\n" + nonVisionImagePlaceholder
}

// translateMessages converts Anthropic-shaped messages to OpenAI format.
// Vision is assumed supported — production requests must instead resolve
// per-model support via translateRequest → translateMessagesVision, since a
// text-only endpoint rejects image_url with a sticky 400.
func translateMessages(messages []types.Message) []openaiMessage {
	return translateMessagesVision(messages, true)
}

// translateMessagesVision converts Anthropic-shaped messages to OpenAI
// format, gating image_url parts on the target model's vision support.
func translateMessagesVision(messages []types.Message, visionSupported bool) []openaiMessage {
	var result []openaiMessage

	for _, msg := range messages {
		var assistantToolCalls []openaiToolCall
		var assistantText strings.Builder
		var assistantReasoning strings.Builder
		var toolResults []openaiMessage
		// Images collected from this turn's tool results. Tool messages are
		// text-only, and strict endpoints (DeepSeek) require tool messages to
		// immediately follow their tool_calls assistant message — so images
		// are batched and appended as ONE user message after the whole run.
		var toolResultImages []openaiImageURL
		// For user messages: collect text and image content. If the message has
		// any image blocks, OpenAI vision requires ONE message whose content is
		// an array of {type:"text"} and {type:"image_url"} blocks rather than a
		// plain string.
		var userTextStrings []string
		var userImages []openaiImageURL
		// Set when a user image is dropped for a text-only model: one
		// trailing omission note replaces every stripped image (omp parity —
		// omittedImages is a bool, not a count).
		userImagesOmitted := false

		for _, cb := range msg.Content {
			switch cb.Type {
			case types.ContentTypeText:
				if msg.Role == types.RoleAssistant {
					assistantText.WriteString(cb.Text)
				} else {
					userTextStrings = append(userTextStrings, cb.Text)
				}

			case types.ContentTypeImage:
				if msg.Role == types.RoleUser && cb.Source != nil {
					if visionSupported {
						userImages = append(userImages, openaiImageURL{
							Type: "image_url",
							ImageURL: openaiImageURLData{
								URL: "data:" + cb.Source.MediaType + ";base64," + cb.Source.Data,
							},
						})
					} else {
						userImagesOmitted = true
					}
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
				text, images := extractToolResultContent(cb.Content)
				hasImages := len(images) > 0
				// The tool message itself is always sent: text if present,
				// a stand-in for image-only results, or the omission note
				// when images are stripped for a text-only model (omp parity).
				var content string
				switch {
				case hasImages && !visionSupported:
					content = joinTextWithImagePlaceholder(text)
				case text != "":
					content = text
				case hasImages:
					content = toolResultImageStandIn
				}
				toolResults = append(toolResults, openaiMessage{
					Role:       "tool",
					ToolCallID: cb.ToolUseID,
					Content:    content,
				})
				if visionSupported {
					toolResultImages = append(toolResultImages, images...)
				}

			case types.ContentTypeThinking:
				// GLM Preserved Thinking requires the prior turn's reasoning
				// to be replayed in a single top-level reasoning_content field.
				// Multiple thinking blocks within one assistant turn (an
				// Anthropic artifact) are concatenated with no separator.
				assistantReasoning.WriteString(cb.Thinking)

			case types.ContentTypeRedacted:
				// Anthropic-only — OpenAI wire format has no equivalent of
				// server-redacted reasoning. The signature-bound encrypted
				// data cannot be reconstructed, so dropping is the only
				// option. Emit a debug log so silent drops are traceable.
				slog.Debug("openai: dropping redacted_thinking block — no OpenAI wire representation",
					"data_len", len(cb.Data))
			}
		}

		// Build assistant message if it has content, tool_calls, or reasoning.
		hasReasoning := assistantReasoning.Len() > 0
		if msg.Role == types.RoleAssistant && (assistantText.String() != "" || len(assistantToolCalls) > 0 || hasReasoning) {
			om := openaiMessage{Role: "assistant"}
			if assistantText.String() != "" {
				om.Content = assistantText.String()
			} else {
				om.Content = nil
			}
			if len(assistantToolCalls) > 0 {
				om.ToolCalls = assistantToolCalls
			}
			if hasReasoning {
				r := assistantReasoning.String()
				om.ReasoningContent = &r
			}
			result = append(result, om)
		}

		// OpenAI requires tool results immediately after the assistant message
		// that made the tool call, before any subsequent user text.
		result = append(result, toolResults...)

		// After the whole tool run, one user message carries every collected
		// image (prefix text + image_url blocks) — never interleaved between
		// tool messages (omp parity).
		if len(toolResultImages) > 0 {
			contentArray := []any{openaiTextContent{Type: "text", Text: toolResultImagePrefixText}}
			for _, img := range toolResultImages {
				contentArray = append(contentArray, img)
			}
			result = append(result, openaiMessage{Role: "user", Content: contentArray})
		}

		// Emit user content. With images present, build a single array-content
		// message (text blocks first, then image blocks); without images, emit
		// one string-content message per text block to preserve prior behavior.
		// Images stripped for a text-only model still yield one array message —
		// text plus a trailing omission note — so the turn is never silently
		// empty (omp parity).
		if msg.Role == types.RoleUser {
			switch {
			case len(userImages) > 0 || userImagesOmitted:
				contentArray := make([]any, 0, len(userTextStrings)+len(userImages)+1)
				for _, t := range userTextStrings {
					contentArray = append(contentArray, openaiTextContent{Type: "text", Text: t})
				}
				for _, img := range userImages {
					contentArray = append(contentArray, img)
				}
				if userImagesOmitted {
					contentArray = append(contentArray, openaiTextContent{Type: "text", Text: nonVisionImagePlaceholder})
				}
				result = append(result, openaiMessage{Role: "user", Content: contentArray})
			default:
				for _, t := range userTextStrings {
					result = append(result, openaiMessage{Role: "user", Content: t})
				}
			}
		}
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

// extractToolResultContent parses tool_result.content (an array of
// ContentBlocks) and separates text from image blocks. Tool messages cannot
// carry images — the caller batches the returned blocks into a trailing user
// message; sourceless image blocks are skipped without panic.
func extractToolResultContent(raw json.RawMessage) (text string, images []openaiImageURL) {
	if len(raw) == 0 {
		return "", nil
	}

	// Try as string (legacy single-wrapped form).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}

	// Try as []ContentBlock (array form — the canonical shape).
	var blocks []types.ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		// Fallback: return raw as text, no images.
		return string(raw), nil
	}

	var texts []string
	for _, b := range blocks {
		switch {
		case b.Text != "":
			texts = append(texts, b.Text)
		case b.Type == types.ContentTypeImage && b.Source != nil:
			images = append(images, openaiImageURL{
				Type: "image_url",
				ImageURL: openaiImageURLData{
					URL: "data:" + b.Source.MediaType + ";base64," + b.Source.Data,
				},
			})
		}
	}
	return strings.Join(texts, "\n"), images
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

func mergeJSON(base []byte, extra map[string]any) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	maps.Copy(m, extra)
	return json.Marshal(m)
}
