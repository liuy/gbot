// Package llm provides the LLM backend abstraction for gbot.
//
// Source reference: services/api/client.ts, services/api/withRetry.ts
// The Provider interface decouples the engine from any specific LLM API.
package llm

import (
	"context"
	"encoding/json"
	"time"

	"github.com/user/gbot/pkg/types"
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
type UsageDelta struct {
	OutputTokens int `json:"output_tokens"`
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
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.Retryable
	}
	return false
}

// IsContextOverflow returns whether the error is a context window overflow.
func IsContextOverflow(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.Status == 400 && apiErr.ErrorCode == "prompt_too_long"
	}
	return false
}

// IsMaxOutputTokens returns whether the error is max_output_tokens withholding.
func IsMaxOutputTokens(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.Type == "max_output_tokens"
	}
	return false
}

// IsRateLimit returns whether the error is a rate limit error.
func IsRateLimit(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.Status == 429
	}
	return false
}

// IsOverloaded returns whether the error is a 529 overloaded error.
func IsOverloaded(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.Status == 529
	}
	return false
}

// IsServerError returns whether the error is a 5xx server error.
func IsServerError(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
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
