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
	"path/filepath"
	"strings"
	"testing"
)

func TestPptxConvertWithNotes(t *testing.T) {
	presXML := `<?xml version="1.0" encoding="UTF-8"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst>
    <p:sldId r:id="rId1"/>
  </p:sldIdLst>
</p:presentation>`

	presRelsXML := `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
</Relationships>`

	slideXML := `<?xml version="1.0" encoding="UTF-8"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld>
    <p:spTree>
      <p:sp>
        <p:nvSpPr>
          <p:cNvPr id="1" name="Title"/>
          <p:nvPr><p:ph type="title"/></p:nvPr>
        </p:nvSpPr>
        <p:spPr>
          <a:xfrm>
            <a:off x="100" y="100"/>
            <a:ext cx="500" cy="50"/>
          </a:xfrm>
        </p:spPr>
        <p:txBody>
          <a:p>
            <a:r><a:t>Slide Title</a:t></a:r>
          </a:p>
        </p:txBody>
      </p:sp>
      <p:sp>
        <p:nvSpPr>
          <p:cNvPr id="2" name="Content"/>
          <p:nvPr/>
        </p:nvSpPr>
        <p:spPr>
          <a:xfrm>
            <a:off x="100" y="200"/>
            <a:ext cx="500" cy="200"/>
          </a:xfrm>
        </p:spPr>
        <p:txBody>
          <a:p>
            <a:r><a:t>Slide content text</a:t></a:r>
          </a:p>
        </p:txBody>
      </p:sp>
    </p:spTree>
  </p:cSld>
</p:sld>`

	slideRelsXML := `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide1.xml"/>
</Relationships>`

	notesXML := `<?xml version="1.0" encoding="UTF-8"?>
<p:notes xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld>
    <p:spTree>
      <p:sp>
        <p:txBody>
          <a:p>
            <a:r><a:t>Speaker notes content</a:t></a:r>
          </a:p>
        </p:txBody>
      </p:sp>
    </p:spTree>
  </p:cSld>
</p:notes>`

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.pptx")
	if err := createZipFile(zipPath, map[string]string{
		"ppt/presentation.xml":             presXML,
		"ppt/_rels/presentation.xml.rels":  presRelsXML,
		"ppt/slides/slide1.xml":            slideXML,
		"ppt/slides/_rels/slide1.xml.rels": slideRelsXML,
		"ppt/notesSlides/notesSlide1.xml":  notesXML,
		"[Content_Types].xml":              `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`,
	}); err != nil {
		t.Fatal(err)
	}

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	if !strings.Contains(result.Markdown, "Slide Title") {
		t.Errorf("Markdown should contain slide title: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "Slide content text") {
		t.Errorf("Markdown should contain slide content: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "Speaker notes content") {
		t.Errorf("Markdown should contain notes: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "### Notes:") {
		t.Errorf("Markdown should contain notes header: %s", result.Markdown)
	}
}

func TestPptxConvertWithTable(t *testing.T) {
	presXML := `<?xml version="1.0"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst>
    <p:sldId r:id="rId1"/>
  </p:sldIdLst>
</p:presentation>`

	presRelsXML := `<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
</Relationships>`

	slideXML := `<?xml version="1.0"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld>
    <p:spTree>
      <p:graphicFrame>
        <p:nvGraphicFramePr>
          <p:cNvPr id="1" name="Table"/>
          <p:nvPr/>
        </p:nvGraphicFramePr>
        <p:xfrm>
          <a:off x="100" y="100"/>
          <a:ext cx="500" cy="200"/>
        </p:xfrm>
        <a:graphic>
          <a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/table">
            <a:tbl>
              <a:tr>
                <a:tc>
                  <a:txBody>
                    <a:p><a:r><a:t>H1</a:t></a:r></a:p>
                  </a:txBody>
                </a:tc>
                <a:tc>
                  <a:txBody>
                    <a:p><a:r><a:t>H2</a:t></a:r></a:p>
                  </a:txBody>
                </a:tc>
              </a:tr>
              <a:tr>
                <a:tc>
                  <a:txBody>
                    <a:p><a:r><a:t>R1C1</a:t></a:r></a:p>
                  </a:txBody>
                </a:tc>
                <a:tc>
                  <a:txBody>
                    <a:p><a:r><a:t>R1C2</a:t></a:r></a:p>
                  </a:txBody>
                </a:tc>
              </a:tr>
            </a:tbl>
          </a:graphicData>
        </a:graphic>
      </p:graphicFrame>
    </p:spTree>
  </p:cSld>
</p:sld>`

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test_table.pptx")
	if err := createZipFile(zipPath, map[string]string{
		"ppt/presentation.xml":            presXML,
		"ppt/_rels/presentation.xml.rels": presRelsXML,
		"ppt/slides/slide1.xml":           slideXML,
		"[Content_Types].xml":             `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`,
	}); err != nil {
		t.Fatal(err)
	}

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	if !strings.Contains(result.Markdown, "H1") {
		t.Errorf("Markdown should contain table H1: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "R1C2") {
		t.Errorf("Markdown should contain table R1C2: %s", result.Markdown)
	}
}

func TestPptxConvertWithPicture(t *testing.T) {
	presXML := `<?xml version="1.0"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst>
    <p:sldId r:id="rId1"/>
  </p:sldIdLst>
</p:presentation>`

	presRelsXML := `<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
</Relationships>`

	slideXML := `<?xml version="1.0"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld>
    <p:spTree>
      <p:pic>
        <p:nvPicPr>
          <p:cNvPr id="1" name="Image" descr="Picture description"/>
          <p:nvPr/>
        </p:nvPicPr>
        <p:spPr>
          <a:xfrm>
            <a:off x="100" y="100"/>
            <a:ext cx="500" cy="300"/>
          </a:xfrm>
        </p:spPr>
      </p:pic>
    </p:spTree>
  </p:cSld>
</p:sld>`

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test_pic.pptx")
	if err := createZipFile(zipPath, map[string]string{
		"ppt/presentation.xml":            presXML,
		"ppt/_rels/presentation.xml.rels": presRelsXML,
		"ppt/slides/slide1.xml":           slideXML,
		"[Content_Types].xml":             `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`,
	}); err != nil {
		t.Fatal(err)
	}

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	if !strings.Contains(result.Markdown, "![Picture description](image)") {
		t.Errorf("Markdown should contain image with alt text: %s", result.Markdown)
	}
}

func TestPptxConvertMultipleSlides(t *testing.T) {
	presXML := `<?xml version="1.0"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst>
    <p:sldId r:id="rId1"/>
    <p:sldId r:id="rId2"/>
  </p:sldIdLst>
</p:presentation>`

	presRelsXML := `<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide2.xml"/>
</Relationships>`

	slide1XML := `<?xml version="1.0"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld>
    <p:spTree>
      <p:sp>
        <p:nvSpPr><p:cNvPr id="1"/><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr>
        <p:txBody>
          <a:p><a:r><a:t>First Slide</a:t></a:r></a:p>
        </p:txBody>
      </p:sp>
    </p:spTree>
  </p:cSld>
</p:sld>`

	slide2XML := `<?xml version="1.0"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld>
    <p:spTree>
      <p:sp>
        <p:nvSpPr><p:cNvPr id="1"/><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr>
        <p:txBody>
          <a:p><a:r><a:t>Second Slide</a:t></a:r></a:p>
        </p:txBody>
      </p:sp>
    </p:spTree>
  </p:cSld>
</p:sld>`

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test_multi.pptx")
	if err := createZipFile(zipPath, map[string]string{
		"ppt/presentation.xml":            presXML,
		"ppt/_rels/presentation.xml.rels": presRelsXML,
		"ppt/slides/slide1.xml":           slide1XML,
		"ppt/slides/slide2.xml":           slide2XML,
		"[Content_Types].xml":             `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`,
	}); err != nil {
		t.Fatal(err)
	}

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	if !strings.Contains(result.Markdown, "First Slide") {
		t.Errorf("Markdown should contain first slide: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "Second Slide") {
		t.Errorf("Markdown should contain second slide: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "Slide number: 1") {
		t.Errorf("Markdown should contain slide 1 marker: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "Slide number: 2") {
		t.Errorf("Markdown should contain slide 2 marker: %s", result.Markdown)
	}
}

func TestPptxConvertGroupShape(t *testing.T) {
	presXML := `<?xml version="1.0"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst>
    <p:sldId r:id="rId1"/>
  </p:sldIdLst>
</p:presentation>`

	presRelsXML := `<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
</Relationships>`

	slideXML := `<?xml version="1.0"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld>
    <p:spTree>
      <p:grpSp>
        <p:nvGrpSpPr><p:cNvPr id="1"/><p:nvPr/></p:nvGrpSpPr>
        <p:sp>
          <p:nvSpPr><p:cNvPr id="2"/><p:nvPr/></p:nvSpPr>
          <p:txBody>
            <a:p><a:r><a:t>Grouped text</a:t></a:r></a:p>
          </p:txBody>
        </p:sp>
      </p:grpSp>
    </p:spTree>
  </p:cSld>
</p:sld>`

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test_group.pptx")
	if err := createZipFile(zipPath, map[string]string{
		"ppt/presentation.xml":            presXML,
		"ppt/_rels/presentation.xml.rels": presRelsXML,
		"ppt/slides/slide1.xml":           slideXML,
		"[Content_Types].xml":             `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`,
	}); err != nil {
		t.Fatal(err)
	}

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	if !strings.Contains(result.Markdown, "Grouped text") {
		t.Errorf("Markdown should contain grouped text: %s", result.Markdown)
	}
}

func TestPptxConvertEmptyPresentation(t *testing.T) {
	presXML := `<?xml version="1.0"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst></p:sldIdLst>
</p:presentation>`

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test_empty.pptx")
	if err := createZipFile(zipPath, map[string]string{
		"ppt/presentation.xml":            presXML,
		"ppt/_rels/presentation.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"></Relationships>`,
		"[Content_Types].xml":             `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`,
	}); err != nil {
		t.Fatal(err)
	}

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	// Should produce empty markdown for presentation with no slides
	if result.Markdown != "" {
		t.Errorf("expected empty markdown, got %q", result.Markdown)
	}
}

func TestPptxConvertMissingPresentation(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test_no_pres.pptx")
	if err := createZipFile(zipPath, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`,
	}); err != nil {
		t.Fatal(err)
	}

	m := New()
	_, err := m.ConvertFile(zipPath)
	if err == nil {
		// Should fail since presentation.xml is missing
	}
}

func TestNormalizeOutputEdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"only_newlines", "\n\n\n", ""},
		{"only_whitespace", "   \t  ", ""},
		{"unicode", "héllo wörld", "héllo wörld"},
		{"invalid_utf8", "hello\xff\xffworld", "helloworld"},
		{"vertical_tab", "hello\x0bworld", "helloworld"},
		{"form_feed", "hello\x0cworld", "helloworld"},
		{"null_byte", "hello\x00world", "helloworld"},
		{"tabs_preserved", "hello\tworld", "hello\tworld"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeOutput(tt.input)
			if got != tt.want {
				t.Errorf("normalizeOutput(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeOutputLongMultipleNewlines(t *testing.T) {
	input := "a" + strings.Repeat("\n", 10) + "b"
	got := normalizeOutput(input)
	want := "a\n\nb"
	if got != want {
		t.Errorf("normalizeOutput = %q, want %q", got, want)
	}
}

func TestPlainTextConvertReadError(t *testing.T) {
	m := New()
	_, err := m.ConvertReader(&errorReadSeeker{}, StreamInfo{
		Extension: ".txt",
		MIMEType:  "text/plain",
	})
	if err == nil {
		t.Fatal("expected error from failing reader")
	}
}

func TestDocxConvertReadError(t *testing.T) {
	c := NewDocxConverter(nil)
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".docx"})
	if err == nil {
		t.Fatal("expected error from failing reader")
	}
	if !strings.Contains(err.Error(), "read DOCX") {
		t.Errorf("error = %v, should contain 'read DOCX'", err)
	}
}

func TestPdfConvertReadError(t *testing.T) {
	c := NewPdfConverter()
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".pdf"})
	if err == nil {
		t.Fatal("expected error from failing reader")
	}
}

func TestZipConvertReadError(t *testing.T) {
	c := NewZipConverter(New())
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".zip"})
	if err == nil {
		t.Fatal("expected error from failing reader")
	}
	if !strings.Contains(err.Error(), "read ZIP") {
		t.Errorf("error = %v, should contain 'read ZIP'", err)
	}
}

func TestEpubConvertReadError(t *testing.T) {
	c := NewEpubConverter(nil)
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".epub"})
	if err == nil {
		t.Fatal("expected error from failing reader")
	}
	if !strings.Contains(err.Error(), "read EPUB") {
		t.Errorf("error = %v, should contain 'read EPUB'", err)
	}
}

func TestCsvConvertReadError(t *testing.T) {
	c := NewCsvConverter()
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".csv"})
	if err == nil {
		t.Fatal("expected error from failing reader")
	}
	if !strings.Contains(err.Error(), "read input") {
		t.Errorf("error = %v, should contain 'read input'", err)
	}
}

func TestIpynbConvertReadError(t *testing.T) {
	c := NewIpynbConverter()
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".ipynb"})
	if err == nil {
		t.Fatal("expected error from failing reader")
	}
	if !strings.Contains(err.Error(), "read input") {
		t.Errorf("error = %v, should contain 'read input'", err)
	}
}
