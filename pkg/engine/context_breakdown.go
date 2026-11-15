// Package engine: context breakdown computation for /context command.
//
// Translates Claude Code's analyzeContext.ts into Go. Uses proportional
// scaling: estimates each component with a heuristic, then scales the
// estimates so they sum to the exact API-reported total (ContextTokens).
//
// Source: claude-code-source-code/src/utils/analyzeContext.ts
package engine

import (
	"encoding/json"
	"maps"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/memory/long"
	"github.com/liuy/gbot/pkg/tool"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/types"
)

// -----------------------------------------------------------------------
// Data structures
// -----------------------------------------------------------------------

// ContextBreakdown is the full snapshot of context usage.
// Source: analyzeContext.ts:131-214 — ContextData
type ContextBreakdown struct {
	Model         string
	ContextWindow int
	TotalTokens   int
	Percentage    float64

	Categories    []ContextCategory
	GridRows      [][]GridSquare
	IsAutoCompact bool

	MCPToolsLoaded       []MCPToolDetail
	MCPToolsDeferred     []MCPToolDetail
	DeferredBuiltinTools []SystemToolDetail
	SystemTools          []SystemToolDetail
	MemoryFiles          []MemoryFileDetail
	Agents               []AgentDetail
	Skills               []SkillDetail
	MessageBreakdown     *MessageBreakdown
}

// ContextCategory is one slice of the context pie.
// Source: analyzeContext.ts:83-92
type ContextCategory struct {
	Name       string
	Tokens     int
	Percentage float64
	Color      string // lipgloss color name
	IsFree     bool
	IsReserved bool

	// ID is the stable semantic identifier the web UI translates client-side;
	// Name stays English for the TUI and as the wui fallback.
	ID string
}

// GridSquare is one cell in the visualization grid.
// Source: analyzeContext.ts:120-128
type GridSquare struct {
	Color          string
	IsFilled       bool
	CategoryName   string
	Tokens         int
	Percentage     float64
	SquareFullness float64 // 0-1
}

// MCPToolDetail describes an MCP tool entry.
// Source: analyzeContext.ts:102-106
type MCPToolDetail struct {
	Name       string
	ServerName string
	Tokens     int
	IsLoaded   bool
}

// SystemToolDetail describes a built-in tool's contribution.
// Source: analyzeContext.ts:107-110
type SystemToolDetail struct {
	Name   string
	Tokens int
}

// MemoryFileDetail describes one loaded memory file.
// Source: analyzeContext.ts:97-100
type MemoryFileDetail struct {
	Path   string
	Tokens int
}

// AgentDetail describes an agent definition.
// Source: analyzeContext.ts:117-120
type AgentDetail struct {
	AgentType string
	Source    string
	Tokens    int
}

// SkillDetail describes one skill frontmatter.
// Source: analyzeContext.ts:122-126
type SkillDetail struct {
	Name   string
	Source string
	Tokens int
}

// MessageBreakdown categorizes token usage across message types.
// Source: analyzeContext.ts:189-198
type MessageBreakdown struct {
	ToolCallTokens      int
	ToolResultTokens    int
	AttachmentTokens    int
	AssistantTextTokens int
	UserTextTokens      int
	ToolCallsByType     []ToolCallByType
	AttachmentsByType   []AttachmentByType
}

// ToolCallByType aggregates one tool's call + result tokens.
// Source: analyzeContext.ts:195
type ToolCallByType struct {
	Name         string
	CallTokens   int
	ResultTokens int
}

// AttachmentByType aggregates one attachment type's tokens.
// Source: analyzeContext.ts:196
type AttachmentByType struct {
	Name   string
	Tokens int
}

// CategoryColorPalette — TUI-friendly color names for categories.
const (
	ColorSystemPrompt = "12"  // blue
	ColorMemoryFiles  = "220" // yellow
	ColorSystemTools  = "45"  // green-cyan
	ColorMCPLoaded    = "46"  // green
	ColorMCPDeferred  = "22"  // dark green
	ColorMessages     = "255" // white
	ColorFree         = "240" // dim
	ColorReserved     = "160" // red
)

// GridWidth is always 20 columns.
const GridWidth = 20

// GridHeight is always 5 rows.
const GridHeight = 5

// TotalSquares is GridWidth × GridHeight = 100.
func TotalSquares(contextWindow int) int {
	return GridWidth * GridHeight
}

// Grid symbols — block characters for intuitive fullness representation.
const (
	SymFilledFull = "█" // U+2588 full block (>=70% full)
	SymFilledPart = "▓" // U+2593 dark shade (<70% full)
	SymFreeSpace  = "░" // U+2591 light shade (free)
	SymReserved   = "▒" // U+2592 medium shade (autocompact buffer)
)

