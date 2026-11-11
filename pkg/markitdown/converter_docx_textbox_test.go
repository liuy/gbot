package markitdown

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The AlternateContent fixture mirrors the real-world shape found in
// template-built docx files (e.g. resume templates): every text box is a
// Choice/Fallback pair — Choice carries w:drawing>wps:txbx>w:txbxContent,
// Fallback carries the SAME text inside w:pict>v:textbox>w:txbxContent.
const textboxAlternateContentBody = `<w:p><mc:AlternateContent xmlns:mc="urn:mc">` +
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
	`</w:drawing></w:r></w:p>`

// TestDocxAlternateContentTextboxTextExtracted: text inside w:txbxContent
// must reach the HTML exactly once per text box — the Choice branch is
// collected, the Fallback duplicate is skipped by construction.
func TestDocxAlternateContentTextboxTextExtracted(t *testing.T) {
	got := render(t, textboxAlternateContentBody)
	if n := strings.Count(got, "姓名：测试学生"); n != 1 {
		t.Errorf("textbox text appears %d times, want exactly 1 (Choice kept, Fallback duplicate skipped): %q", n, got)
	}
}

// TestDocxAlternateContentNestedTextboxDeduped: an AlternateContent pair
// nested INSIDE a drawing already being consumed by the image collector must
// also dedup — the main loop's Fallback case cannot reach subtrees consumed
// by extractDrawingContent, so the consumer loop needs its own skip.
func TestDocxAlternateContentNestedTextboxDeduped(t *testing.T) {
	body := `<w:p><w:r><w:drawing>` +
		`<mc:AlternateContent xmlns:mc="urn:mc">` +
		`<mc:Choice Requires="wps"><wps:txbx xmlns:wps="urn:wps"><w:txbxContent>` +
		`<w:p><w:r><w:t>嵌套文本</w:t></w:r></w:p>` +
		`</w:txbxContent></wps:txbx></mc:Choice>` +
		`<mc:Fallback><w:pict><v:textbox xmlns:v="urn:v"><w:txbxContent>` +
		`<w:p><w:r><w:t>嵌套文本</w:t></w:r></w:p>` +
		`</w:txbxContent></v:textbox></w:pict></mc:Fallback>` +
		`</mc:AlternateContent>` +
		`</w:drawing></w:r></w:p>`
	got := render(t, body)
	if n := strings.Count(got, "嵌套文本"); n != 1 {
		t.Errorf("nested textbox text appears %d times, want exactly 1: %q", n, got)
	}
}

// TestDocxTwoTextBoxesInOneDrawing: multiple text box shapes inside a single
// drawing each contribute their text, in document order, joined by <br/>.
func TestDocxTwoTextBoxesInOneDrawing(t *testing.T) {
	body := `<w:p><w:r><w:drawing>` +
		`<wps:txbx xmlns:wps="urn:wps"><w:txbxContent><w:p><w:r><w:t>框一</w:t></w:r></w:p></w:txbxContent></wps:txbx>` +
		`<wps:txbx xmlns:wps="urn:wps"><w:txbxContent><w:p><w:r><w:t>框二</w:t></w:r></w:p></w:txbxContent></wps:txbx>` +
		`</w:drawing></w:r></w:p>`
	got := render(t, body)
	if want := "<p>框一<br/>框二</p>"; !strings.Contains(got, want) {
		t.Errorf("two text boxes must render in document order: want %q in %q", want, got)
	}
}

// TestDocxBlankTextboxParagraphOmitted: empty/whitespace-only text box
// paragraphs must not leak into the output (TestDocxEmptyParagraphOmitted
// semantics apply inside text boxes too).
func TestDocxBlankTextboxParagraphOmitted(t *testing.T) {
	body := `<w:p><w:r><w:drawing><wps:txbx xmlns:wps="urn:wps"><w:txbxContent>` +
		`<w:p></w:p><w:p><w:r><w:t>   </w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>实文</w:t></w:r></w:p>` +
		`</w:txbxContent></wps:txbx></w:drawing></w:r></w:p>`
	got := render(t, body)
	want := "<html><body><p>实文</p>\n</body></html>"
	if got != want {
		t.Errorf("HTML = %q, want %q (blank text box paragraphs omitted)", got, want)
	}
}

