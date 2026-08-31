// Package llm provides the OpenAI Responses API provider.
//
// This file implements Provider for the OpenAI Responses protocol (first
// target endpoint: GLM https://open.bigmodel.cn/api/v1/responses). Like
// openai.go it translates between Anthropic-shaped internal types and the
// Responses wire format, so the engine sees identical StreamEvents regardless
// of which provider is used. Only the protocol layer is ported — codex
// harness features (built-in tools, encrypted reasoning, WebSocket transport)
// are intentionally absent.
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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// Compile-time proof the interface is fully implemented.
var _ Provider = (*ResponsesProvider)(nil)

// ---------------------------------------------------------------------------
// Provider struct + config
// ---------------------------------------------------------------------------

// ResponsesProvider implements Provider for the OpenAI Responses API.
type ResponsesProvider struct {
	BaseProvider
	apiKey      string
	baseURL     string
	model       string
	extraParams map[string]any
}

// ResponsesConfig configures the Responses provider.
type ResponsesConfig struct {
	Name        string
	APIKey      string
	BaseURL     string // defaults to https://api.openai.com/v1; GLM uses https://open.bigmodel.cn/api/v1
	Model       string
	Timeout     time.Duration
	ExtraParams map[string]any // merged into the request body (e.g. {"reasoning":{"effort":"high"}})
}

// NewResponsesProvider creates a new Responses provider.
func NewResponsesProvider(cfg *ResponsesConfig) *ResponsesProvider {
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultHTTPTimeout
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	return &ResponsesProvider{
		BaseProvider: BaseProvider{
			name:        cfg.Name,
			httpClient:  newLLMHTTPClient(cfg.Timeout),
			retryConfig: DefaultRetryConfig(),
			// 90s, not openai.go's 60s: reasoning-phase deltas can arrive in
			// bursts spaced further apart than chat-completion tokens.
			idleTimeout: DefaultSSETimeout,
		},
		apiKey:      cfg.APIKey,
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		model:       cfg.Model,
		extraParams: cfg.ExtraParams,
	}
}

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

// responsesRequest is the Responses API request body. Field superset is
// trimmed from codex ResponsesApiRequest to what GLM/OpenAI commonly accept;
// harness-only fields (tool_choice, include, text, service_tier, …) are
// omitted.
type responsesRequest struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions,omitempty"`
	Input           []responsesItem `json:"input"`
	Tools           []responsesTool `json:"tools,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens"`
	Store           bool            `json:"store"` // always false: we replay full input every turn
	Stream          bool            `json:"stream"`
	Temperature     *float64        `json:"temperature,omitempty"`
	PromptCacheKey  string          `json:"prompt_cache_key,omitempty"`
}

type responsesTool struct { // no strict field — not part of the GLM contract
	Type        string          `json:"type"` // "function"
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// responsesItem covers the four core input/output item kinds. ID/Status/Summary
// only appear on output (done) items; they are parse-only and never sent.
type responsesItem struct {
	Type    string                 `json:"type"` // message|reasoning|function_call|function_call_output
	ID      string                 `json:"id,omitempty"`
	Role    string                 `json:"role,omitempty"` // user|assistant
	Content []responsesContentPart `json:"content,omitempty"`
	CallID  string                 `json:"call_id,omitempty"`
	Name    string                 `json:"name,omitempty"`
	// Arguments is a JSON-encoded string, not an object (Responses wire quirk).
	Arguments string `json:"arguments,omitempty"`
	// Output is a pointer so an empty function_call_output still serializes
	// "output":"" (the field is required on that item kind) while other item
	// kinds omit it entirely.
	Output *string `json:"output,omitempty"`
	// OpenAI reasoning items carry summaries here instead of content.
	Summary []responsesSummaryPart `json:"summary,omitempty"`
}

type responsesSummaryPart struct {
	Type string `json:"type"` // summary_text
	Text string `json:"text,omitempty"`
}

type responsesContentPart struct {
	Type     string `json:"type"` // input_text|input_image|output_text|reasoning_text|text
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"` // "data:<media>;base64,<data>"
}

// responsesSSEEvent is the SSE data envelope, parsed loosely: unknown fields
// are ignored and the discriminating type comes from the JSON "type" field
// (GLM/codex both duplicate the event: line into the payload).
type responsesSSEEvent struct {
	Type         string          `json:"type"`
	Item         *responsesItem  `json:"item,omitempty"`
	ItemID       string          `json:"item_id,omitempty"`
	CallID       string          `json:"call_id,omitempty"`
	OutputIndex  int             `json:"output_index,omitempty"`
	ContentIndex int             `json:"content_index,omitempty"`
	Delta        string          `json:"delta,omitempty"`
	Response     json.RawMessage `json:"response,omitempty"`
}

// responsesCompleted carries the completed/incomplete/failed payload and the
// non-streaming response body (same shape).
type responsesCompleted struct {
	ID                string          `json:"id"`
	Model             string          `json:"model,omitempty"`
	Status            string          `json:"status,omitempty"`
	Output            []responsesItem `json:"output,omitempty"`
	Usage             *responsesUsage `json:"usage,omitempty"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details,omitempty"`
	Error *responsesWireError `json:"error,omitempty"`
}

// responsesWireError is the error object nested in response.failed payloads
// and in non-200 HTTP bodies.
type responsesWireError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Type    string `json:"type,omitempty"`
}