// ContextBreakdown computes a full context breakdown for /context command.
func (e *Engine) ContextBreakdown() *ContextBreakdown {
	// Snapshot engine state under a single RLock.
	e.mu.RLock()
	totalExact := e.ContextTokens
	contextWindow := e.autoCompactConfig.ContextWindow
	maxTokens := e.maxTokens
	messages := slicesCloneMessages(e.messages)
	toolsSnapshot := toolsClone(e.tools)
	toolSearchSnap := e.toolSearch
	// Read the field directly: this function already holds e.mu.RLock and
	// Go's RWMutex forbids recursive RLock (a queued writer between the two
	// acquisitions would deadlock).
	workingDir := e.workingDir
	memoryDir := e.memoryDir
	skillListing := e.skillListing
	agentDefs := agentDefsClone(e.agentDefs)
	systemPromptRaw := e.systemPrompt
	mcpReg := e.mcpRegistry
	model := e.model
	modalities := e.inputModalities
	e.mu.RUnlock()

	breakdown := &ContextBreakdown{
		Model:         model,
		ContextWindow: contextWindow,
		TotalTokens:   totalExact,
	}

	if totalExact == 0 {
		// No API response yet — caller should show a "send a message first" hint.
		return breakdown
	}

	// Reserved (autocompact buffer) is part of free space — it is the
	// portion of unused tokens set aside for autocompact. Cap at free so
	// it never exceeds what's actually available.
	freeTokens := contextWindow - totalExact
	reserved := min(computeReservedTokens(contextWindow, maxTokens), max(freeTokens, 0))

	// Compute expensive data once, shared by estimates and details. The
	// typed-memory section is extracted from the raw prompt itself — the
	// bytes Build actually wrote — so the strip and the MemoryFiles bucket
	// are consistent by construction even after mid-session MEMORY.md edits.
	// Recomputing from disk drifts the moment the file changes: the fresh
	// text no longer matches the prompt, the strip below turns into a no-op,
	// and the memory bytes land in both "System prompt" and "Memory files".
	memPrompt := extractMemorySection(systemPromptRaw)
	if memPrompt == "" {
		// No typed-memory segment in the raw prompt — a stale prompt built
		// before memory was enabled, or a hand-set system prompt. Fall back
		// to the disk recompute, which returns "" when memory is disabled
		// (matching Build's own gate).
		memPrompt = context.FormatMemoryPrompt(workingDir, memoryDir)
	}
	sections := e.estimateSystemPromptSections(systemPromptRaw, model, modalities, memPrompt)
	// The system prompt injects the typed-memory prompt (builder.go), not
	// every memory file's contents — estimating the full file list here
	// overstated this bucket ~4x and squeezed the message bucket. No legacy
	// fallback is needed: BuildMemoryPrompt returns "" exactly when
	// IsAutoMemoryEnabled is false, and LoadMemoryFiles returns nil under
	// that same gate, so the legacy path could only ever produce nil.
	var memFiles []context.MemoryFile
	if memPrompt != "" {
		memDir := memoryDir
		if memDir == "" {
			memDir = long.GetMemoryPath(workingDir)
		}
		memFiles = []context.MemoryFile{{
			Path:    filepath.Join(memDir, long.EntrypointName),
			Content: memPrompt,
		}}
	}

	estimates := e.estimateComponents(
		sections, memFiles, toolsSnapshot, toolSearchSnap,
		messages, mcpReg, reserved,
	)

	// Scale categories to sum to the full TotalTokens (API-reported actual
	// usage). Reserved (Autocompact buffer) is part of free space, not
	// used tokens, so it must not be subtracted from the scaling target.
	scaled := scaleProportionally(estimates, totalExact)

	detailSlices := e.buildDetails(
		memFiles, toolsSnapshot, toolSearchSnap,
		skillListing, agentDefs, messages, mcpReg,
	)

	if freeTokens < 0 {
		freeTokens = 0
	}

	// Assemble categories in the canonical order matching the TS source.
	// Free space is the remaining unused tokens after subtracting the
	// autocompact buffer (reserved is part of free, shown separately).
	breakdown.Categories = buildCategories(scaled, freeTokens-reserved, reserved, contextWindow)
	breakdown.GridRows = BuildGrid(breakdown.Categories, contextWindow)
	breakdown.MCPToolsLoaded = detailSlices.mcpLoaded
	breakdown.MCPToolsDeferred = detailSlices.mcpDeferred
	breakdown.DeferredBuiltinTools = detailSlices.deferredBuiltin
	breakdown.SystemTools = detailSlices.systemTools
	breakdown.MemoryFiles = detailSlices.memoryFiles
	breakdown.Agents = detailSlices.agents
	breakdown.Skills = detailSlices.skills
	breakdown.MessageBreakdown = detailSlices.messageBreakdown
	breakdown.IsAutoCompact = contextWindow > 0
	breakdown.Percentage = min(safePct(totalExact, contextWindow), 100.0)

	return breakdown
}

