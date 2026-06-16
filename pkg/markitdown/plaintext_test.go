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

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
)

func TestPlainTextAcceptsExtensions(t *testing.T) {
	c := NewPlainTextConverter()
	tests := []struct {
		ext  string
		want bool
	}{
		{".txt", true},
		{".text", true},
		{".md", true},
		{".markdown", true},
		{".json", true},
		{".jsonl", true},
		{".html", false},
		{".csv", false},
		{".xml", false},
		{"", false},
	}
	for _, tt := range tests {
		got := c.Accepts(StreamInfo{Extension: tt.ext})
		if got != tt.want {
			t.Errorf("Accepts(Extension=%q) = %v, want %v", tt.ext, got, tt.want)
		}
	}
}

func TestPlainTextAcceptsMIME(t *testing.T) {
	c := NewPlainTextConverter()
	tests := []struct {
		mime string
		want bool
	}{
		{"text/plain", true},
		{"text/markdown", true},
		{"text/csv", true},
		{"TEXT/PLAIN", true},
		{"application/json", true},
		{"application/jsonl", true},
		{"application/markdown", true},
		{"application/xml", false},
		{"image/png", false},
		{"", false},
	}
	for _, tt := range tests {
		got := c.Accepts(StreamInfo{MIMEType: tt.mime})
		if got != tt.want {
			t.Errorf("Accepts(MIMEType=%q) = %v, want %v", tt.mime, got, tt.want)
		}
	}
}

func TestPlainTextConvert(t *testing.T) {
	m := New()
	result, err := m.ConvertReader(strings.NewReader("hello world"), StreamInfo{
		Extension: ".txt",
		MIMEType:  "text/plain",
	})
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	if result.Markdown != "hello world" {
		t.Errorf("Markdown = %q, want 'hello world'", result.Markdown)
	}
}

func TestPlainTextConvertWithCharset(t *testing.T) {
	// Encode "hello" in ISO-8859-1
	encoded, err := charmap.ISO8859_1.NewEncoder().Bytes([]byte("café"))
	if err != nil {
		t.Fatalf("encode error: %v", err)
	}
	m := New()
	result, err := m.ConvertReader(bytes.NewReader(encoded), StreamInfo{
		Extension: ".txt",
		MIMEType:  "text/plain",
		Charset:   "iso-8859-1",
	})
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	if !strings.Contains(result.Markdown, "café") {
		t.Errorf("Markdown = %q, should contain 'café'", result.Markdown)
	}
}

func TestPlainTextConvertWithUnknownCharset(t *testing.T) {
	// Unknown charset falls back to detection
	m := New()
	result, err := m.ConvertReader(strings.NewReader("hello"), StreamInfo{
		Extension: ".txt",
		MIMEType:  "text/plain",
		Charset:   "nonexistent-charset",
	})
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	if result.Markdown != "hello" {
		t.Errorf("Markdown = %q, want 'hello'", result.Markdown)
	}
}

