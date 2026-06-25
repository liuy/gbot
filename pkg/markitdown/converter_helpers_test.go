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
	"encoding/json"
	"strings"
	"testing"
)

// --- ipynb helpers ---

func TestParseSourceString(t *testing.T) {
	raw := json.RawMessage(`"hello world"`)
	got := parseSource(raw)
	want := "hello world"
	if got != want {
		t.Errorf("parseSource(string) = %q, want %q", got, want)
	}
}

func TestParseSourceArray(t *testing.T) {
	raw := json.RawMessage(`["line1\n", "line2\n", "line3"]`)
	got := parseSource(raw)
	want := "line1\nline2\nline3"
	if got != want {
		t.Errorf("parseSource(array) = %q, want %q", got, want)
	}
}

func TestParseSourceInvalid(t *testing.T) {
	raw := json.RawMessage(`123`)
	got := parseSource(raw)
	if got != "" {
		t.Errorf("parseSource(number) = %q, want empty", got)
	}
}

func TestParseSourceEmpty(t *testing.T) {
	raw := json.RawMessage(`null`)
	got := parseSource(raw)
	if got != "" {
		t.Errorf("parseSource(null) = %q, want empty", got)
	}
}

func TestParseOutputTextFromText(t *testing.T) {
	output := cellOutput{
		OutputType: "stream",
		Text:       json.RawMessage(`"hello output\n"`),
	}
	got := parseOutputText(output)
	want := "hello output"
	if got != want {
		t.Errorf("parseOutputText(text) = %q, want %q", got, want)
	}
}

func TestParseOutputTextFromTextArray(t *testing.T) {
	output := cellOutput{
		OutputType: "stream",
		Text:       json.RawMessage(`["line1\n", "line2"]`),
	}
	got := parseOutputText(output)
	want := "line1\nline2"
	if got != want {
		t.Errorf("parseOutputText(text array) = %q, want %q", got, want)
	}
}

func TestParseOutputTextFromData(t *testing.T) {
	output := cellOutput{
		OutputType: "execute_result",
		Data: map[string]json.RawMessage{
			"text/plain": json.RawMessage(`"result data"`),
		},
	}
	got := parseOutputText(output)
	want := "result data"
	if got != want {
		t.Errorf("parseOutputText(data) = %q, want %q", got, want)
	}
}

func TestParseOutputTextEmpty(t *testing.T) {
	output := cellOutput{}
	got := parseOutputText(output)
	if got != "" {
		t.Errorf("parseOutputText(empty) = %q, want empty", got)
	}
}

func TestParseOutputTextTextEmptyFallsToData(t *testing.T) {
	output := cellOutput{
		Text: json.RawMessage(`""`),
		Data: map[string]json.RawMessage{
			"text/plain": json.RawMessage(`"from data"`),
		},
	}
	got := parseOutputText(output)
	want := "from data"
	if got != want {
		t.Errorf("parseOutputText(empty text, data) = %q, want %q", got, want)
	}
}

func TestIpynbConvertCustomLanguage(t *testing.T) {
	nb := `{
		"metadata": {
			"kernelspec": {
				"language": "javascript"
			}
		},
		"cells": [
			{
				"cell_type": "code",
				"source": "console.log('hi')"
			}
		]
	}`
	m := New()
	result, err := m.ConvertReader(strings.NewReader(nb), StreamInfo{Extension: ".ipynb"})
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	if !strings.Contains(result.Markdown, "```javascript") {
		t.Errorf("Markdown should contain javascript code block, got: %s", result.Markdown)
	}
}

func TestIpynbConvertRawCell(t *testing.T) {
	nb := `{
		"cells": [
			{
				"cell_type": "raw",
				"source": "raw content"
			}
		]
	}`
	m := New()
	result, err := m.ConvertReader(strings.NewReader(nb), StreamInfo{Extension: ".ipynb"})
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	if !strings.Contains(result.Markdown, "raw content") {
		t.Errorf("Markdown should contain raw content, got: %s", result.Markdown)
	}
}

