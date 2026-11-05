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
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

const docxNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`

func docXML(body string) []byte {
	return []byte(`<?xml version="1.0"?><w:document ` + docxNS + `><w:body>` + body + `</w:body></w:document>`)
}

func render(t *testing.T, body string) string {
	t.Helper()
	c := &DocxConverter{}
	return c.documentToHTML(docXML(body), nil, nil, nil, nil, makeEmptyZip(t))
}

func TestDocxConstructorKeepsMarkitdownOptions(t *testing.T) {
	// DOCX image extraction truncates data URIs through the HTML converter
	// unless keepDataURIs is set.
	dir := t.TempDir()
	docxPath := filepath.Join(dir, "img.docx")
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte("A"), 100)...)
	if err := createZipFile(docxPath, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`,
		"word/document.xml": string(docXML(
			`<w:p><w:r><w:drawing><a:blip xmlns:a="x" r:embed="rIdImg" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"/><wp:docPr xmlns:wp="x" descr="photo"/></w:drawing></w:r></w:p>`)),
		"word/_rels/document.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdImg" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/pic.png"/></Relationships>`,
		"word/media/pic.png":           string(png),
	}); err != nil {
		t.Fatal(err)
	}
	m := New(WithKeepDataURIs(true))
	result, err := m.ConvertFile(docxPath)
	if err != nil {
		t.Fatalf("ConvertFile: %v", err)
	}
	if !strings.Contains(result.Markdown, strings.Repeat("QUFB", 20)) {
		t.Errorf("docx converter must honor WithKeepDataURIs, got: %.60s...", result.Markdown)
	}
}

func TestDocxAcceptsByMime(t *testing.T) {
	c := NewDocxConverter(nil)
	if !c.Accepts(StreamInfo{MIMEType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"}) {
		t.Error("Accepts(wordprocessingml mime) = false, want true")
	}
}

func TestDocxConvertReadErrorPreservesCause(t *testing.T) {
	c := NewDocxConverter(nil)
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".docx"})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("read error must wrap the reader error via %%w, got: %v", err)
	}
}

