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
	"sort"
	"strings"

	"github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/tool"
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
	SystemPromptSections []SystemPromptSectionDetail
	MemoryFiles          []MemoryFileDetail
	Agents               []AgentDetail
	Skills               []SkillDetail
	MessageBreakdown     *MessageBreakdown
	APIUsage             *APIUsageSnapshot
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

// SystemPromptSectionDetail describes one section of the system prompt.
// Source: analyzeContext.ts:111-115
type SystemPromptSectionDetail struct {
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

// APIUsageSnapshot is the last API response usage for display.
// Source: analyzeContext.ts:200-205
type APIUsageSnapshot struct {
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

// CategoryColorPalette — TUI-friendly color names for categories.
const (
	ColorSystemPrompt = "12"  // blue
	ColorPlatformInfo = "39"  // sky blue
	ColorGitStatus    = "33"  // dim cyan
	ColorToolPrompts  = "51"  // cyan
	ColorSkillListing = "201" // magenta
	ColorMemoryFiles  = "220" // yellow
	ColorSystemTools  = "45"  // green-cyan
	ColorMCPLoaded    = "46"  // green
	ColorMCPDeferred  = "22"  // dark green
	ColorAgents       = "93"  // purple
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
	workingDir := e.workingDir
	skillListing := e.skillListing
	agentDefs := agentDefsClone(e.agentDefs)
	systemPromptRaw := slicesCloneRawMessage(e.systemPrompt)
	mcpReg := e.mcpRegistry
	model := e.model
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

	// Reserved is capped at totalExact (cannot exceed total usage).
		reserved := min(computeReservedTokens(contextWindow, maxTokens), totalExact)
		freeTokens := contextWindow - totalExact

		// Compute expensive data once, shared by estimates and details.
		sections := estimateSystemPromptSections(systemPromptRaw, workingDir, skillListing, toolsSnapshot)
		memFiles := context.LoadMemoryFiles(workingDir)

		estimates := e.estimateComponents(
			sections, memFiles, toolsSnapshot, toolSearchSnap,
			agentDefs, messages, mcpReg, reserved,
		)

		contentTarget := max(totalExact-reserved, 0)
		scaled := scaleProportionally(estimates, contentTarget)

		detailSlices := e.buildDetails(
			sections, memFiles, toolsSnapshot, toolSearchSnap,
			skillListing, agentDefs, messages, mcpReg,
		)

	if freeTokens < 0 {
		freeTokens = 0
	}

	// Assemble categories in the canonical order matching the TS source.
	breakdown.Categories = buildCategories(scaled, freeTokens, reserved, contextWindow)
	breakdown.GridRows = BuildGrid(breakdown.Categories, contextWindow)
	breakdown.MCPToolsLoaded = detailSlices.mcpLoaded
	breakdown.MCPToolsDeferred = detailSlices.mcpDeferred
	breakdown.DeferredBuiltinTools = detailSlices.deferredBuiltin
	breakdown.SystemTools = detailSlices.systemTools
	breakdown.SystemPromptSections = detailSlices.systemPromptSections
	breakdown.MemoryFiles = detailSlices.memoryFiles
	breakdown.Agents = detailSlices.agents
	breakdown.Skills = detailSlices.skills
	breakdown.MessageBreakdown = detailSlices.messageBreakdown
	breakdown.APIUsage = lastAPIUsage(messages)
	breakdown.IsAutoCompact = contextWindow > 0
	breakdown.Percentage = min(safePct(totalExact, contextWindow), 100.0)

	return breakdown
}

// -----------------------------------------------------------------------
// Per-component estimates
// -----------------------------------------------------------------------

// sysPromptSections holds pre-computed system prompt section token estimates.
type sysPromptSections struct {
	base       int
	platform   int
	git        int
	toolPrompts int
	skill      int
}

type componentEstimates struct {
	SystemPromptBase   int
	PlatformInfo       int
	GitStatus          int
	ToolPrompts        int
	SkillListing       int
	MemoryFiles        int
	SystemTools        int
	MCPLoaded          int
	Agents             int
	Messages           int
	MessagesByCategory *MessageBreakdown
}

func (e *Engine) estimateComponents(
	sections sysPromptSections,
	memFiles []context.MemoryFile,
	toolsSnapshot map[string]tool.Tool,
	toolSearchSnap *toolSearchState,
	agentDefs []*types.AgentDefinition,
	messages []types.Message,
	mcpReg *mcp.Registry,
	reserved int,
) componentEstimates {
	est := componentEstimates{
		MessagesByCategory: &MessageBreakdown{},
		SystemPromptBase:   sections.base,
		PlatformInfo:       sections.platform,
		GitStatus:          sections.git,
		ToolPrompts:        sections.toolPrompts,
		SkillListing:       sections.skill,
	}

	for _, f := range memFiles {
		est.MemoryFiles += types.EstimateTokens(f.Content)
	}

	// 3. System tools (built-in, non-deferred, non-MCP).
	est.SystemTools = estimateSystemTools(toolsSnapshot, toolSearchSnap, mcpReg)

	// 4. MCP loaded tools.
	est.MCPLoaded = estimateMCPLoadedTools(mcpReg)

	// 5. Custom agents.
	est.Agents = estimateAgents(agentDefs)

	// 6. Messages (breakdown + total).
	total := 0
	for i := range messages {
		if messages[i].Role != types.RoleUser && messages[i].Role != types.RoleAssistant {
			continue
		}
		// Attachments: count prompt + meta info as attachment tokens.
		if messages[i].Attachment != nil {
			attTokens := types.EstimateTokens(messages[i].Attachment.Prompt)
			total += attTokens
			est.MessagesByCategory.AttachmentTokens += attTokens
			est.MessagesByCategory.upsertAttachment("file", attTokens)
		}
		for _, block := range messages[i].Content {
			t := tokenCountForBlock(block)
			total += t
			classifyMessageBlock(messages[i], block, t, est.MessagesByCategory)
		}
	}
	// Pad by 4/3 to match EstimateMessagesTokens.
	est.Messages = int(math.Ceil(float64(total) * 4.0 / 3.0))

	return est
}

// estimateSystemPromptSections returns per-section token estimates.
// Source: analyzeContext.ts:289-298 — countSystemPromptSections
func estimateSystemPromptSections(
	systemPromptRaw json.RawMessage,
	workingDir string,
	skillListing string,
	toolsSnapshot map[string]tool.Tool,
) sysPromptSections {
	if len(systemPromptRaw) == 0 {
		return sysPromptSections{}
	}
	var systemPromptStr string
	if err := json.Unmarshal(systemPromptRaw, &systemPromptStr); err != nil {
		systemPromptStr = string(systemPromptRaw)
	}

	// Reconstruct each section by calling the Builder's section methods
	// against the same working dir. We use a fresh Builder to compute
	// each section independently, then subtract the per-section estimate.
	builder := context.NewBuilder(workingDir)
	builder.GitStatus = context.LoadGitStatus(workingDir)
	builder.MemoryFiles = context.LoadMemoryFiles(workingDir)
	builder.SkillListing = skillListing

	baseStr := builder.BaseSystemPrompt()
	base := types.EstimateTokens(baseStr)

	platformStr := builder.PlatformInfo()
	platform := types.EstimateTokens(platformStr)

	gitStr := ""
	if builder.GitStatus != nil {
		gitStr = builder.GitStatusSection()
	}
	git := types.EstimateTokens(gitStr)

	// Tool prompts: sum each tool's Prompt() contribution.
	toolPromptsTotal := 0
	for _, t := range toolsSnapshot {
		if !t.IsEnabled() {
			continue
		}
		prompt := t.Prompt()
		if prompt != "" {
			toolPromptsTotal += types.EstimateTokens(prompt)
		}
	}
	toolPrompts := toolPromptsTotal

		skill := 0
		if skillListing != "" {
			skill = types.EstimateTokens(skillListing)
		}

	return sysPromptSections{base: base, platform: platform, git: git, toolPrompts: toolPrompts, skill: skill}
}

// excluding deferred tools and MCP tools.
func estimateSystemTools(
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
		total += estimateSingleTool(t)
	}
	return total
}

func estimateMCPLoadedTools(mcpReg *mcp.Registry) int {
	if mcpReg == nil {
		return 0
	}
	tools := mcpReg.GetTools()
	total := 0
	for _, t := range tools {
		if t.AlwaysLoad {
			total += estimateMCPSchema(t.Description, t.InputSchema)
		}
	}
	return total
}

// name + description + input schema JSON.
func estimateSingleTool(t tool.Tool) int {
	desc, _ := t.Description(nil)
	name := t.Name()
	return types.EstimateTokens(name+desc) + types.EstimateTokens(string(t.InputSchema()))
}

func estimateMCPSchema(desc string, schema json.RawMessage) int {
	return types.EstimateTokens(desc) + types.EstimateTokens(string(schema))
}

func estimateAgents(defs []*types.AgentDefinition) int {
	if len(defs) == 0 {
		return 0
	}
	total := 0
	for _, d := range defs {
		text := d.AgentType + " " + d.WhenToUse
		if d.SystemPrompt != nil {
			text += " " + d.SystemPrompt()
		}
		total += types.EstimateTokens(text)
	}
	return total
}

func tokenCountForBlock(block types.ContentBlock) int {
	switch block.Type {
	case types.ContentTypeText:
		return types.EstimateTokens(block.Text)
	case types.ContentTypeToolResult:
		return types.EstimateTokens(string(block.Content))
	case types.ContentTypeThinking:
		return types.EstimateTokens(block.Thinking)
	case types.ContentTypeToolUse:
		return types.EstimateTokens(block.Name + string(block.Input))
	default:
		raw, _ := json.Marshal(block)
		return types.EstimateTokens(string(raw))
	}
}

func classifyMessageBlock(
	msg types.Message,
	block types.ContentBlock,
	tokens int,
	mb *MessageBreakdown,
) {
	switch block.Type {
	case types.ContentTypeToolUse:
		mb.ToolCallTokens += tokens
		mb.upsertToolCall(block.Name, tokens, 0)
	case types.ContentTypeToolResult:
		mb.ToolResultTokens += tokens
		// Tool result doesn't carry the tool name; classify via message
		// structure later if needed. For now, attribute to "results".
	default:
		switch msg.Role {
		case types.RoleAssistant:
			mb.AssistantTextTokens += tokens
		case types.RoleUser:
			mb.UserTextTokens += tokens
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
		{"Platform info", est.PlatformInfo},
		{"Git status", est.GitStatus},
		{"Tool prompts", est.ToolPrompts},
		{"Skill listing", est.SkillListing},
		{"Memory files", est.MemoryFiles},
		{"System tools", est.SystemTools},
		{"MCP tools", est.MCPLoaded},
		{"Custom agents", est.Agents},
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
	"Platform info",
	"Git status",
	"Tool prompts",
	"Skill listing",
	"Memory files",
	"System tools",
	"MCP tools",
	"Deferred tools",
	"Custom agents",
	"Messages",
	"Free space",
	"Autocompact buffer",
}

var categoryColors = map[string]string{
	"System prompt":      ColorSystemPrompt,
	"Platform info":      ColorPlatformInfo,
	"Git status":         ColorGitStatus,
	"Tool prompts":       ColorToolPrompts,
	"Skill listing":      ColorSkillListing,
	"Memory files":       ColorMemoryFiles,
	"System tools":       ColorSystemTools,
	"MCP tools":          ColorMCPLoaded,
	"Deferred tools":     ColorMCPDeferred,
	"Custom agents":      ColorAgents,
	"Messages":           ColorMessages,
	"Free space":         ColorFree,
	"Autocompact buffer": ColorReserved,
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

	// Compute non-reserved, non-free total for proportional share.
	shareTotal := 0
	for _, c := range cats {
		if c.IsFree || c.IsReserved {
			continue
		}
		shareTotal += c.Tokens
	}

	// Compute reserved squares first so content categories don't over-allocate.
	reservedSquares := 0
	reservedTokens := 0
	for _, c := range cats {
		if c.IsReserved {
			reservedTokens += c.Tokens
		}
	}
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
	mcpLoaded            []MCPToolDetail
	mcpDeferred          []MCPToolDetail
	deferredBuiltin      []SystemToolDetail
	systemTools          []SystemToolDetail
	systemPromptSections []SystemPromptSectionDetail
	memoryFiles          []MemoryFileDetail
	agents               []AgentDetail
	skills               []SkillDetail
	messageBreakdown     *MessageBreakdown
}

func (e *Engine) buildDetails(
	sections sysPromptSections,
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

	for _, s := range []struct {
		name   string
		tokens int
	}{
		{"Base prompt", sections.base},
		{"Platform info", sections.platform},
		{"Git status", sections.git},
		{"Tool prompts", sections.toolPrompts},
		{"Skill listing", sections.skill},
	} {
		if s.tokens > 0 {
			ds.systemPromptSections = append(ds.systemPromptSections, SystemPromptSectionDetail{Name: s.name, Tokens: s.tokens})
		}
	}

	for _, f := range memFiles {
		ds.memoryFiles = append(ds.memoryFiles, MemoryFileDetail{
			Path:   f.Path,
			Tokens: types.EstimateTokens(f.Content),
		})
	}

	// System tools (per-tool) and deferred built-in tools.
	seenDeferred := map[string]bool{}
	for name, t := range toolsSnapshot {
		if !t.IsEnabled() {
			continue
		}
		if isMCPToolName(name) {
			continue
		}
		if tool.IsDeferred(t) {
			if toolSearchSnap != nil && !toolSearchSnap.IsDiscovered(name) {
				if !seenDeferred[name] {
					ds.deferredBuiltin = append(ds.deferredBuiltin, SystemToolDetail{
						Name:   name,
						Tokens: estimateSingleTool(t),
					})
					seenDeferred[name] = true
				}
			}
			continue
		}
		ds.systemTools = append(ds.systemTools, SystemToolDetail{
			Name:   name,
			Tokens: estimateSingleTool(t),
		})
	}

	// MCP tools (loaded vs deferred).
	if mcpReg != nil {
		for _, t := range mcpReg.GetTools() {
			detail := MCPToolDetail{
				Name:       t.OriginalName,
				ServerName: t.ServerName,
				Tokens:     estimateMCPSchema(t.Description, t.InputSchema),
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

	// Agents.
	for _, d := range agentDefs {
		if d == nil {
			continue
		}
		ds.agents = append(ds.agents, AgentDetail{
			AgentType: d.AgentType,
			Source:    "built-in",
			Tokens:    types.EstimateTokens(d.AgentType + " " + d.WhenToUse),
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
				Tokens: types.EstimateTokens(line),
			})
		}
	}

	// Message breakdown.
	for i := range messages {
		if messages[i].Role != types.RoleUser && messages[i].Role != types.RoleAssistant {
			continue
		}
		if messages[i].Attachment != nil {
			attTokens := types.EstimateTokens(messages[i].Attachment.Prompt)
			ds.messageBreakdown.AttachmentTokens += attTokens
			ds.messageBreakdown.upsertAttachment("file", attTokens)
		}
		for _, block := range messages[i].Content {
			t := tokenCountForBlock(block)
			classifyMessageBlock(messages[i], block, t, ds.messageBreakdown)
		}
	}
	// Sort by call tokens desc, top 5.
	sort.Slice(ds.messageBreakdown.ToolCallsByType, func(i, j int) bool {
		return ds.messageBreakdown.ToolCallsByType[i].CallTokens >
			ds.messageBreakdown.ToolCallsByType[j].CallTokens
	})
	if len(ds.messageBreakdown.ToolCallsByType) > 5 {
		ds.messageBreakdown.ToolCallsByType = ds.messageBreakdown.ToolCallsByType[:5]
	}
	sort.Slice(ds.messageBreakdown.AttachmentsByType, func(i, j int) bool {
		return ds.messageBreakdown.AttachmentsByType[i].Tokens >
			ds.messageBreakdown.AttachmentsByType[j].Tokens
	})
	if len(ds.messageBreakdown.AttachmentsByType) > 5 {
		ds.messageBreakdown.AttachmentsByType = ds.messageBreakdown.AttachmentsByType[:5]
	}

	return ds
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

func computeReservedTokens(contextWindow, maxTokens int) int {
	if contextWindow <= 0 {
		return 0
	}
	// 7% of effective window, min 3K. Source: autoCompactBuffer.
	buf := max(contextWindow*7/100, 3000)
	return buf
}

func lastAPIUsage(messages []types.Message) *APIUsageSnapshot {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == types.RoleAssistant && messages[i].Usage != nil {
			u := messages[i].Usage
			if u.InputTokens == 0 && u.OutputTokens == 0 &&
				u.CacheCreationInputTokens == 0 && u.CacheReadInputTokens == 0 {
				continue
			}
			return &APIUsageSnapshot{
				InputTokens:              u.InputTokens,
				OutputTokens:             u.OutputTokens,
				CacheCreationInputTokens: u.CacheCreationInputTokens,
				CacheReadInputTokens:     u.CacheReadInputTokens,
			}
		}
	}
	return nil
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

func slicesCloneRawMessage(r json.RawMessage) json.RawMessage {
	if r == nil {
		return nil
	}
	out := make([]byte, len(r))
	copy(out, r)
	return json.RawMessage(out)
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