func TestIpynbConvertEmptyCodeCell(t *testing.T) {
	nb := `{
		"cells": [
			{
				"cell_type": "code",
				"source": "   "
			}
		]
	}`
	m := New()
	result, err := m.ConvertReader(strings.NewReader(nb), StreamInfo{Extension: ".ipynb"})
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	if result.Markdown != "" {
		t.Errorf("Markdown should be empty for whitespace-only code cell, got: %q", result.Markdown)
	}
}

func TestIpynbConvertMarkdownTitle(t *testing.T) {
	nb := `{
		"cells": [
			{
				"cell_type": "markdown",
				"source": "# My Title\n\nSome content"
			}
		]
	}`
	m := New()
	result, err := m.ConvertReader(strings.NewReader(nb), StreamInfo{Extension: ".ipynb"})
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	if result.Title != "My Title" {
		t.Errorf("Title = %q, want 'My Title'", result.Title)
	}
}

func TestIpynbConvertInvalidJSON(t *testing.T) {
	m := New()
	_, err := m.ConvertReader(strings.NewReader("not json"), StreamInfo{Extension: ".ipynb"})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse notebook JSON") {
		t.Errorf("error = %v, should contain 'parse notebook JSON'", err)
	}
}

// --- HTML helpers ---

func TestExtractHTMLTitle(t *testing.T) {
	html := `<html><head><title>My Page Title</title></head><body>content</body></html>`
	got := extractHTMLTitle(html)
	want := "My Page Title"
	if got != want {
		t.Errorf("extractHTMLTitle = %q, want %q", got, want)
	}
}

func TestExtractHTMLTitleEmpty(t *testing.T) {
	html := `<html><body>no title here</body></html>`
	got := extractHTMLTitle(html)
	if got != "" {
		t.Errorf("extractHTMLTitle = %q, want empty", got)
	}
}

func TestExtractHTMLTitleInvalidHTML(t *testing.T) {
	got := extractHTMLTitle("<<<not html>>>")
	if got != "" {
		t.Errorf("extractHTMLTitle(invalid) = %q, want empty", got)
	}
}

func TestExtractHTMLTitleWithWhitespace(t *testing.T) {
	html := `<html><head><title>  Spaced Title  </title></head></html>`
	got := extractHTMLTitle(html)
	want := "Spaced Title"
	if got != want {
		t.Errorf("extractHTMLTitle = %q, want %q (trimmed)", got, want)
	}
}

func TestRemoveScriptAndStyle(t *testing.T) {
	html := `<div>keep</div><script>remove()</script><style>.x{}</style><p>also keep</p>`
	got := removeScriptAndStyle(html)
	if strings.Contains(got, "remove()") {
		t.Errorf("script content should be removed: %q", got)
	}
	if strings.Contains(got, ".x{}") {
		t.Errorf("style content should be removed: %q", got)
	}
	if !strings.Contains(got, "keep") {
		t.Errorf("div content should remain: %q", got)
	}
	if !strings.Contains(got, "also keep") {
		t.Errorf("p content should remain: %q", got)
	}
}

func TestTruncateDataURIs(t *testing.T) {
	longB64 := strings.Repeat("A", 100)
	input := "data:image/png;base64," + longB64
	got := truncateDataURIs(input)
	if strings.Contains(got, longB64) {
		t.Errorf("data URI should be truncated: %q", got)
	}
	if !strings.Contains(got, "data:image/png;base64,...") {
		t.Errorf("data URI should end with ..., got: %q", got)
	}
}

func TestTruncateDataURIsShortNotTruncated(t *testing.T) {
	shortB64 := strings.Repeat("A", 10)
	input := "data:image/png;base64," + shortB64
	got := truncateDataURIs(input)
	if got != input {
		t.Errorf("short data URI should not be truncated: got %q, want %q", got, input)
	}
}

func TestHTMLConvertStringWithKeepDataURIs(t *testing.T) {
	longB64 := strings.Repeat("A", 100)
	html := `<img src="data:image/png;base64,` + longB64 + `">`
	m := New(WithKeepDataURIs(true))
	result, err := m.ConvertReader(strings.NewReader(html), StreamInfo{
		Extension: ".html",
		MIMEType:  "text/html",
	})
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	if !strings.Contains(result.Markdown, longB64) {
		t.Errorf("with keepDataURIs, URI should not be truncated: %s", result.Markdown)
	}
}

