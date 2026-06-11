package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/liuy/gbot/pkg/tool/web"
)

var (
	zhipuMCPURL = "https://open.bigmodel.cn/api/mcp/web_search_prime/mcp"
)

const (
	zhipuToolName   = "web_search_prime"
	zhipuDefaultLimit = 10
	zhipuTimeout    = 30 * time.Second
)

// ZhipuProvider implements SearchProvider using Zhipu BigModel MCP search.
// Uses the same API key as the zhipu PaaS LLM endpoint — no separate key needed.
type ZhipuProvider struct {
	Client *http.Client // nil → http.DefaultClient
	APIKey string
}

func (z *ZhipuProvider) ID() string { return "zhipu" }
func (z *ZhipuProvider) IsAvailable() bool {
	return z.APIKey != "" && strings.IndexByte(z.APIKey, '.') >= 0
}

func (z *ZhipuProvider) client() *http.Client {
	if z.Client != nil {
		return z.Client
	}
	return http.DefaultClient
}

// jsonRPCRequest is the JSON-RPC 2.0 payload sent to Zhipu MCP.
type jsonRPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      string        `json:"id"`
	Method  string        `json:"method"`
	Params  jsonRPCParams `json:"params"`
}

type jsonRPCParams struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

// jsonRPCResponse is the top-level JSON-RPC response.
type jsonRPCResponse struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      string         `json:"id"`
	Result  *jsonRPCResult `json:"result,omitempty"`
	Error   *jsonRPCError  `json:"error,omitempty"`
}

type jsonRPCResult struct {
	Content []jsonRPCContent `json:"content"`
	IsError bool             `json:"isError,omitempty"`
}

type jsonRPCContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// zhipuSearchResult is a single result from Zhipu MCP search.
type zhipuSearchResult struct {
	Title   string `json:"title"`
	Link    string `json:"link"`
	Content string `json:"content"`
}

func (z *ZhipuProvider) Search(ctx context.Context, params web.SearchParams) (*web.SearchResponse, error) {
	if params.Limit <= 0 {
		params.Limit = zhipuDefaultLimit
	}

	sessionID := uuid.New().String()
	reqPayload := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      sessionID,
		Method:  "tools/call",
		Params: jsonRPCParams{
			Name: zhipuToolName,
			Arguments: map[string]any{
				"search_query": params.Query,
				"count":        params.Limit,
			},
		},
	}

	body, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("zhipu: marshal request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, zhipuTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, zhipuMCPURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("zhipu: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+z.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)
	req.Header.Set("Session-Id", sessionID)

	resp, err := z.client().Do(req)
	if err != nil {
		return nil, &web.SearchProviderError{
			Provider: "zhipu",
			Message:  fmt.Sprintf("request failed: %v", err),
			Status:   0,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, &web.SearchProviderError{
			Provider: "zhipu",
			Message:  fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody)),
			Status:   resp.StatusCode,
		}
	}

	return parseZhipuResponse(resp.Body, params.Limit)
}

// parseZhipuResponse extracts search results from the SSE JSON-RPC stream.
// The response body contains lines like:
//
//	id:1
//	event:message
//	data:{"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"..."}]}}
func parseZhipuResponse(body io.Reader, limit int) (*web.SearchResponse, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("zhipu: read response: %w", err)
	}

	// Extract data: lines from SSE stream
	var lastData string
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			lastData = after
		}
	}
	if lastData == "" {
		return nil, &web.SearchProviderError{
			Provider: "zhipu",
			Message:  "empty SSE response",
			Status:   500,
		}
	}

	// Parse JSON-RPC envelope
	var rpcResp jsonRPCResponse
	if err := json.Unmarshal([]byte(lastData), &rpcResp); err != nil {
		return nil, fmt.Errorf("zhipu: parse JSON-RPC: %w", err)
	}

	// JSON-RPC level error
	if rpcResp.Error != nil {
		status := rpcResp.Error.Code
		msg := rpcResp.Error.Message
		// Check if message contains an MCP error with a real HTTP status
		if code, m := extractMCPError(msg); code != 0 {
			status = code
			msg = m
		}
		return nil, &web.SearchProviderError{
			Provider: "zhipu",
			Message:  msg,
			Status:   toHTTPStatus(status),
		}
	}

	if rpcResp.Result == nil {
		return nil, &web.SearchProviderError{
			Provider: "zhipu",
			Message:  "missing result in JSON-RPC response",
			Status:   500,
		}
	}

	// MCP-level error in result
	if rpcResp.Result.IsError && len(rpcResp.Result.Content) > 0 {
		errText := rpcResp.Result.Content[0].Text
		if code, msg := extractMCPError(errText); code != 0 {
			return nil, &web.SearchProviderError{
				Provider: "zhipu",
				Message:  msg,
				Status:   toHTTPStatus(code),
			}
		}
		return nil, &web.SearchProviderError{
			Provider: "zhipu",
			Message:  errText,
			Status:   500,
		}
	}

	// Extract text from content blocks and parse search results
	var allSources []web.SearchSource
	for _, c := range rpcResp.Result.Content {
		if len(allSources) >= limit {
			break
		}
		if c.Type != "text" || c.Text == "" {
			continue
		}

		// The text field is a JSON-encoded string containing a JSON array
		var innerStr string
		if err := json.Unmarshal([]byte(c.Text), &innerStr); err != nil {
			innerStr = c.Text
		}

		var results []zhipuSearchResult
		if err := json.Unmarshal([]byte(innerStr), &results); err != nil {
			continue
		}

		for _, r := range results {
			if r.Link == "" {
				continue
			}
			title := r.Title
			if title == "" {
				title = r.Link
			}
			allSources = append(allSources, web.SearchSource{
				Title:   title,
				URL:     r.Link,
				Snippet: r.Content,
			})
			if len(allSources) >= limit {
				break
			}
		}
	}

	return &web.SearchResponse{
		Provider: "zhipu",
		Sources:  allSources,
	}, nil
}

// extractMCPError parses "MCP error -NNN: message" format from Zhipu responses.
// Returns (statusCode, message). Returns (0, "") if not an MCP error format.
func extractMCPError(text string) (int, string) {
	// "MCP error -401: Api key not found"
	// "MCP error -429: {...}"
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "MCP error ") {
		return 0, ""
	}
	rest := strings.TrimPrefix(text, "MCP error ")

	// Extract status code (may be negative like -401)
	before, after, ok := strings.Cut(rest, ":")
	if !ok {
		return 0, text
	}

	var code int
	if _, err := fmt.Sscanf(before, "%d", &code); err != nil {
		return 0, text
	}
	msg := strings.TrimSpace(after)
	return code, msg
}

func toHTTPStatus(code int) int {
	abs := code
	if abs < 0 {
		abs = -abs
	}
	if abs >= 100 && abs <= 599 {
		return abs
	}
	return 500
}