func TestDocxConvertMissingDocumentPreservesCause(t *testing.T) {
	c := NewDocxConverter(nil)
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.docx")
	if err := createZipFile(p, map[string]string{"[Content_Types].xml": "<Types/>"}); err != nil {
		t.Fatal(err)
	}
	_, err := c.Convert(strings.NewReader(string(mustRead(t, p))), StreamInfo{Extension: ".docx"})
	if err == nil {
		t.Fatal("expected read document.xml error")
	}
	if !strings.Contains(err.Error(), "read document.xml") {
		t.Errorf("error = %v, should contain 'read document.xml'", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("read document.xml error must wrap the zip error via %%w, got: %v", err)
	}
}

// Formatting flags reset per paragraph: a bold first paragraph must not
// leak into the second.
func TestDocxParagraphStateResets(t *testing.T) {
	got := render(t,
		`<w:p><w:r><w:rPr><w:b/></w:rPr><w:t>bold</w:t></w:r></w:p>`+
			`<w:p><w:r><w:t>plain</w:t></w:r></w:p>`)
	want := "<html><body><p><b>bold</b></p>\n<p>plain</p>\n</body></html>"
	if got != want {
		t.Errorf("HTML = %q, want %q", got, want)
	}
}

// Run-level formatting resets between runs.
func TestDocxRunFormattingResets(t *testing.T) {
	got := render(t,
		`<w:p><w:r><w:rPr><w:b/></w:rPr><w:t>B</w:t></w:r><w:r><w:t>P</w:t></w:r></w:p>`)
	want := "<html><body><p><b>B</b>P</p>\n</body></html>"
	if got != want {
		t.Errorf("HTML = %q, want %q", got, want)
	}
}

func TestDocxBoldItalicStrikeValZero(t *testing.T) {
	got := render(t,
		`<w:p>`+
			`<w:r><w:rPr><w:b w:val="0"/></w:rPr><w:t>a</w:t></w:r>`+
			`<w:r><w:rPr><w:i w:val="0"/></w:rPr><w:t>b</w:t></w:r>`+
			`<w:r><w:rPr><w:strike/></w:rPr><w:t>c</w:t></w:r>`+
			`</w:p>`)
	want := "<html><body><p>ab<s>c</s></p>\n</body></html>"
	if got != want {
		t.Errorf("HTML = %q, want %q", got, want)
	}
}

func TestDocxTabAndBreakExact(t *testing.T) {
	got := render(t, `<w:p><w:r><w:t>before</w:t><w:tab/><w:t>after</w:t><w:br/><w:t>next</w:t></w:r></w:p>`)
	want := "<html><body><p>before\tafter<br/>next</p>\n</body></html>"
	if got != want {
		t.Errorf("HTML = %q, want %q", got, want)
	}
}

func TestDocxEmptyParagraphOmitted(t *testing.T) {
	got := render(t, `<w:p><w:r><w:t></w:t></w:r></w:p><w:p><w:r><w:t>x</w:t></w:r></w:p>`)
	want := "<html><body><p>x</p>\n</body></html>"
	if got != want {
		t.Errorf("HTML = %q, want %q", got, want)
	}
}

// A numPr with numId 0 is not a list item.
func TestDocxListNumIdZeroIsParagraph(t *testing.T) {
	got := render(t,
		`<w:p><w:pPr><w:numPr><w:numId w:val="0"/></w:numPr></w:pPr><w:r><w:t>item</w:t></w:r></w:p>`)
	want := "<html><body><p>item</p>\n</body></html>"
	if got != want {
		t.Errorf("HTML = %q, want %q", got, want)
	}
}

// List state resets between paragraphs.
func TestDocxListStateResets(t *testing.T) {
	got := render(t,
		`<w:p><w:pPr><w:numPr><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>a</w:t></w:r></w:p>`+
			`<w:p><w:r><w:t>b</w:t></w:r></w:p>`)
	want := "<html><body><li>a</li>\n<p>b</p>\n</body></html>"
	if got != want {
		t.Errorf("HTML = %q, want %q", got, want)
	}
}

// Heading style state resets between paragraphs: a heading followed by a
// plain paragraph keeps the plain one a paragraph.
func TestDocxHeadingStateResets(t *testing.T) {
	got := render(t,
		`<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>H</w:t></w:r></w:p>`+
			`<w:p><w:r><w:t>body</w:t></w:r></w:p>`)
	want := "<html><body><h1>H</h1>\n<p>body</p>\n</body></html>"
	if got != want {
		t.Errorf("HTML = %q, want %q", got, want)
	}
}

func TestDocxHyperlinkFull(t *testing.T) {
	rels := map[string]Relationship{
		"rId1": {ID: "rId1", Target: "https://example.com"},
	}
	c := &DocxConverter{}
	got := c.documentToHTML(docXML(
		`<w:p><w:hyperlink xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" r:id="rId1"><w:r><w:t>link</w:t></w:r></w:hyperlink></w:p>`+
			`<w:p><w:hyperlink xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" r:id="rIdMissing"><w:r><w:t>broken</w:t></w:r></w:hyperlink></w:p>`),
		rels, nil, nil, nil, makeEmptyZip(t))
	want := `<html><body><p><a href="https://example.com">link</a></p>` + "\n" + `<p>broken</p>` + "\n" + `</body></html>`
	if got != want {
		t.Errorf("HTML = %q, want %q", got, want)
	}
}

func TestDocxTableExact(t *testing.T) {
	got := render(t,
		`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>A</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>B</w:t></w:r></w:p></w:tc></w:tr>`+
			`<w:tr><w:tc><w:p><w:r><w:t>1</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`)
	want := "<html><body><table><tr><th>A</th><th>B</th></tr><tr><td>1</td></tr></table>\n</body></html>"
	if got != want {
		t.Errorf("HTML = %q, want %q", got, want)
	}
}

func TestDocxTableAndParagraphSequence(t *testing.T) {
	got := render(t,
		`<w:p><w:r><w:t>intro</w:t></w:r></w:p>`+
			`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>c</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`+
			`<w:p><w:r><w:t>outro</w:t></w:r></w:p>`)
	want := "<html><body><p>intro</p>\n<table><tr><th>c</th></tr></table>\n<p>outro</p>\n</body></html>"
	if got != want {
		t.Errorf("HTML = %q, want %q", got, want)
	}
}

func TestDocxCellContentRouting(t *testing.T) {
	// bold text inside a cell lands in the cell, and paragraphs inside a
	// cell do not wrap in <p>.
	got := render(t,
		`<w:tbl><w:tr><w:tc><w:p><w:r><w:rPr><w:b/></w:rPr><w:t>X</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`)
	want := "<html><body><table><tr><th><b>X</b></th></tr></table>\n</body></html>"
	if got != want {
		t.Errorf("HTML = %q, want %q", got, want)
	}
}

func TestDocxCommentReferenceRenders(t *testing.T) {
	comments := map[string]docxComment{"7": {id: "7", author: "Bob", text: "note text"}}
	c := &DocxConverter{}
	got := c.documentToHTML(docXML(
		`<w:p><w:r><w:t>body</w:t><w:commentReference w:id="7"/></w:r></w:p>`),
		nil, nil, comments, nil, makeEmptyZip(t))
	want := "<html><body><p>body [comment by Bob: note text]</p>\n</body></html>"
	if got != want {
		t.Errorf("HTML = %q, want %q", got, want)
	}
}

func TestDocxHeadingPatterns(t *testing.T) {
	c := &DocxConverter{}
	cases := []struct {
		styleID string
		want    int
	}{
		{"Heading1", 1},
		{"heading 2", 2},
		{"Heading 6", 6},
		{"Title", 0},
	}
	for _, tc := range cases {
		if got := c.getHeadingLevel(tc.styleID, nil); got != tc.want {
			t.Errorf("getHeadingLevel(%q) = %d, want %d", tc.styleID, got, tc.want)
		}
	}
}

func TestDocxHeadingLevelFromStyleName(t *testing.T) {
	styles := map[string]styleInfo{"Custom": {name: "heading 3", styleID: "Custom"}}
	c := &DocxConverter{}
	if got := c.getHeadingLevel("Custom", styles); got != 3 {
		t.Errorf("getHeadingLevel(Custom) = %d, want 3", got)
	}
	if got := c.getHeadingLevel("Custom2", nil); got != 0 {
		t.Errorf("getHeadingLevel(unknown) = %d, want 0", got)
	}
}

// Images: content type from extension, alt text from docPr, fallback alt
// text from the file name when docPr is absent, and zip-root images
// resolve without the word/ prefix.
func TestDocxImageExtraction(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	jpg := []byte{0xFF, 0xD8, 0xFF}
	svg := []byte("<svg/>")
	dir := t.TempDir()
	docxPath := filepath.Join(dir, "img.docx")
	if err := createZipFile(docxPath, map[string]string{
		"word/document.xml": string(docXML(
			`<w:p><w:r><w:drawing>` +
				`<a:blip xmlns:a="x" r:embed="rP" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"/>` +
				`<wp:docPr xmlns:wp="x" descr="pic alt"/></w:drawing></w:r></w:p>` +
				`<w:p><w:r><w:drawing>` +
				`<a:blip xmlns:a="x" r:embed="rJ" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"/></w:drawing></w:r></w:p>` +
				`<w:p><w:r><w:drawing>` +
				`<a:blip xmlns:a="x" r:embed="rS" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"/></w:drawing></w:r></w:p>`)),
		"word/_rels/document.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rP" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/a.png"/>` +
			`<Relationship Id="rJ" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/b.jpg"/>` +
			`<Relationship Id="rS" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="root.svg"/></Relationships>`,
		"word/media/a.png": string(png),
		"word/media/b.jpg": string(jpg),
		"root.svg":         string(svg),
	}); err != nil {
		t.Fatal(err)
	}
	c := NewDocxConverter(nil)
	result, err := c.Convert(strings.NewReader(string(mustRead(t, docxPath))), StreamInfo{Extension: ".docx"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	pngURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	jpgURI := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpg)
	svgURI := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(svg)
	if !strings.Contains(result.Markdown, pngURI) {
		t.Errorf("png data URI missing: %.80s...", result.Markdown)
	}
	if !strings.Contains(result.Markdown, jpgURI) {
		t.Errorf("jpeg data URI missing (content type from extension): %.80s...", result.Markdown)
	}
	if !strings.Contains(result.Markdown, svgURI) {
		t.Errorf("svg data URI missing (zip-root fallback): %.80s...", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "pic alt") {
		t.Errorf("docPr alt text missing: %.80s...", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "b.jpg") {
		t.Errorf("filename fallback alt text missing: %.80s...", result.Markdown)
	}
}

func TestDocxParseStylesNestedNames(t *testing.T) {
	// A style element inside another style's name must not corrupt ids.
	stylesXML := `<?xml version="1.0"?><w:styles ` + docxNS + `>
<w:style w:styleId="A"><w:name w:val="heading 1"/></w:style>
<w:style w:styleId="B"><w:name w:val="heading 2"/></w:style>
</w:styles>`
	zr := makeZipWithFile(t, "word/styles.xml", stylesXML)
	c := &DocxConverter{}
	styles := c.parseStyles(zr)
	if len(styles) != 2 {
		t.Fatalf("styles = %v, want 2 entries", styles)
	}
	if styles["A"].name != "heading 1" || styles["B"].name != "heading 2" {
		t.Errorf("styles = %v, want heading names per id", styles)
	}
}

func TestDocxParseNumberingExact(t *testing.T) {
	numberingXML := `<?xml version="1.0"?><w:numbering ` + docxNS + `>
<w:num w:numId="3"><w:abstractNumId w:val="9"/></w:num>
</w:numbering>`
	zr := makeZipWithFile(t, "word/numbering.xml", numberingXML)
	c := &DocxConverter{}
	numbering := c.parseNumbering(zr)
	def, ok := numbering["3"]
	if !ok || def.abstractNumID != "9" {
		t.Errorf("numbering = %v, want numId 3 -> abstract 9", numbering)
	}
}

func TestDocxParseCommentsNestedDepth(t *testing.T) {
	// Nested elements inside a comment must not terminate it early.
	commentsXML := `<?xml version="1.0"?><w:comments ` + docxNS + `>
<w:comment w:id="1" w:author="Ann"><w:p><w:r><w:t>nested text</w:t></w:r></w:p></w:comment>
</w:comments>`
	zr := makeZipWithFile(t, "word/comments.xml", commentsXML)
	c := &DocxConverter{}
	comments := c.parseComments(zr)
	got, ok := comments["1"]
	if !ok {
		t.Fatalf("comment 1 missing: %v", comments)
	}
	if got.author != "Ann" || got.text != "nested text" {
		t.Errorf("comment = %+v, want Ann / nested text", got)
	}
}

func TestDocxReplaceOMMLBlocksMultiple(t *testing.T) {
	c := &DocxConverter{}
	content := `<w:p>a</w:p>` +
		`<m:oMath xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math"><m:r><m:t>x</m:t></m:r></m:oMath>` +
		`<w:p>b</w:p>` +
		`<m:oMath xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math"><m:r><m:t>y</m:t></m:r></m:oMath>`
	got := c.replaceOMMLBlocks(content, "m:oMath", false)
	if strings.Count(got, "$x$") != 1 || strings.Count(got, "$y$") != 1 {
		t.Errorf("replaceOMMLBlocks = %q, want both oMath replaced", got)
	}
	if strings.Contains(got, "<m:oMath") {
		t.Errorf("all oMath blocks must be replaced, got: %q", got)
	}
}

// --- review-fix batch: previously surviving mutant idxs ---

// Only abstractNumId's val populates the numbering map. A real-world
// lvlOverride/startOverride also carries a val attribute and must not
// overwrite the abstractNumId captured before it.
func TestDocxParseNumberingStartOverrideNotAbstract(t *testing.T) {
	numberingXML := `<?xml version="1.0"?><w:numbering ` + docxNS + `>
<w:num w:numId="3"><w:abstractNumId w:val="9"/><w:lvlOverride w:ilvl="0"><w:startOverride w:val="1"/></w:lvlOverride></w:num>
</w:numbering>`
	zr := makeZipWithFile(t, "word/numbering.xml", numberingXML)
	c := &DocxConverter{}
	numbering := c.parseNumbering(zr)
	def, ok := numbering["3"]
	if !ok || def.abstractNumID != "9" {
		t.Errorf("numbering = %v, want numId 3 -> abstract 9 (startOverride val must not overwrite)", numbering)
	}
}

// The direct id pattern covers exactly Heading1..Heading6. Word defines
// heading styles up to 9, so Heading7 is deliberately outside the id match
// (level 0 here), and heading0 falls through to the style-name lookup.
func TestDocxHeadingLevelBoundsAreOneToSix(t *testing.T) {
	c := &DocxConverter{}
	if got := c.getHeadingLevel("Heading7", nil); got != 0 {
		t.Errorf("getHeadingLevel(Heading7) = %d, want 0 (id match caps at 6)", got)
	}
	styles := map[string]styleInfo{"heading0": {name: "heading 2", styleID: "heading0"}}
	if got := c.getHeadingLevel("heading0", styles); got != 2 {
		t.Errorf("getHeadingLevel(heading0) = %d, want 2 from style name", got)
	}
}
