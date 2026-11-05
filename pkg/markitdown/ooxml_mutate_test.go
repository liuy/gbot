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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rawZipEntryWithUnknownMethod hand-assembles a one-entry archive whose
// compression method is not registered in archive/zip: the directory parses
// but Open() fails, which no stdlib-produced archive can express.
func rawZipEntryWithUnknownMethod(t *testing.T, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	lfh := func() {
		buf.Write([]byte{'P', 'K', 3, 4})
		binary.Write(&buf, binary.LittleEndian, uint16(20)) // version needed
		binary.Write(&buf, binary.LittleEndian, uint16(0))  // flags
		binary.Write(&buf, binary.LittleEndian, uint16(99)) // unknown method
		binary.Write(&buf, binary.LittleEndian, uint16(0))  // modtime
		binary.Write(&buf, binary.LittleEndian, uint16(0))  // moddate
		binary.Write(&buf, binary.LittleEndian, uint32(0))  // crc
		binary.Write(&buf, binary.LittleEndian, uint32(4))  // compressed size
		binary.Write(&buf, binary.LittleEndian, uint32(4))  // uncompressed size
		binary.Write(&buf, binary.LittleEndian, uint16(len(name)))
		binary.Write(&buf, binary.LittleEndian, uint16(0)) // extra len
		buf.WriteString(name)
		buf.WriteString("data")
	}
	lfh()
	cdStart := buf.Len()
	buf.Write([]byte{'P', 'K', 1, 2})
	binary.Write(&buf, binary.LittleEndian, uint16(20)) // version made by
	binary.Write(&buf, binary.LittleEndian, uint16(20)) // version needed
	binary.Write(&buf, binary.LittleEndian, uint16(0))  // flags
	binary.Write(&buf, binary.LittleEndian, uint16(99)) // unknown method
	binary.Write(&buf, binary.LittleEndian, uint16(0))  // modtime
	binary.Write(&buf, binary.LittleEndian, uint16(0))  // moddate
	binary.Write(&buf, binary.LittleEndian, uint32(0))  // crc
	binary.Write(&buf, binary.LittleEndian, uint32(4))
	binary.Write(&buf, binary.LittleEndian, uint32(4))
	binary.Write(&buf, binary.LittleEndian, uint16(len(name)))
	binary.Write(&buf, binary.LittleEndian, uint16(0)) // extra
	binary.Write(&buf, binary.LittleEndian, uint16(0)) // comment
	binary.Write(&buf, binary.LittleEndian, uint16(0)) // disk number
	binary.Write(&buf, binary.LittleEndian, uint16(0)) // internal attrs
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // external attrs
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // local header offset
	buf.WriteString(name)
	cdSize := buf.Len() - cdStart
	buf.Write([]byte{'P', 'K', 5, 6})
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint32(cdSize))
	binary.Write(&buf, binary.LittleEndian, uint32(cdStart))
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	return buf.Bytes()
}

func TestParseRelationshipsOpenError(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "badrels.zip")
	if err := os.WriteFile(zipPath, rawZipEntryWithUnknownMethod(t, "word/_rels/document.xml.rels"), 0o644); err != nil {
		t.Fatal(err)
	}
	zf, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	defer zf.Close()
	rels, err := ParseRelationships(zf, "word/_rels/document.xml.rels")
	if err == nil {
		t.Errorf("expected Open error to propagate, got rels=%v err=nil", rels)
	}
}

func TestParseRelationshipsFromReaderOpenError(t *testing.T) {
	data := rawZipEntryWithUnknownMethod(t, "_rels/.rels")
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	rels, err := ParseRelationshipsFromReader(zr, "_rels/.rels")
	if err == nil {
		t.Errorf("expected Open error to propagate, got rels=%v err=nil", rels)
	}
}

func TestReadFileFromZipOpenError(t *testing.T) {
	data := rawZipEntryWithUnknownMethod(t, "word/document.xml")
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	content, err := ReadFileFromZip(zr, "word/document.xml")
	if err == nil {
		t.Errorf("expected Open error to propagate, got content=%q err=nil", content)
	}
}

func TestParseRelationshipsInvalidXMLPreservesCause(t *testing.T) {
	zr := makeZipWithFile(t, "word/_rels/document.xml.rels", "<<<not rels")
	_, err := ParseRelationshipsFromReader(zr, "word/_rels/document.xml.rels")
	if err == nil {
		t.Fatal("expected decode error")
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("decode error must wrap the xml error via %%w, got: %v", err)
	}
	if !strings.Contains(err.Error(), "decode relationships") {
		t.Errorf("error = %v, should contain 'decode relationships'", err)
	}
}
