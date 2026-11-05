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
	"os"
	"testing"
)

func patchAt(t *testing.T, base []byte, off int, patch []byte) []byte {
	t.Helper()
	out := make([]byte, len(base))
	copy(out, base)
	copy(out[off:], patch)
	return out
}

// A sheet with zero ROW records still has MaxRow=0, so the row loop asks the
// library for row 0 and extrame/xls Row() nil-derefs on the missing map entry
// instead of returning nil. The sheet must be treated as empty and skipped.
func TestXlsConvertEmptySheetSkipped(t *testing.T) {
	base, err := os.ReadFile("testdata/xls_variants.xls")
	if err != nil {
		t.Fatal(err)
	}
	// Neutralize the Single sheet's ROW (id 0x0208 @0x6f8) and LABELSST
	// (id 0x00fd @0x70c) records so the sheet parses to zero rows.
	data := patchAt(t, base, 0x6f8, []byte{0xff, 0xff})
	data = patchAt(t, data, 0x70c, []byte{0xff, 0xff})

	result, err := NewXlsConverter().Convert(bytes.NewReader(data), StreamInfo{Extension: ".xls"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	want := "## Ragged\n| h1 | h2 |  | \n| --- | --- | --- | \n| a | b | c | \n\n"
	if result.Markdown != want {
		t.Errorf("empty Single sheet must be skipped; got %q, want %q", result.Markdown, want)
	}
}

// Rows {0,3} recorded with 1-2 absent: Row(1) nil-derefs inside the library.
// Missing rows must contribute nothing while recorded rows still render, so
// the output equals the unpatched fixture's markdown.
func TestXlsConvertSparseRowsGapTolerated(t *testing.T) {
	base, err := os.ReadFile("testdata/xls_variants.xls")
	if err != nil {
		t.Fatal(err)
	}
	// Ragged sheet's second ROW record @0x87e: row index field (id+size are
	// the first 4 bytes) patched from 1 to 3. The span moves to row 3 while
	// the LABELSST cells still name row 1, so the sheet parses as rows
	// {0: header, 1: cells with zero span, 3: span with no cells} and row 2
	// missing - the gap the library panics on. The gap renders as nothing;
	// the lazily-created rows render empty per their zero-Lcell spans.
	data := patchAt(t, base, 0x87e+4, []byte{0x03, 0x00})

	result, err := NewXlsConverter().Convert(bytes.NewReader(data), StreamInfo{Extension: ".xls"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	want := "## Single\n| only | \n| --- | \n\n## Ragged\n| h1 | h2 |  | \n| --- | --- | --- | \n|  |  |  | \n|  |  |  | \n\n"
	if result.Markdown != want {
		t.Errorf("row gaps must be skipped, not crash; got %q, want %q", result.Markdown, want)
	}
}