// --- CSV helpers ---

func TestRenderMarkdownTableEmpty(t *testing.T) {
	got := renderMarkdownTable(nil, false)
	if got != "" {
		t.Errorf("renderMarkdownTable(nil) = %q, want empty", got)
	}
}

func TestRenderMarkdownTableRaggedRows(t *testing.T) {
	// Rows with fewer columns than header
	records := [][]string{
		{"a", "b", "c"},
		{"1", "2"},
		{"x"},
	}
	got := renderMarkdownTable(records, false)
	// Should still render all rows with 3 columns
	if !strings.Contains(got, "| a | b | c |") {
		t.Errorf("header row missing: %q", got)
	}
	if !strings.Contains(got, "| --- | --- | --- |") {
		t.Errorf("separator row missing: %q", got)
	}
	if !strings.Contains(got, "| 1 | 2 |  |") {
		t.Errorf("data row 1 missing: %q", got)
	}
}

func TestCSVConvertSuccess(t *testing.T) {
	m := New()
	result, err := m.ConvertReader(strings.NewReader("a,b,c\n1,2,3\n"), StreamInfo{
		Extension: ".csv",
		MIMEType:  "text/csv",
	})
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	if !strings.Contains(result.Markdown, "| a | b | c |") {
		t.Errorf("Markdown should contain header: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "| 1 | 2 | 3 |") {
		t.Errorf("Markdown should contain data row: %s", result.Markdown)
	}
}

func TestCSVEmptyInput(t *testing.T) {
	m := New()
	result, err := m.ConvertReader(strings.NewReader(""), StreamInfo{
		Extension: ".csv",
		MIMEType:  "text/csv",
	})
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	if result.Markdown != "" {
		t.Errorf("Markdown = %q, want empty for empty CSV", result.Markdown)
	}
}

func TestRenderMarkdownTableLeadingEmptyRow(t *testing.T) {
	// Reproduces the WeChat spreadsheet shape: row 0 is empty (no cells), data
	// starts in column B. truncate=false (xlsx/xls/pptx mode) must derive the
	// column count from the widest row, not the empty header.
	records := [][]string{
		{},
		{"", "B1", "C1", "D1"},
		{"", "B2", "C2", "D2"},
	}
	got := renderMarkdownTable(records, false)
	if !strings.Contains(got, "|  | B1 | C1 | D1 |") {
		t.Errorf("header row should keep the leading empty cell, got: %q", got)
	}
	if !strings.Contains(got, "|  | B2 | C2 | D2 |") {
		t.Errorf("data row should keep the leading empty cell, got: %q", got)
	}
	if !strings.Contains(got, "| --- | --- | --- | --- |") {
		t.Errorf("separator should be 4 columns (data width), got: %q", got)
	}
}

func TestRenderMarkdownTableTruncateCSVStyle(t *testing.T) {
	// truncate=true mirrors Python markitdown's _csv_converter.py: the header
	// row fixes the column count and wider data rows are truncated to it.
	records := [][]string{
		{"a", "b"},
		{"1", "2", "3", "4"},
		{"x"},
	}
	got := renderMarkdownTable(records, true)
	if !strings.Contains(got, "| a | b |") {
		t.Errorf("header should be 2 columns, got: %q", got)
	}
	if !strings.Contains(got, "| --- | --- |") {
		t.Errorf("separator should be exactly 2 columns, got: %q", got)
	}
	if !strings.Contains(got, "| 1 | 2 |") {
		t.Errorf("data row should be truncated to 2 columns, got: %q", got)
	}
	if strings.Contains(got, "| 3 |") {
		t.Errorf("overflow cell 3 must be truncated under CSV mode, got: %q", got)
	}
	if strings.Contains(got, "| 4 |") {
		t.Errorf("overflow cell 4 must be truncated under CSV mode, got: %q", got)
	}
}

// --- RSS helpers ---

func TestRSSAcceptsVariants(t *testing.T) {
	c := NewRSSConverter()
	tests := []struct {
		info StreamInfo
		want bool
	}{
		{StreamInfo{Extension: ".rss"}, true},
		{StreamInfo{Extension: ".atom"}, true},
		{StreamInfo{Extension: ".xml"}, true},
		{StreamInfo{MIMEType: "application/rss+xml"}, true},
		{StreamInfo{MIMEType: "application/atom+xml"}, true},
		{StreamInfo{MIMEType: "text/xml"}, true},
		{StreamInfo{MIMEType: "application/xml"}, true},
		{StreamInfo{Extension: ".html"}, false},
		{StreamInfo{MIMEType: "text/html"}, false},
		{StreamInfo{Extension: ".txt"}, false},
	}
	for _, tt := range tests {
		got := c.Accepts(tt.info)
		if got != tt.want {
			t.Errorf("Accepts(%+v) = %v, want %v", tt.info, got, tt.want)
		}
	}
}

func TestRSSConvertInvalidFeed(t *testing.T) {
	m := New()
	_, err := m.ConvertReader(strings.NewReader("not a feed"), StreamInfo{
		Extension: ".rss",
	})
	if err == nil {
		t.Fatal("expected error for invalid feed")
	}
}

// --- DOCX helpers ---

func TestGetHeadingLevel(t *testing.T) {
	c := &DocxConverter{}
	tests := []struct {
		styleID string
		styles  map[string]styleInfo
		want    int
	}{
		{"Heading1", nil, 1},
		{"Heading2", nil, 2},
		{"Heading3", nil, 3},
		{"Heading4", nil, 4},
		{"Heading5", nil, 5},
		{"Heading6", nil, 6},
		{"heading1", nil, 1},
		{"Heading 1", nil, 1},
		{"Title", nil, 0},
		{"", nil, 0},
		{"Normal", nil, 0},
	}
	for _, tt := range tests {
		got := c.getHeadingLevel(tt.styleID, tt.styles)
		if got != tt.want {
			t.Errorf("getHeadingLevel(%q) = %d, want %d", tt.styleID, got, tt.want)
		}
	}
}

func TestGetHeadingLevelFromStyleName(t *testing.T) {
	c := &DocxConverter{}
	styles := map[string]styleInfo{
		"CustomHeading": {name: "Heading 1", styleID: "CustomHeading"},
	}
	got := c.getHeadingLevel("CustomHeading", styles)
	if got != 1 {
		t.Errorf("getHeadingLevel(CustomHeading with name 'Heading 1') = %d, want 1", got)
	}
}

func TestEscapeHTMLText(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "hello", "hello"},
		{"ampersand", "a&b", "a&amp;b"},
		{"less_than", "a<b", "a&lt;b"},
		{"greater_than", "a>b", "a&gt;b"},
		{"all_specials", "<>&", "&lt;&gt;&amp;"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeHTMLText(tt.input)
			if got != tt.want {
				t.Errorf("escapeHTMLText(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEscapeHTMLAttr(t *testing.T) {
	got := escapeHTMLAttr(`a"b<c>&d`)
	want := "a&quot;b&lt;c&gt;&amp;d"
	if got != want {
		t.Errorf("escapeHTMLAttr = %q, want %q", got, want)
	}
}

func TestDocxParseNumbering(t *testing.T) {
	// Test parseNumbering with numbering.xml
	numberingXML := `<?xml version="1.0" encoding="UTF-8"?>
<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:num w:numId="1">
    <w:abstractNumId w:val="0"/>
  </w:num>
  <w:num w:numId="2">
    <w:abstractNumId w:val="1"/>
  </w:num>
</w:numbering>`
	zr := makeZipWithFile(t, "word/numbering.xml", numberingXML)
	c := &DocxConverter{}
	numbering := c.parseNumbering(zr)
	if len(numbering) != 2 {
		t.Fatalf("expected 2 numbering defs, got %d", len(numbering))
	}
	if numbering["1"].abstractNumID != "0" {
		t.Errorf("numbering[1].abstractNumID = %q, want 0", numbering["1"].abstractNumID)
	}
	if numbering["2"].abstractNumID != "1" {
		t.Errorf("numbering[2].abstractNumID = %q, want 1", numbering["2"].abstractNumID)
	}
}

func TestDocxParseNumberingMissing(t *testing.T) {
	zr := makeEmptyZip(t)
	c := &DocxConverter{}
	numbering := c.parseNumbering(zr)
	if len(numbering) != 0 {
		t.Errorf("expected empty numbering for missing file, got %d", len(numbering))
	}
}

func TestDocxParseComments(t *testing.T) {
	commentsXML := `<?xml version="1.0" encoding="UTF-8"?>
<w:comments xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:comment w:id="0" w:author="Alice">
    <w:p>
      <w:r>
        <w:t>First comment text</w:t>
      </w:r>
    </w:p>
  </w:comment>
  <w:comment w:id="1" w:author="Bob">
    <w:p>
      <w:r>
        <w:t>Second comment</w:t>
      </w:r>
    </w:p>
  </w:comment>
</w:comments>`
	zr := makeZipWithFile(t, "word/comments.xml", commentsXML)
	c := &DocxConverter{}
	comments := c.parseComments(zr)
	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}
	c0, ok := comments["0"]
	if !ok {
		t.Fatal("comment 0 not found")
	}
	if c0.author != "Alice" {
		t.Errorf("comment 0 author = %q, want Alice", c0.author)
	}
	if !strings.Contains(c0.text, "First comment text") {
		t.Errorf("comment 0 text = %q, should contain 'First comment text'", c0.text)
	}
}

func TestDocxParseCommentsMissing(t *testing.T) {
	zr := makeEmptyZip(t)
	c := &DocxConverter{}
	comments := c.parseComments(zr)
	if len(comments) != 0 {
		t.Errorf("expected empty comments for missing file, got %d", len(comments))
	}
}

func TestDocxConvertInvalidZip(t *testing.T) {
	c := NewDocxConverter(nil)
	_, err := c.Convert(strings.NewReader("not a docx"), StreamInfo{Extension: ".docx"})
	if err == nil {
		t.Fatal("expected error for invalid DOCX")
	}
	if !strings.Contains(err.Error(), "open DOCX ZIP") {
		t.Errorf("error = %v, should contain 'open DOCX ZIP'", err)
	}
}

// --- PPTX helpers ---

func TestPptxSanitizeAltText(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "hello world", "hello world"},
		{"newlines", "hello\nworld", "hello world"},
		{"brackets", "hello[world]", "hello world "},
		{"multiple_spaces", "hello    world", "hello world"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeAltText(tt.input)
			// The function replaces brackets and collapses spaces,
			// result should not contain newlines or brackets
			if strings.Contains(got, "\n") {
				t.Errorf("sanitizeAltText(%q) = %q, should not contain newlines", tt.input, got)
			}
			if strings.Contains(got, "[") || strings.Contains(got, "]") {
				t.Errorf("sanitizeAltText(%q) = %q, should not contain brackets", tt.input, got)
			}
			if strings.Contains(got, "  ") {
				t.Errorf("sanitizeAltText(%q) = %q, should not contain double spaces", tt.input, got)
			}
		})
	}
}

