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
	"encoding/xml"
	"os"
	"strings"
	"testing"
)

func TestDocxConvertOMMLBlock(t *testing.T) {
	c := &DocxConverter{}
	// Simple oMath block with a fraction
	xmlBlock := `<m:oMath xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math"><m:r><m:t>x</m:t></m:r></m:oMath>`
	got := c.convertOMMLBlock(xmlBlock)
	if got != "x" {
		t.Errorf("convertOMMLBlock = %q, want 'x'", got)
	}
}

func TestDocxConvertOMMLBlockInvalidXML(t *testing.T) {
	c := &DocxConverter{}
	got := c.convertOMMLBlock("<<<not xml>>>")
	if got != "" {
		t.Errorf("convertOMMLBlock(invalid) = %q, want empty", got)
	}
}

func TestDocxConvertOMMLBlockEmpty(t *testing.T) {
	c := &DocxConverter{}
	// No oMath element
	xmlBlock := `<m:other xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math">text</m:other>`
	got := c.convertOMMLBlock(xmlBlock)
	if got != "" {
		t.Errorf("convertOMMLBlock(no oMath) = %q, want empty", got)
	}
}

func TestDocxReplaceOMMLBlocks(t *testing.T) {
	c := &DocxConverter{}
	// Content with an oMath block
	content := `<w:p>before <m:oMath xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math"><m:r><m:t>y</m:t></m:r></m:oMath> after</w:p>`
	got := c.replaceOMMLBlocks(content, "m:oMath", false)
	if !strings.Contains(got, "$") {
		t.Errorf("replaceOMMLBlocks should contain $ for inline math: %q", got)
	}
	if !strings.Contains(got, "y") {
		t.Errorf("replaceOMMLBlocks should contain y: %q", got)
	}
}

func TestDocxReplaceOMMLBlocksPara(t *testing.T) {
	c := &DocxConverter{}
	content := `<w:p>before</w:p><m:oMathPara xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math"><m:oMath><m:r><m:t>z</m:t></m:r></m:oMath></m:oMathPara><w:p>after</w:p>`
	got := c.replaceOMMLBlocks(content, "m:oMathPara", true)
	if !strings.Contains(got, "$$") {
		t.Errorf("replaceOMMLBlocks block should contain $$: %q", got)
	}
}

func TestDocxReplaceOMMLBlocksNoneFound(t *testing.T) {
	c := &DocxConverter{}
	content := `<w:p>no math here</w:p>`
	got := c.replaceOMMLBlocks(content, "m:oMath", false)
	if got != content {
		t.Errorf("replaceOMMLBlocks with no match should return unchanged: %q", got)
	}
}

func TestDocxPreProcessMath(t *testing.T) {
	c := &DocxConverter{}
	content := `<w:p>text</w:p><m:oMathPara xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math"><m:oMath><m:r><m:t>a+b</m:t></m:r></m:oMath></m:oMathPara>`
	result := c.preProcessMath([]byte(content))
	got := string(result)
	if !strings.Contains(got, "$$") {
		t.Errorf("preProcessMath should contain $$: %q", got)
	}
}

func TestDocxPreProcessMathInline(t *testing.T) {
	c := &DocxConverter{}
	content := `<w:p>text <m:oMath xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math"><m:r><m:t>x</m:t></m:r></m:oMath> more</w:p>`
	result := c.preProcessMath([]byte(content))
	got := string(result)
	if !strings.Contains(got, "$x$") {
		t.Errorf("preProcessMath should contain $x$: %q", got)
	}
}

func TestDocxPreProcessMathNoMath(t *testing.T) {
	c := &DocxConverter{}
	content := `<w:p>just text</w:p>`
	result := c.preProcessMath([]byte(content))
	if string(result) != content {
		t.Errorf("preProcessMath with no math should be unchanged")
	}
}

func TestDocxConvertEquationsDocx(t *testing.T) {
	path := "testdata/equations.docx"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("test fixture %s not found", path)
	}
	m := New()
	result, err := m.ConvertFile(path)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	// Should contain some math content
	if result.Markdown == "" {
		t.Error("expected non-empty markdown for equations.docx")
	}
}

func TestDocxConvertWithCommentDocx(t *testing.T) {
	path := "testdata/test_with_comment.docx"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("test fixture %s not found", path)
	}
	m := New()
	result, err := m.ConvertFile(path)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	if result.Markdown == "" {
		t.Error("expected non-empty markdown for test_with_comment.docx")
	}
}

