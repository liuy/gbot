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
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestXlsxLeadingEmptyRow(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.SetCellValue("Sheet1", "B2", "Item"))
	must(f.SetCellValue("Sheet1", "C2", "Price"))
	must(f.SetCellValue("Sheet1", "D2", "Qty"))
	must(f.SetCellValue("Sheet1", "B3", "Widget"))
	must(f.SetCellValue("Sheet1", "C3", "9.9"))
	must(f.SetCellValue("Sheet1", "D3", "3"))
	var buf bytes.Buffer
	must(f.Write(&buf))

	m := New()
	result, err := m.ConvertReader(bytes.NewReader(buf.Bytes()), StreamInfo{Extension: ".xlsx"})
	if err != nil {
		t.Fatalf("ConvertReader: %v", err)
	}
	md := result.Markdown
	for _, want := range []string{"Item", "Price", "Qty", "Widget", "9.9", "3"} {
		if !strings.Contains(md, want) {
			t.Errorf("cell %q lost from xlsx markdown: %s", want, md)
		}
	}
	if !strings.Contains(md, "| --- | --- | --- |") {
		t.Errorf("expected 3-column separator (the data width), got: %s", md)
	}
	if strings.Contains(md, "| \n| \n") {
		t.Errorf("regression: output still has empty-header truncation, got: %s", md)
	}
}

func TestDocxTableWideRowNotTruncated(t *testing.T) {
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
        <w:tc><w:p><w:r><w:t>3</w:t></w:r></w:p></w:tc>
        <w:tc><w:p><w:r><w:t>4</w:t></w:r></w:p></w:tc>
      </w:tr>
    </w:tbl>
  </w:body>
</w:document>`)

	html := c.documentToHTML(xmlData, nil, nil, nil, nil, zr)
	for _, want := range []string{"A", "B", "1", "2", "3", "4"} {
		if !strings.Contains(html, want) {
			t.Errorf("cell %q lost from docx table HTML: %s", want, html)
		}
	}
	if !strings.Contains(html, "<table>") {
		t.Errorf("expected <table> in docx HTML: %s", html)
	}
}

func TestPptxTableWideRowNotTruncated(t *testing.T) {
	c := &PptxConverter{}
	rows := [][]string{
		{"H1", "H2"},
		{"a", "b", "c", "d"},
	}
	got := c.tableToMarkdown(rows)
	for _, want := range []string{"H1", "H2", "a", "b", "c", "d"} {
		if !strings.Contains(got, want) {
			t.Errorf("cell %q lost from pptx table markdown: %s", want, got)
		}
	}
}
