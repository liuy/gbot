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
	"errors"
	"io"
	"maps"
	"path/filepath"
	"strings"
	"testing"
)

func TestEpubConstructorKeepsMarkitdownOptions(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "data.epub")
	longB64 := strings.Repeat("A", 100)
	if err := createZipFile(zipPath, map[string]string{
		"META-INF/container.xml": containerXMLFor("content.opf"),
		"content.opf":            opfXMLFor([]string{"ch1"}, map[string]string{"ch1": "chapter1.html"}),
		"chapter1.html":          `<html><body><img src="data:image/png;base64,` + longB64 + `"></body></html>`,
	}); err != nil {
		t.Fatal(err)
	}
	m := New(WithKeepDataURIs(true))
	result, err := m.ConvertFile(zipPath)
	if err != nil {
		t.Fatalf("ConvertFile: %v", err)
	}
	if !strings.Contains(result.Markdown, longB64) {
		t.Errorf("epub converter must honor WithKeepDataURIs, got: %.80s...", result.Markdown)
	}
}

func containerXMLFor(opf string) string {
	return `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="` + opf + `" media-type="application/oebps-package+xml"/></rootfiles></container>`
}

func opfXMLFor(spine []string, manifest map[string]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="2.0">`)
	b.WriteString(`<metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Book</dc:title></metadata><manifest>`)
	for id, href := range manifest {
		b.WriteString(`<item id="` + id + `" href="` + href + `" media-type="application/xhtml+xml"/>`)
	}
	b.WriteString(`</manifest><spine>`)
	for _, id := range spine {
		b.WriteString(`<itemref idref="` + id + `"/>`)
	}
	b.WriteString(`</spine></package>`)
	return b.String()
}

func TestEpubAcceptsMimeVariants(t *testing.T) {
	c := NewEpubConverter(nil)
	for _, mime := range []string{"application/epub", "application/epub+zip", "application/x-epub+zip"} {
		if !c.Accepts(StreamInfo{MIMEType: mime}) {
			t.Errorf("Accepts(%s) = false, want true", mime)
		}
	}
	if c.Accepts(StreamInfo{MIMEType: "application/zip"}) {
		t.Error("Accepts(application/zip) = true, want false")
	}
}

func TestEpubConvertReadErrorPreservesCause(t *testing.T) {
	c := NewEpubConverter(nil)
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".epub"})
	if err == nil {
		t.Fatal("expected read error")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("read error must wrap the reader error via %%w, got: %v", err)
	}
}

func TestEpubConvertOpenErrorPreservesCause(t *testing.T) {
	c := NewEpubConverter(nil)
	_, err := c.Convert(strings.NewReader("nope"), StreamInfo{Extension: ".epub"})
	if err == nil {
		t.Fatal("expected open error")
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("open error must wrap the zip error via %%w, got: %v", err)
	}
}

// A zip without META-INF/container.xml fails at the container read itself,
// not later at rootfile parsing.
func TestEpubConvertMissingContainer(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "nocontainer.epub")
	if err := createZipFile(zipPath, map[string]string{"unrelated.txt": "x"}); err != nil {
		t.Fatal(err)
	}
	c := NewEpubConverter(nil)
	_, err := c.Convert(strings.NewReader(string(mustRead(t, zipPath))), StreamInfo{Extension: ".epub"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found in ZIP") {
		t.Errorf("error = %v, should fail at container read", err)
	}
	if !strings.Contains(err.Error(), "find OPF") {
		t.Errorf("error = %v, should contain 'find OPF'", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("find OPF error must wrap the zip error via %%w, got: %v", err)
	}
}

func TestEpubConvertMissingOPFPreservesCause(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "noopf.epub")
	if err := createZipFile(zipPath, map[string]string{
		"META-INF/container.xml": containerXMLFor("missing.opf"),
	}); err != nil {
		t.Fatal(err)
	}
	c := NewEpubConverter(nil)
	_, err := c.Convert(strings.NewReader(string(mustRead(t, zipPath))), StreamInfo{Extension: ".epub"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "parse OPF") {
		t.Errorf("error = %v, should contain 'parse OPF'", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("parse OPF error must wrap the zip error via %%w, got: %v", err)
	}
}

// minimalEpub builds an epub whose OPF metadata contains only the fields
// under test.
func minimalEpub(t *testing.T, metadata string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.epub")
	base := map[string]string{
		"META-INF/container.xml": containerXMLFor("content.opf"),
		"content.opf": `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="2.0">` +
			`<metadata xmlns:dc="http://purl.org/dc/elements/1.1/">` + metadata + `</metadata>` +
			`<manifest></manifest><spine></spine></package>`,
	}
	maps.Copy(base, files)
	if err := createZipFile(zipPath, base); err != nil {
		t.Fatal(err)
	}
	return string(mustRead(t, zipPath))
}

func convertEpub(t *testing.T, data string) *DocumentConverterResult {
	t.Helper()
	c := NewEpubConverter(nil)
	result, err := c.Convert(strings.NewReader(data), StreamInfo{Extension: ".epub"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	return result
}

func TestEpubConvertNoAuthorsLine(t *testing.T) {
	result := convertEpub(t, minimalEpub(t, `<dc:title>No Author Book</dc:title>`, nil))
	if strings.Contains(result.Markdown, "Authors") {
		t.Errorf("book without creators must not print Authors line, got: %q", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "# No Author Book") {
		t.Errorf("title missing, got: %q", result.Markdown)
	}
}

func TestEpubConvertDateIncluded(t *testing.T) {
	result := convertEpub(t, minimalEpub(t, `<dc:title>T</dc:title><dc:date>2024-05-06</dc:date>`, nil))
	if !strings.Contains(result.Markdown, "**Date:** 2024-05-06") {
		t.Errorf("date must render, got: %q", result.Markdown)
	}
}

// A spine idref missing from the manifest is skipped; later items convert.
func TestEpubConvertSkipsUnknownSpineRef(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.epub")
	if err := createZipFile(zipPath, map[string]string{
		"META-INF/container.xml": containerXMLFor("content.opf"),
		"content.opf":            opfXMLFor([]string{"ghost", "ch1"}, map[string]string{"ch1": "chapter1.html"}),
		"chapter1.html":          `<html><body><p>after ghost</p></body></html>`,
	}); err != nil {
		t.Fatal(err)
	}
	result := convertEpub(t, string(mustRead(t, zipPath)))
	if !strings.Contains(result.Markdown, "after ghost") {
		t.Errorf("items after unknown spine ref must convert, got: %q", result.Markdown)
	}
}

// A missing chapter file is skipped without aborting later chapters.
func TestEpubConvertSkipsMissingChapterFile(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.epub")
	if err := createZipFile(zipPath, map[string]string{
		"META-INF/container.xml": containerXMLFor("content.opf"),
		"content.opf":            opfXMLFor([]string{"gone", "ch1"}, map[string]string{"gone": "missing.html", "ch1": "chapter1.html"}),
		"chapter1.html":          `<html><body><p>recovered chapter</p></body></html>`,
	}); err != nil {
		t.Fatal(err)
	}
	result := convertEpub(t, string(mustRead(t, zipPath)))
	if !strings.Contains(result.Markdown, "recovered chapter") {
		t.Errorf("items after missing file must convert, got: %q", result.Markdown)
	}
}

// Every recognized chapter extension converts even without an HTML media
// type, and an extensionless chapter with an HTML media type routes via the
// manifest.
func TestEpubConvertChapterRouting(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.epub")
	opf := `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="2.0">` +
		`<metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>T</dc:title></metadata>` +
		`<manifest>` +
		`<item id="c1" href="chapter.html" media-type="application/octet-stream"/>` +
		`<item id="c2" href="chapter.htm" media-type="application/octet-stream"/>` +
		`<item id="c3" href="chapter.xhtml" media-type="application/octet-stream"/>` +
		`<item id="c4" href="chapter" media-type="application/xhtml+xml"/>` +
		`<item id="c5" href="texthtml" media-type="text/html"/>` +
		`<item id="c6" href="plain" media-type="text/plain"/>` +
		`</manifest><spine><itemref idref="c1"/><itemref idref="c2"/><itemref idref="c3"/><itemref idref="c4"/><itemref idref="c5"/><itemref idref="c6"/></spine></package>`
	if err := createZipFile(zipPath, map[string]string{
		"META-INF/container.xml": containerXMLFor("content.opf"),
		"content.opf":            opf,
		"chapter.html":           `<html><body><p>alpha content</p></body></html>`,
		"chapter.htm":            `<html><body><p>bravo content</p></body></html>`,
		"chapter.xhtml":          `<html><body><p>charlie content</p></body></html>`,
		"chapter":                `<html><body><p>delta content</p></body></html>`,
		"texthtml":               `<html><body><p>foxtrot content</p></body></html>`,
		"plain":                  "echo content",
	}); err != nil {
		t.Fatal(err)
	}
	result := convertEpub(t, string(mustRead(t, zipPath)))
	for _, want := range []string{"alpha content", "bravo content", "charlie content", "delta content", "foxtrot content"} {
		if !strings.Contains(result.Markdown, want) {
			t.Errorf("chapter %q missing from output: %q", want, result.Markdown)
		}
	}
	if strings.Contains(result.Markdown, "echo content") {
		t.Errorf("non-HTML media type must be skipped, got: %q", result.Markdown)
	}
}

// An absolute href in a subdirectory OPF resolves zip-absolute, not
// relative to the OPF directory.
func TestEpubConvertAbsoluteHrefInSubdirOPF(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.epub")
	if err := createZipFile(zipPath, map[string]string{
		"META-INF/container.xml": containerXMLFor("sub/content.opf"),
		"sub/content.opf": `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="2.0">` +
			`<metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>T</dc:title></metadata>` +
			`<manifest><item id="c1" href="/root.html" media-type="application/xhtml+xml"/></manifest>` +
			`<spine><itemref idref="c1"/></spine></package>`,
		"/root.html": `<html><body><p>absolute chapter</p></body></html>`,
	}); err != nil {
		t.Fatal(err)
	}
	result := convertEpub(t, string(mustRead(t, zipPath)))
	if !strings.Contains(result.Markdown, "absolute chapter") {
		t.Errorf("absolute href must resolve to the zip root, got: %q", result.Markdown)
	}
}

// An empty chapter adds no section: the emptiness filter runs before the
// trailing separators are written.
func TestEpubConvertSkipsEmptyChapter(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.epub")
	if err := createZipFile(zipPath, map[string]string{
		"META-INF/container.xml": containerXMLFor("content.opf"),
		"content.opf":            opfXMLFor([]string{"c1", "c2"}, map[string]string{"c1": "empty.html", "c2": "full.html"}),
		"empty.html":             "",
		"full.html":              `<html><body><p>content</p></body></html>`,
	}); err != nil {
		t.Fatal(err)
	}
	c := NewEpubConverter(nil)
	result, err := c.Convert(strings.NewReader(string(mustRead(t, zipPath))), StreamInfo{Extension: ".epub"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	want := "# Book\n\ncontent\n\n"
	if result.Markdown != want {
		t.Errorf("Markdown = %q, want %q", result.Markdown, want)
	}
}

// A dc:title appearing after the metadata block must be ignored.
func TestEpubConvertTitleOutsideMetadataIgnored(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "late.epub")
	if err := createZipFile(zipPath, map[string]string{
		"META-INF/container.xml": containerXMLFor("content.opf"),
		"content.opf": `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="2.0">` +
			`<metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Real Title</dc:title></metadata>` +
			`<manifest></manifest><spine></spine>` +
			`<dc:title xmlns:dc="http://purl.org/dc/elements/1.1/">Late Title</dc:title></package>`,
	}); err != nil {
		t.Fatal(err)
	}
	res := convertEpub(t, string(mustRead(t, zipPath)))
	if res.Title != "Real Title" {
		t.Errorf("Title = %q, want %q (late title must be ignored)", res.Title, "Real Title")
	}
	if strings.Contains(res.Markdown, "Late Title") {
		t.Errorf("title outside metadata must not render, got: %q", res.Markdown)
	}
}
