// Runtime reasoning-effort dialect negotiation for the chat-completions path.
// Ported line-by-line from oh-my-pi
// packages/ai/src/providers/openai-reasoning-fallback.ts (415 lines) — the
// branch structure, regex verdicts, and precedence order are the product; do
// not simplify them. Differences required by the host are structural only:
// TS's string|null|Symbol tri-state maps onto *reasoningEffortFallback (nil =
// "no conclusion", zero value = "drop the field", value = "remap tier",
// stripTemplateKwarg = "kwargs spelling rejected"), Set<string> onto
// map[string]bool, and the session state lives on the provider instance with
// a mutex because one instance serves the engine, sub-agents, and games
// concurrently.
package llm

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// reasoningEffortFallback is one negotiation conclusion. nil pointer means
// "no conclusion" (TS undefined); zero value means "stop sending the effort
// field" (TS null); value means "remap to this tier" (TS string).
type reasoningEffortFallback struct {
	value string
	// stripTemplateKwarg marks a rejection of the chat_template_kwargs
	// spelling itself (strict kwargs whitelists), not of the effort value.
	stripTemplateKwarg bool
}

// reasoningFallbackState freezes negotiation conclusions per
// endpoint:baseUrl:wireModel for the lifetime of the provider instance. omp
// clears it when the session closes; gbot providers live in the registry for
// the whole process, so clear exists for parity and tests only.
type reasoningFallbackState struct {
	mu        sync.Mutex
	fallbacks map[string]*reasoningEffortFallback
}

var enabledReasoningValues = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

var knownReasoningValue = map[string]bool{
	"none":    true,
	"minimal": true,
	"low":     true,
	"medium":  true,
	"high":    true,
	"xhigh":   true,
	"max":     true,
}

var reasoningValueRank = map[string]int{
	"minimal": 0,
	"low":     1,
	"medium":  2,
	"high":    3,
	"xhigh":   4,
	"max":     5,
}

func newReasoningFallbackState() *reasoningFallbackState {
	return &reasoningFallbackState{fallbacks: map[string]*reasoningEffortFallback{}}
}

// clear drops every frozen conclusion. Nil-receiver safe: a provider literal
// without initialized state must not panic (omp state?. optional chaining).
func (s *reasoningFallbackState) clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallbacks = map[string]*reasoningEffortFallback{}
}

func (s *reasoningFallbackState) get(key string) *reasoningEffortFallback {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fallbacks[key]
}

func (s *reasoningFallbackState) remember(key string, fb *reasoningEffortFallback) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallbacks[key] = fb
}

// reasoningFallbackKey mirrors omp createOpenAIReasoningEffortFallbackKey:
// empty segments stay empty, the colons always separate.
func reasoningFallbackKey(endpoint, baseURL, wireModel string) string {
	return endpoint + ":" + baseURL + ":" + wireModel
}

// readOpenAIReasoningEffort reads the current tier across the three wire
// spellings, in omp precedence order: top-level field, nested reasoning.effort,
// template kwarg. Only string values count; anything else is absent.
func readOpenAIReasoningEffort(params map[string]any) (string, bool) {
	if params == nil {
		return "", false
	}
	if s, ok := params["reasoning_effort"].(string); ok {
		return s, true
	}
	if reasoning, ok := params["reasoning"].(map[string]any); ok {
		if s, ok := reasoning["effort"].(string); ok {
			return s, true
		}
	}
	return readTemplateKwargReasoningEffort(params)
}

func readTemplateKwargReasoningEffort(params map[string]any) (string, bool) {
	if params == nil {
		return "", false
	}
	kwargs, ok := params["chat_template_kwargs"].(map[string]any)
	if !ok {
		return "", false
	}
	s, ok := kwargs["reasoning_effort"].(string)
	return s, ok
}