func TestDocxParseStyles(t *testing.T) {
	stylesXML := `<?xml version="1.0" encoding="UTF-8"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:style w:styleId="Heading1">
    <w:name w:val="heading 1"/>
  </w:style>
  <w:style w:styleId="Title">
    <w:name w:val="Title"/>
  </w:style>
</w:styles>`
	zr := makeZipWithFile(t, "word/styles.xml", stylesXML)
	c := &DocxConverter{}
	styles := c.parseStyles(zr)
	if len(styles) < 2 {
		t.Fatalf("expected at least 2 styles, got %d", len(styles))
	}
	h1, ok := styles["Heading1"]
	if !ok {
		t.Fatal("Heading1 style not found")
	}
	if h1.name != "heading 1" {
		t.Errorf("Heading1 name = %q, want 'heading 1'", h1.name)
	}
}

func TestDocxParseStylesMissing(t *testing.T) {
	zr := makeEmptyZip(t)
	c := &DocxConverter{}
	styles := c.parseStyles(zr)
	if len(styles) != 0 {
		t.Errorf("expected empty styles for missing file, got %d", len(styles))
	}
}

func TestDocxExtractImageNoEmbed(t *testing.T) {
	c := &DocxConverter{}
	zr := makeEmptyZip(t)
	// Drawing without blip/embed
	xmlData := []byte(`<w:drawing><wp:inline><a:graphic><a:graphicData/></a:graphic></wp:inline></w:drawing>`)
	// We can't easily call extractImage since it needs xml.StartElement and decoder
	// but we can verify the overall documentToHTML handles missing images
	html := c.documentToHTML(xmlData, nil, nil, nil, nil, zr)
	if !strings.Contains(html, "<html>") {
		t.Errorf("documentToHTML should produce HTML: %q", html)
	}
}

func TestDocxDocumentToHTMLBasic(t *testing.T) {
	c := &DocxConverter{}
	zr := makeEmptyZip(t)
	// Simple paragraph with text
	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p>
      <w:r>
        <w:t>Hello World</w:t>
      </w:r>
    </w:p>
  </w:body>
</w:document>`)
	html := c.documentToHTML(xmlData, nil, nil, nil, nil, zr)
	if !strings.Contains(html, "Hello World") {
		t.Errorf("HTML should contain text: %q", html)
	}
	if !strings.Contains(html, "<p>") {
		t.Errorf("HTML should contain <p>: %q", html)
	}
}

func TestDocxDocumentToHTMLWithHeading(t *testing.T) {
	c := &DocxConverter{}
	zr := makeEmptyZip(t)
	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p>
      <w:pPr><w:pStyle w:val="Heading1"/></w:pPr>
      <w:r>
        <w:t>My Heading</w:t>
      </w:r>
    </w:p>
  </w:body>
</w:document>`)
	html := c.documentToHTML(xmlData, nil, nil, nil, nil, zr)
	if !strings.Contains(html, "<h1>") {
		t.Errorf("HTML should contain <h1>: %q", html)
	}
	if !strings.Contains(html, "My Heading") {
		t.Errorf("HTML should contain heading text: %q", html)
	}
}

func TestDocxDocumentToHTMLWithBold(t *testing.T) {
	c := &DocxConverter{}
	zr := makeEmptyZip(t)
	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p>
      <w:r>
        <w:rPr><w:b/></w:rPr>
        <w:t>Bold text</w:t>
      </w:r>
    </w:p>
  </w:body>
</w:document>`)
	html := c.documentToHTML(xmlData, nil, nil, nil, nil, zr)
	if !strings.Contains(html, "<b>") {
		t.Errorf("HTML should contain <b>: %q", html)
	}
}

func TestDocxDocumentToHTMLWithItalic(t *testing.T) {
	c := &DocxConverter{}
	zr := makeEmptyZip(t)
	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p>
      <w:r>
        <w:rPr><w:i/></w:rPr>
        <w:t>Italic text</w:t>
      </w:r>
    </w:p>
  </w:body>
</w:document>`)
	html := c.documentToHTML(xmlData, nil, nil, nil, nil, zr)
	if !strings.Contains(html, "<i>") {
		t.Errorf("HTML should contain <i>: %q", html)
	}
}

func TestDocxDocumentToHTMLWithStrike(t *testing.T) {
	c := &DocxConverter{}
	zr := makeEmptyZip(t)
	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p>
      <w:r>
        <w:rPr><w:strike/></w:rPr>
        <w:t>Striked text</w:t>
      </w:r>
    </w:p>
  </w:body>
</w:document>`)
	html := c.documentToHTML(xmlData, nil, nil, nil, nil, zr)
	if !strings.Contains(html, "<s>") {
		t.Errorf("HTML should contain <s>: %q", html)
	}
}

func TestDocxDocumentToHTMLWithHyperlink(t *testing.T) {
	c := &DocxConverter{}
	zr := makeEmptyZip(t)
	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <w:body>
    <w:p>
      <w:hyperlink r:id="rId1">
        <w:r>
          <w:t>Link text</w:t>
        </w:r>
      </w:hyperlink>
    </w:p>
  </w:body>
</w:document>`)
	rels := map[string]Relationship{
		"rId1": {ID: "rId1", Target: "https://example.com"},
	}
	html := c.documentToHTML(xmlData, rels, nil, nil, nil, zr)
	if !strings.Contains(html, "https://example.com") {
		t.Errorf("HTML should contain link URL: %q", html)
	}
	if !strings.Contains(html, "Link text") {
		t.Errorf("HTML should contain link text: %q", html)
	}
}