// -----------------------------------------------------------------------
// Per-component estimates
// -----------------------------------------------------------------------

// sysPromptSections holds the pre-computed system prompt base token estimate.
// The typed-memory section carries its own "Memory files" category; platform
// info and the skill listing are part of the base ("System prompt").
type sysPromptSections struct {
	base int
}

type componentEstimates struct {
	SystemPromptBase int
	MemoryFiles      int
	SystemTools      int
	MCPLoaded        int
	Messages         int
}

func (e *Engine) estimateComponents(
	sections sysPromptSections,
	memFiles []context.MemoryFile,
	toolsSnapshot map[string]tool.Tool,
	toolSearchSnap *toolSearchState,
	messages []types.Message,
	mcpReg *mcp.Registry,
	reserved int,
) componentEstimates {
	est := componentEstimates{
		SystemPromptBase: sections.base,
	}

	for _, f := range memFiles {
		est.MemoryFiles += e.EstimateTokensCalibrated(f.Content)
	}

	// 3. System tools (built-in, non-MCP).
	est.SystemTools = e.estimateSystemTools(toolsSnapshot, toolSearchSnap, mcpReg)

	// 4. MCP loaded tools.
	est.MCPLoaded = e.estimateMCPLoadedTools(mcpReg)

	// 5. Messages (breakdown + total).
	total := 0
	for i := range messages {
		if messages[i].Role != types.RoleUser && messages[i].Role != types.RoleAssistant {
			continue
		}
		// Attachments: count prompt + meta info as attachment tokens.
		if messages[i].Attachment != nil {
			attTokens := e.EstimateTokensCalibrated(messages[i].Attachment.Prompt)
			total += attTokens
		}
		for _, block := range messages[i].Content {
			t := e.tokenCountForBlock(block)
			total += t
		}
	}
	// Pad by 4/3 to match EstimateMessagesTokens.
	est.Messages = int(math.Ceil(float64(total) * 4.0 / 3.0))

	return est
}

// estimateSystemPromptSections returns the base system prompt token estimate.
// Source: analyzeContext.ts:289-298 — countSystemPromptSections
func (e *Engine) estimateSystemPromptSections(
	systemPromptRaw string,
	model string,
	modalities []string,
	memPrompt string,
) sysPromptSections {
	if systemPromptRaw == "" {
		return sysPromptSections{}
	}

	// The typed-memory section carries its own category, so the base estimate
	// strips its exact Build()-written spelling out of the raw prompt first —
	// estimating the full raw text counts the same bytes twice (once as
	// "System prompt", once as "Memory files"). The remainder keeps the
	// template + SOUL + platform info + skills + runtime additions, which is
	// what the merged "System prompt" category bills.
	stripped := systemPromptRaw
	if memPrompt != "" {
		stripped = strings.Replace(stripped, "\n\n"+memPrompt, "", 1)
	}

	// The request path (engine.go callLLM) hot-expands {{SYSTEM}}/{{SOUL}}/
	// {{MODEL}} before the prompt reaches the API; estimating the stub form
	// leaves those thousands of tokens invisible to every category, which
	// scaleProportionally then smears into the other buckets.
	systemText, _ := context.LoadSystemFile()
	if systemText == "" {
		systemText = context.DefaultBasePrompt()
	}
	stripped = strings.Replace(stripped, "{{SYSTEM}}", systemText, 1)

	soulText := ""
	if soul, _ := context.LoadSoulFile(); soul != "" {
		soulText = "\n\n---\n\nEmbody the persona and tone defined below. Follow its guidance unless higher-priority instructions override it.\n\n" + soul
	}
	stripped = strings.Replace(stripped, "{{SOUL}}", soulText, 1)
	stripped = strings.Replace(stripped, "{{MODEL}}", model, 1)
	if len(modalities) == 0 {
		modalities = []string{"text"}
	}
	stripped = strings.Replace(stripped, "{{MODALITIES}}", strings.Join(modalities, "+"), 1)

	return sysPromptSections{base: e.EstimateTokensCalibrated(stripped)}
}

// estimateSystemTools sums the request entries (name + prompt + input schema
// JSON) of enabled built-in tools, excluding deferred-undiscovered tools and
// MCP tools. The Agent tool is a built-in like any other: estimateSingleTool
// bills its full request form, whose Prompt() half carries the per-agent
// lines.
func (e *Engine) estimateSystemTools(
	toolsSnapshot map[string]tool.Tool,
	toolSearchSnap *toolSearchState,
	mcpReg *mcp.Registry,
) int {
	total := 0
	for name, t := range toolsSnapshot {
		if !t.IsEnabled() {
			continue
		}
		if isMCPToolName(name) {
			continue
		}
		if tool.IsDeferred(t) && toolSearchSnap != nil && !toolSearchSnap.IsDiscovered(name) {
			continue
		}
		total += e.estimateSingleTool(t)
	}
	return total
}