func TestPptxGetNotesPath(t *testing.T) {
	relsXML := `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide1.xml"/>
</Relationships>`
	zr := makeZipWithFile(t, "ppt/slides/_rels/slide1.xml.rels", relsXML)
	c := &PptxConverter{}
	got := c.getNotesPath("ppt/slides/slide1.xml", zr)
	if !strings.Contains(got, "notesSlide1.xml") {
		t.Errorf("getNotesPath = %q, should contain notesSlide1.xml", got)
	}
}

func TestPptxGetNotesPathNoNotes(t *testing.T) {
	relsXML := `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
</Relationships>`
	zr := makeZipWithFile(t, "ppt/slides/_rels/slide1.xml.rels", relsXML)
	c := &PptxConverter{}
	got := c.getNotesPath("ppt/slides/slide1.xml", zr)
	if got != "" {
		t.Errorf("getNotesPath without notes = %q, want empty", got)
	}
}

func TestPptxGetNotesPathMissingRels(t *testing.T) {
	zr := makeEmptyZip(t)
	c := &PptxConverter{}
	got := c.getNotesPath("ppt/slides/slide1.xml", zr)
	if got != "" {
		t.Errorf("getNotesPath with missing rels = %q, want empty", got)
	}
}

func TestPptxExtractNotesText(t *testing.T) {
	notesXML := `<?xml version="1.0" encoding="UTF-8"?>
<p:notes xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld>
    <p:spTree>
      <p:sp>
        <p:txBody>
          <a:p>
            <a:r>
              <a:t>Speaker notes here</a:t>
            </a:r>
          </a:p>
        </p:txBody>
      </p:sp>
    </p:spTree>
  </p:cSld>
</p:notes>`
	c := &PptxConverter{}
	got := c.extractNotesText([]byte(notesXML))
	if !strings.Contains(got, "Speaker notes here") {
		t.Errorf("extractNotesText = %q, should contain 'Speaker notes here'", got)
	}
}

