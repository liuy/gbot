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
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZipConvertReadErrorPreservesCause(t *testing.T) {
	c := NewZipConverter(New())
	_, err := c.Convert(&errorReadSeeker{}, StreamInfo{Extension: ".zip"})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("read error must wrap the reader error via %%w, got: %v", err)
	}
}

func TestZipConvertOpenErrorPreservesCause(t *testing.T) {
	c := NewZipConverter(New())
	_, err := c.Convert(strings.NewReader("not a zip"), StreamInfo{Extension: ".zip"})
	if err == nil {
		t.Fatal("expected open error")
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("open error must wrap the zip error via %%w, got: %v", err)
	}
}

// zipEntry describes one raw local-file record for hand-built archives that
// stdlib writers refuse to produce (unknown method, bad CRC).
type zipEntry struct {
	name   string
	method uint16
	crc    uint32
	data   []byte
}

// buildRawZip writes local headers + central directory for the given entries
// with caller-chosen method and CRC values, bypassing zip.Writer validation.
func buildRawZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	type cdMeta struct {
		name   string
		method uint16
		crc    uint32
		size   uint32
		offset uint32
	}
	var metas []cdMeta
	for _, e := range entries {
		offset := uint32(buf.Len())
		buf.Write([]byte{'P', 'K', 3, 4})
		binary.Write(&buf, binary.LittleEndian, uint16(20))
		binary.Write(&buf, binary.LittleEndian, uint16(0))
		binary.Write(&buf, binary.LittleEndian, e.method)
		binary.Write(&buf, binary.LittleEndian, uint16(0))
		binary.Write(&buf, binary.LittleEndian, uint16(0))
		binary.Write(&buf, binary.LittleEndian, e.crc)
		binary.Write(&buf, binary.LittleEndian, uint32(len(e.data)))
		binary.Write(&buf, binary.LittleEndian, uint32(len(e.data)))
		binary.Write(&buf, binary.LittleEndian, uint16(len(e.name)))
		binary.Write(&buf, binary.LittleEndian, uint16(0))
		buf.WriteString(e.name)
		buf.Write(e.data)
		metas = append(metas, cdMeta{e.name, e.method, e.crc, uint32(len(e.data)), offset})
	}
	cdStart := buf.Len()
	for _, m := range metas {
		buf.Write([]byte{'P', 'K', 1, 2})
		binary.Write(&buf, binary.LittleEndian, uint16(20))
		binary.Write(&buf, binary.LittleEndian, uint16(20))
		binary.Write(&buf, binary.LittleEndian, uint16(0))
		binary.Write(&buf, binary.LittleEndian, m.method)
		binary.Write(&buf, binary.LittleEndian, uint16(0))
		binary.Write(&buf, binary.LittleEndian, uint16(0))
		binary.Write(&buf, binary.LittleEndian, m.crc)
		binary.Write(&buf, binary.LittleEndian, m.size)
		binary.Write(&buf, binary.LittleEndian, m.size)
		binary.Write(&buf, binary.LittleEndian, uint16(len(m.name)))
		binary.Write(&buf, binary.LittleEndian, uint16(0))
		binary.Write(&buf, binary.LittleEndian, uint16(0))
		binary.Write(&buf, binary.LittleEndian, uint16(0))
		binary.Write(&buf, binary.LittleEndian, uint16(0))
		binary.Write(&buf, binary.LittleEndian, uint32(0))
		binary.Write(&buf, binary.LittleEndian, m.offset)
		buf.WriteString(m.name)
	}
	cdSize := buf.Len() - cdStart
	buf.Write([]byte{'P', 'K', 5, 6})
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint16(len(metas)))
	binary.Write(&buf, binary.LittleEndian, uint16(len(metas)))
	binary.Write(&buf, binary.LittleEndian, uint32(cdSize))
	binary.Write(&buf, binary.LittleEndian, uint32(cdStart))
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	return buf.Bytes()
}