// deleteTemplateKwargReasoningEffort removes the kwarg, dropping the kwargs
// object from the parent when it becomes empty.
func deleteTemplateKwargReasoningEffort(kwargs, parent map[string]any) {
	delete(kwargs, "reasoning_effort")
	if len(kwargs) == 0 {
		delete(parent, "chat_template_kwargs")
	}
}

func deleteReasoningEffort(reasoning, parent map[string]any) bool {
	if _, ok := reasoning["effort"].(string); !ok {
		return false
	}
	delete(reasoning, "effort")
	for key := range reasoning {
		if key != "effort" {
			return true
		}
	}
	delete(parent, "reasoning")
	return true
}

// applyOpenAIReasoningEffortFallback rewrites the request params in place per
// the conclusion. Reports whether anything changed — a no-change conclusion
// keeps the original body byte-for-byte.
func applyOpenAIReasoningEffortFallback(params map[string]any, fb *reasoningEffortFallback) bool {
	if params == nil {
		return false
	}
	if fb.stripTemplateKwarg {
		kwargs, ok := params["chat_template_kwargs"].(map[string]any)
		if !ok {
			return false
		}
		effort, ok := kwargs["reasoning_effort"].(string)
		if !ok {
			return false
		}
		deleteTemplateKwargReasoningEffort(kwargs, params)
		// The qwen-chat-template dialect rides kwargs alone; keep the effort
		// selection alive on the standard OpenAI field (Ninfer-style servers
		// accept it, vLLM-style renderers ignore it).
		if _, ok := params["reasoning_effort"].(string); !ok {
			params["reasoning_effort"] = effort
		}
		return true
	}
	isNull := fb.value == ""
	changed := false
	if _, ok := params["reasoning_effort"].(string); ok {
		if isNull {
			delete(params, "reasoning_effort")
		} else {
			params["reasoning_effort"] = fb.value
		}
		changed = true
	}
	if reasoning, ok := params["reasoning"].(map[string]any); ok {
		if _, ok := reasoning["effort"].(string); ok {
			if isNull {
				changed = deleteReasoningEffort(reasoning, params) || changed
			} else {
				reasoning["effort"] = fb.value
				changed = true
			}
		}
	}
	// Keep the Qwen template kwarg twin in lockstep — a value remap or drop
	// must not leave a stale effort for kwargs-reading renderers.
	if kwargs, ok := params["chat_template_kwargs"].(map[string]any); ok {
		if _, ok := kwargs["reasoning_effort"].(string); ok {
			if isNull {
				deleteTemplateKwargReasoningEffort(kwargs, params)
			} else {
				kwargs["reasoning_effort"] = fb.value
			}
			changed = true
		}
	}
	return changed
}

// capturedHTTPError is the error-response snapshot the negotiation verdicts
// read. omp carries headers too; this mechanism never reads them.
type capturedHTTPError struct {
	status   int
	bodyText string
	bodyJson map[string]any
}

func newCapturedHTTPError(status int, body []byte) *capturedHTTPError {
	c := &capturedHTTPError{status: status, bodyText: string(body)}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err == nil {
		c.bodyJson = m
	}
	return c
}

// capturedStringField reads a string field from the structured error body,
// preferring the error object and falling back to the top level.
func capturedStringField(c *capturedHTTPError, field string) string {
	if c == nil {
		return ""
	}
	if body := c.bodyJson; body != nil {
		if errObj, ok := body["error"].(map[string]any); ok {
			if s, ok := errObj[field].(string); ok {
				return s
			}
		}
		if s, ok := body[field].(string); ok {
			return s
		}
	}
	return ""
}