func TestDocxDocumentToHTMLWithList(t *testing.T) {
	c := &DocxConverter{}
	zr := makeEmptyZip(t)
	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p>
      <w:pPr>
        <w:numPr>
          <w:ilvl w:val="0"/>
          <w:numId w:val="1"/>
        </w:numPr>
      </w:pPr>
      <w:r>
        <w:t>List item</w:t>
      </w:r>
    </w:p>
  </w:body>
</w:document>`)
	html := c.documentToHTML(xmlData, nil, nil, nil, nil, zr)
	if !strings.Contains(html, "<li>") {
		t.Errorf("HTML should contain <li>: %q", html)
	}
}

func TestDocxDocumentToHTMLWithTable(t *testing.T) {
	c := &DocxConverter{}
	zr := makeEmptyZip(t)
	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:tbl>
      <w:tr>
        <w:tc><w:p><w:r><w:t>A</w:t></w:r></w:p></w:tc>
        <w:tc><w:p><w:r><w:t>B</w:t></w:r></w:p></w:tc>
      </w:tr>
      <w:tr>
        <w:tc><w:p><w:r><w:t>1</w:t></w:r></w:p></w:tc>
        <w:tc><w:p><w:r><w:t>2</w:t></w:r></w:p></w:tc>
      </w:tr>
    </w:tbl>
  </w:body>
</w:document>`)
	html := c.documentToHTML(xmlData, nil, nil, nil, nil, zr)
	if !strings.Contains(html, "<table>") {
		t.Errorf("HTML should contain <table>: %q", html)
	}
	if !strings.Contains(html, "<th>") {
		t.Errorf("HTML should contain <th>: %q", html)
	}
}

func TestDocxDocumentToHTMLWithTab(t *testing.T) {
	c := &DocxConverter{}
	zr := makeEmptyZip(t)
	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p>
      <w:r>
        <w:t>before</w:t>
        <w:tab/>
        <w:t>after</w:t>
      </w:r>
    </w:p>
  </w:body>
</w:document>`)
	html := c.documentToHTML(xmlData, nil, nil, nil, nil, zr)
	if !strings.Contains(html, "before") || !strings.Contains(html, "after") {
		t.Errorf("HTML should contain both text segments: %q", html)
	}
}

func TestDocxDocumentToHTMLWithBreak(t *testing.T) {
	c := &DocxConverter{}
	zr := makeEmptyZip(t)
	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p>
      <w:r>
        <w:t>line1</w:t>
        <w:br/>
        <w:t>line2</w:t>
      </w:r>
    </w:p>
  </w:body>
</w:document>`)
	html := c.documentToHTML(xmlData, nil, nil, nil, nil, zr)
	if !strings.Contains(html, "<br/>") {
		t.Errorf("HTML should contain <br/>: %q", html)
	}
}

