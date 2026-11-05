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
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/unicode"
)

type overrideConverter struct {
	ext  string
	text string
}

func (c *overrideConverter) Accepts(info StreamInfo) bool { return info.Extension == c.ext }

func (c *overrideConverter) Convert(reader io.ReadSeeker, info StreamInfo) (*DocumentConverterResult, error) {
	return &DocumentConverterResult{Markdown: c.text}, nil
}

// Priority boundaries: builtin specific converters sit at PrioritySpecific
// and generic ones at PriorityGeneric; custom priorities sort strictly
// between them.
func TestConvertPriorityBoundaries(t *testing.T) {
	t.Run("custom just above specific loses to builtin csv", func(t *testing.T) {
		m := New()
		m.RegisterConverter("custom", &overrideConverter{ext: ".csv", text: "custom wins"}, 0.5)
		result, err := m.ConvertReader(strings.NewReader("a,b\n"), StreamInfo{Extension: ".csv"})
		if err != nil {
			t.Fatalf("ConvertReader: %v", err)
		}
		if !strings.Contains(result.Markdown, "| a | b |") {
			t.Errorf("builtin csv must run first, got: %q", result.Markdown)
		}
	})
	t.Run("custom below specific wins", func(t *testing.T) {
		m := New()
		m.RegisterConverter("custom", &overrideConverter{ext: ".csv", text: "custom wins"}, -1)
		result, err := m.ConvertReader(strings.NewReader("a,b\n"), StreamInfo{Extension: ".csv"})
		if err != nil {
			t.Fatalf("ConvertReader: %v", err)
		}
		if result.Markdown != "custom wins" {
			t.Errorf("custom converter must run first, got: %q", result.Markdown)
		}
	})
	t.Run("custom just below generic wins", func(t *testing.T) {
		m := New()
		m.RegisterConverter("custom", &overrideConverter{ext: ".txt", text: "custom wins"}, 9.5)
		result, err := m.ConvertReader(strings.NewReader("builtin wins"), StreamInfo{Extension: ".txt"})
		if err != nil {
			t.Fatalf("ConvertReader: %v", err)
		}
		if result.Markdown != "custom wins" {
			t.Errorf("custom converter must run before plaintext, got: %q", result.Markdown)
		}
	})
	t.Run("custom at generic loses to plaintext", func(t *testing.T) {
		m := New()
		m.RegisterConverter("custom", &overrideConverter{ext: ".txt", text: "custom wins"}, 10.5)
		result, err := m.ConvertReader(strings.NewReader("builtin wins"), StreamInfo{Extension: ".txt"})
		if err != nil {
			t.Fatalf("ConvertReader: %v", err)
		}
		if result.Markdown != "builtin wins" {
			t.Errorf("plaintext must run first, got: %q", result.Markdown)
		}
	})
}

func TestConvertHTTPAndHTTPSRouteToURL(t *testing.T) {
	m := New()
	for _, scheme := range []string{"http", "https"} {
		_, err := m.Convert(scheme + "://127.0.0.1:1/x.txt")
		if err == nil {
			t.Fatalf("Convert(%s) should fail to connect", scheme)
		}
		if !strings.Contains(err.Error(), "fetch URL") {
			t.Errorf("Convert(%s) error = %v, should contain 'fetch URL'", scheme, err)
		}
	}
}

func TestConvertFileOpenErrorPreservesCause(t *testing.T) {
	m := New()
	_, err := m.ConvertFile("/nonexistent/x.txt")
	if _, ok := errors.AsType[*os.PathError](err); !ok {
		t.Errorf("open file error must wrap os.PathError via %%w, got: %v", err)
	}
}

// The sniffed MIME of an unsupported binary file is echoed in the
// UnsupportedFormatError message.
func TestConvertFileSniffedMIMEEchoedInError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "image.bin")
	if err := os.WriteFile(path, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	_, err := m.ConvertFile(path)
	if err == nil {
		t.Fatal("expected unsupported format error")
	}
	if !strings.Contains(err.Error(), `mime="image/png"`) {
		t.Errorf("error = %v, should echo sniffed mime image/png", err)
	}
}

// A reader whose Seek fails must surface the seek error from dispatch.
func TestConvertReaderSeekErrorPropagates(t *testing.T) {
	m := New()
	_, err := m.ConvertReader(&errorReadSeeker{}, StreamInfo{Extension: ".txt"})
	if err == nil {
		t.Fatal("expected seek error")
	}
	if !strings.Contains(err.Error(), "seek:") {
		t.Errorf("error = %v, should contain 'seek:'", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("seek error must wrap the reader error via %%w, got: %v", err)
	}
}

// When the first accepting converter fails, later converters still get the
// chance: a bad CSV routed by MIME falls back to plaintext output.
func TestConvertFallsBackAfterConverterError(t *testing.T) {
	m := New()
	result, err := m.ConvertReader(strings.NewReader("a,\"bad\n"), StreamInfo{MIMEType: "text/csv"})
	if err != nil {
		t.Fatalf("ConvertReader: %v", err)
	}
	if result.Markdown != "a,\"bad" {
		t.Errorf("Markdown = %q, want raw fallback text %q", result.Markdown, "a,\"bad")
	}
}

func TestConvertUnsupportedErrorEchoesExtensionAndMIME(t *testing.T) {
	m := New()
	_, err := m.ConvertReader(strings.NewReader("data"), StreamInfo{
		Extension: ".unknownext",
		MIMEType:  "application/x-unknown",
	})
	if err == nil {
		t.Fatal("expected unsupported error")
	}
	if !strings.Contains(err.Error(), `extension=".unknownext"`) {
		t.Errorf("error = %v, should echo extension", err)
	}
	if !strings.Contains(err.Error(), `mime="application/x-unknown"`) {
		t.Errorf("error = %v, should echo mime", err)
	}
}

func TestConvertURLFetchErrorPreservesCause(t *testing.T) {
	m := New()
	_, err := m.ConvertURL("http://127.0.0.1:1/nonexistent")
	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Errorf("fetch error must wrap *url.Error via %%w, got: %v", err)
	}
}

