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
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestPptxConstructorKeepsMarkitdownOptions(t *testing.T) {
	longB64 := strings.Repeat("A", 100)
	uri := "data:image/png;base64," + longB64
	c := NewPptxConverter(New(WithKeepDataURIs(true)))
	got := c.tableToMarkdown([][]string{{"h", uri}})
	if !strings.Contains(got, longB64) {
		t.Errorf("pptx table must honor WithKeepDataURIs, got: %.60s...", got)
	}
}

func TestPptxAcceptsByMime(t *testing.T) {
	c := NewPptxConverter(nil)
	if !c.Accepts(StreamInfo{MIMEType: "application/vnd.openxmlformats-officedocument.presentationml.presentation"}) {
		t.Error("Accepts(presentationml mime) = false, want true")
	}
}

func TestPptxConvertReadErrorPreservesCause(t *testing.T) {
	c := NewPptxConverter(nil)
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".pptx"})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("read error must wrap the reader error via %%w, got: %v", err)
	}
}

func TestPptxConvertOpenErrorPreservesCause(t *testing.T) {
	c := NewPptxConverter(nil)
	_, err := c.Convert(strings.NewReader("nope"), StreamInfo{Extension: ".pptx"})
	if err == nil {
		t.Fatal("expected open error")
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("open error must wrap the zip error via %%w, got: %v", err)
	}
}