func TestPptxExtractNotesTextInvalidXML(t *testing.T) {
	c := &PptxConverter{}
	got := c.extractNotesText([]byte("<<<not xml>>>"))
	if got != "" {
		t.Errorf("extractNotesText(invalid) = %q, want empty", got)
	}
}

func TestPptxTableToMarkdownEmpty(t *testing.T) {
	c := &PptxConverter{}
	got := c.tableToMarkdown(nil)
	if got != "" {
		t.Errorf("tableToMarkdown(nil) = %q, want empty", got)
	}
}

func TestPptxConvertInvalidZip(t *testing.T) {
	c := NewPptxConverter(nil)
	_, err := c.Convert(strings.NewReader("not a pptx"), StreamInfo{Extension: ".pptx"})
	if err == nil {
		t.Fatal("expected error for invalid PPTX")
	}
	if !strings.Contains(err.Error(), "open PPTX ZIP") {
		t.Errorf("error = %v, should contain 'open PPTX ZIP'", err)
	}
}

func TestXmlNodeAllText(t *testing.T) {
	// Node with direct content
	n := &xmlNode{Content: "direct text"}
	if got := n.allText(); got != "direct text" {
		t.Errorf("allText() with content = %q, want 'direct text'", got)
	}

	// Node with children that have content
	n = &xmlNode{
		Children: []xmlNode{
			{Content: "child1 "},
			{Content: "child2"},
		},
	}
	got := n.allText()
	if !strings.Contains(got, "child1") || !strings.Contains(got, "child2") {
		t.Errorf("allText() with children = %q, should contain child1 and child2", got)
	}
}