func TestDocxDocumentToHTMLWithCommentRef(t *testing.T) {
	c := &DocxConverter{}
	zr := makeEmptyZip(t)
	xmlData := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p>
      <w:r>
        <w:t>Text with comment</w:t>
        <w:commentReference w:id="0"/>
      </w:r>
    </w:p>
  </w:body>
</w:document>`)
	comments := map[string]docxComment{
		"0": {id: "0", author: "Alice", text: "This is a comment"},
	}
	html := c.documentToHTML(xmlData, nil, nil, comments, nil, zr)
	if !strings.Contains(html, "Alice") {
		t.Errorf("HTML should contain comment author: %q", html)
	}
	if !strings.Contains(html, "This is a comment") {
		t.Errorf("HTML should contain comment text: %q", html)
	}
}

func TestFindOMath(t *testing.T) {
	results := []string{}
	e := &OMMLElement{
		XMLName: xmlNameLocal("root"),
		Children: []OMMLElement{
			{
				XMLName: xmlNameLocal("oMath"),
				Children: []OMMLElement{
					{XMLName: xmlNameLocal("r"), Children: []OMMLElement{{XMLName: xmlNameLocal("t"), Content: "found"}}},
				},
			},
		},
	}
	findOMath(e, &results)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !strings.Contains(results[0], "found") {
		t.Errorf("result = %q, should contain 'found'", results[0])
	}
}

func TestFindOMathNested(t *testing.T) {
	results := []string{}
	e := &OMMLElement{
		XMLName: xmlNameLocal("root"),
		Children: []OMMLElement{
			{
				XMLName: xmlNameLocal("wrapper"),
				Children: []OMMLElement{
					{
						XMLName: xmlNameLocal("oMath"),
						Children: []OMMLElement{
							{XMLName: xmlNameLocal("r"), Children: []OMMLElement{{XMLName: xmlNameLocal("t"), Content: "x"}}},
						},
					},
				},
			},
		},
	}
	findOMath(e, &results)
	if len(results) != 1 {
		t.Fatalf("expected 1 result from nested, got %d", len(results))
	}
}

func TestFindOMathNone(t *testing.T) {
	results := []string{}
	e := &OMMLElement{
		XMLName: xmlNameLocal("root"),
		Children: []OMMLElement{
			{XMLName: xmlNameLocal("other")},
		},
	}
	findOMath(e, &results)
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestEpubConvertInvalidZip(t *testing.T) {
	c := NewEpubConverter(nil)
	_, err := c.Convert(strings.NewReader("not an epub"), StreamInfo{Extension: ".epub"})
	if err == nil {
		t.Fatal("expected error for invalid EPUB")
	}
	if !strings.Contains(err.Error(), "open EPUB ZIP") {
		t.Errorf("error = %v, should contain 'open EPUB ZIP'", err)
	}
}

func TestEpubFindOPFPathMissing(t *testing.T) {
	zr := makeEmptyZip(t)
	c := NewEpubConverter(nil)
	_, err := c.findOPFPath(zr)
	if err == nil {
		t.Fatal("expected error for missing container.xml")
	}
}

func TestEpubFindOPFPathNoRootfile(t *testing.T) {
	containerXML := `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles></rootfiles>
</container>`
	zr := makeZipWithFile(t, "META-INF/container.xml", containerXML)
	c := NewEpubConverter(nil)
	_, err := c.findOPFPath(zr)
	if err == nil {
		t.Fatal("expected error for missing rootfile")
	}
	if !strings.Contains(err.Error(), "rootfile not found") {
		t.Errorf("error = %v, should contain 'rootfile not found'", err)
	}
}

func TestXlsxConvertInvalid(t *testing.T) {
	c := NewXlsxConverter()
	_, err := c.Convert(strings.NewReader("not xlsx"), StreamInfo{Extension: ".xlsx"})
	if err == nil {
		t.Fatal("expected error for invalid XLSX")
	}
	if !strings.Contains(err.Error(), "open XLSX") {
		t.Errorf("error = %v, should contain 'open XLSX'", err)
	}
}

func TestXlsConvertInvalid(t *testing.T) {
	c := NewXlsConverter()
	_, err := c.Convert(strings.NewReader("not xls"), StreamInfo{Extension: ".xls"})
	if err == nil {
		t.Fatal("expected error for invalid XLS")
	}
	if !strings.Contains(err.Error(), "open XLS") && !strings.Contains(err.Error(), "temp file") {
		t.Errorf("error = %v, should contain 'open XLS' or 'temp file'", err)
	}
}

func TestXmlNodeGetAttr(t *testing.T) {
	n := &xmlNode{
		Attrs: []xml.Attr{
			{Name: xmlNameLocal("x"), Value: "v1"},
			{Name: xmlNameLocal("y"), Value: "v2"},
		},
	}
	if got := n.getAttr("x"); got != "v1" {
		t.Errorf("getAttr(x) = %q, want v1", got)
	}
	if got := n.getAttr("y"); got != "v2" {
		t.Errorf("getAttr(y) = %q, want v2", got)
	}
	if got := n.getAttr("z"); got != "" {
		t.Errorf("getAttr(z) = %q, want empty", got)
	}
}

func TestXmlNodeFindChild(t *testing.T) {
	n := &xmlNode{
		Children: []xmlNode{
			{XMLName: xmlNameLocal("a")},
			{XMLName: xmlNameLocal("b")},
		},
	}
	if n.findChild("a") == nil {
		t.Error("findChild(a) should not be nil")
	}
	if n.findChild("missing") != nil {
		t.Error("findChild(missing) should be nil")
	}
}

func TestXmlNodeFindAll(t *testing.T) {
	n := &xmlNode{
		Children: []xmlNode{
			{XMLName: xmlNameLocal("a")},
			{XMLName: xmlNameLocal("a")},
			{XMLName: xmlNameLocal("b")},
		},
	}
	results := n.findAll("a")
	if len(results) != 2 {
		t.Errorf("findAll(a) found %d, want 2", len(results))
	}
}

// attrKV is kept as a comment to note that tests use xml.Attr directly.
