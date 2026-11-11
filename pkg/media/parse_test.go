package media

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// seedParseCache writes root/parse/v2/{key}.md with the given content,
// creating the dir. Fails the test on any error (setup errors must abort,
// not pass).
func seedParseCache(t *testing.T, root, key, content string) string {
	t.Helper()
	dir := filepath.Join(root, string(CategoryParse), "v2")
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
	cachePath := filepath.Join(root, string(CategoryParse), "v2", "0123456789abcdef.md")
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
	cachePath := filepath.Join(root, string(CategoryParse), "v2", "0123456789abcdef.md")
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
	if _, err := os.Stat(filepath.Join(root, string(CategoryParse), "v2", "0123456789abcdef.md")); err != nil {
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
	if got[1].Type != types.ContentTypeText || got[1].Text != "[Document: good.txt saved at "+good+"]\nparsed body" {
		t.Errorf("got[1] = %+v, want header+markdown text block", got[1])
	}
	if got[2].Type != types.ContentTypeText || got[2].Text != "[Document: bad.bin saved at "+bad+"]" {
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
	if want := "[Document: 0123456789abcdef.bin saved at " + bad + "]"; got[0].Text != want {
		t.Errorf("text = %q, want %q", got[0].Text, want)
	}
}

// ---------------------------------------------------------------------------
// Parse-chain failure paths, cache modes, document expansion
// ---------------------------------------------------------------------------

func TestParseDocumentAt_CacheWriteFailureStillReturnsContent(t *testing.T) {
	logs := captureMediaLogs(t)
	root := t.TempDir()
	parseDir := filepath.Join(root, string(CategoryParse))
	if err := os.MkdirAll(parseDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	doc := filepath.Join(root, "abcd0123efbe4567.txt")
	if err := os.WriteFile(doc, []byte("document body"), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	if err := os.Chmod(parseDir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parseDir, 0o755) })
	got, ok := parseDocumentAt(context.Background(), root, doc)
	if !ok || got != "document body" {
		t.Fatalf("parseDocumentAt = (%q,%v), want the parsed content", got, ok)
	}
	recs := logs.find("media: parse cache write failed")
	if len(recs) != 1 {
		t.Fatalf("cache write failure logged %d times, want 1", len(recs))
	}
	if recs[0].attrs["path"] != parseCachePath(root, "abcd0123efbe4567") {
		t.Errorf("path attr = %v, want the cache path", recs[0].attrs["path"])
	}
}

func TestParseDocumentAt_RenameFailureIsLogged(t *testing.T) {
	logs := captureMediaLogs(t)
	root := t.TempDir()
	parseDir := filepath.Join(root, string(CategoryParse))
	if err := os.MkdirAll(parseDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	doc := filepath.Join(root, "ffff0000aaaa1111.txt")
	if err := os.WriteFile(doc, []byte("rename me"), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	cachePath := parseCachePath(root, "ffff0000aaaa1111")
	if err := os.MkdirAll(cachePath, 0o755); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	got, ok := parseDocumentAt(context.Background(), root, doc)
	if !ok || got != "rename me" {
		t.Fatalf("parseDocumentAt = (%q,%v), want the parsed content", got, ok)
	}
	recs := logs.find("media: parse cache rename failed")
	if len(recs) != 1 {
		t.Fatalf("rename failure logged %d times, want 1", len(recs))
	}
	tmpLeft, _ := filepath.Glob(cachePath + ".tmp.*")
	if len(tmpLeft) != 0 {
		t.Errorf("temp cache files left behind: %v", tmpLeft)
	}
}

func TestParseDocumentAt_CacheFileModes(t *testing.T) {
	root := t.TempDir()
	doc := filepath.Join(root, "1234567890abcdef.txt")
	if err := os.WriteFile(doc, []byte("mode probe"), 0o600); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	if _, ok := parseDocumentAt(context.Background(), root, doc); !ok {
		t.Fatal("parseDocumentAt failed")
	}
	cachePath := parseCachePath(root, "1234567890abcdef")
	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("cache file mode = %v, want 0644", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Join(root, string(CategoryParse)))
	if err != nil {
		t.Fatalf("parse dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o755 {
		t.Errorf("parse dir mode = %v, want 0755", dirInfo.Mode().Perm())
	}
}

func TestParseDocumentAt_EmptyContentIsNotCached(t *testing.T) {
	logs := captureMediaLogs(t)
	root := t.TempDir()
	doc := filepath.Join(root, "0000111122223333.txt")
	if err := os.WriteFile(doc, nil, 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	got, ok := parseDocumentAt(context.Background(), root, doc)
	if ok {
		t.Errorf("parseDocumentAt on an empty document = (%q,true), want ok=false", got)
	}
	if got := logs.find("media: document parse returned empty content"); len(got) != 1 {
		t.Errorf("empty-content logged %d times, want 1", len(got))
	}
	if _, err := os.Stat(parseCachePath(root, "0000111122223333")); err == nil {
		t.Error("an empty parse result was cached")
	}
}

func TestExpandDocumentBlocks_PreservesCacheControlAndHeader(t *testing.T) {
	// cacheRoot() reads $HOME — pin it or the parse cache lands in the real
	// ~/.gbot/cache (root-run CI would bypass 0555 walls and still hit it).
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	doc := filepath.Join(root, "deadbeefcafebabe.txt")
	if err := os.WriteFile(doc, []byte("expanded body"), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	blocks := []types.ContentBlock{
		types.NewTextBlock("lead"),
		{Type: types.ContentTypeDocument, Path: doc},
	}
	blocks[1].CacheControl = &types.CacheControlConfig{Type: "ephemeral"}
	got := ExpandDocumentBlocks(context.Background(), blocks)
	if len(got) != 2 {
		t.Fatalf("blocks = %d, want 2", len(got))
	}
	if got[0].Text != "lead" {
		t.Errorf("first block = %q, want it untouched", got[0].Text)
	}
	if got[1].Type != types.ContentTypeText {
		t.Fatalf("document block type = %q, want text", got[1].Type)
	}
	if got[1].Text != "[Document: deadbeefcafebabe.txt saved at "+doc+"]\nexpanded body" {
		t.Errorf("expanded block = %q", got[1].Text)
	}
	if got[1].CacheControl == nil {
		t.Error("the cache_control breakpoint was lost across the document swap")
	}
	if blocks[1].Type != types.ContentTypeDocument {
		t.Error("the input slice was mutated")
	}
}

// TestExpandDocumentBlocks_EmptyPathKeepsLegacyHeader pins the guard branch:
// a document block with no Path has no location to tell the model about, so
// the header must keep the legacy "[Document: <name>]" shape instead of the
// saved-at form with an empty path.
func TestExpandDocumentBlocks_EmptyPathKeepsLegacyHeader(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".gbot", "cache")
	if _, err := NewAt(root); err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	parseable := writeStoredDoc(t, home, "1111222233334444", ".txt", []byte("parsed body"))

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeDocument, Name: "x.txt"},
		types.NewDocumentBlock("y.txt", parseable, "text/plain", 11, 0),
	}
	got := ExpandDocumentBlocks(context.Background(), blocks)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Type != types.ContentTypeText {
		t.Fatalf("got[0].Type = %q, want text", got[0].Type)
	}
	if want := "[Document: x.txt]"; got[0].Text != want {
		t.Errorf("empty-path header = %q, want %q", got[0].Text, want)
	}
	if want := "[Document: y.txt saved at " + parseable + "]\nparsed body"; got[1].Text != want {
		t.Errorf("saved-at header = %q, want %q", got[1].Text, want)
	}
}

// fixturePNG is a real PNG header plus padding; the base64 payload exceeds
// truncateDataURIs' 64-char threshold, so a leaked data URI would surface in
// the expanded text as "data:image/png;base64,...".
func fixturePNG() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte("A"), 100)...)
}

// writeTextboxDocx builds the AlternateContent fixture docx in memory: one
// Choice/Fallback text box pair ("姓名：测试学生") plus an independent blip
// drawing (docPr alt "证书图片") targeting media/pic.png.
func writeTextboxDocx(t *testing.T) []byte {
	t.Helper()
	png := fixturePNG()
	body := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		`<w:p><mc:AlternateContent xmlns:mc="urn:mc">` +
		`<mc:Choice Requires="wps"><w:drawing><wps:txbx xmlns:wps="urn:wps"><w:txbxContent>` +
		`<w:p><w:r><w:t>姓名：测试学生</w:t></w:r></w:p>` +
		`</w:txbxContent></wps:txbx></w:drawing></mc:Choice>` +
		`<mc:Fallback><w:pict><v:shape xmlns:v="urn:v"><v:textbox><w:txbxContent>` +
		`<w:p><w:r><w:t>姓名：测试学生</w:t></w:r></w:p>` +
		`</w:txbxContent></v:textbox></v:shape></w:pict></mc:Fallback>` +
		`</mc:AlternateContent></w:p>` +
		`<w:p><w:r><w:drawing>` +
		`<a:blip xmlns:a="urn:a" r:embed="rIdImg" xmlns:r="urn:r"/>` +
		`<wp:docPr xmlns:wp="urn:wp" descr="证书图片"/>` +
		`</w:drawing></w:r></w:p>` +
		`</w:body></w:document>`
	files := []struct{ name, content string }{
		{"[Content_Types].xml", `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`},
		{"word/document.xml", body},
		{"word/_rels/document.xml.rels", `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdImg" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/pic.png"/></Relationships>`},
		{"word/media/pic.png", string(png)},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, fi := range files {
		w, err := zw.Create(fi.name)
		if err != nil {
			t.Fatalf("zip create %s: %v", fi.name, err)
		}
		if _, err := w.Write([]byte(fi.content)); err != nil {
			t.Fatalf("zip write %s: %v", fi.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// TestParseDocumentAt_TextboxDocxEndToEnd drives the full production chain —
// ExpandDocumentBlocks → fileread → markitdown → v2 cache + image
// materialization — through three lifecycle states:
//
//  1. cold start: no cache, the docx parses, text box text and file-path
//     image references reach the text block, image bytes land on disk;
//  2. hot path: with the source file corrupted, a second expansion serves
//     the identical cached markdown without touching the source;
//  3. recovery: a poisoned v1 cache entry is bypassed by the v2 miss.
func TestParseDocumentAt_TextboxDocxEndToEnd(t *testing.T) {
	const key = "0123456789abcdef"
	docxBytes := writeTextboxDocx(t)
	png := fixturePNG()

	// --- cold start ---
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".gbot", "cache")
	if _, err := NewAt(root); err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	docPath := writeStoredDoc(t, home, key, ".docx", docxBytes)

	newBlock := func() types.ContentBlock {
		return types.NewDocumentBlock("简历.docx", docPath, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", int64(len(docxBytes)), 0)
	}
	got := ExpandDocumentBlocks(context.Background(), []types.ContentBlock{newBlock()})
	if len(got) != 1 {
		t.Fatalf("blocks = %d, want 1", len(got))
	}
	if got[0].Type != types.ContentTypeText {
		t.Fatalf("block type = %q, want text", got[0].Type)
	}
	text := got[0].Text
	if !strings.HasPrefix(text, "[Document: 简历.docx saved at "+docPath+"]\n") {
		t.Errorf("header = %.80q, want saved-at header prefix", text)
	}
	if n := strings.Count(text, "姓名：测试学生"); n != 1 {
		t.Errorf("text box text appears %d times, want exactly 1", n)
	}
	imgPath := filepath.Join(root, string(CategoryParseMedia), key, "pic.png")
	if wantRef := "![证书图片](" + imgPath + ")"; !strings.Contains(text, wantRef) {
		t.Errorf("text missing image reference %q", wantRef)
	}
	if strings.Contains(text, "data:image") {
		t.Errorf("expanded text still carries a data URI placeholder")
	}
	materialized, err := os.ReadFile(imgPath)
	if err != nil {
		t.Fatalf("image not materialized at %s: %v", imgPath, err)
	}
	if !bytes.Equal(materialized, png) {
		t.Errorf("materialized image bytes mismatch (%d bytes)", len(materialized))
	}

	// --- hot path: cache hit must not touch the (now corrupted) source ---
	if err := os.WriteFile(docPath, []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatalf("corrupt source: %v", err)
	}
	got2 := ExpandDocumentBlocks(context.Background(), []types.ContentBlock{newBlock()})
	if got2[0].Text != text {
		t.Errorf("second expansion differs from the first (%d vs %d chars) — the cache was not served",
			len(got2[0].Text), len(text))
	}

	// --- recovery: a v1 poison entry must be bypassed by the v2 miss ---
	home2 := t.TempDir()
	t.Setenv("HOME", home2)
	root2 := filepath.Join(home2, ".gbot", "cache")
	if _, err := NewAt(root2); err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	v1Dir := filepath.Join(root2, string(CategoryParse))
	if err := os.MkdirAll(v1Dir, 0o755); err != nil {
		t.Fatalf("mkdir v1 dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(v1Dir, key+".md"), []byte("STALE-V1"), 0o644); err != nil {
		t.Fatalf("seed stale v1 cache: %v", err)
	}
	docPath2 := writeStoredDoc(t, home2, key, ".docx", docxBytes)
	got3 := ExpandDocumentBlocks(context.Background(), []types.ContentBlock{
		types.NewDocumentBlock("简历.docx", docPath2, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", int64(len(docxBytes)), 0),
	})
	text3 := got3[0].Text
	if strings.Contains(text3, "STALE-V1") {
		t.Errorf("the poisoned v1 cache entry was served to the model")
	}
	if !strings.Contains(text3, "姓名：测试学生") {
		t.Errorf("v2 re-parse lost the text box text: %.120q", text3)
	}
	if _, err := os.Stat(filepath.Join(root2, string(CategoryParse), "v2", key+".md")); err != nil {
		t.Errorf("fresh parse was not written to the v2 cache location: %v", err)
	}
	if dirInfo, err := os.Stat(filepath.Join(root, string(CategoryParseMedia), key)); err != nil {
		t.Errorf("parse-media dir missing: %v", err)
	} else if dirInfo.Mode().Perm() != 0o755 {
		t.Errorf("parse-media dir mode = %v, want 0755", dirInfo.Mode().Perm())
	}
}

// TestParseDocumentAt_LargeDocumentUncapped: a document whose text exceeds
// MaxFileReadTokens (25000 tokens) still parses — parseDocumentAt runs the
// chain with UncappedOutput, because the expansion exists precisely to serve
// documents too large for the LLM-facing fileread cap.
func TestParseDocumentAt_LargeDocumentUncapped(t *testing.T) {
	root := t.TempDir()
	if _, err := NewAt(root); err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	body := strings.Repeat("a", 400*1024) // ~45K estimated tokens, above MaxFileReadTokens
	doc := writeStoredDoc(t, root, "0123456789abcdef", ".txt", []byte(body))

	got, ok := parseDocumentAt(context.Background(), root, doc)
	if !ok {
		t.Fatal("ok = false — the large document was rejected instead of parsed uncapped")
	}
	if got != body {
		t.Errorf("content length = %d, want the full %d bytes", len(got), len(body))
	}
}
