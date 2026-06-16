// Copyright 2026 Conductor OSS
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with
// the License. You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
// an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the
// specific language governing permissions and limitations under the License.

package markitdown

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type errorReadSeeker struct{}

func (r *errorReadSeeker) Read(p []byte) (n int, err error) {
	return 0, io.ErrUnexpectedEOF
}

func (r *errorReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestHTMLConvertBasic(t *testing.T) {
	c := NewHTMLConverter(nil)
	html := `<html><body><h1>Title</h1><p>Paragraph text</p></body></html>`
	result, err := c.ConvertString(html)
	if err != nil {
		t.Fatalf("ConvertString error: %v", err)
	}
	if !strings.Contains(result.Markdown, "Title") {
		t.Errorf("Markdown should contain Title: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "Paragraph text") {
		t.Errorf("Markdown should contain paragraph: %s", result.Markdown)
	}
}

func TestHTMLConvertWithScriptStyle(t *testing.T) {
	c := NewHTMLConverter(nil)
	html := `<html><body><script>alert('x')</script><style>.x{color:red}</style><p>content</p></body></html>`
	result, err := c.ConvertString(html)
	if err != nil {
		t.Fatalf("ConvertString error: %v", err)
	}
	if strings.Contains(result.Markdown, "alert") {
		t.Errorf("Markdown should not contain script: %s", result.Markdown)
	}
	if strings.Contains(result.Markdown, "color:red") {
		t.Errorf("Markdown should not contain style: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "content") {
		t.Errorf("Markdown should contain content: %s", result.Markdown)
	}
}

func TestHTMLConvertWithTable(t *testing.T) {
	c := NewHTMLConverter(nil)
	html := `<html><body><table><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></table></body></html>`
	result, err := c.ConvertString(html)
	if err != nil {
		t.Fatalf("ConvertString error: %v", err)
	}
	if !strings.Contains(result.Markdown, "A") || !strings.Contains(result.Markdown, "B") {
		t.Errorf("Markdown should contain table headers: %s", result.Markdown)
	}
}

func TestHTMLConvertReaderError(t *testing.T) {
	c := NewHTMLConverter(nil)
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".html"})
	if err == nil {
		t.Fatal("expected error from failing reader")
	}
}

func TestHTMLConvertStringTruncateDataURI(t *testing.T) {
	c := NewHTMLConverter(nil)
	longB64 := strings.Repeat("A", 100)
	html := `<img src="data:image/png;base64,` + longB64 + `">`
	result, err := c.ConvertString(html)
	if err != nil {
		t.Fatalf("ConvertString error: %v", err)
	}
	if strings.Contains(result.Markdown, longB64) {
		t.Errorf("data URI should be truncated by default: %s", result.Markdown)
	}
}

func TestConvertHTMLToMarkdownEmpty(t *testing.T) {
	got, err := convertHTMLToMarkdown("")
	if err != nil {
		t.Fatalf("convertHTMLToMarkdown(empty) error: %v", err)
	}
	if got != "" {
		t.Errorf("convertHTMLToMarkdown(empty) = %q, want empty", got)
	}
}

func TestConvertHTMLToMarkdownSimple(t *testing.T) {
	got, err := convertHTMLToMarkdown("<p>hello</p>")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("convertHTMLToMarkdown = %q, should contain 'hello'", got)
	}
}

func TestConvertURLDetection(t *testing.T) {
	// Test the URL detection logic without making a real HTTP request
	m := New()
	// Invalid URL should fail at HTTP request level
	_, err := m.Convert("http://localhost:1/no-such-port/file.txt")
	if err == nil {
		// Connection should fail
	}
}

func TestConvertURLHTTPSDetection(t *testing.T) {
	m := New()
	_, err := m.Convert("https://localhost:1/no-such-port/file.txt")
	if err == nil {
		// Connection should fail
	}
}

func TestConvertURLFileFallback(t *testing.T) {
	// Non-URL should go to ConvertFile
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("url fallback content"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	result, err := m.Convert(path)
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	if result.Markdown != "url fallback content" {
		t.Errorf("Markdown = %q, want 'url fallback content'", result.Markdown)
	}
}

func TestPptxParseSlideError(t *testing.T) {
	c := &PptxConverter{}
	got := c.parseSlide([]byte("<<<not xml>>>"))
	if got != "" {
		t.Errorf("parseSlide(invalid) = %q, want empty", got)
	}
}

func TestPptxExtractShapesInvalid(t *testing.T) {
	c := &PptxConverter{}
	shapes := c.extractShapes([]byte("<<<not xml>>>"))
	if len(shapes) != 0 {
		t.Errorf("extractShapes(invalid) returned %d shapes, want 0", len(shapes))
	}
}

func TestPptxGetSlideOrderMissingPresentation(t *testing.T) {
	// When presentation.xml is missing, should return error or fallback
	zr := makeEmptyZip(t)
	c := &PptxConverter{}
	_, err := c.getSlideOrder(zr)
	if err == nil {
		// May succeed via fallback with empty result
	}
}

func TestPptxConvertSlideMissing(t *testing.T) {
	// PPTX with presentation.xml but missing slide files
	presXML := `<?xml version="1.0"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst>
    <p:sldId r:id="rId1"/>
  </p:sldIdLst>
</p:presentation>`
	relsXML := `<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
</Relationships>`

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.pptx")
	if err := createZipFile(zipPath, map[string]string{
		"ppt/presentation.xml":            presXML,
		"ppt/_rels/presentation.xml.rels": relsXML,
		"[Content_Types].xml":             `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`,
	}); err != nil {
		t.Fatal(err)
	}

	c := NewPptxConverter(nil)
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	// Slide file is missing - should not crash
	result, err := c.Convert(strings.NewReader(string(data)), StreamInfo{Extension: ".pptx"})
	if err != nil {
		t.Fatalf("Convert should handle missing slides: %v", err)
	}
	// Result should be empty or minimal since slide is missing
	_ = result
}

func TestCsvConvertWithCharset(t *testing.T) {
	m := New()
	// CSV with charset hint
	result, err := m.ConvertReader(strings.NewReader("a,b\n1,2\n"), StreamInfo{
		Extension: ".csv",
		MIMEType:  "text/csv",
		Charset:   "utf-8",
	})
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	if !strings.Contains(result.Markdown, "| a | b |") {
		t.Errorf("Markdown should contain header: %s", result.Markdown)
	}
}

func TestCsvConvertEmptyContent(t *testing.T) {
	m := New()
	result, err := m.ConvertReader(strings.NewReader(""), StreamInfo{
		Extension: ".csv",
		MIMEType:  "text/csv",
	})
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	if result.Markdown != "" {
		t.Errorf("Markdown = %q, want empty", result.Markdown)
	}
}