// A truncated response body (Content-Length promises more than is sent)
// must surface a read response error.
func TestConvertURLReadResponseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(200)
		if hijacker, ok := w.(http.Hijacker); ok {
			conn, _, _ := hijacker.Hijack()
			conn.Close()
		}
	}))
	defer server.Close()
	m := New()
	_, err := m.ConvertURL(server.URL + "/broken")
	if err == nil {
		t.Fatal("expected read response error")
	}
	if !strings.Contains(err.Error(), "read response") {
		t.Errorf("error = %v, should contain 'read response'", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("read response error must wrap the transport error via %%w, got: %v", err)
	}
}

// The Content-Type header routes conversion even when the body would sniff
// as something else.
func TestConvertURLContentTypeRoutesConversion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("1 > 0"))
	}))
	defer server.Close()
	m := New()
	result, err := m.ConvertURL(server.URL + "/file.txt")
	if err != nil {
		t.Fatalf("ConvertURL: %v", err)
	}
	if !strings.Contains(result.Markdown, "1 &gt; 0") {
		t.Errorf("text/html body must convert via the HTML converter, got: %q", result.Markdown)
	}
}

// With no Content-Type and no extension in the URL, detection must still
// classify HTML content.
func TestConvertURLNoContentTypeDetectsHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An explicitly empty header suppresses net/http's automatic
		// content sniffing, so no Content-Type reaches the client.
		w.Header().Set("Content-Type", "")
		w.Write([]byte("<p>detected paragraph</p>"))
	}))
	defer server.Close()
	m := New()
	result, err := m.ConvertURL(server.URL + "/download")
	if err != nil {
		t.Fatalf("ConvertURL: %v", err)
	}
	if !strings.Contains(result.Markdown, "detected paragraph") {
		t.Errorf("sniffed HTML must convert via the HTML converter, got: %q", result.Markdown)
	}
	if strings.Contains(result.Markdown, "<p>") {
		t.Errorf("HTML tags must not survive conversion, got: %q", result.Markdown)
	}
}

// A charset parameter on the Content-Type must drive decoding (UTF-16LE
// bytes would otherwise pass detection as NUL-laced ASCII).
func TestConvertURLCharsetParamDecodes(t *testing.T) {
	encoded, err := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewEncoder().Bytes([]byte("a,b\n1,2\n"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv; charset=utf-16le")
		w.Write(encoded)
	}))
	defer server.Close()
	m := New()
	result, err := m.ConvertURL(server.URL + "/data.csv")
	if err != nil {
		t.Fatalf("ConvertURL: %v", err)
	}
	// Exact match: without the charset hint the NUL-laced raw bytes parse
	// into an extra empty table row that survives normalization.
	if want := "| a | b |\n| --- | --- |\n| 1 | 2 |"; result.Markdown != want {
		t.Errorf("charset hint must decode UTF-16LE CSV, got: %q", result.Markdown)
	}
}

// When several charset parameters appear, the last one wins.
func TestConvertURLDoubleCharsetLastWins(t *testing.T) {
	encoded, err := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewEncoder().Bytes([]byte("a,b\n1,2\n"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8; charset=utf-16le")
		w.Write(encoded)
	}))
	defer server.Close()
	m := New()
	result, err := m.ConvertURL(server.URL + "/data.csv")
	if err != nil {
		t.Fatalf("ConvertURL: %v", err)
	}
	if want := "| a | b |\n| --- | --- |\n| 1 | 2 |"; result.Markdown != want {
		t.Errorf("last charset parameter must win, got: %q", result.Markdown)
	}
}

// The filename for zip conversion comes from the URL path.
func TestConvertURLFilenameFromPath(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "data.zip")
	if err := createZipFile(zipPath, map[string]string{"z.txt": "inner text"}); err != nil {
		t.Fatal(err)
	}
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write(zipBytes)
	}))
	defer server.Close()
	m := New()
	result, err := m.ConvertURL(server.URL + "/files/data.zip")
	if err != nil {
		t.Fatalf("ConvertURL: %v", err)
	}
	if !strings.Contains(result.Markdown, "Content from the zip file `data.zip`") {
		t.Errorf("zip header must cite data.zip from the URL path, got: %q", result.Markdown)
	}
}