type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details,omitempty"`
	OutputTokens        int `json:"output_tokens"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details,omitempty"`
	TotalTokens int `json:"total_tokens"`
}

// responsesUsageToUsage applies the same arithmetic as openai.go: billed new
// input excludes cached tokens, and the Responses API reports no cache writes.
func responsesUsageToUsage(u *responsesUsage) types.Usage {
	if u == nil {
		return types.Usage{}
	}
	cached := 0
	if u.InputTokensDetails != nil {
		cached = u.InputTokensDetails.CachedTokens
	}
	return types.Usage{
		InputTokens:              u.InputTokens - cached,
		OutputTokens:             u.OutputTokens,
		CacheReadInputTokens:     cached,
		CacheCreationInputTokens: 0,
	}
}

// ---------------------------------------------------------------------------
// Request translation (Anthropic → Responses)
// ---------------------------------------------------------------------------

// translateResponsesItems converts Anthropic-shaped messages to Responses
// input items, preserving per-message block order. Assistant reasoning is
// replayed as reasoning items (GLM accepts plain-text reasoning without ids).
func translateResponsesItems(messages []types.Message) []responsesItem {
	var result []responsesItem

	for _, msg := range messages {
		var toolOutputs []responsesItem
		// User text/image blocks are batched into one trailing message item
		// (text parts first, then image parts — same convention as openai.go).
		var userTexts []string
		var userImages []responsesContentPart

		for _, cb := range msg.Content {
			switch cb.Type {
			case types.ContentTypeThinking:
				if msg.Role == types.RoleAssistant && cb.Thinking != "" {
					result = append(result, responsesItem{
						Type:    "reasoning",
						Content: []responsesContentPart{{Type: "reasoning_text", Text: cb.Thinking}},
					})
				}

			case types.ContentTypeText:
				if msg.Role == types.RoleAssistant {
					if cb.Text != "" {
						result = append(result, responsesItem{
							Type:    "message",
							Role:    "assistant",
							Content: []responsesContentPart{{Type: "output_text", Text: cb.Text}},
						})
					}
				} else {
					userTexts = append(userTexts, cb.Text)
				}

			case types.ContentTypeToolUse:
				if msg.Role == types.RoleAssistant {
					result = append(result, responsesItem{
						Type:      "function_call",
						CallID:    cb.ID,
						Name:      cb.Name,
						Arguments: string(cb.Input),
					})
				}

			case types.ContentTypeToolResult:
				text, imageMsgs := extractToolResultContent(cb.Content)
				if len(imageMsgs) > 0 {
					// function_call_output is text-only; unlike openai.go there
					// is no companion message to rescue the images into.
					slog.Debug("responses: dropping tool_result image content — function_call_output accepts text only",
						"tool_use_id", cb.ToolUseID, "images", len(imageMsgs))
				}
				toolOutputs = append(toolOutputs, responsesItem{
					Type:   "function_call_output",
					CallID: cb.ToolUseID,
					Output: &text,
				})

			case types.ContentTypeImage:
				if msg.Role == types.RoleUser && cb.Source != nil {
					userImages = append(userImages, responsesContentPart{
						Type:     "input_image",
						ImageURL: "data:" + cb.Source.MediaType + ";base64," + cb.Source.Data,
					})
				}

			case types.ContentTypeRedacted:
				// Anthropic-only; the Responses API has no unencrypted
				// equivalent (encrypted_content is ignored by GLM), so the
				// signature-bound blob cannot be replayed.
				slog.Debug("responses: dropping redacted_thinking block — no Responses wire representation",
					"data_len", len(cb.Data))
			}
		}

		// Tool outputs must follow the assistant turn that issued the calls,
		// before any subsequent user text.
		result = append(result, toolOutputs...)

		if msg.Role == types.RoleUser && (len(userTexts) > 0 || len(userImages) > 0) {
			content := make([]responsesContentPart, 0, len(userTexts)+len(userImages))
			for _, t := range userTexts {
				content = append(content, responsesContentPart{Type: "input_text", Text: t})
			}
			content = append(content, userImages...)
			result = append(result, responsesItem{
				Type:    "message",
				Role:    "user",
				Content: content,
			})
		}
	}

	return result
}

