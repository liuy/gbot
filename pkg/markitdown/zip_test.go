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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZipConvertBasic(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")
	files := map[string]string{
		"readme.txt": "This is a readme file",
		"data.json":  `{"key": "value"}`,
	}
	if err := createZipFile(zipPath, files); err != nil {
		t.Fatal(err)
	}

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}

	if !strings.Contains(result.Markdown, "readme.txt") {
		t.Errorf("Markdown should contain readme.txt, got: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "This is a readme file") {
		t.Errorf("Markdown should contain readme content, got: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "data.json") {
		t.Errorf("Markdown should contain data.json, got: %s", result.Markdown)
	}
}

func TestZipConvertEmptyFilename(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")
	if err := createZipFile(zipPath, map[string]string{"file.txt": "content"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}

	c := NewZipConverter(New())
	result, err := c.Convert(bytes.NewReader(data), StreamInfo{
		Extension: ".zip",
		Filename:  "",
	})
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	if !strings.Contains(result.Markdown, "archive") {
		t.Errorf("Markdown should mention 'archive' when filename is empty, got: %s", result.Markdown)
	}
}

func TestZipConvertWithEmptyFile(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")
	if err := createZipFile(zipPath, map[string]string{
		"empty.txt": "",
		"full.txt":  "has content",
	}); err != nil {
		t.Fatal(err)
	}

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	if strings.Contains(result.Markdown, "empty.txt") {
		t.Errorf("Markdown should NOT contain empty.txt since it has no content, got: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "full.txt") {
		t.Errorf("Markdown should contain full.txt, got: %s", result.Markdown)
	}
}

func TestZipConvertWithDirectoryEntries(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")

	// Create zip with directory entry
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	// Add a directory entry
	hdr := &zip.FileHeader{
		Name:   "subdir/",
		Method: zip.Store,
	}
	hdr.SetMode(0o755 | os.ModeDir)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(nil); err != nil {
		t.Fatal(err)
	}
	// Add a file entry
	w2, err := zw.Create("subdir/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w2.Write([]byte("in subdir")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	if !strings.Contains(result.Markdown, "subdir/file.txt") {
		t.Errorf("Markdown should contain subdir/file.txt, got: %s", result.Markdown)
	}
}

func TestZipConvertWithUnconvertableFile(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")
	// Use a binary format that no converter handles
	binaryData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} // PNG header
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("file.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(binaryData); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	// Binary file should not be in the output
	if strings.Contains(result.Markdown, "file.bin") {
		t.Errorf("Markdown should NOT contain unconvertable binary file, got: %s", result.Markdown)
	}
}

func TestZipConvertInvalidZip(t *testing.T) {
	c := NewZipConverter(New())
	_, err := c.Convert(bytes.NewReader([]byte("not a zip file")), StreamInfo{
		Extension: ".zip",
	})
	if err == nil {
		t.Fatal("expected error for invalid ZIP")
	}
	if !strings.Contains(err.Error(), "open ZIP") {
		t.Errorf("error = %v, should contain 'open ZIP'", err)
	}
}

func TestZipAcceptsExtension(t *testing.T) {
	c := NewZipConverter(nil)
	if !c.Accepts(StreamInfo{Extension: ".zip"}) {
		t.Error("Accepts(.zip) should be true")
	}
	if !c.Accepts(StreamInfo{MIMEType: "application/zip"}) {
		t.Error("Accepts(application/zip) should be true")
	}
	if !c.Accepts(StreamInfo{MIMEType: "application/zip+something"}) {
		t.Error("Accepts(application/zip+something) should be true")
	}
	if c.Accepts(StreamInfo{Extension: ".tar"}) {
		t.Error("Accepts(.tar) should be false")
	}
	if c.Accepts(StreamInfo{MIMEType: "application/x-tar"}) {
		t.Error("Accepts(application/x-tar) should be false")
	}
}