// TestDocxBarePictTextboxExtracted: a legacy pict text box WITHOUT a Choice
// twin must still be collected — the Fallback skip must not swallow picts
// that are not part of an AlternateContent pair.
func TestDocxBarePictTextboxExtracted(t *testing.T) {
	body := `<w:p><w:pict><v:shape xmlns:v="urn:v"><v:textbox><w:txbxContent>` +
		`<w:p><w:r><w:t>legacy 文本</w:t></w:r></w:p>` +
		`</w:txbxContent></v:textbox></v:shape></w:pict></w:p>`
	got := render(t, body)
	if n := strings.Count(got, "legacy 文本"); n != 1 {
		t.Errorf("bare pict textbox text appears %d times, want exactly 1: %q", n, got)
	}
}

// fixturePNG is a real PNG header followed by padding — long enough that the
// base64 payload crosses truncateDataURIs' 64-char threshold, so a leaked
// data URI shows up in markdown as "data:image/png;base64,...".
func fixturePNG() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte("A"), 100)...)
}

// writeTextboxImageDocx zips the shared AlternateContent fixture (text box
// pair + one blip image with docPr alt text) into a real docx file.
func writeTextboxImageDocx(t *testing.T, path string, png []byte) {
	t.Helper()
	if err := createZipFile(path, map[string]string{
		"[Content_Types].xml":          `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`,
		"word/document.xml":            string(docXML(textboxAlternateContentBody)),
		"word/_rels/document.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdImg" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/pic.png"/></Relationships>`,
		"word/media/pic.png":           string(png),
	}); err != nil {
		t.Fatal(err)
	}
}

// TestDocxImageDirMaterializesFiles: WithImageDir must turn embedded images
// into real files under the given directory and reference them by absolute
// path — a truncated data URI is a dead reference no LLM can act on.
func TestDocxImageDirMaterializesFiles(t *testing.T) {
	png := fixturePNG()
	docxPath := filepath.Join(t.TempDir(), "fixture.docx")
	writeTextboxImageDocx(t, docxPath, png)
	// A directory that does not exist yet: the converter must create it with
	// 0755 (and the file with 0644), which the mode assertions below pin.
	imgDir := filepath.Join(t.TempDir(), "imgs")

	m := New(WithImageDir(imgDir))
	result, err := m.ConvertFile(docxPath)
	if err != nil {
		t.Fatalf("ConvertFile: %v", err)
	}

	wantRef := "![证书图片](" + filepath.Join(imgDir, "pic.png") + ")"
	if !strings.Contains(result.Markdown, wantRef) {
		t.Errorf("markdown missing file reference %q: %q", wantRef, result.Markdown)
	}
	written, err := os.ReadFile(filepath.Join(imgDir, "pic.png"))
	if err != nil {
		t.Fatalf("image not materialized: %v", err)
	}
	if !bytes.Equal(written, png) {
		t.Errorf("materialized file = %d bytes with wrong content, want the %d source bytes", len(written), len(png))
	}
	if strings.Contains(result.Markdown, "data:image") {
		t.Errorf("markdown must reference the file, not a data URI: %q", result.Markdown)
	}
	if dirInfo, err := os.Stat(imgDir); err != nil {
		t.Fatalf("image dir not created: %v", err)
	} else if dirInfo.Mode().Perm() != 0o755 {
		t.Errorf("image dir mode = %v, want 0755", dirInfo.Mode().Perm())
	}
	if fileInfo, err := os.Stat(filepath.Join(imgDir, "pic.png")); err != nil {
		t.Fatalf("stat materialized file: %v", err)
	} else if fileInfo.Mode().Perm() != 0o644 {
		t.Errorf("materialized file mode = %v, want 0644", fileInfo.Mode().Perm())
	}
}