// translateResponsesTools converts Anthropic tool definitions to Responses format.
func translateResponsesTools(tools []ToolDef) []responsesTool {
	result := make([]responsesTool, len(tools))
	for i, t := range tools {
		result[i] = responsesTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		}
	}
	return result
}

func (p *ResponsesProvider) translateRequest(req *Request, stream bool) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	maxOutput := req.MaxTokens
	if maxOutput == 0 {
		maxOutput = 32768
	}

	// Effort is deliberately unset: gbot's ThinkingConfig has no numeric
	// tier to map, and GLM keeps thinking on regardless (only depth changes).
	// The extra_params merge is the manual override channel until effort
	// becomes a first-class config.

	if len(req.StopSequences) > 0 {
		slog.Debug("responses: dropping stop_sequences — Responses API has no stop parameter",
			"count", len(req.StopSequences))
	}

	rReq := responsesRequest{
		Model:           model,
		Instructions:    extractSystemPrompt(req.System),
		Input:           translateResponsesItems(req.Messages),
		MaxOutputTokens: maxOutput,
		Store:           false,
		Stream:          stream,
		Temperature:     req.Temperature,
	}
	if len(req.Tools) > 0 {
		rReq.Tools = translateResponsesTools(req.Tools)
	}
	if req.PromptStateKey != nil {
		rReq.PromptCacheKey = req.PromptStateKey.String()
	}

	body, err := json.Marshal(rReq)
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

func (p *ResponsesProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
}

// ---------------------------------------------------------------------------
// Complete — non-streaming
// ---------------------------------------------------------------------------

func (p *ResponsesProvider) Complete(ctx context.Context, req *Request) (*Response, error) {
	if err := ValidateImagesForAPI(req.Messages); err != nil {
		return nil, err
	}

	body, err := p.translateRequest(req, false)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/responses", bytes.NewReader(body))
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
		return nil, p.parseHTTPError(respBody, httpResp.StatusCode)
	}

	return p.translateResponse(respBody)
}

// mapResponsesStopReason derives the Anthropic-shaped stop_reason shared by
// the streaming and non-streaming paths. Tool continuation in the engine is
// driven by block presence, but the persisted StopReason field still needs
// the right value.
func mapResponsesStopReason(status, incompleteReason string, sawFunctionCall bool) string {
	if status == "incomplete" && incompleteReason == "max_output_tokens" {
		return "max_tokens" // engine's continuation-recovery trigger
	}
	if sawFunctionCall {
		return "tool_use"
	}
	return "end_turn"
}

