package media

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// seedParseCache writes root/parse/{key}.md with the given content, creating
// the dir. Fails the test on any error (setup errors must abort, not pass).
func seedParseCache(t *testing.T, root, key, content string) string {
	t.Helper()
	dir := filepath.Join(root, string(CategoryParse))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir parse dir: %v", err)
	}
	p := filepath.Join(dir, key+".md")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("seed parse cache: %v", err)
	}
	return p
}

// writeStoredDoc writes bytes at root/documents/{key}{ext}, creating the dir,
// and returns the path — the shape media.Save produces for uploads.
func writeStoredDoc(t *testing.T, root, key, ext string, data []byte) string {
	t.Helper()
	dir := filepath.Join(root, string(CategoryDocument))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir documents dir: %v", err)
	}
	p := filepath.Join(dir, key+ext)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write stored doc: %v", err)
	}
	return p
}

func TestParseCacheKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want string
	}{
		{"/home/u/.gbot/cache/documents/0123456789abcdef.pdf", "0123456789abcdef"},
		{"/tmp/x/deadbeefdeadbeef.bin", "deadbeefdeadbeef"},
		{"/tmp/x/notes.txt", ""},             // base name not 16-hex
		{"/tmp/x/0123456789ABCDEF.txt", ""},  // uppercase hex not a media key
		{"/tmp/x/0123456789abcde.txt", ""},   // 15 chars
		{"/tmp/x/0123456789abcdefg.txt", ""}, // 17 chars
		// filepath.Ext keeps only the trailing ext, so a multi-part ext leaves
		// a non-16-hex base. Fine: media.Save names files with a single ext
		// (preserveDocExt), so this shape never occurs for stored documents.
		{"/tmp/x/0123456789abcdef.tar.gz", ""},
	}
	for _, tc := range tests {
		if got := ParseCacheKey(tc.path); got != tc.want {
			t.Errorf("ParseCacheKey(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestParseDocumentAt_CacheHit_SkipsParse seeds the parse cache, then points
// at a stored document that CANNOT parse (null bytes). Only a cache hit can
// return content — a parse attempt would fail — so the returned markdown
// proves the parse chain was never invoked.
func TestParseDocumentAt_CacheHit_SkipsParse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	seedParseCache(t, root, "0123456789abcdef", "# cached markdown")
	doc := writeStoredDoc(t, root, "0123456789abcdef", ".txt", []byte{0x00, 0x01, 0x02, 0x00})

	got, ok := parseDocumentAt(context.Background(), root, doc)
	if !ok {
		t.Fatal("ok = false, want true (cache hit)")
	}
	if got != "# cached markdown" {
		t.Errorf("markdown = %q, want cached content", got)
	}
}

// TestParseDocumentAt_CacheMiss_ParsesAndWrites starts with an empty parse
// cache; a plaintext document parses through fileread and the markdown lands
// in parse/{key}.md for the next call.
func TestParseDocumentAt_CacheMiss_ParsesAndWrites(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := NewAt(root); err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	doc := writeStoredDoc(t, root, "0123456789abcdef", ".txt", []byte("the quick brown fox"))

	got, ok := parseDocumentAt(context.Background(), root, doc)
	if !ok {
		t.Fatal("ok = false, want true (parseable plaintext)")
	}
	if got != "the quick brown fox" {
		t.Errorf("markdown = %q, want file content", got)
	}
	cachePath := filepath.Join(root, string(CategoryParse), "0123456789abcdef.md")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("parse cache not written: %v", err)
	}
	if string(data) != "the quick brown fox" {
		t.Errorf("cache file = %q, want parsed markdown", string(data))
	}
}

// TestParseDocumentAt_Failure_NotCached drives an unparseable document (null
// bytes fail fileread's binary check): ok=false and no cache entry appears,
// so the next turn retries the parse instead of caching the failure.
func TestParseDocumentAt_Failure_NotCached(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := NewAt(root); err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	doc := writeStoredDoc(t, root, "0123456789abcdef", ".bin", []byte{0x00, 0x01, 0x00, 0x02})

	got, ok := parseDocumentAt(context.Background(), root, doc)
	if ok {
		t.Fatalf("ok = true, want false; markdown = %q", got)
	}
	if got != "" {
		t.Errorf("markdown = %q, want empty on failure", got)
	}
	cachePath := filepath.Join(root, string(CategoryParse), "0123456789abcdef.md")
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Errorf("failure must not be cached, stat err = %v", err)
	}
}

