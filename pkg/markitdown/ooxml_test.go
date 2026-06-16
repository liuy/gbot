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

func TestParseRelationshipsFromReader(t *testing.T) {
	relsXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://example.com/image" Target="media/image1.png"/>
  <Relationship Id="rId2" Type="http://example.com/hyperlink" Target="https://example.com" TargetMode="External"/>
</Relationships>`

	zr := makeZipWithFile(t, "word/_rels/document.xml.rels", relsXML)
	rels, err := ParseRelationshipsFromReader(zr, "word/_rels/document.xml.rels")
	if err != nil {
		t.Fatalf("ParseRelationshipsFromReader error: %v", err)
	}
	if len(rels) != 2 {
		t.Fatalf("expected 2 relationships, got %d", len(rels))
	}
	r1, ok := rels["rId1"]
	if !ok {
		t.Fatal("rId1 not found")
	}
	if r1.Target != "media/image1.png" {
		t.Errorf("rId1 Target = %q, want media/image1.png", r1.Target)
	}
	if r1.Type != "http://example.com/image" {
		t.Errorf("rId1 Type = %q", r1.Type)
	}
	r2, ok := rels["rId2"]
	if !ok {
		t.Fatal("rId2 not found")
	}
	if r2.Target != "https://example.com" {
		t.Errorf("rId2 Target = %q", r2.Target)
	}
	if r2.TargetMode != "External" {
		t.Errorf("rId2 TargetMode = %q, want External", r2.TargetMode)
	}
}

func TestParseRelationshipsFromReaderNotFound(t *testing.T) {
	zr := makeEmptyZip(t)
	rels, err := ParseRelationshipsFromReader(zr, "nonexistent.rels")
	if err != nil {
		t.Fatalf("error for missing file: %v", err)
	}
	if len(rels) != 0 {
		t.Errorf("expected empty map for missing file, got %d entries", len(rels))
	}
}

func TestParseRelationshipsFromReaderInvalidXML(t *testing.T) {
	zr := makeZipWithFile(t, "test.rels", "<<<not xml>>>")
	_, err := ParseRelationshipsFromReader(zr, "test.rels")
	if err == nil {
		t.Fatal("expected error for invalid XML, got nil")
	}
	if !strings.Contains(err.Error(), "decode relationships") {
		t.Errorf("error = %v, should contain 'decode relationships'", err)
	}
}

func TestReadFileFromZip(t *testing.T) {
	content := "file content here"
	zr := makeZipWithFile(t, "test.txt", content)
	data, err := ReadFileFromZip(zr, "test.txt")
	if err != nil {
		t.Fatalf("ReadFileFromZip error: %v", err)
	}
	if string(data) != content {
		t.Errorf("data = %q, want %q", string(data), content)
	}
}

func TestReadFileFromZipNotFound(t *testing.T) {
	zr := makeEmptyZip(t)
	_, err := ReadFileFromZip(zr, "nonexistent.txt")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, should contain 'not found'", err)
	}
}

func TestRelsPathFor(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"root_file", "document.xml", "_rels/document.xml.rels"},
		{"nested_file", "word/document.xml", "word/_rels/document.xml.rels"},
		{"deep_nested", "a/b/c/file.xml", "a/b/c/_rels/file.xml.rels"},
		{"slides", "ppt/slides/slide1.xml", "ppt/slides/_rels/slide1.xml.rels"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RelsPathFor(tt.input)
			if got != tt.expected {
				t.Errorf("RelsPathFor(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestResolveTarget(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
		target   string
		expected string
	}{
		{"relative_same_dir", "word/document.xml", "styles.xml", "word/styles.xml"},
		{"relative_subdir", "word/document.xml", "media/image.png", "word/media/image.png"},
		{"relative_parent", "word/sub/file.xml", "../styles.xml", "word/styles.xml"},
		{"absolute_path", "word/document.xml", "/absolute/path.xml", "absolute/path.xml"},
		{"root_base", "file.xml", "target.xml", "target.xml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveTarget(tt.basePath, tt.target)
			if got != tt.expected {
				t.Errorf("ResolveTarget(%q, %q) = %q, want %q",
					tt.basePath, tt.target, got, tt.expected)
			}
		})
	}
}

func TestParseRelationships(t *testing.T) {
	relsXML := `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="r1" Type="type1" Target="target1.xml"/>
</Relationships>`

	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "test.zip")
	if err := createZipFile(zipPath, map[string]string{"_rels/.rels": relsXML}); err != nil {
		t.Fatal(err)
	}
	zf, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zf.Close()

	rels, err := ParseRelationships(zf, "_rels/.rels")
	if err != nil {
		t.Fatalf("ParseRelationships error: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship, got %d", len(rels))
	}
	if rels["r1"].Target != "target1.xml" {
		t.Errorf("r1 Target = %q, want target1.xml", rels["r1"].Target)
	}
}

func TestParseRelationshipsMissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "empty.zip")
	if err := createZipFile(zipPath, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	zf, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zf.Close()

	rels, err := ParseRelationships(zf, "missing.rels")
	if err != nil {
		t.Fatalf("ParseRelationships error: %v", err)
	}
	if len(rels) != 0 {
		t.Errorf("expected empty map, got %d entries", len(rels))
	}
}

// makeZipWithFile creates a zip.Reader containing a single file with the given content.
func makeZipWithFile(t *testing.T, filename, content string) *zip.Reader {
	t.Helper()
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "test.zip")
	if err := createZipFile(zipPath, map[string]string{filename: content}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return zr
}

// makeEmptyZip creates a zip.Reader with no files.
func makeEmptyZip(t *testing.T) *zip.Reader {
	t.Helper()
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "empty.zip")
	if err := createZipFile(zipPath, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return zr
}

// createZipFile writes a zip archive containing the given files.
func createZipFile(path string, files map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(content)); err != nil {
			return err
		}
	}
	return zw.Close()
}
