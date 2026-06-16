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

func TestMimeFromExtension(t *testing.T) {
	tests := []struct {
		ext  string
		want string
	}{
		{".pdf", "application/pdf"},
		{".docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{".pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
		{".xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{".xls", "application/vnd.ms-excel"},
		{".html", "text/html"},
		{".htm", "text/html"},
		{".csv", "text/csv"},
		{".txt", "text/plain"},
		{".text", "text/plain"},
		{".md", "text/markdown"},
		{".markdown", "text/markdown"},
		{".json", "application/json"},
		{".jsonl", "application/jsonl"},
		{".xml", "text/xml"},
		{".rss", "application/rss+xml"},
		{".atom", "application/atom+xml"},
		{".epub", "application/epub+zip"},
		{".zip", "application/zip"},
		{".ipynb", "application/x-ipynb+json"},
		{".unknown", "application/octet-stream"},
		{"", "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			got := mimeFromExtension(tt.ext)
			if got != tt.want {
				t.Errorf("mimeFromExtension(%q) = %q, want %q", tt.ext, got, tt.want)
			}
		})
	}
}

func TestDetectMIMETypeFromContent(t *testing.T) {
	// PNG signature
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	r := bytes.NewReader(pngData)
	got := detectMIMEType(r, ".txt")
	if got != "image/png" {
		t.Errorf("detectMIMEType(png data) = %q, want image/png", got)
	}
}

func TestDetectMIMETypeFallbackToExtension(t *testing.T) {
	// Plain text - mimetype returns application/octet-stream for ambiguous data
	// so should fall back to extension
	r := bytes.NewReader([]byte("plain text"))
	got := detectMIMEType(r, ".txt")
	if !strings.HasPrefix(got, "text/plain") {
		t.Errorf("detectMIMEType(plain text, .txt) = %q, want text/plain prefix", got)
	}
}

func TestConvertNoConverterAccepts(t *testing.T) {
	m := New()
	_, err := m.ConvertReader(strings.NewReader("data"), StreamInfo{
		Extension: ".unknownext",
		MIMEType:  "application/x-unknown",
	})
	if err == nil {
		t.Fatal("expected error for unknown format, got nil")
	}
	if !IsUnsupportedFormat(err) {
		t.Errorf("error should be UnsupportedFormatError, got %T: %v", err, err)
	}
}

func TestConvertConverterError(t *testing.T) {
	m := New()
	// Register a converter that accepts but fails
	m.RegisterConverter("failing", &failingConverter{}, 0.0)
	_, err := m.ConvertReader(strings.NewReader("data"), StreamInfo{
		Extension: ".fail",
	})
	if err == nil {
		t.Fatal("expected error from failing converter")
	}

	var convErr *ConversionError
	if !errors.As(err, &convErr) {
		t.Errorf("error should be *ConversionError, got %T: %v", err, err)
	}
	if len(convErr.Attempts) != 1 {
		t.Errorf("expected 1 attempt, got %d", len(convErr.Attempts))
	}
	if convErr.Attempts[0].Converter != "failing" {
		t.Errorf("attempt converter = %q, want 'failing'", convErr.Attempts[0].Converter)
	}
}

type failingConverter struct{}

func (c *failingConverter) Accepts(info StreamInfo) bool {
	return info.Extension == ".fail"
}

func (c *failingConverter) Convert(r io.ReadSeeker, info StreamInfo) (*DocumentConverterResult, error) {
	return nil, errors.New("intentional failure")
}

func TestConvertFileMissingFile(t *testing.T) {
	m := New()
	_, err := m.ConvertFile("/nonexistent/path/to/file.txt")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "open file") {
		t.Errorf("error = %v, should contain 'open file'", err)
	}
}

func TestConvertFilePlainText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello world content"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	result, err := m.ConvertFile(path)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	if result.Markdown != "hello world content" {
		t.Errorf("Markdown = %q, want 'hello world content'", result.Markdown)
	}
}

func TestConvertAutoDetectFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auto.txt")
	if err := os.WriteFile(path, []byte("auto content"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	result, err := m.Convert(path)
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	if result.Markdown != "auto content" {
		t.Errorf("Markdown = %q, want 'auto content'", result.Markdown)
	}
}

func TestRegisterConverter(t *testing.T) {
	m := New()
	initialCount := len(m.converters)
	m.RegisterConverter("custom", &failingConverter{}, 5.0)
	if len(m.converters) != initialCount+1 {
		t.Errorf("expected %d converters, got %d", initialCount+1, len(m.converters))
	}
	// Verify it's sorted by priority
	for i := 1; i < len(m.converters); i++ {
		if m.converters[i-1].priority > m.converters[i].priority {
			t.Errorf("converters not sorted: [%d].priority=%.1f > [%d].priority=%.1f",
				i-1, m.converters[i-1].priority, i, m.converters[i].priority)
		}
	}
}

func TestConvertNormalizesOutput(t *testing.T) {
	m := New()
	// Register a converter that produces un-normalized output
	m.RegisterConverter("whitespace", &whitespaceConverter{}, -1.0)
	result, err := m.ConvertReader(strings.NewReader("data"), StreamInfo{Extension: ".ws"})
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	// normalizeOutput should have trimmed trailing whitespace
	if strings.Contains(result.Markdown, "   \n") {
		t.Errorf("output should be normalized, got %q", result.Markdown)
	}
}

type whitespaceConverter struct{}

func (c *whitespaceConverter) Accepts(info StreamInfo) bool {
	return info.Extension == ".ws"
}

func (c *whitespaceConverter) Convert(r io.ReadSeeker, info StreamInfo) (*DocumentConverterResult, error) {
	return &DocumentConverterResult{Markdown: "text   \n   more"}, nil
}
