// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
//
// This file: Sampling handler — routes MCP server sampling requests to gbot's LLM Provider.
// Source: TS doesn't implement sampling, but this is a differentiation advantage.
//
// Maps MCP CreateMessageParams → gbot llm.Request → Provider.Complete → llm.Response → MCP CreateMessageResult.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// SamplingProvider — interface for LLM completion
// ---------------------------------------------------------------------------

// SamplingProvider wraps the LLM provider for MCP sampling requests.
// Only exposes Complete (not Stream) — sampling is a synchronous operation.
type SamplingProvider interface {
	Complete(ctx context.Context, req *llm.Request) (*llm.Response, error)
}

// ---------------------------------------------------------------------------
// ClientManager — Sampling integration
// ---------------------------------------------------------------------------

// SetSamplingProvider sets the LLM provider for sampling requests.
// Can be called after ClientManager creation.
// When nil (default), sampling requests return an error and SamplingCapabilities
// are not advertised.
func (cm *ClientManager) SetSamplingProvider(p SamplingProvider) {
	cm.samplingProvider.Store(&p)
}

// makeSamplingHandler creates the ClientOptions.CreateMessageHandler closure.
// Source: TS doesn't implement this — gbot differentiation.
//
// Conversion path:
//
//	MCP SamplingMessage → types.Message
//	MCP CreateMessageParams → llm.Request
//	llm.Response → MCP CreateMessageResult
func (cm *ClientManager) makeSamplingHandler(serverName string) func(context.Context, *mcp.ClientRequest[*mcp.CreateMessageParams]) (*mcp.CreateMessageResult, error) {
	return func(ctx context.Context, req *mcp.ClientRequest[*mcp.CreateMessageParams]) (*mcp.CreateMessageResult, error) {
		slog.Info("mcp: sampling request received", "server", serverName, "maxTokens", req.Params.MaxTokens)
		provider := cm.samplingProvider.Load()
		if provider == nil {
			slog.Warn("mcp: sampling rejected — no provider configured", "server", serverName)
			return nil, fmt.Errorf("mcp: sampling not supported (no provider configured)")
		}

		// Timeout protection — prevent malicious/slow servers from blocking indefinitely.
		// Source: performance review — 30s timeout.
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		params := req.Params

		// Convert MCP messages → gbot messages
		messages := make([]types.Message, 0, len(params.Messages))
		for _, msg := range params.Messages {
			messages = append(messages, samplingMessageToLLM(msg))
		}

		// Build LLM request
		llmReq := &llm.Request{
			Model:     cm.samplingModel,
			MaxTokens: int(params.MaxTokens),
			Messages:  messages,
		}

		// System prompt
		if params.SystemPrompt != "" {
			sysJSON, _ := json.Marshal(params.SystemPrompt)
			llmReq.System = sysJSON
		}

		// Temperature (only if non-zero)
		if params.Temperature > 0 {
			temp := params.Temperature
			llmReq.Temperature = &temp
		}

		// Stop sequences
		if len(params.StopSequences) > 0 {
			llmReq.StopSequences = params.StopSequences
		}

		slog.Info("mcp: sampling request", "server", serverName, "messages", len(messages), "maxTokens", params.MaxTokens)

		resp, err := (*provider).Complete(ctx, llmReq)
		if err != nil {
			return nil, fmt.Errorf("mcp: sampling failed for server %q: %w", serverName, err)
		}

		// Convert llm.Response → MCP CreateMessageResult
		result := &mcp.CreateMessageResult{
			Model:      resp.Model,
			Role:       "assistant",
			StopReason: mapStopReason(resp.StopReason),
		}

		// Convert content blocks → single MCP Content
		// MCP CreateMessageResult.Content is a single Content, not a slice.
		if len(resp.Content) > 0 {
			result.Content = contentBlockToMCP(resp.Content[0])
		}

		return result, nil
	}
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

// samplingMessageToLLM converts an MCP SamplingMessage to a gbot types.Message.
func samplingMessageToLLM(msg *mcp.SamplingMessage) types.Message {
	var blocks []types.ContentBlock
	if msg.Content != nil {
		blocks = append(blocks, mcpContentToBlock(msg.Content))
	}
	return types.Message{
		Role:    types.Role(msg.Role),
		Content: blocks,
	}
}

// mcpContentToBlock converts an MCP Content interface to a gbot ContentBlock.
func mcpContentToBlock(c mcp.Content) types.ContentBlock {
	switch v := c.(type) {
	case *mcp.TextContent:
		return types.NewTextBlock(v.Text)
	case *mcp.ImageContent:
		// Image content — store as base64 text representation
		return types.NewTextBlock(fmt.Sprintf("[image: %s, %d bytes]", v.MIMEType, len(v.Data)))
	default:
		return types.NewTextBlock(fmt.Sprintf("%v", c))
	}
}

// contentBlockToMCP converts a gbot ContentBlock to an MCP Content.
// All block types are returned as TextContent — MCP sampling only supports text output.
func contentBlockToMCP(block types.ContentBlock) mcp.Content {
	return &mcp.TextContent{Text: block.Text}
}

// mapStopReason converts gbot stop reasons to MCP stop reasons.
func mapStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "endTurn"
	case "max_tokens":
		return "maxTokens"
	case "tool_use":
		return "toolUse"
	case "stop_sequence":
		return "stopSequence"
	default:
		return reason
	}
}