// translateResponse converts a Responses API response body to our Response.
func (p *ResponsesProvider) translateResponse(body []byte) (*Response, error) {
	var r responsesCompleted
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	// A 200 body can still carry status:"failed" — an empty end_turn reply
	// would silently poison the next turn's history.
	if r.Status == "failed" {
		// The error object is optional in failed payloads — mirror the
		// stream path's fallback to the raw body instead of panicking.
		apiErr := &APIError{Status: 200}
		if r.Error != nil {
			apiErr.Type = r.Error.Type
			apiErr.Message = r.Error.Message
		}
		if apiErr.Message == "" {
			apiErr.Message = truncateForLog(string(body), 200)
		}
		return nil, apiErr
	}

	var content []types.ContentBlock
	sawFunctionCall := false
	for _, item := range r.Output {
		switch item.Type {
		case "reasoning":
			// Concatenate without separator: parts are segments of one
			// continuous reasoning stream (same convention as openai.go's
			// multi-thinking-block merge).
			var texts []string
			for _, s := range item.Summary {
				if s.Type == "summary_text" && s.Text != "" {
					texts = append(texts, s.Text)
				}
			}
			for _, part := range item.Content {
				if (part.Type == "reasoning_text" || part.Type == "text") && part.Text != "" {
					texts = append(texts, part.Text)
				}
			}
			if len(texts) > 0 {
				content = append(content, types.ContentBlock{
					Type:     types.ContentTypeThinking,
					Thinking: strings.Join(texts, ""),
				})
			}

		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" && part.Text != "" {
					content = append(content, types.NewTextBlock(part.Text))
				}
			}

		case "function_call":
			sawFunctionCall = true
			content = append(content, types.ContentBlock{
				Type:  types.ContentTypeToolUse,
				ID:    item.CallID,
				Name:  item.Name,
				Input: json.RawMessage(item.Arguments),
			})
		}
	}

	var incompleteReason string
	if r.IncompleteDetails != nil {
		incompleteReason = r.IncompleteDetails.Reason
	}

	return &Response{
		ID:         r.ID,
		Type:       "message",
		Role:       "assistant",
		Content:    content,
		Model:      r.Model,
		StopReason: mapResponsesStopReason(r.Status, incompleteReason, sawFunctionCall),
		Usage:      responsesUsageToUsage(r.Usage),
	}, nil
}

