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
	"encoding/csv"
	"errors"
	"io"
	"strings"
	"testing"

	"golang.org/x/text/encoding/unicode"
)

func TestCsvConstructorReturnsInstance(t *testing.T) {
	if NewCsvConverter() == nil {
		t.Fatal("NewCsvConverter() = nil, want non-nil")
	}
}

func TestCsvAcceptsApplicationCSVMime(t *testing.T) {
	c := NewCsvConverter()
	// Both mime arms are load-bearing: "application/csv" covers servers that
	// send the RFC misspelling, "text/csv; charset=..." covers parameterized
	// headers from ConvertURL.
	if !c.Accepts(StreamInfo{MIMEType: "application/csv"}) {
		t.Error("Accepts(application/csv) = false, want true")
	}
	if !c.Accepts(StreamInfo{MIMEType: "text/csv; charset=utf-8"}) {
		t.Error("Accepts(text/csv; charset=utf-8) = false, want true")
	}
}

func TestCsvConvertReadErrorPreservesCause(t *testing.T) {
	c := NewCsvConverter()
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".csv"})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Convert read error must wrap the reader error via %%w, got: %v", err)
	}
}

// A charset hint must win over content sniffing: UTF-16LE bytes are NUL-laced
// ASCII that detection would pass through unchanged.
func TestCsvConvertCharsetHintUsed(t *testing.T) {
	encoded, err := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewEncoder().Bytes([]byte("a,b\n1,2\n"))
	if err != nil {
		t.Fatal(err)
	}
	c := NewCsvConverter()
	result, err := c.Convert(strings.NewReader(string(encoded)), StreamInfo{
		Extension: ".csv",
		Charset:   "utf-16le",
	})
	if err != nil {
		t.Fatalf("Convert with charset hint: %v", err)
	}
	want := "| a | b | \n| --- | --- | \n| 1 | 2 | \n"
	if result.Markdown != want {
		t.Errorf("Markdown = %q, want %q", result.Markdown, want)
	}
}

// Variable field counts must be allowed (FieldsPerRecord = -1) while the
// rendered table still truncates to the header width (CSV mode).
func TestCsvConvertRaggedRowsTruncatedToHeader(t *testing.T) {
	c := NewCsvConverter()
	result, err := c.Convert(strings.NewReader("a,b\n1,2,3\n"), StreamInfo{Extension: ".csv"})
	if err != nil {
		t.Fatalf("ragged CSV must not be a parse error: %v", err)
	}
	want := "| a | b | \n| --- | --- | \n| 1 | 2 | \n"
	if result.Markdown != want {
		t.Errorf("Markdown = %q, want %q", result.Markdown, want)
	}
}

func TestCsvConvertSingleRecord(t *testing.T) {
	c := NewCsvConverter()
	result, err := c.Convert(strings.NewReader("h1,h2\n"), StreamInfo{Extension: ".csv"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	want := "| h1 | h2 | \n| --- | --- | \n"
	if result.Markdown != want {
		t.Errorf("Markdown = %q, want %q", result.Markdown, want)
	}
}

func TestCsvConvertParseError(t *testing.T) {
	c := NewCsvConverter()
	_, err := c.Convert(strings.NewReader("a,\"unterminated\n"), StreamInfo{Extension: ".csv"})
	if err == nil {
		t.Fatal("expected parse error for unterminated quote")
	}
	if _, ok := errors.AsType[*csv.ParseError](err); !ok {
		t.Errorf("parse error must wrap csv.ParseError via %%w, got: %v", err)
	}
	if !strings.Contains(err.Error(), "parse CSV") {
		t.Errorf("error = %v, should contain 'parse CSV'", err)
	}
}
