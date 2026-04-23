// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
// Source: src/services/mcp/ (22 files, ~12K lines TS)
//
// This file: tool execution, result transformation, and large output handling.
// Source: client.ts:2478-3245 (transformResultContent, processMCPResult, callMCPTool)
package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// MCPProgress — progress callback data
// Source: client.ts:3055-3066 — onProgress callback shape
// ---------------------------------------------------------------------------

// MCPProgress represents progress data from an MCP tool call.
type MCPProgress struct {
	Type            string // "progress"
	Status          string // e.g. "in_progress"
	ServerName      string
	ToolName        string
	Progress        float64
	Total           float64
	ProgressMessage string
}

// ---------------------------------------------------------------------------
// Tool timeout — Source: client.ts:3068-3089 getMcpToolTimeoutMs
// ---------------------------------------------------------------------------

// GetToolTimeoutMs returns the tool call timeout from MCP_TOOL_TIMEOUT env or default.
// Source: client.ts:3068 — parseInt(process.env.MCP_TOOL_TIMEOUT) || 100_000_000
func GetToolTimeoutMs() int {
	if v := os.Getenv("MCP_TOOL_TIMEOUT"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return ms
		}
	}
	return DefaultToolCallTimeoutMs
}

// ---------------------------------------------------------------------------
// CallMCPTool — execute a tool on a connected MCP server
// Source: client.ts:3029-3245 — callMCPTool
// ---------------------------------------------------------------------------

// CallMCPToolParams holds the parameters for an MCP tool call.
// Source: client.ts:3029-3045
type CallMCPToolParams struct {
	Server     *ConnectedServer
	ToolName   string
	Args       map[string]any
	Meta       map[string]any
	OnProgress func(MCPProgress)
}

// MCPToolCallResult holds the result of an MCP tool call.
type MCPToolCallResult struct {
	Content           []mcp.Content
	Meta              map[string]any
	StructuredContent any
}

// CallMCPTool executes a tool on a connected MCP server with timeout and progress.
// Source: client.ts:3029-3245
func CallMCPTool(ctx context.Context, params CallMCPToolParams) (*MCPToolCallResult, error) {
	if params.Server == nil || params.Server.Session == nil {
		return nil, fmt.Errorf("mcp: server not connected")
	}

	timeoutMs := GetToolTimeoutMs()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	// Source: client.ts:3080-3089 — callTool with timeout
	callParams := &mcp.CallToolParams{
		Name:      params.ToolName,
		Arguments: params.Args,
		Meta:      params.Meta,
	}

	// Note: Go SDK handles progress at the Client level via
	// ProgressNotificationHandler, not per-call. The OnProgress
	// callback is preserved in the params for future wiring.
	result, err := params.Server.Session.CallTool(ctx, callParams)
	if err != nil {
		// Source: client.ts:3196-3208 — auth error detection
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "Unauthorized") {
			return nil, &McpAuthError{
				ServerName: params.Server.Name,
				Message:    err.Error(),
			}
		}
		// Source: client.ts:3210-3231 — session expiry detection
		if IsMcpSessionExpiredError(err) {
			return nil, &McpToolCallError{
				ServerName: params.Server.Name,
				ToolName:   params.ToolName,
				Err:        fmt.Errorf("session expired: %w", err),
			}
		}
		return nil, &McpToolCallError{
			ServerName: params.Server.Name,
			ToolName:   params.ToolName,
			Err:        err,
		}
	}

	// Source: client.ts:3124-3149 — isError result handling
	if result.IsError {
		errMsg := extractErrorMessage(result)
		return nil, &McpToolCallError{
			ServerName: params.Server.Name,
			ToolName:   params.ToolName,
			Err:        fmt.Errorf("%s", errMsg),
		}
	}

	// Source: client.ts:3150-3178 — process result
	var meta map[string]any
	if result.Meta != nil {
		meta = result.Meta
	}

	return &MCPToolCallResult{
		Content:           result.Content,
		Meta:              meta,
		StructuredContent: result.StructuredContent,
	}, nil
}

