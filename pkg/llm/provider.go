// Package llm provides the LLM backend abstraction for gbot.
//
// Source reference: services/api/client.ts, services/api/withRetry.ts
// The Provider interface decouples the engine from any specific LLM API.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// Provider is the interface for LLM backends.
// Source: services/api/client.ts — abstracts the Anthropic SDK client.
type Provider interface {
	// Complete sends a non-streaming request and returns the full response.
	Complete(ctx context.Context, req *Request) (*Response, error)

	// Stream sends a streaming request and returns a channel of events.
	// Source: TS async generator pattern → Go channel pattern.
	Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error)
}

// Request represents an LLM API request.
// Source: Anthropic Messages API POST /v1/messages
type Request struct {
	Model     string           `json:"model"`
	MaxTokens int              `json:"max_tokens"`
	Messages  []types.Message  `json:"messages"`
	System    json.RawMessage  `json:"system,omitempty"`    // string or []ContentBlock
	Tools     []ToolDef        `json:"tools,omitempty"`
	Stream    bool             `json:"stream,omitempty"`

	// Thinking configuration (extended thinking)
	Thinking *ThinkingConfig  `json:"thinking,omitempty"`

	// Temperature (0.0 to 1.0)
	Temperature *float64 `json:"temperature,omitempty"`

	// Stop sequences
	StopSequences []string `json:"stop_sequences,omitempty"`

	// Metadata
	Metadata *RequestMetadata `json:"metadata,omitempty"`

	// Cache control for Anthropic prompt caching.
	// Source: claude.ts:358-374 — when non-nil, system blocks get cache_control markers.
	CacheControl *types.CacheControlConfig `json:"-"`

	// SystemBlocks stores structured system blocks for hash computation.
	// When CacheControl is non-nil, Complete/Stream use these blocks and inject cache_control.
	// Source: claude.ts system prompt assembly.
	SystemBlocks []SystemBlockParam `json:"-"`

	// PromptStateKey identifies the tracking key for cache break detection.
	// When non-nil, RecordPromptState and CheckResponseForCacheBreak are called around the API call.
	// Source: promptCacheBreakDetection.ts:149-158
	PromptStateKey *PromptStateKey `json:"-"`
}

// RequestMetadata carries request-level metadata.
// Source: Anthropic API metadata field.
type RequestMetadata struct {
	UserID string `json:"user_id,omitempty"`
}

// ThinkingConfig configures extended thinking.
// Source: utils/thinking.ts
type ThinkingConfig struct {
	Type         string `json:"type"`                    // "enabled" or "disabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// ToolDef represents a tool definition sent to the LLM.
// Source: Anthropic API tool definition format.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Response represents a non-streaming LLM API response.
// Source: Anthropic Messages API response.
type Response struct {
	ID         string               `json:"id"`
	Type       string               `json:"type"`        // "message"
	Role       string               `json:"role"`         // "assistant"
	Content    []types.ContentBlock `json:"content"`
	Model      string               `json:"model"`
	StopReason string               `json:"stop_reason"`
	Usage      types.Usage          `json:"usage"`
}

// StreamEvent represents a single event in the SSE stream.
// Source: Anthropic SSE event types.
// This is a discriminated union — use the Type field to determine variant.
type StreamEvent struct {
	Type string `json:"type"`

	// message_start
	Message *MessageStart `json:"message,omitempty"`

	// content_block_start, content_block_delta, content_block_stop
	Index    int              `json:"index,omitempty"`
	ContentBlock *types.ContentBlock `json:"content_block,omitempty"`   // content_block_start
	Delta    *StreamDelta     `json:"delta,omitempty"`   // content_block_delta

	// message_delta (final usage/stop_reason)
	Usage    *UsageDelta      `json:"usage,omitempty"`
	DeltaMsg *MessageDelta    `json:"delta_msg,omitempty"`

	// Error
	Error *APIError `json:"error,omitempty"`

	// Raw event data for debugging
	Raw json.RawMessage `json:"raw,omitempty"`
}

// MessageStart carries the initial message metadata.
type MessageStart struct {
	ID    string        `json:"id"`
	Type  string        `json:"type"`
	Role  string        `json:"role"`
	Model string        `json:"model"`
	Usage types.Usage   `json:"usage"`
}

// StreamDelta carries incremental content during streaming.
// Source: Anthropic content_block_delta events.
type StreamDelta struct {
	Type string `json:"type"` // "text_delta", "input_json_delta", "thinking_delta"

	// text_delta
	Text string `json:"text,omitempty"`

	// input_json_delta
	PartialJSON string `json:"partial_json,omitempty"`

	// thinking_delta
	Thinking string `json:"thinking,omitempty"`

	// Stop reason (in message_delta event)
	StopReason string `json:"stop_reason,omitempty"`
}

// MessageDelta carries the final message-level delta.
type MessageDelta struct {
	StopReason string     `json:"stop_reason,omitempty"`
	Usage      *UsageDelta `json:"usage,omitempty"`
}

// UsageDelta carries incremental usage info.
// Source: Anthropic message_delta event — extended for cache token support.
type UsageDelta struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens    int `json:"cache_read_input_tokens,omitempty"`
}

// APIError represents an error from the LLM API.
// Source: services/api/errors.ts
type APIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Status  int    `json:"status_code,omitempty"`

	// Error code for specific error types
	ErrorCode string `json:"error_code,omitempty"`

	// Whether this error is retryable
	Retryable bool `json:"-"`
}

func (e *APIError) Error() string {
	return e.Message
}

// IsRetryable returns whether the error can be retried.
func IsRetryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable
	}
	return false
}

