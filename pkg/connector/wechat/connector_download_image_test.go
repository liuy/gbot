package wechat

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/media"
)

// buildTextboxDocx zips a docx with an AlternateContent text box pair and a
// blip image (docPr alt "证书图片") — the template-resume shape the parse
// chain must recover text and materialized images from.
func buildTextboxDocx(t *testing.T, png []byte) []byte {
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

// TestDownloadFile_MaterializesDocxImages: a docx arriving through the wechat
// download path is parsed with the same image-dir rule as media.parseDocumentAt
// — hash-named caches get documents/parse-media/{key}/ materialization, and
// the emitted block references those files instead of data URI placeholders.
func TestDownloadFile_MaterializesDocxImages(t *testing.T) {
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte("A"), 100)...)
	docx := buildTextboxDocx(t, png)
	key := []byte("0123456789abcdef")
	ciphertext := encryptAesEcbForMediaTest(docx, key)
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	c, store := newMediaTestConnector(t)
	block := c.downloadFile(context.Background(), &FileItem{
		FileName: "resume.docx",
		Media:    &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64},
	})
	if block.Type != "text" {
		t.Fatalf("block.Type = %q, want text", block.Type)
	}

	// The stored filename is the content hash; the image dir hangs off it.
	sum := sha256.Sum256(docx)
	key16 := hex.EncodeToString(sum[:8])
	wantRef := "![证书图片](" + filepath.Join(store.RootDir, string(media.CategoryParseMedia), key16, "pic.png") + ")"
	if !strings.Contains(block.Text, wantRef) {
		t.Errorf("block missing materialized image reference %q: %.300q", wantRef, block.Text)
	}
	if n := strings.Count(block.Text, "姓名：测试学生"); n != 1 {
		t.Errorf("textbox text appears %d times, want exactly 1", n)
	}
	if !strings.Contains(block.Text, "[Document: resume.docx saved at ") {
		t.Errorf("block header missing the saved-at path: %.120q", block.Text)
	}
	written, err := os.ReadFile(filepath.Join(store.RootDir, string(media.CategoryParseMedia), key16, "pic.png"))
	if err != nil {
		t.Fatalf("image not materialized: %v", err)
	}
	if !bytes.Equal(written, png) {
		t.Errorf("materialized image content mismatch (%d bytes)", len(written))
	}
}