// TestParseDocument_HomeRoot pins the exported entry point to the
// ~/.gbot/cache/parse location via a HOME override.
func TestParseDocument_HomeRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".gbot", "cache")
	if _, err := NewAt(root); err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	doc := writeStoredDoc(t, home, "0123456789abcdef", ".txt", []byte("body via home"))

	got, ok := ParseDocument(context.Background(), doc)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got != "body via home" {
		t.Errorf("markdown = %q, want %q", got, "body via home")
	}
	if _, err := os.Stat(filepath.Join(root, string(CategoryParse), "0123456789abcdef.md")); err != nil {
		t.Fatalf("cache not written under HOME root: %v", err)
	}
}

// TestExpandDocumentBlocks_Mixed drives expansion over [text, parseable doc,
// unparseable doc]: text passes through untouched, the parseable doc becomes
// header+markdown, the unparseable one degrades to a header-only block, and
// the ORIGINAL slice still carries document blocks (callers' stored history
// must not be mutated by the LLM-context expansion).
func TestExpandDocumentBlocks_Mixed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".gbot", "cache")
	if _, err := NewAt(root); err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	good := writeStoredDoc(t, home, "0123456789abcdef", ".txt", []byte("parsed body"))
	bad := writeStoredDoc(t, home, "fedcba9876543210", ".bin", []byte{0x00, 0x00})

	blocks := []types.ContentBlock{
		types.NewTextBlock("hello"),
		types.NewDocumentBlock("good.txt", good, "text/plain", 11, 0),
		types.NewDocumentBlock("bad.bin", bad, "application/octet-stream", 2, 0),
	}
	// marshalMessages stamps the breakpoint on the last block; the input
	// mirrors that so the swap must carry it through.
	blocks[2].CacheControl = &types.CacheControlConfig{Type: "ephemeral"}
	got := ExpandDocumentBlocks(context.Background(), blocks)

	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Type != types.ContentTypeText || got[0].Text != "hello" {
		t.Errorf("got[0] = %+v, want untouched text block", got[0])
	}
	if got[1].Type != types.ContentTypeText || got[1].Text != "[Document: good.txt]\nparsed body" {
		t.Errorf("got[1] = %+v, want header+markdown text block", got[1])
	}
	if got[2].Type != types.ContentTypeText || got[2].Text != "[Document: bad.bin]" {
		t.Errorf("got[2] = %+v, want header-only text block", got[2])
	}
	// The last block is exactly where marshalMessages stamps the
	// prompt-cache breakpoint — losing it across the swap would silently
	// disable the prefix cache write.
	if got[2].CacheControl == nil || got[2].CacheControl.Type != "ephemeral" {
		t.Errorf("expanded last block lost cache_control: %+v", got[2].CacheControl)
	}
	if blocks[1].Type != types.ContentTypeDocument || blocks[2].Type != types.ContentTypeDocument {
		t.Errorf("input slice mutated: blocks[1].Type=%q blocks[2].Type=%q, both want document",
			blocks[1].Type, blocks[2].Type)
	}
}

// TestExpandDocumentBlocks_NoDocuments_SameSlice verifies the fast path: a
// slice without document blocks is returned as-is (no copy) and stays valid.
func TestExpandDocumentBlocks_NoDocuments_SameSlice(t *testing.T) {
	t.Parallel()
	blocks := []types.ContentBlock{types.NewTextBlock("a"), types.NewTextBlock("b")}
	got := ExpandDocumentBlocks(context.Background(), blocks)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if !slices.EqualFunc(got, blocks, func(a, b types.ContentBlock) bool { return a.Text == b.Text && a.Type == b.Type }) {
		t.Errorf("got = %+v, want %+v unchanged", got, blocks)
	}
}

// TestExpandDocumentBlocks_EmptyNameFallsBackToBase covers the document block
// whose Name was never populated — the header must still identify the file.
func TestExpandDocumentBlocks_EmptyNameFallsBackToBase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".gbot", "cache")
	if _, err := NewAt(root); err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	bad := writeStoredDoc(t, home, "0123456789abcdef", ".bin", []byte{0x00, 0x00})

	got := ExpandDocumentBlocks(context.Background(), []types.ContentBlock{
		types.NewDocumentBlock("", bad, "", 2, 0),
	})
	if len(got) != 1 || got[0].Type != types.ContentTypeText {
		t.Fatalf("got = %+v, want one text block", got)
	}
	if want := "[Document: 0123456789abcdef.bin]"; got[0].Text != want {
		t.Errorf("text = %q, want %q", got[0].Text, want)
	}
}