// mcpToolLLMLoaded reports whether a discovered MCP tool enters the LLM
// request: direct-mode servers always load; code-mode never; default keeps
// the historical _meta alwaysLoad signal.
func mcpToolLLMLoaded(t mcp.DiscoveredTool) bool {
	switch t.Mode {
	case mcp.ModeDirect:
		return true
	case mcp.ModeCode:
		return false
	default:
		return t.AlwaysLoad
	}
}

func (e *Engine) estimateMCPLoadedTools(mcpReg *mcp.Registry) int {
	if mcpReg == nil {
		return 0
	}
	tools := mcpReg.GetTools()
	total := 0
	for _, t := range tools {
		if mcpToolLLMLoaded(t) {
			total += e.estimateMCPSchema(t.Description, t.InputSchema)
		}
	}
	return total
}

// name + prompt + input schema JSON — the full request form buildToolDefs
// ships per tool. Description() is UI-only and never enters the request.
func (e *Engine) estimateSingleTool(t tool.Tool) int {
	return e.EstimateTokensCalibrated(t.Name()+t.Prompt()) + e.EstimateTokensCalibrated(string(t.InputSchema()))
}

func (e *Engine) estimateMCPSchema(desc string, schema json.RawMessage) int {
	return e.EstimateTokensCalibrated(desc) + e.EstimateTokensCalibrated(string(schema))
}

func (e *Engine) tokenCountForBlock(block types.ContentBlock) int {
	switch block.Type {
	case types.ContentTypeText:
		return e.EstimateTokensCalibrated(block.Text)
	case types.ContentTypeToolResult:
		return e.EstimateTokensCalibrated(string(block.Content))
	case types.ContentTypeThinking:
		return e.EstimateTokensCalibrated(block.Thinking)
	case types.ContentTypeToolUse:
		return e.EstimateTokensCalibrated(block.Name + string(block.Input))
	case types.ContentTypeDocument:
		// Mirrors the estimator's document case; the default JSON branch
		// would show a 50k-token document as ~20.
		if block.EstTokens > 0 {
			return block.EstTokens
		}
		return int(block.Size) / 4
	default:
		raw, _ := json.Marshal(block)
		return e.EstimateTokensCalibrated(string(raw))
	}
}

// toolAttribution resolves each tool_result block to the tool that produced it
// and accumulates those tokens per tool name. TS keeps the two halves as
// separate Maps — a tool_use_id→name pre-scan (:871-886) and toolResultsByType
// (:777-778) — and merges the second into the first's buckets once the scan
// ends (:1299-1314). Go maps iterate in random order, so the arrival order of
// result buckets is tracked explicitly to reproduce a Map's insertion order in
// the merged slice.
type toolAttribution struct {
	useNames map[string]string
	results  map[string]int
	order    []string
}

// newToolAttribution pre-scans the whole history for tool_use ids, so a result
// resolves a call that sits later in the transcript (analyzeContext.ts:871-886).
func newToolAttribution(messages []types.Message) *toolAttribution {
	ta := &toolAttribution{useNames: map[string]string{}, results: map[string]int{}}
	for _, m := range messages {
		if m.Role != types.RoleAssistant {
			continue
		}
		for _, b := range m.Content {
			// Only tool_use blocks seed the map, and an empty id is skipped
			// (analyzeContext.ts:876-882) — that is what keeps a tool_result
			// without a tool_use_id from resolving to some other block.
			if b.Type == types.ContentTypeToolUse && b.ID != "" {
				ta.useNames[b.ID] = blockToolName(b)
			}
		}
	}
	return ta
}

// blockToolName — analyzeContext.ts:793: block.name || 'unknown'. It feeds both
// the call bucket and the id→name map, so an unnamed tool's call and result
// merge into one bucket instead of splitting between "" and "unknown".
func blockToolName(b types.ContentBlock) string {
	if b.Name == "" {
		return "unknown"
	}
	return b.Name
}

// resultToolName — analyzeContext.ts:825-827. A tool_result whose tool_use_id
// matches nothing (missing field, or a call trimmed from the transcript) stays
// in the "unknown" bucket instead of being dropped. TS guards the lookup twice
// — `if (toolUseId)` on the way in (:879) and `toolUseId ? … : undefined` on
// the way out (:826) — one guard is enough here because the prescan never keys
// an empty id and a stored name is never empty, so a miss (including an empty
// id) always reads as absent.
func (ta *toolAttribution) resultToolName(b types.ContentBlock) string {
	if name, ok := ta.useNames[b.ToolUseID]; ok {
		return name
	}
	return "unknown"
}

