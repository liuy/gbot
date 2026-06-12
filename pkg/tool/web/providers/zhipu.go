package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

)

var (
	zhipuMCPURL = "https://open.bigmodel.cn/api/mcp/web_search_prime/mcp"
)

const (
	zhipuToolName    = "web_search_prime"
	zhipuTimeout     = 30 * time.Second
	zhipuMaxResponse = 10 * 1024 * 1024 // 10MB
)

// ZhipuProvider uses the same API key as the zhipu PaaS LLM endpoint.
type ZhipuProvider struct {
	Client *http.Client
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

type zhipuSearchResult struct {
	Title   string `json:"title"`
	Link    string `json:"link"`
	Content string `json:"content"`
}

func (z *ZhipuProvider) Search(ctx context.Context, params SearchParams) (*SearchResponse, error) {
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
		return nil, &SearchProviderError{
			Provider: "zhipu",
			Message:  fmt.Sprintf("request failed: %v", err),
			Status:   0,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, &SearchProviderError{
			Provider: "zhipu",
			Message:  fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody)),
			Status:   resp.StatusCode,
		}
	}

	return parseZhipuResponse(resp.Body, params.Limit)
}

// parseZhipuResponse parses SSE JSON-RPC stream.
// Response body lines look like:
//
//	id:1
//	event:message
//	data:{"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"..."}]}}
func parseZhipuResponse(body io.Reader, limit int) (*SearchResponse, error) {
	var lastData string
	scanner := bufio.NewScanner(io.LimitReader(body, zhipuMaxResponse))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			lastData = after
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("zhipu: read response: %w", err)
	}
	if lastData == "" {
		return nil, &SearchProviderError{
			Provider: "zhipu",
			Message:  "empty SSE response",
			Status:   500,
		}
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal([]byte(lastData), &rpcResp); err != nil {
		return nil, fmt.Errorf("zhipu: parse JSON-RPC: %w", err)
	}

	if rpcResp.Error != nil {
		status := rpcResp.Error.Code
		msg := rpcResp.Error.Message
		if code, m := extractMCPError(msg); code != 0 {
			status = code
			msg = m
		}
		return nil, &SearchProviderError{
			Provider: "zhipu",
			Message:  msg,
			Status:   toHTTPStatus(status),
		}
	}

	if rpcResp.Result == nil {
		return nil, &SearchProviderError{
			Provider: "zhipu",
			Message:  "missing result in JSON-RPC response",
			Status:   500,
		}
	}

	if rpcResp.Result.IsError && len(rpcResp.Result.Content) > 0 {
		errText := rpcResp.Result.Content[0].Text
		if code, msg := extractMCPError(errText); code != 0 {
			return nil, &SearchProviderError{
				Provider: "zhipu",
				Message:  msg,
				Status:   toHTTPStatus(code),
			}
		}
		return nil, &SearchProviderError{
			Provider: "zhipu",
			Message:  errText,
			Status:   500,
		}
	}

	var allSources []SearchSource
	for _, c := range rpcResp.Result.Content {
		if len(allSources) >= limit {
			break
		}
		if c.Type != "text" || c.Text == "" {
			continue
		}

		// Zhipu wraps the result array in double-encoded JSON: text → JSON string → JSON array
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
			allSources = append(allSources, SearchSource{
				Title:   title,
				URL:     r.Link,
				Snippet: r.Content,
			})
			if len(allSources) >= limit {
				break
			}
		}
	}

	return &SearchResponse{
		Provider: "zhipu",
		Sources:  allSources,
	}, nil
}

// extractMCPError parses "MCP error -NNN: message" format.
func extractMCPError(text string) (int, string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "MCP error ") {
		return 0, ""
	}
	rest := strings.TrimPrefix(text, "MCP error ")

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
