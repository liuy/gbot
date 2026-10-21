package wui

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/types"
)

type contextBreakdownOutbound struct {
	Type                 string                    `json:"type"`
	Model                string                    `json:"model"`
	ContextWindow        int                       `json:"contextWindow"`
	TotalTokens          int                       `json:"totalTokens"`
	Percentage           float64                   `json:"percentage"`
	IsAutoCompact        bool                      `json:"isAutoCompact"`
	Categories           []contextCategoryWire     `json:"categories"`
	MCPToolsLoaded       []mcpToolDetailWire       `json:"mcpToolsLoaded"`
	MCPToolsDeferred     []mcpToolDetailWire       `json:"mcpToolsDeferred"`
	DeferredBuiltinTools []systemToolDetailWire    `json:"deferredBuiltinTools"`
	SystemTools          []systemToolDetailWire    `json:"systemTools"`
	SystemPromptSections []systemPromptSectionWire `json:"systemPromptSections"`
	MemoryFiles          []memoryFileDetailWire    `json:"memoryFiles"`
	Agents               []agentDetailWire         `json:"agents"`
	Skills               []skillDetailWire         `json:"skills"`
	MessageBreakdown     *messageBreakdownWire     `json:"messageBreakdown"`
	APIUsage             *apiUsageSnapshotWire     `json:"apiUsage"`
}

type contextCategoryWire struct {
	Name       string  `json:"name"`
	ID         string  `json:"id"`
	Tokens     int     `json:"tokens"`
	Percentage float64 `json:"percentage"`
	Color      string  `json:"color"`
	IsFree     bool    `json:"isFree"`
	IsReserved bool    `json:"isReserved"`
}

type mcpToolDetailWire struct {
	Name       string `json:"name"`
	ServerName string `json:"serverName"`
	Tokens     int    `json:"tokens"`
	IsLoaded   bool   `json:"isLoaded"`
}

type systemToolDetailWire struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
}

type systemPromptSectionWire struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	Tokens int    `json:"tokens"`
}

type memoryFileDetailWire struct {
	Path   string `json:"path"`
	Tokens int    `json:"tokens"`
}

type agentDetailWire struct {
	AgentType string `json:"agentType"`
	Source    string `json:"source"`
	Tokens    int    `json:"tokens"`
}

type skillDetailWire struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Tokens int    `json:"tokens"`
}

type messageBreakdownWire struct {
	ToolCallTokens      int                    `json:"toolCallTokens"`
	ToolResultTokens    int                    `json:"toolResultTokens"`
	AttachmentTokens    int                    `json:"attachmentTokens"`
	AssistantTextTokens int                    `json:"assistantTextTokens"`
	UserTextTokens      int                    `json:"userTextTokens"`
	ToolCallsByType     []toolCallByTypeWire   `json:"toolCallsByType"`
	AttachmentsByType   []attachmentByTypeWire `json:"attachmentsByType"`
}

type toolCallByTypeWire struct {
	Name         string `json:"name"`
	CallTokens   int    `json:"callTokens"`
	ResultTokens int    `json:"resultTokens"`
}

type attachmentByTypeWire struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
}

type apiUsageSnapshotWire struct {
	InputTokens              int `json:"inputTokens"`
	OutputTokens             int `json:"outputTokens"`
	CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
	CacheReadInputTokens     int `json:"cacheReadInputTokens"`
}

func convertMCPTools(in []engine.MCPToolDetail) []mcpToolDetailWire {
	out := make([]mcpToolDetailWire, len(in))
	for i, t := range in {
		out[i] = mcpToolDetailWire{
			Name:       t.Name,
			ServerName: t.ServerName,
			Tokens:     t.Tokens,
			IsLoaded:   t.IsLoaded,
		}
	}
	return out
}

func convertSystemTools(in []engine.SystemToolDetail) []systemToolDetailWire {
	out := make([]systemToolDetailWire, len(in))
	for i, t := range in {
		out[i] = systemToolDetailWire{Name: t.Name, Tokens: t.Tokens}
	}
	return out
}

func convertPromptSections(in []engine.SystemPromptSectionDetail) []systemPromptSectionWire {
	out := make([]systemPromptSectionWire, len(in))
	for i, t := range in {
		out[i] = systemPromptSectionWire{Name: t.Name, ID: t.ID, Tokens: t.Tokens}
	}
	return out
}

func convertMemoryFiles(in []engine.MemoryFileDetail) []memoryFileDetailWire {
	out := make([]memoryFileDetailWire, len(in))
	for i, f := range in {
		out[i] = memoryFileDetailWire{Path: f.Path, Tokens: f.Tokens}
	}
	return out
}