// orderedZip writes a zip with fixed member order (map-based helpers would
// randomize it, and slide order is under test).
func orderedZip(t *testing.T, members []struct{ name, content string }) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range members {
		w, err := zw.Create(m.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(m.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const pptxContentTypes = `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`

func slideXML(text string, x, y string) string {
	off := ""
	if x != "" || y != "" {
		off = `<a:off x="` + x + `" y="` + y + `"/>`
	}
	return `<?xml version="1.0"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:nvSpPr><p:cNvPr id="1" name="T"/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm>` + off + `</a:xfrm></p:spPr><p:txBody><a:p><a:r><a:t>` + text + `</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`
}

func presXML(rids ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:sldIdLst>`)
	for i, rid := range rids {
		b.WriteString(`<p:sldId id="` + itoa(256+i) + `" r:id="` + rid + `"/>`)
	}
	b.WriteString(`</p:sldIdLst></p:presentation>`)
	return b.String()
}

func itoa(n int) string {
	d := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

func presRelsXML(pairs ...[2]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for _, p := range pairs {
		b.WriteString(`<Relationship Id="` + p[0] + `" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="` + p[1] + `"/>`)
	}
	b.WriteString(`</Relationships>`)
	return b.String()
}

// Presentation order (slide2 then slide1) must beat the sorted fallback.
func TestPptxSlideOrderFromPresentation(t *testing.T) {
	data := orderedZip(t, []struct{ name, content string }{
		{"[Content_Types].xml", pptxContentTypes},
		{"ppt/presentation.xml", presXML("rId2", "rId1")},
		{"ppt/_rels/presentation.xml.rels", presRelsXML([2]string{"rId1", "slides/slide1.xml"}, [2]string{"rId2", "slides/slide2.xml"})},
		{"ppt/slides/slide1.xml", slideXML("ALPHA", "10", "10")},
		{"ppt/slides/slide2.xml", slideXML("BRAVO", "20", "10")},
	})
	c := NewPptxConverter(nil)
	result, err := c.Convert(bytes.NewReader(data), StreamInfo{Extension: ".pptx"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	alphaIdx, bravoIdx := strings.Index(result.Markdown, "ALPHA"), strings.Index(result.Markdown, "BRAVO")
	if alphaIdx < 0 || bravoIdx < 0 {
		t.Fatalf("both slides must render, got: %q", result.Markdown)
	}
	if bravoIdx > alphaIdx {
		t.Errorf("presentation order (BRAVO first) must be honored, got: %q", result.Markdown)
	}
}

// Without sldId/rels resolution the fallback scans slide files and sorts
// them by name; a decoy non-xml file must not join.
func TestPptxSlideFallbackScanSorted(t *testing.T) {
	data := orderedZip(t, []struct{ name, content string }{
		{"[Content_Types].xml", pptxContentTypes},
		{"ppt/presentation.xml", `<?xml version="1.0"?><p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:sldIdLst></p:sldIdLst></p:presentation>`},
		{"ppt/_rels/presentation.xml.rels", presRelsXML()},
		{"ppt/slides/slide2.xml", slideXML("CHARLIE", "10", "10")},
		{"ppt/slides/slide1.xml", slideXML("DELTA", "20", "10")},
		{"ppt/slides/slide9.txt", slideXML("DECOY", "30", "10")},
		{"ppt/props/decoy.xml", slideXML("XMLDECOY", "40", "10")},
	})
	c := NewPptxConverter(nil)
	result, err := c.Convert(bytes.NewReader(data), StreamInfo{Extension: ".pptx"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	deltaIdx, charlieIdx := strings.Index(result.Markdown, "DELTA"), strings.Index(result.Markdown, "CHARLIE")
	if deltaIdx < 0 || charlieIdx < 0 {
		t.Fatalf("fallback slides must render, got: %q", result.Markdown)
	}
	if charlieIdx < deltaIdx {
		t.Errorf("fallback must sort slides by name (DELTA first), got: %q", result.Markdown)
	}
	if strings.Contains(result.Markdown, "DECOY") {
		t.Errorf("non-xml file must not join the fallback, got: %q", result.Markdown)
	}
	if strings.Contains(result.Markdown, "XMLDECOY") {
		t.Errorf("non-slide xml must not join the fallback, got: %q", result.Markdown)
	}
}

// A single slide must render exactly once whether routed by rels or by the
// name-sorted fallback.
func TestPptxSingleSlideRendersOnce(t *testing.T) {
	data := orderedZip(t, []struct{ name, content string }{
		{"[Content_Types].xml", pptxContentTypes},
		{"ppt/presentation.xml", presXML("rId1")},
		{"ppt/_rels/presentation.xml.rels", presRelsXML([2]string{"rId1", "slides/slide1.xml"})},
		{"ppt/slides/slide1.xml", slideXML("SOLO", "10", "10")},
	})
	c := NewPptxConverter(nil)
	result, err := c.Convert(bytes.NewReader(data), StreamInfo{Extension: ".pptx"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if got := strings.Count(result.Markdown, "SOLO"); got != 1 {
		t.Errorf("SOLO rendered %d times, want 1: %q", got, result.Markdown)
	}
}

// Without presentation.xml the conversion fails instead of silently
// scanning.
func TestPptxConvertMissingPresentationFails(t *testing.T) {
	data := orderedZip(t, []struct{ name, content string }{
		{"[Content_Types].xml", pptxContentTypes},
		{"ppt/slides/slide1.xml", slideXML("ALPHA", "10", "10")},
	})
	c := NewPptxConverter(nil)
	_, err := c.Convert(bytes.NewReader(data), StreamInfo{Extension: ".pptx"})
	if err == nil {
		t.Fatal("expected get slide order error")
	}
	if !strings.Contains(err.Error(), "get slide order") {
		t.Errorf("error = %v, should contain 'get slide order'", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("get slide order error must wrap the zip error via %%w, got: %v", err)
	}
}

// A missing slide file is skipped; later slides still render.
func TestPptxConvertSkipsMissingSlide(t *testing.T) {
	data := orderedZip(t, []struct{ name, content string }{
		{"[Content_Types].xml", pptxContentTypes},
		{"ppt/presentation.xml", presXML("rId1", "rId2")},
		{"ppt/_rels/presentation.xml.rels", presRelsXML([2]string{"rId1", "slides/ghost.xml"}, [2]string{"rId2", "slides/slide2.xml"})},
		{"ppt/slides/slide2.xml", slideXML("RECOVERED", "10", "10")},
	})
	c := NewPptxConverter(nil)
	result, err := c.Convert(bytes.NewReader(data), StreamInfo{Extension: ".pptx"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !strings.Contains(result.Markdown, "RECOVERED") {
		t.Errorf("slides after a missing one must render, got: %q", result.Markdown)
	}
}

// Two shapes at the same vertical position order left-to-right regardless of
// document order.
func TestPptxSameRowShapesOrderByLeft(t *testing.T) {
	data := orderedZip(t, []struct{ name, content string }{
		{"[Content_Types].xml", pptxContentTypes},
		{"ppt/presentation.xml", presXML("rId1")},
		{"ppt/_rels/presentation.xml.rels", presRelsXML([2]string{"rId1", "slides/slide1.xml"})},
		{"ppt/slides/slide1.xml", `<?xml version="1.0"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree>` +
			slideSpXML("RIGHT", "900", "10") + slideSpXML("LEFT", "100", "10") +
			`</p:spTree></p:cSld></p:sld>`},
	})
	c := NewPptxConverter(nil)
	result, err := c.Convert(bytes.NewReader(data), StreamInfo{Extension: ".pptx"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	leftIdx, rightIdx := strings.Index(result.Markdown, "LEFT"), strings.Index(result.Markdown, "RIGHT")
	if leftIdx < 0 || rightIdx < 0 {
		t.Fatalf("both shapes must render, got: %q", result.Markdown)
	}
	if leftIdx > rightIdx {
		t.Errorf("same-row shapes must order left-to-right, got: %q", result.Markdown)
	}
}

func slideSpXML(text, x, y string) string {
	return `<p:sp><p:nvSpPr><p:cNvPr id="1" name="T"/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="` + x + `" y="` + y + `"/></a:xfrm></p:spPr><p:txBody><a:p><a:r><a:t>` + text + `</a:t></a:r></a:p></p:txBody></p:sp>`
}

// An unpositioned shape sorts after positioned ones (MaxInt64 default).
func TestPptxUnpositionedShapeSortsLast(t *testing.T) {
	data := orderedZip(t, []struct{ name, content string }{
		{"[Content_Types].xml", pptxContentTypes},
		{"ppt/presentation.xml", presXML("rId1")},
		{"ppt/_rels/presentation.xml.rels", presRelsXML([2]string{"rId1", "slides/slide1.xml"})},
		{"ppt/slides/slide1.xml", `<?xml version="1.0"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree>` +
			slideSpXML("NOWHERE", "", "") + slideSpXML("POSITIONED", "50", "50") +
			`</p:spTree></p:cSld></p:sld>`},
	})
	c := NewPptxConverter(nil)
	result, err := c.Convert(bytes.NewReader(data), StreamInfo{Extension: ".pptx"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	posIdx, nowhereIdx := strings.Index(result.Markdown, "POSITIONED"), strings.Index(result.Markdown, "NOWHERE")
	if posIdx < 0 || nowhereIdx < 0 {
		t.Fatalf("both shapes must render, got: %q", result.Markdown)
	}
	if nowhereIdx < posIdx {
		t.Errorf("unpositioned shape must sort last, got: %q", result.Markdown)
	}
}

// A shape whose xfrm has no off element must not crash position extraction.
func TestPptxXfrmWithoutOffIsSafe(t *testing.T) {
	c := &PptxConverter{}
	md := c.parseSlide([]byte(`<?xml version="1.0"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:nvSpPr><p:cNvPr id="1" name="T"/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm/></p:spPr><p:txBody><a:p><a:r><a:t>NO OFF</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`))
	if !strings.Contains(md, "NO OFF") {
		t.Errorf("shape without off must render, got: %q", md)
	}
}

func graphicFrameSlide(tblInner string) string {
	return `<?xml version="1.0"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:graphicFrame><p:nvGraphicFramePr><p:cNvPr id="1" name="Table"/><p:nvPr/></p:nvGraphicFramePr><p:xfrm><a:off x="1" y="1"/></p:xfrm><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/table"><a:tbl>` + tblInner + `</a:tbl></a:graphicData></a:graphic></p:graphicFrame></p:spTree></p:cSld></p:sld>`
}

func trTC(cells ...string) string {
	var b strings.Builder
	b.WriteString(`<a:tr>`)
	for _, c := range cells {
		if c == "" {
			b.WriteString(`<a:tc/>`)
			continue
		}
		b.WriteString(`<a:tc><a:txBody><a:p><a:r><a:t>` + c + `</a:t></a:r></a:p></a:txBody></a:tc>`)
	}
	b.WriteString(`</a:tr>`)
	return b.String()
}

// A one-row table still renders as a table.
func TestPptxSingleRowTable(t *testing.T) {
	c := &PptxConverter{}
	md := c.parseSlide([]byte(graphicFrameSlide(trTC("H1", "H2"))))
	if !strings.Contains(md, "H1") || !strings.Contains(md, "H2") {
		t.Errorf("single-row table must render, got: %q", md)
	}
	want := "| H1 | H2 |\n|----|----|\n"
	if md != want {
		t.Errorf("single-row table = %q, want %q", md, want)
	}
}

// A tc without txBody yields an empty cell, keeping columns aligned.
func TestPptxTableEmptyCellKeepsColumns(t *testing.T) {
	c := &PptxConverter{}
	md := c.parseSlide([]byte(graphicFrameSlide(trTC("H1", "", "H3") + trTC("a", "b", "c"))))
	want := "| H1 |   | H3 |\n|----|---|----|\n| a  | b | c  |\n"
	if md != want {
		t.Errorf("table = %q, want %q", md, want)
	}
}

func picSpXML(alt, x, y string) string {
	off := ""
	if x != "" || y != "" {
		off = `<a:off x="` + x + `" y="` + y + `"/>`
	}
	return `<p:pic><p:nvPicPr><p:cNvPr id="7" name="P" descr="` + alt + `"/><p:nvPr/></p:nvPicPr><p:spPr><a:xfrm>` + off + `</a:xfrm></p:spPr></p:pic>`
}

// An unpositioned shape sorts after a positioned one at the same height
// (left defaults to MaxInt64).
func TestPptxUnpositionedSameRowSortsLast(t *testing.T) {
	c := &PptxConverter{}
	noXSp := `<p:sp><p:nvSpPr><p:cNvPr id="1" name="T"/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off y="10"/></a:xfrm></p:spPr><p:txBody><a:p><a:r><a:t>NOX</a:t></a:r></a:p></p:txBody></p:sp>`
	md := c.parseSlide([]byte(`<?xml version="1.0"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree>` +
		noXSp + slideSpXML("PLACED", "100", "10") +
		`</p:spTree></p:cSld></p:sld>`))
	noxIdx, placedIdx := strings.Index(md, "NOX"), strings.Index(md, "PLACED")
	if placedIdx < 0 || noxIdx < 0 {
		t.Fatalf("both shapes must render, got: %q", md)
	}
	if noxIdx < placedIdx {
		t.Errorf("shape without x must sort after a positioned one at the same height, got: %q", md)
	}
}

// Unpositioned pictures also sort last (picture position defaults).
func TestPptxUnpositionedPictureSortsLast(t *testing.T) {
	c := &PptxConverter{}
	noXPic := `<p:pic><p:nvPicPr><p:cNvPr id="7" name="P" descr="noxic"/><p:nvPr/></p:nvPicPr><p:spPr><a:xfrm><a:off y="10"/></a:xfrm></p:spPr></p:pic>`
	md := c.parseSlide([]byte(`<?xml version="1.0"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree>` +
		noXPic + picSpXML("anchored", "100", "10") +
		`</p:spTree></p:cSld></p:sld>`))
	noxIdx, anchoredIdx := strings.Index(md, "noxic"), strings.Index(md, "anchored")
	if noxIdx < 0 || anchoredIdx < 0 {
		t.Fatalf("both pictures must render, got: %q", md)
	}
	if noxIdx < anchoredIdx {
		t.Errorf("picture without x must sort after a positioned one at the same height, got: %q", md)
	}
}

// A picture with no xfrm at all keeps the MaxInt64 top sentinel from the
// extractPic literal, so it sorts after a positioned shape at any height.
func TestPptxNoXfrmPictureSortsLast(t *testing.T) {
	c := &PptxConverter{}
	noXfrmPic := `<p:pic><p:nvPicPr><p:cNvPr id="8" name="N" descr="noxfrm"/><p:nvPr/></p:nvPicPr><p:spPr/></p:pic>`
	md := c.parseSlide([]byte(`<?xml version="1.0"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree>` +
		noXfrmPic + picSpXML("anchored", "100", "10") +
		`</p:spTree></p:cSld></p:sld>`))
	noXfrmIdx, anchoredIdx := strings.Index(md, "noxfrm"), strings.Index(md, "anchored")
	if noXfrmIdx < 0 || anchoredIdx < 0 {
		t.Fatalf("both pictures must render, got: %q", md)
	}
	if noXfrmIdx < anchoredIdx {
		t.Errorf("picture without xfrm must sort after a positioned one, got: %q", md)
	}
}