// collectMessageParts joins every clue-bearing string from the error and the
// captured body with newlines, dropping blanks.
func collectMessageParts(apiErr *APIError, c *capturedHTTPError) string {
	var parts []string
	add := func(s string) {
		if strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	if apiErr != nil {
		add(apiErr.Message)
	}
	add(capturedStringField(c, "message"))
	add(capturedStringField(c, "param"))
	add(capturedStringField(c, "code"))
	add(capturedStringField(c, "type"))
	if c != nil {
		add(c.bodyText)
	}
	return strings.Join(parts, "\n")
}

// Text that identifies a 400 as being about the reasoning-effort field.
// OpenAI-compatible gateways (cliproxy, …) never name the field — they reject
// the value alone with `level "none" not supported, valid levels: low, …` — so
// the allowed-level phrasing counts as a mention too. GitHub Copilot phrases
// the same rejection with `Supported values are: …`, so value lists count too.
var reasoningEffortFieldPattern = regexp.MustCompile(`(?i)reasoning[_. ]?effort|reasoning value|(?:valid|supported|allowed) (?:levels?|values?)`)

func mentionsReasoningEffort(apiErr *APIError, c *capturedHTTPError) bool {
	return reasoningEffortFieldPattern.MatchString(capturedStringField(c, "param")) ||
		reasoningEffortFieldPattern.MatchString(capturedStringField(c, "code")) ||
		reasoningEffortFieldPattern.MatchString(capturedStringField(c, "type")) ||
		reasoningEffortFieldPattern.MatchString(collectMessageParts(apiErr, c))
}

// Which request field a rejection attributes itself to, parsed once.
const (
	effortFieldReasoningEffort = "reasoning-effort"
	effortFieldOther           = "other"
	effortFieldUnknown         = "unknown"
)

// effortRejectionSignal is the parsed attribution of a 400/422, in precedence
// order: the structured error field first, the message verdict second, bare
// vocabulary last. New server wordings extend these parsers; the decision in
// isInvalidReasoningEffortError stays fixed.
type effortRejectionSignal struct {
	// field is the authoritative attribution: explicit param, else message
	// content, else unknown.
	field string
	// namesFieldInMessage: the message names the reasoning-effort option
	// without a verdict.
	namesFieldInMessage bool
	// messageVerdict: the message carries a fielded rejection verdict (any
	// word order).
	messageVerdict bool
	// listsLevels: the message lists allowed tiers in gateway levels
	// vocabulary.
	listsLevels bool
	// rejectedMatches: the rejected effort is quoted next to a rejection
	// verdict.
	rejectedMatches bool
}

var effortFieldPattern = regexp.MustCompile(`(?i)reasoning[_. ]?effort|reasoning value`)
var allowedLevelsPattern = regexp.MustCompile(`(?i)(?:valid|supported|allowed) levels?`)

// Fielded rejection verdicts in any word order: verdict-first, field-first, or
// bare mention plus verdict.
var messageEffortVerdictPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)invalid[^\n]*(?:reasoning[_. ]?effort|reasoning value)`),
	regexp.MustCompile(`(?i)(?:reasoning[_. ]?effort|reasoning value)[^\n]*(?:invalid|unsupported|not supported|not permitted|must be|expected|unknown|unexpected|unrecognized)`),
	regexp.MustCompile(`(?i)(?:unsupported|not supported|not permitted|unknown|unexpected|unrecognized|extra)[^\n]*(?:reasoning[_. ]?effort|reasoning value)`),
}

func messageCarriesEffortVerdict(message string) bool {
	for _, p := range messageEffortVerdictPatterns {
		if p.MatchString(message) {
			return true
		}
	}
	return false
}

func parseEffortRejectionSignal(message string, c *capturedHTTPError, currentEffort string) effortRejectionSignal {
	namesFieldInMessage := effortFieldPattern.MatchString(message)
	param := capturedStringField(c, "param")
	quotedClass := "[\"'`]" + regexp.QuoteMeta(currentEffort) + "[\"'`]"
	quotedAfterVerdict := regexp.MustCompile(`(?i)(?:invalid|unsupported|not supported)[^\n]*` + quotedClass)
	quotedBeforeVerdict := regexp.MustCompile(`(?i)` + quotedClass + `[^\n]*(?:invalid|unsupported|not supported)`)
	field := effortFieldUnknown
	switch {
	case namesFieldInMessage || effortFieldPattern.MatchString(param):
		field = effortFieldReasoningEffort
	case strings.TrimSpace(param) != "":
		field = effortFieldOther
	}
	return effortRejectionSignal{
		field:               field,
		namesFieldInMessage: namesFieldInMessage,
		messageVerdict:      messageCarriesEffortVerdict(message),
		listsLevels:         allowedLevelsPattern.MatchString(message),
		rejectedMatches:     quotedAfterVerdict.MatchString(message) || quotedBeforeVerdict.MatchString(message),
	}
}