func convertZipBytes(t *testing.T, data []byte) string {
	t.Helper()
	c := NewZipConverter(New())
	result, err := c.Convert(bytes.NewReader(data), StreamInfo{Extension: ".zip", Filename: "archive.zip"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	return result.Markdown
}

// An entry whose compression method is unknown fails at Open and must be
// skipped while later entries still convert.
func TestZipConvertSkipsUnopenableEntry(t *testing.T) {
	data := buildRawZip(t, []zipEntry{
		{name: "broken.txt", method: 99, crc: 0, data: []byte("x")},
		{name: "good.txt", method: 0, crc: crc32.ChecksumIEEE([]byte("still here")), data: []byte("still here")},
	})
	md := convertZipBytes(t, data)
	if !strings.Contains(md, "good.txt") || !strings.Contains(md, "still here") {
		t.Errorf("entries after an unopenable one must convert, got: %q", md)
	}
	if strings.Contains(md, "broken.txt") {
		t.Errorf("unopenable entry must be skipped, got: %q", md)
	}
}

// An entry with a mismatched CRC fails at ReadAll; conversion continues.
func TestZipConvertSkipsBadCRCEntry(t *testing.T) {
	data := buildRawZip(t, []zipEntry{
		{name: "corrupt.txt", method: 0, crc: 0xdeadbeef, data: []byte("corrupt payload")},
		{name: "good.txt", method: 0, crc: crc32.ChecksumIEEE([]byte("after corrupt")), data: []byte("after corrupt")},
	})
	md := convertZipBytes(t, data)
	if !strings.Contains(md, "good.txt") || !strings.Contains(md, "after corrupt") {
		t.Errorf("entries after a corrupt one must convert, got: %q", md)
	}
	if strings.Contains(md, "corrupt payload") {
		t.Errorf("corrupt payload must not leak, got: %q", md)
	}
}

func TestZipConvertInnerFilesSeparated(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, m := range []struct{ name, content string }{
		{"a.txt", "alpha content"},
		{"b.txt", "beta content"},
	} {
		w, err := zw.Create(m.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(m.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	c := NewZipConverter(New())
	result, err := c.Convert(bytes.NewReader(data), StreamInfo{Extension: ".zip", Filename: "test.zip"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	want := "Content from the zip file `test.zip`:\n\n" +
		"## File: a.txt\nalpha content\n\n" +
		"## File: b.txt\nbeta content\n\n"
	if result.Markdown != want {
		t.Errorf("Markdown = %q, want %q", result.Markdown, want)
	}
}

// The reader must be rewound to byte zero after MIME detection: even one
// byte of drift truncates the converted content.
func TestZipConvertRewindsAfterMIMEDetection(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")
	if err := createZipFile(zipPath, map[string]string{
		"note.txt": "full content here",
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	c := NewZipConverter(New())
	result, err := c.Convert(bytes.NewReader(data), StreamInfo{Extension: ".zip", Filename: "test.zip"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !strings.Contains(result.Markdown, "full content here") {
		t.Errorf("content must convert from offset 0, got: %q", result.Markdown)
	}
}

// A CSV member must render as a table (Extension routing) and an
// extensionless HTML member must route via detected MIME.
func TestZipConvertMemberTypeDetection(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")
	if err := createZipFile(zipPath, map[string]string{
		"data.csv": "a,b\n1,2\n",
		"README":   "<p>extensionless html</p>",
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	md := convertZipBytes(t, data)
	if !strings.Contains(md, "| a | b |") {
		t.Errorf("csv member must render as table (Extension routing), got: %q", md)
	}
	if !strings.Contains(md, "extensionless html") {
		t.Errorf("extensionless member must route via detected MIME, got: %q", md)
	}
}

// Nested zip: the inner archive's header cites the member filename.
func TestZipConvertNestedZipFilename(t *testing.T) {
	dir := t.TempDir()
	innerPath := filepath.Join(dir, "inner.zip")
	if err := createZipFile(innerPath, map[string]string{"z.txt": "inner text"}); err != nil {
		t.Fatal(err)
	}
	inner, err := os.ReadFile(innerPath)
	if err != nil {
		t.Fatal(err)
	}
	outerPath := filepath.Join(dir, "outer.zip")
	if err := createZipFile(outerPath, map[string]string{"inner.zip": string(inner)}); err != nil {
		t.Fatal(err)
	}
	outer, err := os.ReadFile(outerPath)
	if err != nil {
		t.Fatal(err)
	}
	md := convertZipBytes(t, outer)
	if !strings.Contains(md, "Content from the zip file `inner.zip`") {
		t.Errorf("nested zip header must cite inner.zip, got: %q", md)
	}
	if !strings.Contains(md, "inner text") {
		t.Errorf("nested content must convert, got: %q", md)
	}
}

// A member no converter can handle (PNG) must be skipped while later
// members still convert: the skip is a continue of the member loop, not a
// break out of it.
func TestZipConvertSkipsFailedConversionMember(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")
	if err := createZipFile(zipPath, map[string]string{
		"file.png": "\x89PNG\x0d\x0a\x1a\x0a binary payload",
		"good.txt": "recovered content",
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	md := convertZipBytes(t, data)
	if strings.Contains(md, "file.png") {
		t.Errorf("unconvertable member must be skipped, got: %q", md)
	}
	if !strings.Contains(md, "recovered content") {
		t.Errorf("members after an unconvertable one must convert, got: %q", md)
	}
}

// A .docx member must convert as a document (Extension routing beats the
// sniffed application/zip, which would otherwise recurse into the zip
// converter).
func TestZipConvertMemberDocxRoutesByExtension(t *testing.T) {
	dir := t.TempDir()
	docxPath := filepath.Join(dir, "report.docx")
	if err := createZipFile(docxPath, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":   `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Routed as document</w:t></w:r></w:p></w:body></w:document>`,
	}); err != nil {
		t.Fatal(err)
	}
	docxBytes, err := os.ReadFile(docxPath)
	if err != nil {
		t.Fatal(err)
	}
	outerPath := filepath.Join(dir, "outer.zip")
	if err := createZipFile(outerPath, map[string]string{"report.docx": string(docxBytes)}); err != nil {
		t.Fatal(err)
	}
	outer, err := os.ReadFile(outerPath)
	if err != nil {
		t.Fatal(err)
	}
	md := convertZipBytes(t, outer)
	if !strings.Contains(md, "Routed as document") {
		t.Errorf("docx member must convert as document, got: %q", md)
	}
	if strings.Contains(md, "Content from the zip file `report.docx`") {
		t.Errorf("docx member must not fall back to nested-zip conversion, got: %q", md)
	}
}

// A .ipynb member routes only by extension (sniffed MIME is generic JSON):
// the notebook must convert to code fences, not raw JSON.
func TestZipConvertMemberIpynbRoutesByExtension(t *testing.T) {
	dir := t.TempDir()
	outerPath := filepath.Join(dir, "outer.zip")
	nb := `{"metadata":{},"cells":[{"cell_type":"code","source":"print(1)"}]}`
	if err := createZipFile(outerPath, map[string]string{"notebook.ipynb": nb}); err != nil {
		t.Fatal(err)
	}
	outer, err := os.ReadFile(outerPath)
	if err != nil {
		t.Fatal(err)
	}
	md := convertZipBytes(t, outer)
	if !strings.Contains(md, "```python") {
		t.Errorf("ipynb member must convert to code fences (Extension routing), got: %q", md)
	}
	if strings.Contains(md, `"cell_type"`) {
		t.Errorf("ipynb member must not fall back to raw JSON, got: %q", md)
	}
}
