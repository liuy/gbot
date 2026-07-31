package llm

import (
	"fmt"
	"testing"
)

// runOverflowCase asserts that IsContextOverflow correctly classifies err as
// want. Pass want=true for overflow, false for non-overflow. err is built
// from an APIError with the given message/status so the test mirrors what
// providers actually return.
func runOverflowCase(t *testing.T, message string, status int, want bool) {
	t.Helper()
	err := &APIError{Message: message, Status: status}
	got := IsContextOverflow(err)
	if got != want {
		t.Errorf("IsContextOverflow(message=%q) = %v, want %v", message, want, got)
	}
}

// runOverflowCaseCode asserts that an APIError with the given ErrorCode is
// classified as overflow — preserves the fast-path (ErrorCode-based) coverage
// that openai.go/anthropic.go already set.
func runOverflowCaseCode(t *testing.T, code string, want bool) {
	t.Helper()
	err := &APIError{ErrorCode: code, Status: 400}
	got := IsContextOverflow(err)
	if got != want {
		t.Errorf("IsContextOverflow(ErrorCode=%q) = %v, want %v", code, want, got)
	}
}

func TestIsContextOverflow_Anthropic(t *testing.T) {
	t.Parallel()
	runOverflowCase(t, "prompt is too long: 213462 tokens > 200000 maximum", 400, true)
}

func TestIsContextOverflow_OpenAI(t *testing.T) {
	t.Parallel()
	runOverflowCase(t, "Your input exceeds the context window of this model", 400, true)
}

func TestIsContextOverflow_DeepSeek(t *testing.T) {
	t.Parallel()
	// Real error returned by DeepSeek API (also OpenAI-compatible providers).
	runOverflowCase(t,
		"This model's maximum context length is 1048576 tokens. However, you requested 1113499 tokens (1080731 in the messages, 32768 in the completion). Please reduce the length of the messages or completion.",
		400, true)
}

func TestIsContextOverflow_Google(t *testing.T) {
	t.Parallel()
	runOverflowCase(t, "The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)", 400, true)
}

func TestIsContextOverflow_xAI(t *testing.T) {
	t.Parallel()
	runOverflowCase(t, "This model's maximum prompt length is 131072 but the request contains 537812 tokens", 400, true)
}

func TestIsContextOverflow_Groq(t *testing.T) {
	t.Parallel()
	runOverflowCase(t, "Please reduce the length of the messages or completion", 400, true)
}

func TestIsContextOverflow_OpenRouter(t *testing.T) {
	t.Parallel()
	runOverflowCase(t, "This endpoint's maximum context length is 8192 tokens. However, you requested about 12345 tokens", 400, true)
}

func TestIsContextOverflow_Llamacpp(t *testing.T) {
	t.Parallel()
	runOverflowCase(t, "the request exceeds the available context size, try increasing it", 400, true)
}

func TestIsContextOverflow_LMStudio(t *testing.T) {
	t.Parallel()
	runOverflowCase(t, "tokens to keep from the initial prompt is greater than the context length", 400, true)
}

func TestIsContextOverflow_Copilot(t *testing.T) {
	t.Parallel()
	runOverflowCase(t, "prompt token count of 12345 exceeds the limit of 8192", 400, true)
}

func TestIsContextOverflow_MiniMax(t *testing.T) {
	t.Parallel()
	runOverflowCase(t, "invalid params, context window exceeds limit", 400, true)
}

func TestIsContextOverflow_Kimi(t *testing.T) {
	t.Parallel()
	runOverflowCase(t, "Your request exceeded model token limit: 131072 (requested: 200000)", 400, true)
}

func TestIsContextOverflow_Bedrock(t *testing.T) {
	t.Parallel()
	runOverflowCase(t, "input is too long for requested model", 400, true)
}

func TestIsContextOverflow_zAI_FinishReason(t *testing.T) {
	t.Parallel()
	// z.ai sometimes surfaces non-standard finish_reason as error text.
	runOverflowCase(t, "Provider finish_reason: model_context_window_exceeded", 400, true)
	runOverflowCase(t, "model_context_window_exceeded", 400, true)
}

func TestIsContextOverflow_HTTP413(t *testing.T) {
	t.Parallel()
	runOverflowCase(t, "request_too_large", 413, true)
	runOverflowCase(t, "Request exceeds the maximum size allowed by this model", 413, true)
	runOverflowCase(t, "413 Payload Too Large", 413, true)
	runOverflowCase(t, "413 Request Entity Too Large", 413, true)
}

func TestIsContextOverflow_400NoBody(t *testing.T) {
	t.Parallel()
	// Cerebras/Mistral return bare 400/413 with no body; proxies wrap them.
	runOverflowCase(t, "400 status code (no body)", 400, true)
	runOverflowCase(t, "413 status code (no body)", 400, true)
	runOverflowCase(t, `400 status code: {"error":"Error from inference backend: 400 status code (no body)"}`, 400, true)
	runOverflowCase(t, "Upstream rejected request: 400 status code (no body)", 400, true)
}