func TestXmlNodeFindDeep(t *testing.T) {
	n := &xmlNode{
		XMLName: xmlNameLocal("root"),
		Children: []xmlNode{
			{
				XMLName: xmlNameLocal("a"),
				Children: []xmlNode{
					{XMLName: xmlNameLocal("target"), Content: "found"},
				},
			},
		},
	}
	found := n.findDeep("target")
	if found == nil {
		t.Fatal("findDeep(target) returned nil")
	}
	if found.Content != "found" {
		t.Errorf("findDeep(target).Content = %q, want 'found'", found.Content)
	}

	// Not found
	if n.findDeep("nonexistent") != nil {
		t.Error("findDeep(nonexistent) should return nil")
	}
}

func TestXmlNodeFindAllDeep(t *testing.T) {
	n := &xmlNode{
		XMLName: xmlNameLocal("root"),
		Children: []xmlNode{
			{XMLName: xmlNameLocal("target"), Content: "1"},
			{
				XMLName: xmlNameLocal("a"),
				Children: []xmlNode{
					{XMLName: xmlNameLocal("target"), Content: "2"},
				},
			},
		},
	}
	results := n.findAllDeep("target")
	if len(results) != 2 {
		t.Errorf("findAllDeep(target) found %d, want 2", len(results))
	}
}