var reasoningContentPattern = regexp.MustCompile(`(?i)reasoning[_ ]content`)

func isInvalidReasoningEffortError(apiErr *APIError, c *capturedHTTPError, currentEffort string) bool {
	status := 0
	if apiErr != nil {
		status = apiErr.Status
	}
	if status == 0 && c != nil {
		status = c.status
	}
	if status != 400 && status != 422 {
		return false
	}
	if !mentionsReasoningEffort(apiErr, c) {
		return false
	}
	message := collectMessageParts(apiErr, c)
	// A GLM-style reasoning_content rejection is not about the effort field.
	if reasoningContentPattern.MatchString(message) && !reasoningEffortFieldPattern.MatchString(message) {
		return false
	}
	signal := parseEffortRejectionSignal(message, c, currentEffort)
	// Precedence is fixed: an explicit foreign field defeats the heuristic,
	// a fielded verdict always qualifies, and fieldless values-lists are
	// trusted only for the reasoning-off value (levels vocabulary is
	// gateway-effort dialect, so it stays trusted for every tier).
	if signal.field == effortFieldOther && !signal.namesFieldInMessage {
		return false
	}
	if signal.messageVerdict {
		return true
	}
	if strings.ToLower(currentEffort) != "none" && !signal.namesFieldInMessage && !signal.listsLevels {
		return false
	}
	return signal.rejectedMatches
}

var quotedReasoningValuePattern = regexp.MustCompile("(?i)[\"'`](none|minimal|low|medium|high|xhigh|max)[\"'`]")
var allowedCueSegmentPattern = regexp.MustCompile(`(?i)(?:must be|one of|allowed values?|supported values?(?: are)?|expected|(?:valid|supported|allowed) levels?(?: are)?)[^.\n]+`)
var allowedCueTestPattern = regexp.MustCompile(`(?i)must be|one of|allowed values?|supported values?|expected|(?:valid|supported|allowed) levels?`)
var bareReasoningValuePattern = regexp.MustCompile(`(?i)\b(none|minimal|low|medium|high|xhigh|max)\b`)

func parseKnownReasoningValues(text string) map[string]bool {
	values := map[string]bool{}
	for _, m := range quotedReasoningValuePattern.FindAllStringSubmatch(text, -1) {
		values[strings.ToLower(m[1])] = true
	}
	if allowed := allowedCueSegmentPattern.FindString(text); allowed != "" {
		for _, m := range bareReasoningValuePattern.FindAllStringSubmatch(allowed, -1) {
			values[strings.ToLower(m[1])] = true
		}
	}
	return values
}

// parseAllowedReasoningValues returns the allowed-tier vocabulary with the
// current tier removed. The bool is the TS undefined distinction: false only
// when there is no allowed-cue AND no collectable value — a cue with nothing
// collectable yields a non-nil empty map, which callers must treat as "defined"
// (resolveStripTemplateKwargFallback routes such messages to value remapping
// instead of stripping).
func parseAllowedReasoningValues(message, currentEffort string) (map[string]bool, bool) {
	values := parseKnownReasoningValues(message)
	hasAllowedCue := allowedCueTestPattern.MatchString(message)
	delete(values, strings.ToLower(currentEffort))
	if !hasAllowedCue && len(values) == 0 {
		return nil, false
	}
	return values, true
}

func orderedEnabledAllowedValues(allowed map[string]bool) []string {
	var out []string
	for _, v := range enabledReasoningValues {
		if allowed[v] {
			out = append(out, v)
		}
	}
	return out
}

