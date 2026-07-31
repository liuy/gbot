package llm

import "regexp"

// overflowPatterns matches error messages returned when the input exceeds the
// model's context window. Provider list (with example messages) is documented
// inline so future contributors know which line covers which backend.
//
// Translated from omp packages/ai/src/utils/overflow.ts OVERFLOW_PATTERNS.
// omp chose regex-on-message over per-provider error-code mapping because
// most OpenAI-compatible providers (DeepSeek, GLM/z.ai, OpenRouter, Kimi,
// MiniMax, …) omit the "code" field on 400 errors — only the message text is
// stable. Adding a new provider then needs no per-provider branch here; it
// only needs a matching pattern, and most reuse an existing one.
var overflowPatterns = []*regexp.Regexp{
	// Anthropic: "prompt is too long: 213462 tokens > 200000 maximum"
	regexp.MustCompile(`(?i)prompt is too long`),
	// Amazon Bedrock
	regexp.MustCompile(`(?i)input is too long for requested model`),
	// OpenAI (Completions & Responses API)
	regexp.MustCompile(`(?i)exceeds the context window`),
	// Google (Gemini)
	regexp.MustCompile(`(?i)input token count.*exceeds the maximum`),
	// xAI (Grok): "maximum prompt length is 131072 but the request contains …"
	regexp.MustCompile(`(?i)maximum prompt length is \d+`),
	// Groq
	regexp.MustCompile(`(?i)reduce the length of the messages`),
	// OpenRouter (all backends) — DeepSeek returns this identical wording:
	//   "This model's maximum context length is 1048576 tokens. However, …"
	regexp.MustCompile(`(?i)maximum context length is \d+ tokens`),
	// GitHub Copilot
	regexp.MustCompile(`(?i)exceeds the limit of \d+`),
	// llama.cpp server
	regexp.MustCompile(`(?i)exceeds the available context size`),
	// llama.cpp / OpenAI-compatible local servers
	regexp.MustCompile(`(?i)requested tokens?.*exceed.*context (window|length|size)`),
	// Generic local server variants
	regexp.MustCompile(`(?i)context (window|length|size).*(exceeded|overflow|too small)`),
	// llama.cpp phrasing variants
	regexp.MustCompile(`(?i)(prompt|input).*(too long|too large).*(context|n_ctx)`),
	regexp.MustCompile(`(?i)requested tokens?.*(exceeds?|greater than).*(n_ctx|context)`),
	// LM Studio
	regexp.MustCompile(`(?i)greater than the context length`),
	// MiniMax
	regexp.MustCompile(`(?i)context window exceeds limit`),
	// Kimi For Coding: "exceeded model token limit: 131072 (requested: 200000)"
	regexp.MustCompile(`(?i)exceeded model token limit`),
	// Generic fallback — covers OpenAI "context_length_exceeded" surfaced as text
	regexp.MustCompile(`(?i)context[_ ]length[_ ]exceeded`),
	// Generic fallbacks
	regexp.MustCompile(`(?i)too many tokens`),
	regexp.MustCompile(`(?i)token limit exceeded`),
	// Anthropic 413 (request body too large)
	regexp.MustCompile(`(?i)request_too_large`),
	regexp.MustCompile(`(?i)request exceeds the maximum size`),
	// Generic HTTP 413 variants
	regexp.MustCompile(`(?i)payload too large`),
	regexp.MustCompile(`(?i)entity too large`),
	// "413 Request Entity Too Large" variants
	regexp.MustCompile(`(?i)\b413\b.*\b(request|payload|entity)\b.*\btoo large\b`),
	// z.ai non-standard finish_reason surfaced as error text
	regexp.MustCompile(`(?i)model_context_window_exceeded`),
}

// noBodyStatusPattern matches the "400/413 status code (no body)" messages
// returned by Cerebras and Mistral on context overflow, including when a
// proxy provider (e.g. api.synthetic.new) wraps the upstream no-body
// response inside a JSON envelope — the phrase may appear anywhere.
// 429 is intentionally excluded: rate limiting is not context overflow.
var noBodyStatusPattern = regexp.MustCompile(`(?i)\b4(00|13)\s*(status code)?\s*\(no body\)`)

// messageLooksLikeOverflow reports whether msg matches a known context
// overflow phrasing from any provider. Callers should still try the
// ErrorCode fast-path first (see IsContextOverflow).
func messageLooksLikeOverflow(msg string) bool {
	if msg == "" {
		return false
	}
	for _, p := range overflowPatterns {
		if p.MatchString(msg) {
			return true
		}
	}
	return noBodyStatusPattern.MatchString(msg)
}