// extractErrorMessage extracts error message from an error tool result.
// Source: client.ts:3131-3145
func extractErrorMessage(result *mcp.CallToolResult) string {
	if len(result.Content) > 0 {
		if tc, ok := result.Content[0].(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return "unknown tool error"
}

// ---------------------------------------------------------------------------
// InferCompactSchema — recursive type signature
// Source: client.ts:2644-2660 — inferCompactSchema
// ---------------------------------------------------------------------------

// InferCompactSchema generates a compact type signature string for a value.
// Source: client.ts:2644-2660
func InferCompactSchema(value any, depth ...int) string {
	d := 2
	if len(depth) > 0 {
		d = depth[0]
	}

	if value == nil {
		return "null"
	}

	switch v := value.(type) {
	case bool:
		return "boolean"
	case float64, float32, int, int64, int32:
		return "number"
	case string:
		return "string"

	case []any:
		if len(v) == 0 {
			return "[]"
		}
		return "[" + InferCompactSchema(v[0], d-1) + "]"

	case map[string]any:
		if d <= 0 {
			return "{...}"
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		limit := min(len(keys), 10)
		parts := make([]string, 0, limit)
		for i := range limit {
			parts = append(parts, keys[i]+": "+InferCompactSchema(v[keys[i]], d-1))
		}
		result := "{" + strings.Join(parts, ", ")
		if len(keys) > 10 {
			result += ", ..."
		}
		result += "}"
		return result

	default:
		// Try JSON round-trip for other types
		b, err := json.Marshal(v)
		if err != nil {
			return "unknown"
		}
		var decoded any
		if json.Unmarshal(b, &decoded) != nil {
			return "unknown"
		}
		return InferCompactSchema(decoded, d)
	}
}

// ---------------------------------------------------------------------------
// TransformResultContent — per-content-block transformation
// Source: client.ts:2478-2591 — transformResultContent
// ---------------------------------------------------------------------------

// TransformResultContent transforms a single MCP content block into text.
// Source: client.ts:2478-2591
//
// Handles text, image, audio, resource, and resource_link content types.
// Returns one or more TransformedMCPResult items.
func TransformResultContent(content mcp.Content, serverName string) []TransformedMCPResult {
	switch c := content.(type) {
	case *mcp.TextContent:
		// Source: client.ts:2482-2484 — identity passthrough
		return []TransformedMCPResult{{
			Type:    MCPResultText,
			Content: c.Text,
		}}

	case *mcp.ImageContent:
		// Source: client.ts:2514-2533 — base64 image data
		data := base64.StdEncoding.EncodeToString(c.Data)
		return []TransformedMCPResult{{
			Type: MCPResultImage,
			Content: fmt.Sprintf(
				"[Image from %s: %s, %d bytes]",
				serverName, c.MIMEType, len(c.Data),
			),
			// Store raw data for downstream use
			RawData:  data,
			MIMEType: c.MIMEType,
		}}

	case *mcp.AudioContent:
		// Source: client.ts:2488-2510 — audio → persist or text
		return []TransformedMCPResult{{
			Type: MCPResultAudio,
			Content: fmt.Sprintf(
				"[Audio from %s: %s, %d bytes]",
				serverName, c.MIMEType, len(c.Data),
			),
			RawData:  base64.StdEncoding.EncodeToString(c.Data),
			MIMEType: c.MIMEType,
		}}

	case *mcp.EmbeddedResource:
		// Source: client.ts:2538-2588 — resource text or blob
		if c.Resource == nil {
			return nil
		}
		if c.Resource.Text != "" {
			return []TransformedMCPResult{{
				Type:    MCPResultResource,
				Content: fmt.Sprintf("[Resource from %s at %s]\n%s", serverName, c.Resource.URI, c.Resource.Text),
			}}
		}
		if len(c.Resource.Blob) > 0 {
			return []TransformedMCPResult{{
				Type: MCPResultResource,
				Content: fmt.Sprintf(
					"[Binary resource from %s at %s: %s, %d bytes]",
					serverName, c.Resource.URI, c.Resource.MIMEType, len(c.Resource.Blob),
				),
				RawData:  base64.StdEncoding.EncodeToString(c.Resource.Blob),
				MIMEType: c.Resource.MIMEType,
			}}
		}
		return nil

	case *mcp.ResourceLink:
		// Source: client.ts:2559-2562 — resource_link
		desc := c.Description
		if desc == "" {
			desc = "no description"
		}
		return []TransformedMCPResult{{
			Type:    MCPResultText,
			Content: fmt.Sprintf("[Resource link: %s] %s (%s)", c.Name, c.URI, desc),
		}}

	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// TransformMCPResult — result type discrimination
// Source: client.ts:2662-2706 — transformMCPResult
// ---------------------------------------------------------------------------

// TransformMCPResult discriminates the result type and transforms content.
// Source: client.ts:2662-2706
func TransformMCPResult(result *mcp.CallToolResult, toolName, serverName string) ([]TransformedMCPResult, string) {
	// Source: client.ts:2675-2683 — structuredContent
	if result.StructuredContent != nil {
		schema := InferCompactSchema(result.StructuredContent)
		b, _ := json.Marshal(result.StructuredContent)
		return []TransformedMCPResult{{
			Type:    MCPResultText,
			Content: string(b),
		}}, schema
	}

	// Source: client.ts:2686-2696 — content array
	if len(result.Content) > 0 {
		var all []TransformedMCPResult
		for _, c := range result.Content {
			all = append(all, TransformResultContent(c, serverName)...)
		}
		schema := ""
		if len(all) > 0 {
			schema = InferCompactSchema(all[0].Content)
		}
		return all, schema
	}

	return nil, ""
}

// ---------------------------------------------------------------------------
// ContentContainsImages — image detection
// Source: client.ts:2713-2718
// ---------------------------------------------------------------------------

// ContentContainsImages returns true if any content block is an image.
// Source: client.ts:2713-2718
func ContentContainsImages(content []mcp.Content) bool {
	for _, c := range content {
		if _, ok := c.(*mcp.ImageContent); ok {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// ProcessMCPResult — large output handling
// Source: client.ts:2720-2799
// ---------------------------------------------------------------------------

// maxMCPContentChars is the threshold for large output persistence.
// Source: client.ts:2734-2736 — mcpContentNeedsTruncation
const maxMCPContentChars = 50000

// ProcessMCPResult transforms and optionally persists large MCP tool output.
// Source: client.ts:2720-2799
func ProcessMCPResult(result *mcp.CallToolResult, toolName, serverName string) (string, error) {
	transformed, _ := TransformMCPResult(result, toolName, serverName)

	// Combine all text content
	var parts []string
	for _, t := range transformed {
		if s, ok := t.Content.(string); ok && s != "" {
			parts = append(parts, s)
		}
	}
	content := strings.Join(parts, "\n")

	// Source: client.ts:2734-2736 — check if truncation needed
	if len(content) <= maxMCPContentChars {
		return content, nil
	}

	// Source: client.ts:2741-2748 — ENABLE_MCP_LARGE_OUTPUT_FILES gate
	if env := os.Getenv("ENABLE_MCP_LARGE_OUTPUT_FILES"); env != "" {
		if env == "false" || env == "0" {
			return truncateContent(content, maxMCPContentChars), nil
		}
	}

	// Source: client.ts:2758-2765 — image content fallback
	if ContentContainsImages(result.Content) {
		return truncateContent(content, maxMCPContentChars), nil
	}

	// Source: client.ts:2768-2798 — persist to file
	persistID := fmt.Sprintf("mcp-%s-%s-%d", serverName, toolName, time.Now().UnixMilli())
	filePath, err := persistToolResult(content, persistID)
	if err != nil {
		// Fallback to truncation on failure
		return truncateContent(content, maxMCPContentChars), nil
	}

	return fmt.Sprintf("[Large output persisted to %s (%d chars). Read the file to see full output.]",
		filePath, len(content)), nil
}

// truncateContent truncates content to maxLen with a truncation notice.
func truncateContent(content string, maxLen int) string {
	if len(content) <= maxLen {
		return content
	}
	return content[:maxLen] + "\n... [truncated]"
}

// persistToolResult writes tool output to a temp file.
// Source: client.ts:2768-2798 — persistToolResult
func persistToolResult(content, persistID string) (string, error) {
	tmpDir := os.TempDir()
	fileName := persistID + ".txt"
	filePath := filepath.Join(tmpDir, fileName)

	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		return "", fmt.Errorf("mcp: failed to persist tool result: %w", err)
	}

	return filePath, nil
}

// ---------------------------------------------------------------------------
// CallMCPToolWithUrlElicitationRetry — stub
// Source: client.ts:2813-3027
//
// URL elicitation retry is a complex feature requiring UI integration.
// This stub delegates to CallMCPTool directly.
// ---------------------------------------------------------------------------

// CallMCPToolWithUrlElicitationRetry is a stub that delegates to CallMCPTool.
// Source: client.ts:2813-3027 — full implementation handles -32042 error code
// with URL elicitation UI flow. For now, this just calls CallMCPTool.
func CallMCPToolWithUrlElicitationRetry(ctx context.Context, params CallMCPToolParams) (*MCPToolCallResult, error) {
	return CallMCPTool(ctx, params)
}
