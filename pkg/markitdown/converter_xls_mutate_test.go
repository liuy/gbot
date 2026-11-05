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
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestXlsConstructorReturnsInstance(t *testing.T) {
	if NewXlsConverter() == nil {
		t.Fatal("NewXlsConverter() = nil, want non-nil")
	}
}

func TestXlsAcceptsByMime(t *testing.T) {
	c := NewXlsConverter()
	if !c.Accepts(StreamInfo{MIMEType: "application/vnd.ms-excel"}) {
		t.Error("Accepts(application/vnd.ms-excel) = false, want true")
	}
}

func TestXlsConvertTempFileCreateError(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))
	c := NewXlsConverter()
	_, err := c.Convert(strings.NewReader("whatever"), StreamInfo{Extension: ".xls"})
	if err == nil {
		t.Fatal("expected create temp file error")
	}
	if !strings.Contains(err.Error(), "create temp file") {
		t.Errorf("error = %v, should contain 'create temp file'", err)
	}
	if _, ok := errors.AsType[*os.PathError](err); !ok {
		t.Errorf("create temp error must wrap os.PathError via %%w, got: %v", err)
	}
}

func TestXlsConvertWriteTempError(t *testing.T) {
	c := NewXlsConverter()
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".xls"})
	if err == nil {
		t.Fatal("expected write temp file error")
	}
	if !strings.Contains(err.Error(), "write temp file") {
		t.Errorf("error = %v, should contain 'write temp file'", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("write temp error must wrap the reader error via %%w, got: %v", err)
	}
}

func TestXlsConvertOpenErrorPreservesCause(t *testing.T) {
	c := NewXlsConverter()
	_, err := c.Convert(strings.NewReader("not an xls file"), StreamInfo{Extension: ".xls"})
	if err == nil {
		t.Fatal("expected open error")
	}
	if !strings.Contains(err.Error(), "open XLS") {
		t.Errorf("error = %v, should contain 'open XLS'", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("open error must wrap the xls error via %%w, got: %v", err)
	}
}

// The committed fixture has two sheets: Single (one row) and Ragged (header of
// 2 cells above a data row of 3). Single pins the one-row happy path; Ragged
// pins that xls tables never truncate to the header width.
func TestXlsConvertVariantsFixture(t *testing.T) {
	c := NewXlsConverter()
	result, err := c.Convert(strings.NewReader(string(mustRead(t, "testdata/xls_variants.xls"))), StreamInfo{Extension: ".xls"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !strings.Contains(result.Markdown, "## Single\n| only | \n") {
		t.Errorf("single-row sheet must render, got: %q", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "| h1 | h2 |  | \n") {
		t.Errorf("ragged header must pad to widest row, got: %q", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "| a | b | c | \n") {
		t.Errorf("ragged data row must keep all 3 cells, got: %q", result.Markdown)
	}
}

// A sheet whose BOUNDSHEET name length is zero must fall back to Sheet<N>
// (1-based). The fixture's first sheet is patched in-memory at its
// BOUNDSHEET cch byte, two bytes before the name text.
func TestXlsConvertUnnamedSheetFallback(t *testing.T) {
	data := mustRead(t, "testdata/xls_variants.xls")
	idx := bytes.Index(data, []byte("Single"))
	if idx < 2 || data[idx-12] != 0x85 {
		t.Fatalf("fixture layout changed: no BOUNDSHEET before name at %d", idx)
	}
	patched := make([]byte, len(data))
	copy(patched, data)
	patched[idx-2] = 0 // cch: 6 -> 0

	c := NewXlsConverter()
	result, err := c.Convert(bytes.NewReader(patched), StreamInfo{Extension: ".xls"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !strings.Contains(result.Markdown, "## Sheet1\n| only | \n") {
		t.Errorf("unnamed sheet must use Sheet1 fallback, got: %q", result.Markdown)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