// TestDocxImageDirSameBasenameCollisions: two relationship targets sharing a
// basename must both materialize, the second disambiguated with a -2 suffix
// (extension preserved), and the markdown must reference each file exactly
// once by its distinct path.
func TestDocxImageDirSameBasenameCollisions(t *testing.T) {
	pngA := fixturePNG()
	pngB := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte("B"), 100)...)
	body := `<w:p><w:r><w:drawing>` +
		`<a:blip xmlns:a="urn:a" r:embed="rId1" xmlns:r="urn:r"/><wp:docPr xmlns:wp="urn:wp" descr="证书图片"/>` +
		`</w:drawing></w:r></w:p>` +
		`<w:p><w:r><w:drawing>` +
		`<a:blip xmlns:a="urn:a" r:embed="rId2" xmlns:r="urn:r"/><wp:docPr xmlns:wp="urn:wp" descr="证书图片"/>` +
		`</w:drawing></w:r></w:p>`
	docxPath := filepath.Join(t.TempDir(), "collide.docx")
	if err := createZipFile(docxPath, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`,
		"word/document.xml":   string(docXML(body)),
		"word/_rels/document.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/pic.png"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/sub/pic.png"/></Relationships>`,
		"word/media/pic.png":     string(pngA),
		"word/media/sub/pic.png": string(pngB),
	}); err != nil {
		t.Fatal(err)
	}
	imgDir := t.TempDir()

	m := New(WithImageDir(imgDir))
	result, err := m.ConvertFile(docxPath)
	if err != nil {
		t.Fatalf("ConvertFile: %v", err)
	}

	refA := "![证书图片](" + filepath.Join(imgDir, "pic.png") + ")"
	refB := "![证书图片](" + filepath.Join(imgDir, "pic-2.png") + ")"
	if strings.Count(result.Markdown, refA) != 1 || strings.Count(result.Markdown, refB) != 1 {
		t.Errorf("markdown must reference pic.png and pic-2.png exactly once each: %q", result.Markdown)
	}
	gotA, err := os.ReadFile(filepath.Join(imgDir, "pic.png"))
	if err != nil {
		t.Fatalf("pic.png not materialized: %v", err)
	}
	gotB, err := os.ReadFile(filepath.Join(imgDir, "pic-2.png"))
	if err != nil {
		t.Fatalf("pic-2.png not materialized: %v", err)
	}
	if !bytes.Equal(gotA, pngA) || !bytes.Equal(gotB, pngB) {
		t.Errorf("collision files swapped or corrupted: len(pic.png)=%d len(pic-2.png)=%d", len(gotA), len(gotB))
	}
}