// parseHTTPError parses a Responses error response body. GLM uses the same
// {"error":{code,message,type}} shape as OpenAI.
func (p *ResponsesProvider) parseHTTPError(body []byte, statusCode int) *APIError {
	apiErr := &APIError{
		Status:    statusCode,
		Retryable: IsRetryableStatus(statusCode),
	}

	var errResp struct {
		Error responsesWireError `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err == nil {
		apiErr.Type = errResp.Error.Type
		apiErr.Message = errResp.Error.Message
		if apiErr.Message == "" {
			apiErr.Message = string(body)
		}
	} else {
		slog.Warn("parse Responses error response failed", "error", err, "status", statusCode)
		apiErr.Message = string(body)
	}

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
// Stream — streaming
// ---------------------------------------------------------------------------

func (p *ResponsesProvider) Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error) {
	if err := ValidateImagesForAPI(req.Messages); err != nil {
		return nil, err
	}

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

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/responses", bytes.NewReader(body))
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
		apiErr := p.parseHTTPError(errBody, httpResp.StatusCode)

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
		var body io.Reader = httpResp.Body
		var td TimeoutDisabler
		if p.idleTimeout > 0 {
			tr := &timeoutReader{reader: httpResp.Body, timeout: p.idleTimeout, ctx: ctx}
			body = tr
			td = tr
		}
		if err := p.parseResponsesSSE(ctx, req, body, td, eventCh); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("responses sse: scanner error", "error", err)
			send(ctx, eventCh, StreamEvent{
				Type: "error",
				Error: &APIError{
					Type:      "transport_error",
					Message:   err.Error(),
					Retryable: false,
				},
			})
		}
	}()

	return eventCh, nil
}

// responsesItemState tracks one streaming item. Items arrive sequentially
// (added → deltas → done), but function_call argument deltas carry only
// item_id — no call_id — so state is keyed by item id with the call identity
// memorized from output_item.added.
type responsesItemState struct {
	contentIndex int
	kind         string // "thinking" | "text" | "tool_use"
	open         bool
	argsEmitted  bool
	argsLen      int
}

// responsesStreamAPIError classifies an error from response.failed or a
// top-level error event into an APIError. Classification mirrors codex
// sse/responses.rs: context/quota errors are fatal, overload errors retry.
func responsesStreamAPIError(we *responsesWireError, raw []byte) *APIError {
	apiErr := &APIError{
		Type:      we.Type,
		Message:   we.Message,
		ErrorCode: we.Code,
		Retryable: false,
	}
	if apiErr.Type == "" {
		apiErr.Type = "api_error"
	}
	switch we.Code {
	case "context_length_exceeded":
		apiErr.Type = "prompt_too_long"
		apiErr.ErrorCode = "prompt_too_long"
	case "insufficient_quota", "usage_not_included":
		// fatal, no retry
	case "rate_limit_exceeded", "server_is_overloaded", "slow_down":
		apiErr.Retryable = true
	}
	if apiErr.Message == "" {
		apiErr.Message = truncateForLog(string(raw), 200)
	}
	return apiErr
}

// parseResponsesSSE parses the Responses SSE stream and emits Anthropic-shaped
// StreamEvents. td, when non-nil, allows the parser to disable idle timeout
// during tool input phase.
//
// Returns the underlying scanner error if the stream failed mid-read. A clean
// EOF without response.completed returns nil WITHOUT message_stop — mirroring
// anthropic.go ParseSSE so the engine raises StreamInterruptedError and
// retries; returning an error here would surface as a terminal APIError.
func (p *ResponsesProvider) parseResponsesSSE(ctx context.Context, req *Request, body io.Reader, td TimeoutDisabler, eventCh chan<- StreamEvent) error {
	// Safety net: re-enable the idle timeout on ANY return path, but only
	// if this parser disabled it — a text-only stream must leave the
	// disabler untouched.
	timeoutDisabled := false
	if td != nil {
		defer func() {
			if timeoutDisabled {
				td.SetTimeoutDisabled(false)
			}
		}()
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	started := false
	nextContentIndex := 0
	sawFunctionCall := false
	states := map[string]*responsesItemState{}
	order := []*responsesItemState{} // deterministic close-out order

	// Item events carry no top-level item_id (the id sits inside item.id)
	// while delta events always carry item_id — both must resolve to the
	// same key. output_index is the last-resort key for id-less items.
	itemKey := func(itemID string, outputIndex int) string {
		if itemID != "" {
			return itemID
		}
		return "#" + strconv.Itoa(outputIndex)
	}

	// itemEventKey resolves the state key for added/done events.
	itemEventKey := func(evt responsesSSEEvent) string {
		key := evt.ItemID
		if key == "" && evt.Item != nil {
			key = evt.Item.ID
		}
		return itemKey(key, evt.OutputIndex)
	}

	stateFor := func(key, kind string) *responsesItemState {
		if st, ok := states[key]; ok {
			return st
		}
		st := &responsesItemState{contentIndex: nextContentIndex, kind: kind}
		nextContentIndex++
		states[key] = st
		order = append(order, st)
		return st
	}

	// Streams that never send response.created still must begin with
	// message_start — the engine keys on it for model/usage.
	emit := func(ev StreamEvent) {
		if !started {
			started = true
			send(ctx, eventCh, StreamEvent{
				Type: "message_start",
				Message: &MessageStart{
					ID:    "msg_" + randomID(),
					Role:  string(types.RoleAssistant),
					Model: req.Model,
					Usage: types.Usage{},
				},
			})
		}
		send(ctx, eventCh, ev)
	}

	closeOpenBlocks := func() {
		for _, st := range order {
			if st.open {
				st.open = false
				emit(StreamEvent{Type: "content_block_stop", Index: st.contentIndex})
			}
		}
	}

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if len(line) > 100_000 {
			slog.Warn("responses sse: line too long, skipping", "length", len(line))
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue // event:/id:/retry: lines carry no payload we need
		}

		if strings.TrimSpace(data) == "[DONE]" {
			// GLM sends [DONE] after response.completed (already returned);
			// reaching it first means the terminal event went missing — exit
			// gracefully rather than treating the stream as interrupted.
			closeOpenBlocks()
			emit(StreamEvent{Type: "message_stop"})
			return nil
		}

		var evt responsesSSEEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			slog.Warn("responses sse: failed to parse chunk", "error", err, "data", truncateForLog(data, 200))
			continue
		}

		switch evt.Type {
		case "response.created":
			started = true
			msg := &MessageStart{
				ID:    "msg_" + randomID(),
				Role:  string(types.RoleAssistant),
				Model: req.Model,
				Usage: types.Usage{},
			}
			if len(evt.Response) > 0 {
				var comp responsesCompleted
				if err := json.Unmarshal(evt.Response, &comp); err == nil {
					if comp.ID != "" {
						msg.ID = comp.ID
					}
					if comp.Model != "" {
						msg.Model = comp.Model
					}
				}
			}
			send(ctx, eventCh, StreamEvent{Type: "message_start", Message: msg})

		case "response.in_progress",
			"response.content_part.added",
			"response.content_part.done",
			"response.reasoning_text.done",
			"response.reasoning_summary_part.added",
			"response.reasoning_summary_text.done",
			"response.output_text.done",
			"response.function_call_arguments.done":
			// no-op

		case "response.output_item.added":
			if evt.Item == nil {
				continue
			}
			switch evt.Item.Type {
			case "reasoning":
				stateFor(itemEventKey(evt), "thinking") // lazy open
			case "message":
				stateFor(itemEventKey(evt), "text") // lazy open
			case "function_call":
				sawFunctionCall = true
				st := stateFor(itemEventKey(evt), "tool_use")
				st.open = true
				if td != nil {
					td.SetTimeoutDisabled(true)
					timeoutDisabled = true
				}
				emit(StreamEvent{
					Type:  "content_block_start",
					Index: st.contentIndex,
					ContentBlock: &types.ContentBlock{
						Type: types.ContentTypeToolUse,
						ID:   evt.Item.CallID,
						Name: evt.Item.Name,
					},
				})
			}

		case "response.reasoning_text.delta",
			"response.reasoning_summary_text.delta":
			// OpenAI's o-series never exposes raw thought — it streams a
			// summary instead. Which flavor arrives is server policy; the
			// client treats both as thinking text.
			if evt.Delta == "" {
				continue
			}
			st := stateFor(itemEventKey(evt), "thinking")
			if !st.open {
				st.open = true
				emit(StreamEvent{
					Type:         "content_block_start",
					Index:        st.contentIndex,
					ContentBlock: &types.ContentBlock{Type: types.ContentTypeThinking},
				})
			}
			emit(StreamEvent{
				Type:  "content_block_delta",
				Index: st.contentIndex,
				Delta: &StreamDelta{Type: "thinking_delta", Thinking: evt.Delta},
			})

		case "response.output_text.delta":
			if evt.Delta == "" {
				continue
			}
			st := stateFor(itemEventKey(evt), "text")
			if !st.open {
				st.open = true
				emit(StreamEvent{
					Type:         "content_block_start",
					Index:        st.contentIndex,
					ContentBlock: &types.ContentBlock{Type: types.ContentTypeText},
				})
			}
			emit(StreamEvent{
				Type:  "content_block_delta",
				Index: st.contentIndex,
				Delta: &StreamDelta{Type: "text_delta", Text: evt.Delta},
			})

		case "response.function_call_arguments.delta":
			st := stateFor(itemEventKey(evt), "tool_use")
			if !st.open {
				// output_item.added went missing — open defensively so the
				// block still reaches the engine.
				st.open = true
				emit(StreamEvent{
					Type:  "content_block_start",
					Index: st.contentIndex,
					ContentBlock: &types.ContentBlock{
						Type: types.ContentTypeToolUse,
						ID:   evt.CallID,
					},
				})
			}
			if st.argsLen+len(evt.Delta) > maxToolArgumentsSize {
				slog.Warn("responses sse: tool arguments exceed size limit",
					"index", st.contentIndex, "size", st.argsLen)
				return nil
			}
			st.argsLen += len(evt.Delta)
			st.argsEmitted = true
			emit(StreamEvent{
				Type:  "content_block_delta",
				Index: st.contentIndex,
				Delta: &StreamDelta{Type: "input_json_delta", PartialJSON: evt.Delta},
			})

		case "response.output_item.done":
			if evt.Item == nil {
				continue
			}
			switch evt.Item.Type {
			case "reasoning", "message":
				if st := states[itemEventKey(evt)]; st != nil && st.open {
					st.open = false
					emit(StreamEvent{Type: "content_block_stop", Index: st.contentIndex})
				}

			case "function_call":
				sawFunctionCall = true
				st := stateFor(itemEventKey(evt), "tool_use")
				if !st.open {
					st.open = true
					emit(StreamEvent{
						Type:  "content_block_start",
						Index: st.contentIndex,
						ContentBlock: &types.ContentBlock{
							Type: types.ContentTypeToolUse,
							ID:   evt.Item.CallID,
							Name: evt.Item.Name,
						},
					})
				}
				if !st.argsEmitted && evt.Item.Arguments != "" &&
					len(evt.Item.Arguments) <= maxToolArgumentsSize {
					// No delta stream for this call — the full arguments ride
					// on the done item; emit them as one delta so the engine
					// assembles non-empty input.
					st.argsEmitted = true
					emit(StreamEvent{
						Type:  "content_block_delta",
						Index: st.contentIndex,
						Delta: &StreamDelta{Type: "input_json_delta", PartialJSON: evt.Item.Arguments},
					})
				}
				st.open = false
				emit(StreamEvent{Type: "content_block_stop", Index: st.contentIndex})
				// Timeout re-enable happens once, via the deferred guard set
				// at function entry.
			}

		case "response.completed", "response.incomplete":
			var comp responsesCompleted
			if len(evt.Response) > 0 {
				if err := json.Unmarshal(evt.Response, &comp); err != nil {
					slog.Warn("responses sse: failed to parse terminal payload", "error", err)
				}
			}
			// A missing output_item.done would leave a block dangling — the
			// engine only registers tool_use on content_block_stop, so close
			// everything before terminating.
			closeOpenBlocks()
			var incompleteReason string
			if comp.IncompleteDetails != nil {
				incompleteReason = comp.IncompleteDetails.Reason
			}
			usage := responsesUsageToUsage(comp.Usage)
			emit(StreamEvent{
				Type:     "message_delta",
				DeltaMsg: &MessageDelta{StopReason: mapResponsesStopReason(comp.Status, incompleteReason, sawFunctionCall)},
				Usage:    &usage,
			})
			emit(StreamEvent{Type: "message_stop"})
			return nil

		case "response.failed":
			var apiErr *APIError
			if len(evt.Response) > 0 {
				var comp responsesCompleted
				if err := json.Unmarshal(evt.Response, &comp); err == nil && comp.Error != nil {
					apiErr = responsesStreamAPIError(comp.Error, evt.Response)
				}
			}
			if apiErr == nil {
				apiErr = &APIError{Type: "api_error", Message: truncateForLog(string(evt.Response), 200)}
			}
			emit(StreamEvent{Type: "error", Error: apiErr})
			return nil

		case "error":
			// Two shapes in the wild: flat {type,code,message} and nested
			// {type,error:{code,message}}.
			var top struct {
				Code    string              `json:"code"`
				Message string              `json:"message"`
				Error   *responsesWireError `json:"error"`
			}
			if err := json.Unmarshal([]byte(data), &top); err != nil {
				slog.Warn("responses sse: failed to parse error event", "error", err, "data", truncateForLog(data, 200))
				continue
			}
			we := &responsesWireError{Code: top.Code, Message: top.Message}
			if top.Error != nil {
				we = top.Error
			}
			emit(StreamEvent{Type: "error", Error: responsesStreamAPIError(we, []byte(data))})
			return nil
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	// Clean EOF without completed: emit nothing further. The engine turns
	// "no message_stop + content" into StreamInterruptedError and retries.
	return nil
}