// IsContextOverflow returns whether the error is a context window overflow.
func IsContextOverflow(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == 400 && apiErr.ErrorCode == "prompt_too_long"
	}
	return false
}

// IsMaxOutputTokens returns whether the error is max_output_tokens withholding.
func IsMaxOutputTokens(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Type == "max_output_tokens"
	}
	return false
}

// IsRateLimit returns whether the error is a rate limit error.
func IsRateLimit(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == 429
	}
	return false
}

// IsOverloaded returns whether the error is a 529 overloaded error.
func IsOverloaded(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == 529
	}
	return false
}

// IsServerError returns whether the error is a 5xx server error.
func IsServerError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= 500 && apiErr.Status < 600
	}
	return false
}

// RetryConfig configures retry behavior.
// Source: services/api/withRetry.ts
type RetryConfig struct {
	MaxRetries  int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

// DefaultRetryConfig returns the default retry configuration.
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxRetries:  10,
		BaseBackoff: 500 * time.Millisecond,
		MaxBackoff:  32 * time.Second,
	}
}

// BaseProvider holds fields shared by all providers.
type BaseProvider struct {
	httpClient  *http.Client
	retryConfig *RetryConfig
	idleTimeout time.Duration // SSE idle timeout, used by OpenAI provider
}

// CalculateBackoff computes exponential backoff with jitter.
// Source: services/api/withRetry.ts — 1:1 port.
func CalculateBackoff(attempt int, cfg *RetryConfig) time.Duration {
	base := float64(cfg.BaseBackoff)
	exponential := base * math.Pow(2, float64(attempt))
	withJitter := exponential * (0.5 + float64(fastrand())/float64(1<<32)) // ±50% jitter
	capped := math.Min(withJitter, float64(cfg.MaxBackoff))
	return time.Duration(capped)
}

// CalculateBackoffWithRetryAfter respects Retry-After header when present.
func CalculateBackoffWithRetryAfter(attempt int, cfg *RetryConfig, retryAfter time.Duration) time.Duration {
	base := CalculateBackoff(attempt, cfg)
	if retryAfter > 0 && retryAfter > base {
		return retryAfter
	}
	return base
}

// IsRetryableStatus returns true for retryable HTTP status codes.
func IsRetryableStatus(statusCode int) bool {
	switch statusCode {
	case 429, 529, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

// IsConnectionError returns true for connection-level errors.
func IsConnectionError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "temporary")
}

// truncateForLog truncates a string for safe logging.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}


// SystemBlockParam represents a single system prompt block.
// Source: Anthropic API system parameter (array variant).
type SystemBlockParam struct {
	Type         string              `json:"type"`                    // "text"
	Text         string              `json:"text"`
	CacheControl *types.CacheControlConfig `json:"cache_control,omitempty"`
}

// PromptStateKey identifies a tracked prompt state source.
// Source: promptCacheBreakDetection.ts:149-158
type PromptStateKey struct {
	QuerySource string // e.g. "repl_main_thread", "agent:custom"
	AgentID     string
}

func (k PromptStateKey) String() string {
	if k.AgentID != "" {
		return k.QuerySource + ":" + k.AgentID
	}
	return k.QuerySource
}

// promptStateInternal is the internal state stored for break detection.
// Contains lazy diff-building closure. Not serializable.
// Source: promptCacheBreakDetection.ts:28-69
type promptStateInternal struct {
	SystemHash           uint32
	ToolsHash            uint32
	CacheControlHash     uint32
	ToolNames            []string
	PerToolHashes        map[string]uint32
	SystemCharCount      int
	Model                string
	FastMode             bool
	GlobalCacheStrategy  string
	Betas                []string
	AutoModeActive       bool
	IsUsingOverage       bool
	CachedMCEnabled      bool
	EffortValue          string
	ExtraBodyHash        uint32
	CallCount            int
	PrevCacheRead        int
	CacheDeletionsPending bool
	BuildDiffableContent func() string // lazy eval (TS:206-222)
	PendingChanges       *PendingChanges
}

// PromptStateSnapshot records pre-call state for break detection (public input).
// Source: promptCacheBreakDetection.ts:227-241
type PromptStateSnapshot struct {
	SystemHash           uint32
	ToolsHash            uint32
	CacheControlHash     uint32
	ToolNames            []string
	PerToolHashes        map[string]uint32
	SystemCharCount      int
	Model                string
	FastMode             bool
	GlobalCacheStrategy  string
	Betas                []string
	CallCount            int
	PrevCacheRead        int
	CacheDeletionsPending bool
}

// PendingChanges records what changed between calls.
// Source: promptCacheBreakDetection.ts:71-99
type PendingChanges struct {
	SystemPromptChanged         bool
	ToolSchemasChanged          bool
	ModelChanged                bool
	FastModeChanged             bool
	CacheControlChanged         bool
	GlobalCacheStrategyChanged  bool
	BetasChanged                bool
	AutoModeActiveChanged       bool
	OverageChanged              bool
	CachedMCEnabledChanged      bool
	EffortChanged               bool
	ExtraBodyChanged            bool
	AddedToolCount              int
	RemovedToolCount            int
	SystemCharDelta             int
	AddedTools                  []string
	RemovedTools                []string
	ChangedToolSchemas          []string
	PreviousModel               string
	NewModel                    string
	PrevGlobalCacheStrategy     string
	NewGlobalCacheStrategy      string
	PrevEffortValue             string
	NewEffortValue              string
	AddedBetas                  []string
	RemovedBetas                []string
	BuildPrevDiffableContent    func() string // lazy eval: diff written only on cache break
}