func lowestEnabledAllowedValue(allowed map[string]bool) string {
	for _, v := range enabledReasoningValues {
		if allowed[v] {
			return v
		}
	}
	return ""
}

func nearestEnabledReasoningFallback(currentEffort string, allowed map[string]bool) string {
	current := strings.ToLower(currentEffort)
	allowedEnabled := orderedEnabledAllowedValues(allowed)
	if len(allowedEnabled) == 0 {
		return ""
	}
	if current == "minimal" && slices.Contains(allowedEnabled, "low") {
		return "low"
	}
	if current == "xhigh" && slices.Contains(allowedEnabled, "max") {
		return "max"
	}
	if current == "xhigh" && slices.Contains(allowedEnabled, "high") {
		return "high"
	}
	if current == "max" && slices.Contains(allowedEnabled, "xhigh") {
		return "xhigh"
	}
	currentRank, ok := reasoningValueRank[current]
	if !ok {
		return ""
	}
	best := ""
	bestDistance := math.MaxInt
	bestRank := math.MinInt
	for _, candidate := range allowedEnabled {
		if candidate == current {
			continue
		}
		candidateRank, ok := reasoningValueRank[candidate]
		if !ok {
			continue
		}
		distance := candidateRank - currentRank
		if distance < 0 {
			distance = -distance
		}
		if distance < bestDistance || (distance == bestDistance && candidateRank > bestRank) {
			best = candidate
			bestDistance = distance
			bestRank = candidateRank
		}
	}
	return best
}

// Text that identifies a rejection of the kwargs spelling itself: the server
// names chat_template_kwargs together with reasoning_effort (Ninfer-style
// strict kwargs whitelists: `chat_template_kwargs.reasoning_effort is not
// supported`).
var templateKwargEffortPattern = regexp.MustCompile(`(?i)chat_template_kwargs[^\n]{0,120}reasoning[_. ]?effort|reasoning[_. ]?effort[^\n]{0,120}chat_template_kwargs`)
var fieldRejectionPattern = regexp.MustCompile(`(?i)invalid|unsupported|not supported|not permitted|unknown|unexpected|unrecognized|rejected|extra input`)

func resolveStripTemplateKwargFallback(apiErr *APIError, c *capturedHTTPError, params map[string]any) bool {
	if params == nil {
		return false
	}
	effort, ok := readTemplateKwargReasoningEffort(params)
	if !ok {
		return false
	}
	status := 0
	if apiErr != nil {
		status = apiErr.Status
	}
	if status == 0 && c != nil {
		status = c.status
	}
	if status != 400 && status != 422 {
		return false
	}
	message := collectMessageParts(apiErr, c)
	if !templateKwargEffortPattern.MatchString(message) || !fieldRejectionPattern.MatchString(message) {
		return false
	}
	// A value-level rejection listing allowed levels wants the value remapped
	// (in every spelling) by the ordinary flow, not the kwarg stripped.
	if _, hasAllowed := parseAllowedReasoningValues(message, effort); hasAllowed {
		return false
	}
	return true
}