func convertAgents(in []engine.AgentDetail) []agentDetailWire {
	out := make([]agentDetailWire, len(in))
	for i, a := range in {
		out[i] = agentDetailWire{AgentType: a.AgentType, Source: a.Source, Tokens: a.Tokens}
	}
	return out
}

func convertSkills(in []engine.SkillDetail) []skillDetailWire {
	out := make([]skillDetailWire, len(in))
	for i, s := range in {
		out[i] = skillDetailWire{Name: s.Name, Source: s.Source, Tokens: s.Tokens}
	}
	return out
}

func convertCategories(in []engine.ContextCategory) []contextCategoryWire {
	out := make([]contextCategoryWire, len(in))
	for i, c := range in {
		out[i] = contextCategoryWire{
			Name:       c.Name,
			ID:         c.ID,
			Tokens:     c.Tokens,
			Percentage: c.Percentage,
			Color:      c.Color,
			IsFree:     c.IsFree,
			IsReserved: c.IsReserved,
		}
	}
	return out
}

func convertMessageBreakdown(mb *engine.MessageBreakdown) *messageBreakdownWire {
	if mb == nil {
		return nil
	}
	calls := make([]toolCallByTypeWire, len(mb.ToolCallsByType))
	for i, c := range mb.ToolCallsByType {
		calls[i] = toolCallByTypeWire{
			Name:         c.Name,
			CallTokens:   c.CallTokens,
			ResultTokens: c.ResultTokens,
		}
	}
	atts := make([]attachmentByTypeWire, len(mb.AttachmentsByType))
	for i, a := range mb.AttachmentsByType {
		atts[i] = attachmentByTypeWire{Name: a.Name, Tokens: a.Tokens}
	}
	return &messageBreakdownWire{
		ToolCallTokens:      mb.ToolCallTokens,
		ToolResultTokens:    mb.ToolResultTokens,
		AttachmentTokens:    mb.AttachmentTokens,
		AssistantTextTokens: mb.AssistantTextTokens,
		UserTextTokens:      mb.UserTextTokens,
		ToolCallsByType:     calls,
		AttachmentsByType:   atts,
	}
}

func convertAPIUsage(u *engine.APIUsageSnapshot) *apiUsageSnapshotWire {
	if u == nil {
		return nil
	}
	return &apiUsageSnapshotWire{
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens,
	}
}

func buildContextBreakdown(bd *engine.ContextBreakdown) []byte {
	if bd == nil || bd.TotalTokens == 0 {
		payload, _ := json.Marshal(struct {
			Type        string `json:"type"`
			TotalTokens int    `json:"totalTokens"`
		}{Type: "context_breakdown", TotalTokens: 0})
		return payload
	}

	out := contextBreakdownOutbound{
		Type:                 "context_breakdown",
		Model:                bd.Model,
		ContextWindow:        bd.ContextWindow,
		TotalTokens:          bd.TotalTokens,
		Percentage:           bd.Percentage,
		IsAutoCompact:        bd.IsAutoCompact,
		Categories:           convertCategories(bd.Categories),
		MCPToolsLoaded:       convertMCPTools(bd.MCPToolsLoaded),
		MCPToolsDeferred:     convertMCPTools(bd.MCPToolsDeferred),
		DeferredBuiltinTools: convertSystemTools(bd.DeferredBuiltinTools),
		SystemTools:          convertSystemTools(bd.SystemTools),
		SystemPromptSections: convertPromptSections(bd.SystemPromptSections),
		MemoryFiles:          convertMemoryFiles(bd.MemoryFiles),
		Agents:               convertAgents(bd.Agents),
		Skills:               convertSkills(bd.Skills),
		MessageBreakdown:     convertMessageBreakdown(bd.MessageBreakdown),
		APIUsage:             convertAPIUsage(bd.APIUsage),
	}

	payload, err := json.Marshal(out)
	if err != nil {
		slog.Warn("wui: marshal context_breakdown failed", "error", err)
		return nil
	}
	return payload
}

func (c *WUIConnector) handleContextRequest() {
	eng := c.activeEngine()
	if eng == nil {
		return
	}
	bd := eng.ContextBreakdown()
	payload := buildContextBreakdown(bd)
	if payload != nil {
		c.sendWS(payload)
	}
}

func (c *WUIConnector) handleCompactRequest() {
	eng := c.activeEngine()
	if eng == nil {
		return
	}
	userMsg := types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("/compact")})
	if _, err := eng.ManualCompact(context.Background(), userMsg, ""); err != nil {
		slog.Warn("wui: manual compact failed", "error", err)
	}
}