func TestHasHighBytes(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"ascii_only", []byte("hello world"), false},
		{"empty", []byte{}, false},
		{"high_bytes", []byte{0x80, 0x41}, true},
		{"utf8_multibyte", []byte{0xC3, 0xA9}, true}, // é
		{"mixed", []byte("hello\x80world"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasHighBytes(tt.data)
			if got != tt.want {
				t.Errorf("hasHighBytes(%v) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

func TestScoreDecodedText(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		confidence int
	}{
		{"ascii_text", "hello world", 50},
		{"with_cjk", "佐藤太郎", 50},
		{"with_replacement", "hello\ufffdworld", 50},
		{"with_control", "hello\x01world", 50},
		{"empty", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := scoreDecodedText(tt.text, tt.confidence)
			if score == 0 && len(tt.text) > 0 {
				t.Errorf("scoreDecodedText(%q, %d) = 0, should be non-zero for non-empty text", tt.text, tt.confidence)
			}
		})
	}
}

func TestScoreDecodedTextPenalizesReplacement(t *testing.T) {
	good := scoreDecodedText("hello world", 50)
	bad := scoreDecodedText("hello\ufffd\ufffd\ufffd", 50)
	if bad >= good {
		t.Errorf("replacement-heavy text score %d should be < clean text score %d", bad, good)
	}
}

func TestScoreDecodedTextBonusesCJK(t *testing.T) {
	noCJK := scoreDecodedText("abcdef", 50)
	withCJK := scoreDecodedText("佐藤太郎abcdef", 50)
	if withCJK <= noCJK {
		t.Errorf("CJK text score %d should be > ascii-only score %d", withCJK, noCJK)
	}
}

func TestLookupEncoding(t *testing.T) {
	tests := []struct {
		name    string
		charset string
		wantNil bool
	}{
		{"utf8", "utf-8", false},
		{"utf8_nohyphen", "utf8", false},
		{"utf8bom", "utf-8-bom", false},
		{"utf16le", "utf-16le", false},
		{"utf16be", "utf-16be", false},
		{"latin1", "latin1", false},
		{"iso88591", "iso-8859-1", false},
		{"iso88592", "iso-8859-2", false},
		{"iso88595", "iso-8859-5", false},
		{"iso88596", "iso-8859-6", false},
		{"iso88597", "iso-8859-7", false},
		{"iso88598", "iso-8859-8", false},
		{"iso88599", "iso-8859-9", false},
		{"iso885915", "iso-8859-15", false},
		{"windows1250", "windows-1250", false},
		{"windows1251", "windows-1251", false},
		{"windows1252", "windows-1252", false},
		{"windows1253", "windows-1253", false},
		{"windows1254", "windows-1254", false},
		{"windows1255", "windows-1255", false},
		{"windows1256", "windows-1256", false},
		{"koi8r", "koi8-r", false},
		{"shiftjis", "shift_jis", false},
		{"sjis", "sjis", false},
		{"cp932", "cp932", false},
		{"windows31j", "windows-31j", false},
		{"eucjp", "euc-jp", false},
		{"iso2022jp", "iso-2022-jp", false},
		{"euckr", "euc-kr", false},
		{"cp949", "cp949", false},
		{"gb2312", "gb2312", false},
		{"gbk", "gbk", false},
		{"cp936", "cp936", false},
		{"gb18030", "gb18030", false},
		{"big5", "big5", false},
		{"cp950", "cp950", false},
		{"ascii", "ascii", false},
		{"usascii", "us-ascii", false},
		{"unknown", "nonexistent-charset", true},
		{"empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc := lookupEncoding(tt.charset)
			if tt.wantNil && enc != nil {
				t.Errorf("lookupEncoding(%q) = %v, want nil", tt.charset, enc)
			}
			if !tt.wantNil && enc == nil {
				t.Errorf("lookupEncoding(%q) = nil, want non-nil", tt.charset)
			}
		})
	}
}

func TestLookupEncodingReturnsCorrectType(t *testing.T) {
	if enc := lookupEncoding("utf-8"); enc != unicode.UTF8 {
		t.Errorf("lookupEncoding(utf-8) returned wrong encoding")
	}
	if enc := lookupEncoding("iso-8859-1"); enc != charmap.ISO8859_1 {
		t.Errorf("lookupEncoding(iso-8859-1) returned wrong encoding")
	}
	if enc := lookupEncoding("shift_jis"); enc != japanese.ShiftJIS {
		t.Errorf("lookupEncoding(shift_jis) returned wrong encoding")
	}
	if enc := lookupEncoding("euc-kr"); enc != korean.EUCKR {
		t.Errorf("lookupEncoding(euc-kr) returned wrong encoding")
	}
	if enc := lookupEncoding("gbk"); enc != simplifiedchinese.GBK {
		t.Errorf("lookupEncoding(gbk) returned wrong encoding")
	}
	if enc := lookupEncoding("big5"); enc != traditionalchinese.Big5 {
		t.Errorf("lookupEncoding(big5) returned wrong encoding")
	}
}

func TestDecodeWithDetectionASCII(t *testing.T) {
	got := decodeWithDetection([]byte("plain ascii text"))
	want := "plain ascii text"
	if got != want {
		t.Errorf("decodeWithDetection() = %q, want %q", got, want)
	}
}

func TestDecodeWithDetectionEmpty(t *testing.T) {
	got := decodeWithDetection([]byte{})
	if got != "" {
		t.Errorf("decodeWithDetection(empty) = %q, want empty", got)
	}
}

func TestDecodeWithDetectionUTF8(t *testing.T) {
	input := []byte("héllo wörld")
	got := decodeWithDetection(input)
	want := "héllo wörld"
	if got != want {
		t.Errorf("decodeWithDetection() = %q, want %q", got, want)
	}
}

func TestDecodeWithDetectionValidUTF8WithHighBytes(t *testing.T) {
	// UTF-8 encoded Japanese
	input := []byte("佐藤太郎")
	got := decodeWithDetection(input)
	if !strings.Contains(got, "佐藤") {
		t.Errorf("decodeWithDetection() = %q, should contain Japanese characters", got)
	}
}
