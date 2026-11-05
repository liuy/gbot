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
	"strings"
	"testing"
)

func TestXlsxConstructorReturnsInstance(t *testing.T) {
	if NewXlsxConverter() == nil {
		t.Fatal("NewXlsxConverter() = nil, want non-nil")
	}
}

func TestXlsxAcceptsByMime(t *testing.T) {
	c := NewXlsxConverter()
	if !c.Accepts(StreamInfo{MIMEType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}) {
		t.Error("Accepts(spreadsheetml mime) = false, want true")
	}
}

func TestXlsxConvertOpenErrorPreservesCause(t *testing.T) {
	c := NewXlsxConverter()
	_, err := c.Convert(strings.NewReader("not xlsx"), StreamInfo{Extension: ".xlsx"})
	if err == nil {
		t.Fatal("expected open error")
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("open error must wrap the excelize error via %%w, got: %v", err)
	}
}

// ghostSheetZip builds a workbook with three sheets: Bad (unparsable XML),
// Empty (valid, no rows) and Good (one inline cell). They exercise the
// GetRows error path, the zero-row skip, and the happy path in order.
func ghostSheetZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string]string{
		"[Content_Types].xml":        `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/xl/worksheets/sheet2.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/worksheets/sheet3.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`,
		"_rels/.rels":                `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/workbook.xml":            `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Ghost" sheetId="1" r:id="rId9"/><sheet name="Empty" sheetId="2" r:id="rId2"/><sheet name="Good" sheetId="3" r:id="rId3"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet3.xml"/></Relationships>`,
		"xl/worksheets/sheet2.xml":   `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData/></worksheet>`,
		"xl/worksheets/sheet3.xml":   `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>GoodCell</t></is></c></row></sheetData></worksheet>`,
	}
	for n, c := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestXlsxConvertSkipsBrokenAndEmptySheets(t *testing.T) {
	c := NewXlsxConverter()
	result, err := c.Convert(bytes.NewReader(ghostSheetZip(t)), StreamInfo{Extension: ".xlsx"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if strings.Contains(result.Markdown, "## Ghost") {
		t.Errorf("ghost sheet must be skipped, got: %q", result.Markdown)
	}
	if strings.Contains(result.Markdown, "## Empty") {
		t.Errorf("zero-row sheet must be skipped, got: %q", result.Markdown)
	}
	want := "## Good\n| GoodCell | \n| --- | \n\n"
	if result.Markdown != want {
		t.Errorf("Markdown = %q, want %q", result.Markdown, want)
	}
}