func TestIsContextOverflow_FastPathErrorCode(t *testing.T) {
	t.Parallel()
	// ErrorCode fast-path preserves openai.go/anthropic.go mappings.
	runOverflowCaseCode(t, "prompt_too_long", true)
	// A non-overflow code with empty message must not classify as overflow
	// via the regex fallback (which would see an empty string).
	runOverflowCaseCode(t, "rate_limit_error", false)
	runOverflowCaseCode(t, "", false)
}

func TestIsContextOverflow_429GuardsGenericFallback(t *testing.T) {
	t.Parallel()
	// Status 429 is rate limiting and must never classify as overflow,
	// even if the message contains a generic phrase like "too many tokens"
	// that p18 would otherwise catch.
	runOverflowCase(t, "429 Too Many Requests: too many tokens per minute", 429, false)
	runOverflowCase(t, "too many tokens per minute", 429, false)
}

func TestIsContextOverflow_NonOverflow(t *testing.T) {
	t.Parallel()
	// Negative cases — must NOT match.
	runOverflowCase(t, "400 Bad Request: invalid API key", 400, false)
	runOverflowCase(t, "429 Too Many Requests", 429, false)
	runOverflowCase(t, "429 status code (no body)", 429, false)
	runOverflowCase(t, "413 Forbidden", 413, false)
	runOverflowCase(t, "internal server error", 500, false)
	runOverflowCase(t, "", 400, false)
}

func TestIsContextOverflow_Nil(t *testing.T) {
	t.Parallel()
	if IsContextOverflow(nil) {
		t.Error("IsContextOverflow(nil) = true, want false")
	}
	if IsContextOverflow(fmt.Errorf("not an APIError")) {
		t.Error("IsContextOverflow(non-APIError) = true, want false")
	}
}

// TestMessageLooksLikeOverflow_AllPatterns pairs each of the 25 patterns with
// a representative provider message, so breaking or deleting any single
// pattern fails its line. Messages are crafted to hit ONLY the target pattern
// where feasible (otherwise verified by the matrix in review).
func TestMessageLooksLikeOverflow_AllPatterns(t *testing.T) {
	t.Parallel()
	cases := []string{
		"prompt is too long: 213462 tokens > 200000 maximum",             // p1  Anthropic
		"input is too long for requested model",                          // p2  Bedrock
		"Your input exceeds the context window of this model",            // p3  OpenAI
		"The input token count (1196265) exceeds the maximum allowed",    // p4  Google
		"This model's maximum prompt length is 131072 but request",       // p5  xAI
		"Please reduce the length of the messages or completion",         // p6  Groq
		"This model's maximum context length is 1048576 tokens",          // p7  OpenRouter/DeepSeek
		"prompt token count of 12345 exceeds the limit of 8192",          // p8  Copilot
		"the request exceeds the available context size, try increasing", // p9 llama.cpp
		"requested tokens exceed context window",                         // p10 llama.cpp/compat
		"context window overflow detected",                               // p11 generic local
		"prompt is too long for n_ctx=4096",                              // p12 llama.cpp phrasing
		"requested token exceeds n_ctx of model",                         // p13 llama.cpp n_ctx
		"tokens to keep is greater than the context length",              // p14 LM Studio
		"invalid params, context window exceeds limit",                   // p15 MiniMax
		"Your request exceeded model token limit: 131072",                // p16 Kimi
		"context_length_exceeded",                                        // p17 OpenAI code-as-text
		"error: too many tokens in request body",                         // p18 generic fallback
		"token limit exceeded for this model",                            // p19 generic fallback
		"request_too_large",                                              // p20 Anthropic 413
		"Request exceeds the maximum size allowed",                       // p21 Anthropic 413 variant
		"413 Payload Too Large",                                          // p22 HTTP 413
		"413 Request Entity Too Large",                                   // p23 HTTP 413
		"413 payload too large",                                          // p24 (same line shape; combined w/ p22/23 in patterns)
		"Provider finish_reason: model_context_window_exceeded",          // p25 z.ai
	}
	for _, msg := range cases {
		if !messageLooksLikeOverflow(msg) {
			t.Errorf("expected overflow match for %q", msg)
		}
	}
}

// TestMessageLooksLikeOverflow_NoBodyStatus confirms the noBodyStatusPattern
// (separate from the 25) catches Cerebras/Mistral bare 400/413 + proxy wraps.
func TestMessageLooksLikeOverflow_NoBodyStatus(t *testing.T) {
	t.Parallel()
	cases := []string{
		"400 status code (no body)",
		"413 status code (no body)",
		`400 status code: {"error":"backend: 400 status code (no body)"}`,
		"Upstream: 400 (no body)",
	}
	for _, msg := range cases {
		if !messageLooksLikeOverflow(msg) {
			t.Errorf("expected no-body status match for %q", msg)
		}
	}
}
