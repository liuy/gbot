package fileread_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/fileread"
)

// fixturePNG is a real PNG header plus padding; the base64 payload exceeds
// truncateDataURIs' 64-char threshold, so a leaked data URI appears in the
// output as "data:image/png;base64,...".
func fixturePNG() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte("A"), 100)...)
}

// writeTextboxImageDocx zips a docx whose body holds one AlternateContent
// text box pair plus a blip image (docPr alt "证书图片") — the shape media
// parsing sees for template-built documents.
func writeTextboxImageDocx(t *testing.T, path string, png []byte) {
	t.Helper()
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
	rels := `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdImg" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/pic.png"/></Relationships>`

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create docx: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	files := []struct{ name, content string }{
		{"[Content_Types].xml", `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`},
		{"word/document.xml", body},
		{"word/_rels/document.xml.rels", rels},
		{"word/media/pic.png", string(png)},
	}
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
}

// TestExecuteDocumentMaterializesImagesViaContext: when the caller provides
// ToolUseContext.DocumentImageDir, the docx parse chain writes embedded
// images to that directory and the text output references them by path. The
// control group (no DocumentImageDir — the LLM's own fileread calls) keeps
// the data URI placeholder, unchanged.
func TestExecuteDocumentMaterializesImagesViaContext(t *testing.T) {
	png := fixturePNG()
	docxPath := filepath.Join(t.TempDir(), "resume.docx")
	writeTextboxImageDocx(t, docxPath, png)

	input, err := json.Marshal(fileread.Input{FilePath: docxPath})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	imgDir := t.TempDir()
	result, err := fileread.Execute(context.Background(), input, &tool.ToolUseContext{
		Ctx:              context.Background(),
		UncappedOutput:   true,
		DocumentImageDir: imgDir,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}

	wantRef := "![证书图片](" + filepath.Join(imgDir, "pic.png") + ")"
	if !strings.Contains(out.Content, wantRef) {
		t.Errorf("content missing image reference %q: %q", wantRef, out.Content)
	}
	written, err := os.ReadFile(filepath.Join(imgDir, "pic.png"))
	if err != nil {
		t.Fatalf("image not materialized via context: %v", err)
	}
	if !bytes.Equal(written, png) {
		t.Errorf("materialized image content mismatch (%d bytes)", len(written))
	}

	// Control group: no DocumentImageDir in the context — the placeholder
	// form is preserved and nothing is written anywhere.
	plain, err := fileread.Execute(context.Background(), input, &tool.ToolUseContext{
		Ctx:            context.Background(),
		UncappedOutput: true,
	})
	if err != nil {
		t.Fatalf("Execute (control): %v", err)
	}
	plainOut, ok := plain.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("control Data type = %T, want fileread.TextOutput", plain.Data)
	}
	if !strings.Contains(plainOut.Content, "data:image/png;base64,...") {
		t.Errorf("control content must keep the truncated data URI placeholder: %q", plainOut.Content)
	}
	if strings.Contains(plainOut.Content, wantRef) {
		t.Errorf("control content must not reference an image dir: %q", plainOut.Content)
	}
}
