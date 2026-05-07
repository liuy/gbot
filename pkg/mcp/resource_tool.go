// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
//
// This file: MCP resource tools — ListMcpResources, ReadMcpResource.
// Source: src/tools/ListMcpResourcesTool/ListMcpResourcesTool.ts (123 lines)
// Source: src/tools/ReadMcpResourceTool/ReadMcpResourceTool.ts (158 lines)
// Source: src/utils/mcpOutputStorage.ts (persistBinaryContent, extensionForMimeType, getBinaryBlobSavedMessage)
package mcp

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// ListMcpResources — Source: ListMcpResourcesTool.ts:66-101
// ---------------------------------------------------------------------------

// ListMcpResources lists available resources from connected MCP servers.
// Source: ListMcpResourcesTool.ts:66-101
//
// If serverName is non-empty, returns resources from that specific server only.
// If empty, returns aggregated resources from all connected servers.
// Errors from individual servers are isolated — one failure doesn't block others.
func ListMcpResources(ctx context.Context, reg *Registry, serverName string) ([]ServerResource, error) {
	if serverName != "" {
		conn, ok := reg.GetConnection(serverName)
		if !ok {
			// Source: ListMcpResourcesTool.ts:74-76 — include available server names for self-correction
			available := availableServerNames(reg)
			return nil, fmt.Errorf("server %q not found. Available servers: %s", serverName, strings.Join(available, ", "))
		}
		// Source: ListMcpResourcesTool.ts:86 — client.type !== 'connected' → return []
		cs, ok := conn.(*ConnectedServer)
		if !ok {
			return []ServerResource{}, nil
		}
		resources, err := FetchResourcesForServer(ctx, cs, reg.resourceCache)
		if err != nil {
			// Error isolation — log and return empty, don't propagate
			slog.Warn("mcp: failed to fetch resources", "server", serverName, "error", err)
			return []ServerResource{}, nil
		}
		return resources, nil
	}

	// No filter — return all aggregated resources (already warm from startup ConnectAll)
	return reg.GetResources(), nil
}

// availableServerNames returns the names of all configured servers.
func availableServerNames(reg *Registry) []string {
	configs := reg.GetConfigs()
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	return names
}

// ---------------------------------------------------------------------------
// ReadMcpResource — Source: ReadMcpResourceTool.ts:75-144
// ---------------------------------------------------------------------------

// ResourceContent represents a single content block from reading an MCP resource.
// Source: ReadMcpResourceTool.ts:31-44 — output schema
type ResourceContent struct {
	URI         string `json:"uri"`
	MimeType    string `json:"mimeType,omitempty"`
	Text        string `json:"text,omitempty"`
	BlobSavedTo string `json:"blobSavedTo,omitempty"`
}

// ReadMcpResource reads a specific resource from an MCP server by URI.
// Source: ReadMcpResourceTool.ts:75-144
//
// Returns content blocks which may be text, binary (persisted to disk), or minimal (URI + mimeType).
// Binary blobs are persisted to temp files with MIME-derived extensions.
func ReadMcpResource(ctx context.Context, reg *Registry, serverName, uri string) ([]ResourceContent, error) {
	conn, ok := reg.GetConnection(serverName)
	if !ok {
		available := availableServerNames(reg)
		return nil, fmt.Errorf("server %q not found. Available servers: %s", serverName, strings.Join(available, ", "))
	}

	cs, ok := conn.(*ConnectedServer)
	if !ok {
		return nil, fmt.Errorf("server %q is not connected", serverName)
	}

	// Source: ReadMcpResourceTool.ts:90-92 — fast-fail before RPC
	if cs.Capabilities == nil || cs.Capabilities.Resources == nil {
		return nil, fmt.Errorf("server %q does not support resources", serverName)
	}

	// Source: ReadMcpResourceTool.ts:95-101 — resources/read RPC
	result, err := cs.Session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return nil, fmt.Errorf("mcp: read resource %q from %q: %w", uri, serverName, err)
	}

	if result == nil || len(result.Contents) == 0 {
		return []ResourceContent{}, nil
	}

	// Source: ReadMcpResourceTool.ts:106-139 — process each content block
	contents := make([]ResourceContent, 0, len(result.Contents))
	for i, c := range result.Contents {
		// Go struct 判别规则:
		// TS 用 'text' in c (属性存在性), Go struct 字段始终存在, 用值检查
		if c.Text != "" {
			contents = append(contents, ResourceContent{
				URI:      c.URI,
				MimeType: c.MIMEType,
				Text:     c.Text,
			})
		} else if len(c.Blob) > 0 {
			// Blob is []byte — Go SDK already decoded base64.
			// TS needs Buffer.from(c.blob, 'base64') because TS SDK returns string.
			persistID := fmt.Sprintf("mcp-resource-%d-%d-%s", time.Now().UnixMilli(), i, randomBase36(6))
			filepath, size, persistErr := persistBinaryContent(c.Blob, c.MIMEType, persistID)
			if persistErr != nil {
				// Source: ReadMcpResourceTool.ts:120-125 — error fallback: text field with error message
				contents = append(contents, ResourceContent{
					URI:      c.URI,
					MimeType: c.MIMEType,
					Text:     fmt.Sprintf("Binary content could not be saved to disk: %s", persistErr),
				})
			} else {
				sourceDesc := fmt.Sprintf("[Resource from %s at %s] ", serverName, c.URI)
				contents = append(contents, ResourceContent{
					URI:         c.URI,
					MimeType:    c.MIMEType,
					Text:        getBinaryBlobSavedMessage(filepath, c.MIMEType, size, sourceDesc),
					BlobSavedTo: filepath,
				})
			}
		} else {
			// Neither text nor blob — minimal response
			contents = append(contents, ResourceContent{
				URI:      c.URI,
				MimeType: c.MIMEType,
			})
		}
	}

	return contents, nil
}

