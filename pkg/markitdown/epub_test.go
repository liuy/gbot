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
	"path/filepath"
	"strings"
	"testing"
)

func TestEpubConvertMinimal(t *testing.T) {
	containerXML := `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

	opfXML := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Test Book</dc:title>
    <dc:creator>Test Author</dc:creator>
    <dc:language>en</dc:language>
    <dc:publisher>Test Publisher</dc:publisher>
    <dc:date>2024-01-01</dc:date>
    <dc:description>A test description</dc:description>
    <dc:identifier>id123</dc:identifier>
  </metadata>
  <manifest>
    <item id="ch1" href="chapter1.html" media-type="application/xhtml+xml"/>
    <item id="ch2" href="chapter2.xhtml" media-type="application/xhtml+xml"/>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
  </manifest>
  <spine>
    <itemref idref="ch1"/>
    <itemref idref="ch2"/>
  </spine>
</package>`

	ch1HTML := `<html><head><title>Ch1</title></head><body><h1>Chapter 1</h1><p>First chapter content</p></body></html>`
	ch2HTML := `<html><head><title>Ch2</title></head><body><h1>Chapter 2</h1><p>Second chapter content</p></body></html>`

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.epub")
	if err := createZipFile(zipPath, map[string]string{
		"META-INF/container.xml": containerXML,
		"OEBPS/content.opf":      opfXML,
		"OEBPS/chapter1.html":    ch1HTML,
		"OEBPS/chapter2.xhtml":   ch2HTML,
		"OEBPS/toc.ncx":          `<?xml version="1.0"?><ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1"></ncx>`,
		"mimetype":               "application/epub+zip",
	}); err != nil {
		t.Fatal(err)
	}

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	if result.Title != "Test Book" {
		t.Errorf("Title = %q, want 'Test Book'", result.Title)
	}
	if !strings.Contains(result.Markdown, "Test Author") {
		t.Errorf("Markdown should contain author: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "Chapter 1") {
		t.Errorf("Markdown should contain Chapter 1: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "Chapter 2") {
		t.Errorf("Markdown should contain Chapter 2: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "First chapter content") {
		t.Errorf("Markdown should contain first chapter content: %s", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "Test Publisher") {
		t.Errorf("Markdown should contain publisher: %s", result.Markdown)
	}
}

func TestEpubConvertWithMissingFile(t *testing.T) {
	containerXML := `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

	opfXML := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Missing File Book</dc:title>
  </metadata>
  <manifest>
    <item id="ch1" href="missing.html" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="ch1"/>
  </spine>
</package>`

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test_missing.epub")
	if err := createZipFile(zipPath, map[string]string{
		"META-INF/container.xml": containerXML,
		"content.opf":            opfXML,
	}); err != nil {
		t.Fatal(err)
	}

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	if result.Title != "Missing File Book" {
		t.Errorf("Title = %q, want 'Missing File Book'", result.Title)
	}
}

func TestEpubConvertOPFInSubdirectory(t *testing.T) {
	containerXML := `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="sub/dir/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

	opfXML := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Subdir Book</dc:title>
  </metadata>
  <manifest>
    <item id="ch1" href="chapter.html" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="ch1"/>
  </spine>
</package>`

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test_subdir.epub")
	if err := createZipFile(zipPath, map[string]string{
		"META-INF/container.xml": containerXML,
		"sub/dir/content.opf":    opfXML,
		"sub/dir/chapter.html":   `<html><body><p>Content in subdir</p></body></html>`,
	}); err != nil {
		t.Fatal(err)
	}

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	if !strings.Contains(result.Markdown, "Content in subdir") {
		t.Errorf("Markdown should contain subdir content: %s", result.Markdown)
	}
}

func TestEpubConvertNoSpineItems(t *testing.T) {
	containerXML := `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

	opfXML := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>No Spine</dc:title>
  </metadata>
  <manifest>
    <item id="ch1" href="ch.html" media-type="application/xhtml+xml"/>
  </manifest>
  <spine></spine>
</package>`

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test_nospine.epub")
	if err := createZipFile(zipPath, map[string]string{
		"META-INF/container.xml": containerXML,
		"content.opf":            opfXML,
	}); err != nil {
		t.Fatal(err)
	}

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	if result.Title != "No Spine" {
		t.Errorf("Title = %q, want 'No Spine'", result.Title)
	}
}

func TestEpubConvertMultipleAuthors(t *testing.T) {
	containerXML := `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

	opfXML := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Multi Author</dc:title>
    <dc:creator>Author One</dc:creator>
    <dc:creator>Author Two</dc:creator>
  </metadata>
  <manifest></manifest>
  <spine></spine>
</package>`

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test_multiauthor.epub")
	if err := createZipFile(zipPath, map[string]string{
		"META-INF/container.xml": containerXML,
		"content.opf":            opfXML,
	}); err != nil {
		t.Fatal(err)
	}

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	if !strings.Contains(result.Markdown, "Author One") || !strings.Contains(result.Markdown, "Author Two") {
		t.Errorf("Markdown should contain both authors: %s", result.Markdown)
	}
}

func TestEpubConvertNonHTMLSpineItem(t *testing.T) {
	containerXML := `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

	opfXML := `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Non HTML</dc:title>
  </metadata>
  <manifest>
    <item id="img" href="image.png" media-type="image/png"/>
  </manifest>
  <spine>
    <itemref idref="img"/>
  </spine>
</package>`

	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test_nonhtml.epub")
	if err := createZipFile(zipPath, map[string]string{
		"META-INF/container.xml": containerXML,
		"content.opf":            opfXML,
		"image.png":              "fake png data",
	}); err != nil {
		t.Fatal(err)
	}

	m := New()
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile error: %v", err)
	}
	// Non-HTML items should be silently skipped
	if result.Title != "Non HTML" {
		t.Errorf("Title = %q, want 'Non HTML'", result.Title)
	}
}

func TestParseOPFMissingOPFFile(t *testing.T) {
	zr := makeEmptyZip(t)
	c := NewEpubConverter(nil)
	_, _, _, err := c.parseOPF(zr, "nonexistent.opf")
	if err == nil {
		t.Fatal("expected error for missing OPF file")
	}
}