func (ta *toolAttribution) addResult(name string, tokens int) {
	if _, ok := ta.results[name]; !ok {
		ta.order = append(ta.order, name)
	}
	ta.results[name] += tokens
}

// merge folds the accumulated result tokens into their call buckets
// (analyzeContext.ts:1310-1314). Result-only buckets — an orphan, or a tool
// whose call was trimmed from the transcript — are appended after every bucket
// that has a call, which is where TS's two-Map merge puts them.
func (ta *toolAttribution) merge(mb *MessageBreakdown) {
	for _, name := range ta.order {
		mb.upsertToolCall(name, 0, ta.results[name])
	}
}

func classifyMessageBlock(
	msg types.Message,
	block types.ContentBlock,
	tokens int,
	mb *MessageBreakdown,
	ta *toolAttribution,
) {
	switch block.Type {
	case types.ContentTypeToolUse:
		mb.ToolCallTokens += tokens
		mb.upsertToolCall(blockToolName(block), tokens, 0)
	case types.ContentTypeToolResult:
		mb.ToolResultTokens += tokens
		ta.addResult(ta.resultToolName(block), tokens)
	default:
		// Compact summary is synthetic, not real user input.
		if msg.HasFlag(types.FlagCompactSummary) {
			return
		}
		// FlagMeta messages (tool screenshots, etc.) are tool products
		// placed in user messages due to API constraints. Count them as
		// tool result tokens, not user text.
		if msg.HasFlag(types.FlagMeta) {
			mb.ToolResultTokens += tokens
			return
		}
		switch msg.Role {
		case types.RoleAssistant:
			mb.AssistantTextTokens += tokens
		case types.RoleUser:
			switch block.Type {
			case types.ContentTypeImage, types.ContentTypeVideo:
				mb.AttachmentTokens += tokens
			default:
				mb.UserTextTokens += tokens
			}
		}
	}
}

func (mb *MessageBreakdown) upsertToolCall(name string, call, result int) {
	for i := range mb.ToolCallsByType {
		if mb.ToolCallsByType[i].Name == name {
			mb.ToolCallsByType[i].CallTokens += call
			mb.ToolCallsByType[i].ResultTokens += result
			return
		}
	}
	mb.ToolCallsByType = append(mb.ToolCallsByType, ToolCallByType{
		Name: name, CallTokens: call, ResultTokens: result,
	})
}

func (mb *MessageBreakdown) upsertAttachment(name string, tokens int) {
	for i := range mb.AttachmentsByType {
		if mb.AttachmentsByType[i].Name == name {
			mb.AttachmentsByType[i].Tokens += tokens
			return
		}
	}
	mb.AttachmentsByType = append(mb.AttachmentsByType, AttachmentByType{
		Name: name, Tokens: tokens,
	})
}

// Bucket key — source: analyzeContext.ts:846 — msg.attachment.type || 'unknown'.
// Empty type falls back so untyped attachments still get attributed.
func attachmentBucket(att *types.Attachment) string {
	if att.Type == "" {
		return "unknown"
	}
	return string(att.Type)
}

// -----------------------------------------------------------------------
// Proportional scaling
// -----------------------------------------------------------------------

type scaledComponents map[string]int

// scaleProportionally scales each estimate so the sum equals target.
// Formula: scaled[i] = target × (estimate[i] / sumEstimate)
// Returns nil if sumEstimate == 0 (caller should fall back to raw estimates).
func scaleProportionally(est componentEstimates, target int) scaledComponents {
	estimates := []struct {
		name  string
		value int
	}{
		{"System prompt", est.SystemPromptBase},
		{"Memory files", est.MemoryFiles},
		{"System tools", est.SystemTools},
		{"MCP tools", est.MCPLoaded},
		{"Messages", est.Messages},
	}

	sumEstimate := 0
	for _, e := range estimates {
		sumEstimate += e.value
	}

	if sumEstimate == 0 || target <= 0 {
		// Cannot scale — return raw estimates as-is.
		out := make(scaledComponents, len(estimates))
		for _, e := range estimates {
			out[e.name] = e.value
		}
		return out
	}

	out := make(scaledComponents, len(estimates))
	allocated := 0
	for _, e := range estimates {
		scaled := int(math.Round(float64(target) * float64(e.value) / float64(sumEstimate)))
		if e.value > 0 && scaled == 0 {
			scaled = 1 // never drop a non-zero category entirely
		}
		out[e.name] = scaled
		allocated += scaled
	}
	// Reconcile rounding: adjust the largest category by (target - allocated).
	diff := target - allocated
	if diff != 0 {
		largest := estimates[0].name
		largestVal := -1
		for _, e := range estimates {
			if e.value > largestVal {
				largest = e.name
				largestVal = e.value
			}
		}
		out[largest] += diff
	}
	return out
}