// ---------------------------------------------------------------------------
// getBinaryBlobSavedMessage — Source: mcpOutputStorage.ts:181-189
// ---------------------------------------------------------------------------

// getBinaryBlobSavedMessage builds a message telling the model where binary content was saved.
// Source: mcpOutputStorage.ts:181-189
func getBinaryBlobSavedMessage(filepath, mimeType string, size int, sourceDescription string) string {
	mt := mimeType
	if mt == "" {
		mt = "unknown type"
	}
	return fmt.Sprintf("%sBinary content (%s, %s) saved to %s", sourceDescription, mt, formatFileSize(size), filepath)
}

// ---------------------------------------------------------------------------
// persistBinaryContent — Source: mcpOutputStorage.ts:148-174
// ---------------------------------------------------------------------------

// persistBinaryContent writes raw binary bytes to a temp file with a MIME-derived extension.
// Source: mcpOutputStorage.ts:148-174
//
// Unlike persistToolResult (which stringifies), this writes bytes as-is so
// the resulting file can be opened with native tools (PDFs, images, xlsx, etc.).
func persistBinaryContent(data []byte, mimeType, persistID string) (string, int, error) {
	ext := extensionForMimeType(mimeType)
	filename := persistID + "." + ext
	fp := filepath.Join(os.TempDir(), filename)

	if err := os.WriteFile(fp, data, 0600); err != nil {
		slog.Error("mcp: failed to persist binary content", "error", err)
		return "", 0, fmt.Errorf("mcp: failed to persist binary content: %w", err)
	}

	return fp, len(data), nil
}

// ---------------------------------------------------------------------------
// extensionForMimeType — Source: mcpOutputStorage.ts:66-118
// ---------------------------------------------------------------------------

// extensionForMimeType maps a MIME type to a file extension.
// Source: mcpOutputStorage.ts:66-118 — 22 entries
//
// Strips charset parameters before matching. Unknown types return "bin".
func extensionForMimeType(mimeType string) string {
	if mimeType == "" {
		return "bin"
	}
	// Strip charset/boundary parameter
	mt := strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0])
	mt = strings.ToLower(mt)

	switch mt {
	case "application/pdf":
		return "pdf"
	case "application/json":
		return "json"
	case "text/csv":
		return "csv"
	case "text/plain":
		return "txt"
	case "text/html":
		return "html"
	case "text/markdown":
		return "md"
	case "application/zip":
		return "zip"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return "docx"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return "xlsx"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return "pptx"
	case "application/msword":
		return "doc"
	case "application/vnd.ms-excel":
		return "xls"
	case "audio/mpeg":
		return "mp3"
	case "audio/wav":
		return "wav"
	case "audio/ogg":
		return "ogg"
	case "video/mp4":
		return "mp4"
	case "video/webm":
		return "webm"
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/svg+xml":
		return "svg"
	default:
		return "bin"
	}
}

// ---------------------------------------------------------------------------
// formatFileSize — Source: format.ts
// ---------------------------------------------------------------------------

// formatFileSize returns a human-readable file size string.
// Source: format.ts
//
// Format: "N bytes" (<1KB), "N.NKB" (<1MB), "N.NMB" (<1GB), "N.NGB".
// Trailing ".0" is stripped.
func formatFileSize(bytes int) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case bytes < kb:
		return fmt.Sprintf("%d bytes", bytes)
	case bytes < mb:
		return stripTrailingZero(fmt.Sprintf("%.1f", float64(bytes)/float64(kb))) + "KB"
	case bytes < gb:
		return stripTrailingZero(fmt.Sprintf("%.1f", float64(bytes)/float64(mb))) + "MB"
	default:
		return stripTrailingZero(fmt.Sprintf("%.1f", float64(bytes)/float64(gb))) + "GB"
	}
}

// stripTrailingZero removes trailing ".0" from formatted size strings.
func stripTrailingZero(s string) string {
	return strings.TrimSuffix(s, ".0")
}

// ---------------------------------------------------------------------------
// randomBase36 — Source: ReadMcpResourceTool.ts:114 — Math.random().toString(36).slice(2, 8)
// ---------------------------------------------------------------------------

// randomBase36 generates a random alphanumeric string of the given length.
// Source: ReadMcpResourceTool.ts:114 — Math.random().toString(36).slice(2, 8)
const base36Chars = "abcdefghijklmnopqrstuvwxyz0123456789"

func randomBase36(length int) string {
	result := make([]byte, length)
	max := big.NewInt(int64(len(base36Chars)))
	for i := range result {
		n, _ := rand.Int(rand.Reader, max)
		result[i] = base36Chars[n.Int64()]
	}
	return string(result)
}