// TestDocxImageDirWriteFailureFallsBackToDataURI: when the image cannot be
// written (read-only imageDir), the converter falls back to the truncated
// data URI form rather than dropping the reference entirely.
//
// The 0555 chmod only blocks writes for non-root users — run as root the
// second segment would materialize the file instead and the fallback
// assertions would fail spuriously.
func TestDocxImageDirWriteFailureFallsBackToDataURI(t *testing.T) {
	png := fixturePNG()
	docxPath := filepath.Join(t.TempDir(), "fixture.docx")
	writeTextboxImageDocx(t, docxPath, png)

	// First segment: a writable imageDir produces the path reference.
	imgDir := t.TempDir()
	m := New(WithImageDir(imgDir))
	result, err := m.ConvertFile(docxPath)
	if err != nil {
		t.Fatalf("ConvertFile (writable dir): %v", err)
	}
	wantRef := "![证书图片](" + filepath.Join(imgDir, "pic.png") + ")"
	if !strings.Contains(result.Markdown, wantRef) {
		t.Errorf("writable dir segment: markdown missing %q: %q", wantRef, result.Markdown)
	}
	written, err := os.ReadFile(filepath.Join(imgDir, "pic.png"))
	if err != nil {
		t.Fatalf("writable dir segment: image not materialized: %v", err)
	}
	if !bytes.Equal(written, png) {
		t.Errorf("writable dir segment: materialized content mismatch")
	}

	// Second segment: a read-only imageDir falls back to the data URI.
	roDir := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(roDir, 0o755); err != nil {
		t.Fatalf("mkdir ro dir: %v", err)
	}
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatalf("chmod ro dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o755) })
	roM := New(WithImageDir(roDir))
	roResult, err := roM.ConvertFile(docxPath)
	if err != nil {
		t.Fatalf("ConvertFile (read-only dir): %v", err)
	}
	if !strings.Contains(roResult.Markdown, "data:image/png;base64,...") {
		t.Errorf("read-only dir segment: markdown must fall back to the truncated data URI: %q", roResult.Markdown)
	}
	if strings.Contains(roResult.Markdown, roDir) {
		t.Errorf("read-only dir segment: markdown must not reference an unwritable path: %q", roResult.Markdown)
	}
}

// TestDocxTextboxAfterFallbackInsideDrawing: a text box placed after the
// AlternateContent pair inside the SAME drawing must still be collected via
// the textbox path (joined with <br/>), proving the Fallback skip consumed
// exactly the Fallback subtree and nothing more.
func TestDocxTextboxAfterFallbackInsideDrawing(t *testing.T) {
	body := `<w:p><w:r><w:drawing>` +
		`<mc:AlternateContent xmlns:mc="urn:mc">` +
		`<mc:Choice Requires="wps"><wps:txbx xmlns:wps="urn:wps"><w:txbxContent>` +
		`<w:p><w:r><w:t>文甲</w:t></w:r></w:p></w:txbxContent></wps:txbx></mc:Choice>` +
		`<mc:Fallback><w:pict><v:textbox xmlns:v="urn:v"><w:txbxContent>` +
		`<w:p><w:r><w:t>文甲</w:t></w:r></w:p></w:txbxContent></v:textbox></w:pict></mc:Fallback>` +
		`</mc:AlternateContent>` +
		`<wps:txbx xmlns:wps="urn:wps"><w:txbxContent><w:p><w:r><w:t>文乙</w:t></w:r></w:p></w:txbxContent></wps:txbx>` +
		`</w:drawing></w:r></w:p>`
	got := render(t, body)
	if want := "<p>文甲<br/>文乙</p>"; !strings.Contains(got, want) {
		t.Errorf("post-Fallback text box must join the Choice text with <br/>: want %q in %q", want, got)
	}
}

// TestDocxTextboxStrayCharDataIgnored: character data outside a w:t run —
// before the first run and after a closed run — must not leak into the
// collected text box paragraph.
func TestDocxTextboxStrayCharDataIgnored(t *testing.T) {
	body := `<w:p><w:r><w:drawing><w:txbxContent>` +
		`脏前<w:p><w:r><w:t>正文甲</w:t>脏尾</w:r></w:p>` +
		`</w:txbxContent></w:drawing></w:r></w:p>`
	got := render(t, body)
	if !strings.Contains(got, "正文甲") {
		t.Errorf("run text missing: %q", got)
	}
	if strings.Contains(got, "脏前") || strings.Contains(got, "脏尾") {
		t.Errorf("stray character data leaked into the text box paragraph: %q", got)
	}
}

// TestDocxTextboxTabAndBreak: tabs and breaks inside a text box run render
// like their body-level counterparts (\t and <br/>).
func TestDocxTextboxTabAndBreak(t *testing.T) {
	body := `<w:p><w:r><w:drawing><w:txbxContent>` +
		`<w:p><w:r><w:t>甲</w:t><w:tab/><w:t>乙</w:t><w:br/><w:t>丙</w:t></w:r></w:p>` +
		`</w:txbxContent></w:drawing></w:r></w:p>`
	got := render(t, body)
	if want := "<p>甲\t乙<br/>丙</p>"; !strings.Contains(got, want) {
		t.Errorf("textbox tab/break: want %q in %q", want, got)
	}
}

// TestDocxTwoParagraphsInOneTextbox: two non-empty paragraphs inside one
// text box each survive as their own segment — no concatenation, no loss.
func TestDocxTwoParagraphsInOneTextbox(t *testing.T) {
	body := `<w:p><w:r><w:drawing><w:txbxContent>` +
		`<w:p><w:r><w:t>段一</w:t></w:r></w:p><w:p><w:r><w:t>段二</w:t></w:r></w:p>` +
		`</w:txbxContent></w:drawing></w:r></w:p>`
	got := render(t, body)
	if want := "<p>段一<br/>段二</p>"; !strings.Contains(got, want) {
		t.Errorf("two textbox paragraphs: want %q in %q", want, got)
	}
}

// TestDocxBlipInsideTextboxIgnored: a blip nested inside txbxContent is part
// of the text box subtree and must not produce image markup (body-level
// template docx files never anchor images there; collecting it would double
// count shapes rendered as text).
func TestDocxBlipInsideTextboxIgnored(t *testing.T) {
	png := fixturePNG()
	dir := t.TempDir()
	docxPath := filepath.Join(dir, "inner.docx")
	if err := createZipFile(docxPath, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`,
		"word/document.xml": string(docXML(
			`<w:p><w:r><w:drawing><w:txbxContent>` +
				`<w:p><w:r><w:t>注</w:t></w:r></w:p>` +
				`<w:p><w:r><w:drawing>` +
				`<a:blip xmlns:a="urn:a" r:embed="rIdImg" xmlns:r="urn:r"/>` +
				`<wp:docPr xmlns:wp="urn:wp" descr="证书图片"/>` +
				`</w:drawing></w:r></w:p>` +
				`</w:txbxContent></w:drawing></w:r></w:p>`)),
		"word/_rels/document.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdImg" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/pic.png"/></Relationships>`,
		"word/media/pic.png":           string(png),
	}); err != nil {
		t.Fatal(err)
	}
	c := NewDocxConverter(nil)
	result, err := c.Convert(strings.NewReader(string(mustRead(t, docxPath))), StreamInfo{Extension: ".docx"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !strings.Contains(result.Markdown, "注") {
		t.Errorf("textbox text missing: %q", result.Markdown)
	}
	if strings.Contains(result.Markdown, "data:image") || strings.Contains(result.Markdown, "证书图片") {
		t.Errorf("blip inside the text box must not produce image markup: %q", result.Markdown)
	}
}

// TestDocxImageAfterAlternateContentExtracted: the template-resume shape —
// an AlternateContent text box pair followed by a sibling blip inside the
// same drawing. The text box closure must reset the in-textbox state so the
// sibling image is still collected, next to the (deduplicated) text.
func TestDocxImageAfterAlternateContentExtracted(t *testing.T) {
	png := fixturePNG()
	dir := t.TempDir()
	docxPath := filepath.Join(dir, "resume.docx")
	if err := createZipFile(docxPath, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`,
		"word/document.xml": string(docXML(
			`<w:p><w:r><w:drawing>` +
				`<mc:AlternateContent xmlns:mc="urn:mc">` +
				`<mc:Choice Requires="wps"><wps:txbx xmlns:wps="urn:wps"><w:txbxContent>` +
				`<w:p><w:r><w:t>姓名：测试学生</w:t></w:r></w:p></w:txbxContent></wps:txbx></mc:Choice>` +
				`<mc:Fallback><w:pict><v:textbox xmlns:v="urn:v"><w:txbxContent>` +
				`<w:p><w:r><w:t>姓名：测试学生</w:t></w:r></w:p></w:txbxContent></v:textbox></w:pict></mc:Fallback>` +
				`</mc:AlternateContent>` +
				`<a:blip xmlns:a="urn:a" r:embed="rIdImg" xmlns:r="urn:r"/>` +
				`<wp:docPr xmlns:wp="urn:wp" descr="证书图片"/>` +
				`</w:drawing></w:r></w:p>`)),
		"word/_rels/document.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdImg" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/pic.png"/></Relationships>`,
		"word/media/pic.png":           string(png),
	}); err != nil {
		t.Fatal(err)
	}
	c := NewDocxConverter(nil)
	result, err := c.Convert(strings.NewReader(string(mustRead(t, docxPath))), StreamInfo{Extension: ".docx"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if n := strings.Count(result.Markdown, "姓名：测试学生"); n != 1 {
		t.Errorf("textbox text appears %d times, want exactly 1: %q", n, result.Markdown)
	}
	if !strings.Contains(result.Markdown, "![证书图片](data:image/png;base64,...)") {
		t.Errorf("sibling image after the AlternateContent pair must still be extracted: %q", result.Markdown)
	}
}

// TestDocxTruncatedFallbackDoesNotHang: a document truncated mid-Fallback
// terminates parsing cleanly instead of hanging. The output is exactly the
// empty shell — the enclosing paragraph never closes, so nothing renders,
// and no partial Fallback content leaks.
func TestDocxTruncatedFallbackDoesNotHang(t *testing.T) {
	truncated := docXML(
		`<w:p><mc:AlternateContent xmlns:mc="urn:mc">` +
			`<mc:Choice Requires="wps"><w:drawing><wps:txbx xmlns:wps="urn:wps"><w:txbxContent>` +
			`<w:p><w:r><w:t>文甲</w:t></w:r></w:p>` +
			`</w:txbxContent></wps:txbx></w:drawing></mc:Choice>` +
			`<mc:Fallback><w:pict><v:textbox xmlns:v="urn:v"><w:txbxContent><w:p>`)
	c := &DocxConverter{}
	got := c.documentToHTML(truncated, nil, nil, nil, nil, makeEmptyZip(t))
	if want := "<html><body></body></html>"; got != want {
		t.Errorf("truncated document output = %q, want exactly %q (clean termination, nothing rendered)", got, want)
	}
}