// -----------------------------------------------------------------------
// Categories + grid
// -----------------------------------------------------------------------

var categoryOrder = []string{
	"System prompt",
	"Memory files",
	"System tools",
	"MCP tools",
	"Deferred tools",
	"Messages",
	"Free space",
	"Autocompact buffer",
}

var categoryColors = map[string]string{
	"System prompt":      ColorSystemPrompt,
	"Memory files":       ColorMemoryFiles,
	"System tools":       ColorSystemTools,
	"MCP tools":          ColorMCPLoaded,
	"Deferred tools":     ColorMCPDeferred,
	"Messages":           ColorMessages,
	"Free space":         ColorFree,
	"Autocompact buffer": ColorReserved,
}

// categoryIDs maps every emittable category name to its wire-level semantic
// id. "Deferred tools" is absent because buildCategories only emits names
// present in the scaled map, which never contains it.
var categoryIDs = map[string]string{
	"System prompt":      "system_prompt",
	"Memory files":       "memory_files",
	"System tools":       "system_tools",
	"MCP tools":          "mcp_tools",
	"Messages":           "messages",
	"Free space":         "free_space",
	"Autocompact buffer": "autocompact_buffer",
}

func buildCategories(scaled scaledComponents, free, reserved, total int) []ContextCategory {
	out := make([]ContextCategory, 0, len(categoryOrder))
	for _, name := range categoryOrder {
		tokens := 0
		isFree := false
		isReserved := false
		switch name {
		case "Free space":
			tokens = free
			isFree = true
		case "Autocompact buffer":
			tokens = reserved
			isReserved = true
		default:
			tokens = scaled[name]
		}
		if tokens == 0 && !isFree && !isReserved {
			continue
		}
		out = append(out, ContextCategory{
			Name:       name,
			Tokens:     tokens,
			Percentage: safePct(tokens, total),
			Color:      categoryColors[name],
			IsFree:     isFree,
			IsReserved: isReserved,
			ID:         categoryIDs[name],
		})
	}
	return out
}

// absorbing the remainder so the grid always totals TotalSquares.
// Exported for use by TUI render tests.
// Source: analyzeContext.ts:1176-1300 — buildGrid
func BuildGrid(cats []ContextCategory, contextWindow int) [][]GridSquare {
	totalSquares := TotalSquares(contextWindow)
	width := GridWidth

	// Compute reserved squares first so content categories don't over-allocate.
	reservedSquares := 0
	// perSquare: tokens per grid square based on the total context window.
	// All tokens (content + reserved + free) share totalSquares proportionally.
	allTokens := 0
	for _, c := range cats {
		allTokens += c.Tokens
	}
	perSquare := float64(allTokens) / float64(totalSquares)

	// Phase 1: assign squares for reserved categories.
	assignments := make([]int, len(cats))
	if perSquare > 0 {
		for i, c := range cats {
			if c.IsReserved {
				sq := int(math.Round(float64(c.Tokens) / perSquare))
				if sq == 0 && c.Tokens > 0 {
					sq = 1
				}
				assignments[i] = sq
				reservedSquares += sq
			}
		}
	}

	// Phase 2: assign squares for content categories.
	// Cap content so it leaves room for free space squares.
	contentCap := totalSquares - reservedSquares
	totalAssigned := 0
	for i, c := range cats {
		if c.IsFree || c.IsReserved {
			continue
		}
		if perSquare == 0 {
			assignments[i] = 0
			continue
		}
		assignments[i] = int(math.Round(float64(c.Tokens) / perSquare))
		if c.Tokens > 0 && assignments[i] == 0 {
			assignments[i] = 1
		}
		totalAssigned += assignments[i]
	}
	// If content overflows the cap, scale down proportionally.
	if totalAssigned > contentCap {
		scale := float64(contentCap) / float64(totalAssigned)
		totalAssigned = 0
		for i, c := range cats {
			if c.IsFree || c.IsReserved {
				continue
			}
			assignments[i] = max(int(math.Round(float64(assignments[i])*scale)), 0)
			if c.Tokens > 0 && assignments[i] == 0 {
				assignments[i] = 1
			}
			totalAssigned += assignments[i]
		}
	}

	// Phase 3: free space absorbs the remainder.
	freeIdx := -1
	for i, c := range cats {
		if c.IsFree {
			freeIdx = i
			break
		}
	}
	if freeIdx >= 0 {
		assignments[freeIdx] = max(totalSquares-totalAssigned-reservedSquares, 0)
	}

	// Build the square list.
	type sq struct {
		color    string
		name     string
		fullness float64
	}
	var squares []sq
	for i, c := range cats {
		for k := 0; k < assignments[i]; k++ {
			fullness := 0.0
			// Source: analyzeContext.ts:1208-1211 — squareFullness.
			if !c.IsFree && !c.IsReserved && perSquare > 0 {
				idealSquares := float64(c.Tokens) / perSquare
				if idealSquares > 0 {
					fullness = 1.0 / idealSquares
					if fullness > 1.0 {
						fullness = 1.0
					}
				}
			} else if c.IsFree {
				fullness = 0.0
			} else if c.IsReserved {
				fullness = 1.0
			}
			squares = append(squares, sq{
				color:    c.Color,
				name:     c.Name,
				fullness: fullness,
			})
		}
	}

	// Reshape into rows.
	rows := make([][]GridSquare, 0, GridHeight)
	for r := range GridHeight {
		start := r * width
		end := start + width
		if start >= len(squares) {
			break
		}
		if end > len(squares) {
			end = len(squares)
		}
		row := make([]GridSquare, 0, width)
		for _, s := range squares[start:end] {
			row = append(row, GridSquare{
				Color:          s.color,
				IsFilled:       !strings.Contains(s.name, "Free") && s.fullness >= 0.7,
				CategoryName:   s.name,
				SquareFullness: s.fullness,
			})
		}
		// Pad row with free-space squares if needed.
		for len(row) < width {
			row = append(row, GridSquare{
				Color:          ColorFree,
				IsFilled:       false,
				CategoryName:   "Free space",
				SquareFullness: 0,
			})
		}
		rows = append(rows, row)
	}
	return rows
}

