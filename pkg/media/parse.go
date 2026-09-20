// Document parse cache + LLM-context expansion for document content blocks.
//
// Moved from pkg/connector/wui/server.go parseDocument so both the live
// send path and history-context replay share one parse chain (fileread.Execute
// → markitdown) and one disk cache.

package media

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/fileread"
	"github.com/liuy/gbot/pkg/types"
)

// parseKeyPattern matches the {sha256-16}{ext} filenames Store.Save produces.
// Media-store files are content-hash immutable, so the derived parse never
// needs invalidation — a cache entry can only ever match the same bytes.
var parseKeyPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// ParseCacheKey extracts the sha256-16 hex key embedded in a stored document
// filename ({sha256-16}{ext}). Returns "" for non-hash-named paths — those
// are parsed fresh every time (no cache, still correct).
func ParseCacheKey(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	if !parseKeyPattern.MatchString(base[:len(base)-len(ext)]) {
		return ""
	}
	return base[:len(base)-len(ext)]
}

// parseCachePath returns {root}/parse/{key}.md for root != ""; "" disables
// caching (used when home resolution fails — parse still works).
func parseCachePath(root, key string) string {
	if root == "" || key == "" {
		return ""
	}
	return filepath.Join(root, string(CategoryParse), key+".md")
}

// cacheRoot resolves ~/.gbot/cache like New(); "" means "no cache" rather
// than an error — a document must still reach the LLM when home is broken.
func cacheRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gbot", "cache")
}

// parseDocumentAt runs the parse chain on path, consulting the disk cache
// under root first. Returns (markdown, true) on success. Failures are never
// cached — the next expansion attempt retries the parse.
func parseDocumentAt(ctx context.Context, root, path string) (string, bool) {
	key := ParseCacheKey(path)
	if cachePath := parseCachePath(root, key); cachePath != "" {
		if data, err := os.ReadFile(cachePath); err == nil {
			return string(data), true
		}
	}

	input, err := json.Marshal(fileread.Input{FilePath: path})
	if err != nil {
		return "", false
	}
	result, err := fileread.Execute(ctx, input, &tool.ToolUseContext{UncappedOutput: true})
	if err != nil || result == nil {
		slog.Warn("media: document parse failed", "file", path, "error", err)
		return "", false
	}
	content := ""
	if out, ok := result.Data.(fileread.TextOutput); ok {
		content = out.Content
	} else if s, ok := result.Data.(string); ok {
		content = s
	}
	if content == "" {
		slog.Warn("media: document parse returned empty content", "file", path)
		return "", false
	}
	if cachePath := parseCachePath(root, key); cachePath != "" {
		// The parse category nests under documents/ — stores created
		// through older paths may not have ensured it.
		_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
		// tmp+rename so a crash mid-write can never leave a truncated cache
		// entry that a later hit would serve as the document body.
		tmp := cachePath + ".tmp." + fmt.Sprintf("%d", os.Getpid())
		if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
			slog.Warn("media: parse cache write failed", "path", cachePath, "error", err)
			return content, true
		}
		if err := os.Rename(tmp, cachePath); err != nil {
			slog.Warn("media: parse cache rename failed", "path", cachePath, "error", err)
			_ = os.Remove(tmp)
		}
	}
	return content, true
}

// ParseDocument returns the markdown for the document at path via the
// fileread chain, served from the ~/.gbot/cache/parse disk cache when the
// filename embeds a sha256-16 key. ok=false means the parse failed — callers
// degrade to a header-only block and the failure is retried next turn.
func ParseDocument(ctx context.Context, path string) (string, bool) {
	return parseDocumentAt(ctx, cacheRoot(), path)
}

// ExpandDocumentBlocks replaces document content blocks with text blocks
// shaped "[Document: <name>]\n<markdown>" (header-only "[Document: <name>]"
// when the parse fails), so providers with no document-block schema still
// receive the content. The input slice is never mutated; the same slice is
// returned when no document block is present.
func ExpandDocumentBlocks(ctx context.Context, blocks []types.ContentBlock) []types.ContentBlock {
	hasDoc := false
	for i := range blocks {
		if blocks[i].Type == types.ContentTypeDocument {
			hasDoc = true
			break
		}
	}
	if !hasDoc {
		return blocks
	}
	root := cacheRoot()
	out := make([]types.ContentBlock, len(blocks))
	copy(out, blocks)
	for i := range out {
		if out[i].Type != types.ContentTypeDocument {
			continue
		}
		name := out[i].Name
		if name == "" {
			name = filepath.Base(out[i].Path)
		}
		if md, ok := parseDocumentAt(ctx, root, out[i].Path); ok {
			out[i] = types.NewTextBlock(fmt.Sprintf("[Document: %s]\n%s", name, md))
		} else {
			out[i] = types.NewTextBlock(fmt.Sprintf("[Document: %s]", name))
		}
		// marshalMessages stamps cache_control on the LAST block — a
		// document sitting there must keep the breakpoint across the
		// swap, or the whole prefix loses its prompt-cache write.
		out[i].CacheControl = blocks[i].CacheControl
	}
	return out
}