// resolveOpenAIReasoningEffortFallback decides the retry conclusion for a
// failed request: strip a rejected kwargs spelling, drop the field when no
// allowed vocabulary exists, or remap to the nearest allowed tier. nil means
// "not a negotiable rejection — surface the error".
// explicitDisable is the gbot form of omp's disableReasoning===true: the
// request carried Thinking=none (an effort preference retained while forced
// off).
func resolveOpenAIReasoningEffortFallback(apiErr *APIError, c *capturedHTTPError, params map[string]any, explicitDisable bool) *reasoningEffortFallback {
	if resolveStripTemplateKwargFallback(apiErr, c, params) {
		return &reasoningEffortFallback{stripTemplateKwarg: true}
	}
	currentEffort, ok := readOpenAIReasoningEffort(params)
	if !ok || currentEffort == "" {
		return nil
	}
	normalizedCurrent := strings.ToLower(currentEffort)
	if !knownReasoningValue[normalizedCurrent] {
		return nil
	}
	if !isInvalidReasoningEffortError(apiErr, c, currentEffort) {
		return nil
	}
	message := collectMessageParts(apiErr, c)
	allowed, hasAllowed := parseAllowedReasoningValues(message, currentEffort)
	if !hasAllowed {
		// No vocabulary to remap onto — dropping the field is the only move.
		return &reasoningEffortFallback{}
	}
	if explicitDisable {
		if normalizedCurrent != "none" && allowed["none"] {
			return &reasoningEffortFallback{value: "none"}
		}
		if fallback := lowestEnabledAllowedValue(allowed); fallback != "" && fallback != normalizedCurrent {
			return &reasoningEffortFallback{value: fallback}
		}
		return &reasoningEffortFallback{}
	}
	if normalizedCurrent == "none" {
		return &reasoningEffortFallback{}
	}
	if nearest := nearestEnabledReasoningFallback(normalizedCurrent, allowed); nearest != "" {
		return &reasoningEffortFallback{value: nearest}
	}
	return &reasoningEffortFallback{}
}

// ---------------------------------------------------------------------------
// Provider wiring (chat-completions Stream/Complete). omp keeps this glue in
// openai-completions.ts; gbot aggregates it here beside the machine it drives.
// ---------------------------------------------------------------------------

// reasoningFallbackKeyFor scopes a request to its freeze entry. The wire model
// resolves exactly like translateRequest: request model first, provider
// default second.
func (p *OpenAIProvider) reasoningFallbackKeyFor(req *Request) string {
	wireModel := req.Model
	if wireModel == "" {
		wireModel = p.model
	}
	return reasoningFallbackKey("chat-completions", p.baseURL, wireModel)
}

// applyFrozenReasoningFallback rewrites the request body with the frozen
// conclusion for this endpoint/model, if any. Frozen conclusions only
// rewrite spellings that are already present — an auto request (no effort
// field anywhere) is never injected with one.
func (p *OpenAIProvider) applyFrozenReasoningFallback(req *Request, body []byte) []byte {
	if fb := p.reasoningFallbacks.get(p.reasoningFallbackKeyFor(req)); fb != nil {
		return applyReasoningFallbackToBody(body, fb)
	}
	return body
}

// resolveReasoningFallback decides the retry conclusion for a failed chat
// request. err is whatever surfaced from the transport layer — a wrapped
// APIError unwraps, and the resolver ignores non-negotiable statuses on its
// own, so an unwrapped transport error or unrelated 400 lands on nil.
func (p *OpenAIProvider) resolveReasoningFallback(err error, captured *capturedHTTPError, body []byte, explicitDisable bool) *reasoningEffortFallback {
	if captured == nil {
		return nil
	}
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok {
		return nil
	}
	params := chatBodyParams(body)
	if params == nil {
		return nil
	}
	return resolveOpenAIReasoningEffortFallback(apiErr, captured, params, explicitDisable)
}

func (p *OpenAIProvider) rememberReasoningFallback(req *Request, fb *reasoningEffortFallback) {
	p.reasoningFallbacks.remember(p.reasoningFallbackKeyFor(req), fb)
}

// applyReasoningFallbackToBody rewrites the effort spellings in a serialized
// chat request per the conclusion. Bodies that fail to round-trip, and
// conclusions that change nothing, keep the original bytes (omp re-sends
// unmodified params as-is).
func applyReasoningFallbackToBody(body []byte, fb *reasoningEffortFallback) []byte {
	params := chatBodyParams(body)
	if params == nil {
		return body
	}
	if !applyOpenAIReasoningEffortFallback(params, fb) {
		return body
	}
	rewritten, err := json.Marshal(params)
	if err != nil {
		return body
	}
	return rewritten
}

func chatBodyParams(body []byte) map[string]any {
	var params map[string]any
	if err := json.Unmarshal(body, &params); err != nil {
		return nil
	}
	return params
}