// -----------------------------------------------------------------------
// Per-item details
// -----------------------------------------------------------------------

type detailSlices struct {
	mcpLoaded        []MCPToolDetail
	mcpDeferred      []MCPToolDetail
	deferredBuiltin  []SystemToolDetail
	systemTools      []SystemToolDetail
	memoryFiles      []MemoryFileDetail
	agents           []AgentDetail
	skills           []SkillDetail
	messageBreakdown *MessageBreakdown
}

func (e *Engine) buildDetails(
	memFiles []context.MemoryFile,
	toolsSnapshot map[string]tool.Tool,
	toolSearchSnap *toolSearchState,
	skillListing string,
	agentDefs []*types.AgentDefinition,
	messages []types.Message,
	mcpReg *mcp.Registry,
) detailSlices {
	ds := detailSlices{
		messageBreakdown: &MessageBreakdown{},
	}

	for _, f := range memFiles {
		ds.memoryFiles = append(ds.memoryFiles, MemoryFileDetail{
			Path:   f.Path,
			Tokens: e.EstimateTokensCalibrated(f.Content),
		})
	}

	// System tools (per-tool) and deferred built-in tools. The Agent tool is
	// billed like every other built-in — its full request entry, whose
	// Prompt() half carries the per-agent lines.
	for name, t := range toolsSnapshot {
		if !t.IsEnabled() {
			continue
		}
		if isMCPToolName(name) {
			continue
		}
		if tool.IsDeferred(t) {
			if toolSearchSnap != nil && !toolSearchSnap.IsDiscovered(name) {
				ds.deferredBuiltin = append(ds.deferredBuiltin, SystemToolDetail{
					Name:   name,
					Tokens: e.estimateSingleTool(t),
				})
			}
			continue
		}
		ds.systemTools = append(ds.systemTools, SystemToolDetail{
			Name:   name,
			Tokens: e.estimateSingleTool(t),
		})
	}

	// MCP tools (loaded vs deferred).
	if mcpReg != nil {
		for _, t := range mcpReg.GetTools() {
			// Code-mode tools are neither loaded nor deferred in the breakdown —
			// they never enter the LLM request, and the dump mirrors the request.
			if t.Mode == mcp.ModeCode {
				continue
			}
			detail := MCPToolDetail{
				Name:       t.OriginalName,
				ServerName: t.ServerName,
				Tokens:     e.estimateMCPSchema(t.Description, t.InputSchema),
			}
			// An MCP tool is "loaded" iff it's in the engine's tools map.
			if _, ok := toolsSnapshot[BuildMcpName(t.ServerName, t.OriginalName)]; ok {
				detail.IsLoaded = true
				ds.mcpLoaded = append(ds.mcpLoaded, detail)
			} else {
				ds.mcpDeferred = append(ds.mcpDeferred, detail)
			}
		}
	}

	// Agents. The rows are informational detail: each row estimates the
	// definition's FormatAgentLine text, the same lines the Agent tool's
	// Prompt() embeds — that cost sits inside the "System tools" category
	// via the Agent tool's request entry, not in a category of its own.
	for _, d := range agentDefs {
		if d == nil {
			continue
		}
		ds.agents = append(ds.agents, AgentDetail{
			AgentType: d.AgentType,
			Source:    string(d.Source),
			Tokens:    e.EstimateTokensCalibrated(agenttool.FormatAgentLine(d)),
		})
	}

	// Skills (one entry per line in the skill listing).
	if skillListing != "" {
		for line := range strings.SplitSeq(skillListing, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			ds.skills = append(ds.skills, SkillDetail{
				Name:   line,
				Source: "plugin",
				Tokens: e.EstimateTokensCalibrated(line),
			})
		}
	}

	// Message breakdown.
	attribution := newToolAttribution(messages)
	for i := range messages {
		if messages[i].Role != types.RoleUser && messages[i].Role != types.RoleAssistant {
			continue
		}
		if messages[i].Attachment != nil {
			attTokens := e.EstimateTokensCalibrated(messages[i].Attachment.Prompt)
			ds.messageBreakdown.AttachmentTokens += attTokens
			ds.messageBreakdown.upsertAttachment(
				attachmentBucket(messages[i].Attachment), attTokens)
		}
		for _, block := range messages[i].Content {
			t := e.tokenCountForBlock(block)
			classifyMessageBlock(messages[i], block, t, ds.messageBreakdown, attribution)
		}
	}
	attribution.merge(ds.messageBreakdown)
	// Sort by call + result tokens desc; TS (:1316-1325) sorts without a top-N
	// slice, and Array.prototype.sort is stable — ties keep insertion order.
	sort.SliceStable(ds.messageBreakdown.ToolCallsByType, func(i, j int) bool {
		a := ds.messageBreakdown.ToolCallsByType[i]
		b := ds.messageBreakdown.ToolCallsByType[j]
		return a.CallTokens+a.ResultTokens > b.CallTokens+b.ResultTokens
	})
	sort.SliceStable(ds.messageBreakdown.AttachmentsByType, func(i, j int) bool {
		return ds.messageBreakdown.AttachmentsByType[i].Tokens >
			ds.messageBreakdown.AttachmentsByType[j].Tokens
	})

	return ds
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// extractMemorySection pulls the typed-memory segment back out of the raw
// system prompt. Build writes it as "\n\n" + FormatMemoryPrompt(...) and the
// segment starts with the "# auto memory" heading, so it runs from that
// heading to the next section boundary or the end of the prompt. The
// boundaries are the exact spellings the production assembly writes after the
// memory section — Build's skills header and AppendVoiceBlock's tail — not a
// generic "\n\n# " heading match: MEMORY.md content routinely carries level-1
// headings of its own ("# Memory"), which a heading-level boundary would cut
// the segment at. The returned text is the segment without the leading
// "\n\n", i.e. exactly the string Build passed to WriteString, which is why
// the strip and the MemoryFiles estimate can share it byte for byte. Returns
// "" when the prompt carries no typed-memory segment.
func extractMemorySection(raw string) string {
	const marker = "\n\n# auto memory"
	start := strings.Index(raw, marker)
	if start < 0 {
		return ""
	}
	rest := raw[start+len("\n\n"):]
	end := len(rest)
	for _, boundary := range []string{
		"\n\n## Available Skills",
		"\n\n" + context.VoicePromptBlock,
	} {
		// Cut against the not-yet-clamped window: an absent boundary
		// returns the window itself, so end stays put with no found-flag
		// to get wrong.
		before, _, _ := strings.Cut(rest[:end], boundary)
		end = len(before)
	}
	return rest[:end]
}

func computeReservedTokens(contextWindow, maxTokens int) int {
	if contextWindow <= 0 {
		return 0
	}
	// 7% of effective window, min 3K. Source: autoCompactBuffer.
	buf := max(contextWindow*7/100, 3000)
	return buf
}

func safePct(part, whole int) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) * 100.0 / float64(whole)
}

// MCP tools are stored in e.tools with names like "mcp__server__tool".
func isMCPToolName(name string) bool {
	return strings.HasPrefix(name, "mcp__")
}

// Mirrors mcp.BuildMcpToolName. We can't import mcp here for the build
// (circular), so this is a small string concat. The mcp package exposes
// the canonical builder; tests can compare against it.
func BuildMcpName(server, toolName string) string {
	// Simplified form used in engine.tools keys.
	return "mcp__" + server + "__" + toolName
}

func slicesCloneMessages(m []types.Message) []types.Message {
	if m == nil {
		return nil
	}
	out := make([]types.Message, len(m))
	copy(out, m)
	return out
}

func toolsClone(m map[string]tool.Tool) map[string]tool.Tool {
	if m == nil {
		return nil
	}
	out := make(map[string]tool.Tool, len(m))
	maps.Copy(out, m)
	return out
}

func agentDefsClone(defs []*types.AgentDefinition) []*types.AgentDefinition {
	if defs == nil {
		return nil
	}
	out := make([]*types.AgentDefinition, len(defs))
	copy(out, defs)
	return out
}
