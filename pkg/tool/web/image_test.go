package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/liuy/gbot/pkg/tool/web/providers"
)

// pngFixture renders a small deterministic PNG so tests can prove the bytes
// reach the model base64-intact instead of as a lossy string round-trip.
func pngFixture(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatalf("encode png fixture: %v", err)
	}
	return buf.Bytes()
}

func fetchExecute(t *testing.T, url string) *Output {
	t.Helper()
	input := json.RawMessage(fmt.Sprintf(`{"query": "%s"}`, url))
	result, err := execute(context.Background(), input, &providers.SearchChain{}, nil, nil)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("expected *Output, got %T", result.Data)
	}
	return out
}

// The model must receive a real image ContentBlock (base64 + media_type), not
// the JSON-mangled binary body text the pre-image-branch fetch served.
func TestExecute_URLFetch_ImageInline(t *testing.T) {
	pngBytes := pngFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer server.Close()

	out := fetchExecute(t, server.URL)

	if out.Image == nil {
		t.Fatalf("Output.Image = nil, want inline image (Content=%q)", out.Content)
	}
	if out.Image.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", out.Image.MimeType)
	}
	decoded, err := base64.StdEncoding.DecodeString(out.Image.Base64)
	if err != nil {
		t.Fatalf("Image.Base64 does not decode: %v", err)
	}
	if !bytes.Equal(decoded, pngBytes) {
		t.Errorf("decoded image bytes diverge from the served body (%d vs %d bytes)", len(decoded), len(pngBytes))
	}
	wantContent := "[note] Fetched image binary\n\nFetched image content (image/png)."
	if out.Content != wantContent {
		t.Errorf("Content = %q, want %q", out.Content, wantContent)
	}
}

// A body past the 20MB source ceiling (omp MAX_INLINE_IMAGE_SOURCE_BYTES)
// must come back as text + note, never as an inline block. The body carries a
// real PNG header so the magic-number guard is not the branch under test.
func TestExecute_URLFetch_ImageTooLarge(t *testing.T) {
	body := pngHeaderPadded(4, 4, 20*1024*1024+1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	out := fetchExecute(t, server.URL)

	if out.Image != nil {
		t.Fatalf("Output.Image must stay nil for an oversized image")
	}
	want := fmt.Sprintf("[note] Fetched image binary; Image exceeds inline source limit (%d bytes > %d bytes)\n\nFetched image content (image/png), but it is too large to inline render.",
		len(body), 20*1024*1024)
	if out.Content != want {
		t.Errorf("Content = %q, want %q", out.Content, want)
	}
}

// Exactly at the 20MB source ceiling still enters the inline path (strict `>`);
// the undecodable padding then lands on the could-not-decode fallback instead
// of the source-limit note.
func TestExecute_URLFetch_ImageAtSourceLimit(t *testing.T) {
	body := pngHeaderPadded(4, 4, 20*1024*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	out := fetchExecute(t, server.URL)

	if out.Image != nil {
		t.Fatalf("Output.Image = nil expected on the decode-failure fallback, got an inline image")
	}
	if !strings.Contains(out.Content, "Fetched payload could not be decoded as image/png") {
		t.Errorf("Content = %q, want the could-not-decode note", out.Content)
	}
	if strings.Contains(out.Content, "exceeds inline source limit") {
		t.Errorf("an at-limit body must not trip the source-limit note: %q", out.Content)
	}
}

// A lying Content-Type (HTML served as image/png) must not produce an inline
// block; the magic-number sniff degrades it to text + note and the body is
// served as the textual rendering.
func TestExecute_URLFetch_ImageFakeContentType(t *testing.T) {
	body := []byte("<html><body>not an image</body></html>")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	out := fetchExecute(t, server.URL)

	if out.Image != nil {
		t.Fatalf("Output.Image must stay nil for an image/png-labeled HTML body")
	}
	want := "[note] Fetched image binary; Fetched payload could not be decoded as image/png; returning text metadata only\n\n" +
		"<html><body>not an image</body></html>"
	if out.Content != want {
		t.Errorf("Content = %q, want %q", out.Content, want)
	}
}

// An image URL answering 204 (or a zero-byte 200) yields an empty payload that
// must degrade to text + note — never an empty-base64 image block.
func TestExecute_URLFetch_ImageEmptyBody(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		// note present only on the 204 variant (LoadPage records it)
		want string
	}{
		{
			name: "204 no content",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "image/png")
				w.WriteHeader(http.StatusNoContent)
			},
			// omp renderUrl short-circuits 204 before the image path —
			// the empty body must not stack image-path notes.
			want: "[note] HTTP 204 No Content\n\n",
		},
		{
			name: "200 zero bytes",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "image/png")
				w.WriteHeader(http.StatusOK)
			},
			want: "[note] Fetched image binary; Fetched payload could not be decoded as image/png; returning text metadata only\n\n" +
				"Fetched payload was labeled image/png, but bytes were not a valid image.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()

			out := fetchExecute(t, server.URL)

			if out.Image != nil {
				t.Fatalf("Output.Image must stay nil for an empty image body")
			}
			if out.Content != tc.want {
				t.Errorf("Content = %q, want %q", out.Content, tc.want)
			}
		})
	}
}

// oversizeSolidPNG renders a >3.75MB, <20MB source whose content compresses to
// a sliver of the 300KB output budget: an uncompressed (png.NoCompression)
// solid-color RGBA frame. The old fetch rejected this class at 3.75MB; the
// resize pipeline must now shrink it into an inline block.
func oversizeSolidPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.NoCompression}
	if err := enc.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1200, 1200))); err != nil {
		t.Fatalf("encode oversize png fixture: %v", err)
	}
	if buf.Len() <= 3932160 || buf.Len() > 20*1024*1024 {
		t.Fatalf("oversize fixture = %d bytes, want it in (3.75MB, 20MB)", buf.Len())
	}
	return buf.Bytes()
}

// omp semantics: a source over the old 3.75MB ceiling but under the 20MB cap
// still inlines — resized until the payload fits the 300KB output budget.
func TestExecute_URLFetch_ImageOversizeSourceResizedIntoBudget(t *testing.T) {
	src := oversizeSolidPNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(src)
	}))
	defer server.Close()

	out := fetchExecute(t, server.URL)

	if out.Image == nil {
		t.Fatalf("Output.Image = nil, want the resized image inlined (Content=%q)", out.Content)
	}
	decoded, err := base64.StdEncoding.DecodeString(out.Image.Base64)
	if err != nil {
		t.Fatalf("Image.Base64 does not decode: %v", err)
	}
	if len(decoded) > 300*1024 {
		t.Errorf("resized payload = %d bytes, want <= the 300KB output budget", len(decoded))
	}
	if !supportedInlineImageMime(out.Image.MimeType) {
		t.Errorf("MimeType = %q, want one of the four inline types", out.Image.MimeType)
	}
	if want := "[note] Fetched image binary\n\nFetched image content (image/png)."; out.Content != want {
		t.Errorf("Content = %q, want %q", out.Content, want)
	}
}

// When the ladder terminator cannot reach the output budget (a small solid
// frame cannot compress below its own size — see the utils budget test), omp's
// "exceeds inline output limit after resize" note rejects the inline instead
// of serving an over-budget block. Budget shrunk so the fixture provably
// cannot fit it.
func TestExecute_URLFetch_ImageExceedsOutputBudgetAfterResize(t *testing.T) {
	saved := maxInlineImageOutputBytes
	maxInlineImageOutputBytes = 100
	defer func() { maxInlineImageOutputBytes = saved }()

	pngBytes := pngFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer server.Close()

	out := fetchExecute(t, server.URL)

	if out.Image != nil {
		t.Fatalf("Output.Image must stay nil when the resized payload exceeds the output budget")
	}
	wantNote := "Image exceeds inline output limit after resize ("
	if !strings.Contains(out.Content, wantNote) {
		t.Errorf("Content = %q, want it to contain %q", out.Content, wantNote)
	}
	if !strings.Contains(out.Content, "Fetched image content (image/png), but it is too large to inline render.") {
		t.Errorf("Content = %q, want the too-large sentence", out.Content)
	}

	// At-budget equality inlines: the gate is a strict `>`, so a fast-path
	// payload of exactly maxInlineImageOutputBytes bytes must still ride the
	// wire (a `>=` mutant rejects it).
	saved2 := maxInlineImageOutputBytes
	maxInlineImageOutputBytes = len(pngBytes)
	out = fetchExecute(t, server.URL)
	maxInlineImageOutputBytes = saved2
	if out.Image == nil {
		t.Fatalf("Output.Image = nil at exactly-budget, want inline (Content=%q)", out.Content)
	}
}

// Undecodable payloads that pass the magic sniff degrade to text + note. The
// two routes through the resizer: a header that does not parse at all
// (catch-path passthrough), and a header-valid/body-broken payload that rides
// the fast path until the fetch-side full-decode validation rejects it.
func TestExecute_URLFetch_ImageResizeFailureFallsBack(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{"unparseable header", garbageHeader(64)},
		{"valid header, broken body", pngHeaderPadded(4, 4, 64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(tc.body)
			}))
			defer server.Close()

			out := fetchExecute(t, server.URL)

			if out.Image != nil {
				t.Fatalf("Output.Image must stay nil for an undecodable payload")
			}
			if !strings.Contains(out.Content, "Fetched payload could not be decoded as image/png") {
				t.Errorf("Content = %q, want the could-not-decode note", out.Content)
			}
		})
	}
	t.Run("dimensions exceed resizer cap", func(t *testing.T) {
		body := pngHeaderPadded(9999, 9999, 64)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(body)
		}))
		defer server.Close()

		out := fetchExecute(t, server.URL)

		if out.Image != nil {
			t.Fatalf("Output.Image must stay nil when the resizer errors")
		}
		if !strings.Contains(out.Content, "Fetched payload could not be decoded as image/png") {
			t.Errorf("Content = %q, want the could-not-decode note", out.Content)
		}
		if !strings.Contains(out.Content, "dimensions exceed") {
			t.Errorf("Content = %q, want the resizer error surfaced", out.Content)
		}
	})
}

// Image types the model cannot serialize inline split by body kind (omp serves
// the textual rendering): an svg's text body rides along under the notes, a
// binary (bmp) body stays metadata-only so raw bytes never render as mojibake.
func TestExecute_URLFetch_UnsupportedImageMime(t *testing.T) {
	t.Run("svg body served as text", func(t *testing.T) {
		svg := `<svg xmlns="http://www.w3.org/2000/svg"><circle r="4"/></svg>`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = fmt.Fprint(w, svg)
		}))
		defer server.Close()

		out := fetchExecute(t, server.URL)

		if out.Image != nil {
			t.Fatalf("Output.Image must stay nil for unsupported image MIME type")
		}
		want := "[note] Image MIME type image/svg+xml is unsupported for inline model serialization; returning text metadata only; " +
			"Falling back to textual rendering from initial response\n\n" + svg
		if out.Content != want {
			t.Errorf("Content = %q, want %q", out.Content, want)
		}
	})
	t.Run("binary body metadata only", func(t *testing.T) {
		bmp := append([]byte("BM"), make([]byte, 32)...)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/bmp")
			_, _ = w.Write(bmp)
		}))
		defer server.Close()

		out := fetchExecute(t, server.URL)

		if out.Image != nil {
			t.Fatalf("Output.Image must stay nil for unsupported image MIME type")
		}
		want := "[note] Image MIME type image/bmp is unsupported for inline model serialization; returning text metadata only\n\n" +
			"Fetched image content (image/bmp), but its MIME type is unsupported for inline model serialization."
		if out.Content != want {
			t.Errorf("Content = %q, want %q", out.Content, want)
		}
		if strings.Contains(out.Content, "BM") {
			t.Errorf("binary unsupported-image output must not embed the raw body")
		}
	})
}

// omp: when the binary re-fetch fails, notes explain the fallback and the
// initial response's textual rendering is served.
func TestExecute_URLFetch_ImageBinaryFetchFailsFallsBackToText(t *testing.T) {
	pngBytes := pngFixture(t)
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only GETs count: fetchAndConvertDocument probes with a HEAD first.
		if r.Method != http.MethodGet {
			w.Header().Set("Content-Type", "image/png")
			return
		}
		if gets.Add(1) == 1 {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	out := fetchExecute(t, server.URL)

	if out.Image != nil {
		t.Fatalf("Output.Image must stay nil when the binary re-fetch fails")
	}
	wantNote := "[note] Binary fetch failed: HTTP 500; Falling back to textual rendering from initial response"
	if !strings.Contains(out.Content, wantNote) {
		t.Errorf("Content = %q, want it to contain %q", out.Content, wantNote)
	}
}

// Literal pins for the omp budget pair (fetch.ts:87-88): threshold-value
// mutants are otherwise unkillable — every behavioral test runs either far
// under the output budget or at a shrunk override.
func TestInlineImageBudgetLiterals(t *testing.T) {
	if maxInlineImageSourceBytes != 20971520 {
		t.Errorf("maxInlineImageSourceBytes = %d, want 20971520 (20MB)", maxInlineImageSourceBytes)
	}
	if maxInlineImageOutputBytes != 307200 {
		t.Errorf("maxInlineImageOutputBytes = %d, want 307200 (300KB)", maxInlineImageOutputBytes)
	}
}

// pngHeaderPadded builds a PNG-signature + IHDR(w×h) header (real CRC) padded
// with zero bytes to exactly total bytes. The body is undecodable by design;
// with the valid IHDR the header parsers succeed and only the full decode
// fails; garbageHeader is the variant whose header itself does not parse.
func pngHeaderPadded(w, h uint32, total int) []byte {
	out := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	ihdr := make([]byte, 25) // length + "IHDR" + 13 data bytes + CRC
	binary.BigEndian.PutUint32(ihdr[0:4], 13)
	copy(ihdr[4:8], "IHDR")
	binary.BigEndian.PutUint32(ihdr[8:12], w)
	binary.BigEndian.PutUint32(ihdr[12:16], h)
	ihdr[16], ihdr[17] = 8, 6 // bit depth 8, color type RGBA
	binary.BigEndian.PutUint32(ihdr[21:25], crc32.ChecksumIEEE(ihdr[4:21]))
	out = append(out, ihdr...)
	return append(out, make([]byte, total-len(out))...)
}

// garbageHeader is a PNG signature followed by junk: no parser gets past it,
// so the resizer takes its catch path (small-buffer passthrough) and the
// fetch-side full-decode guard must reject the result.
func garbageHeader(total int) []byte {
	out := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 'j', 'u', 'n', 'k'}
	return append(out, make([]byte, total-len(out))...)
}

// Generic binary MIME + image extension must resolve to the extension's
// image type (omp resolveImageMimeType extHint path) and inline it.
func TestExecute_URLFetch_ImageByExtensionHint(t *testing.T) {
	pngBytes := pngFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(pngBytes)
	}))
	defer server.Close()

	out := fetchExecute(t, server.URL+"/img.png")

	if out.Image == nil {
		t.Fatalf("Output.Image = nil, want extension-hinted inline image (Content=%q)", out.Content)
	}
	if out.Image.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", out.Image.MimeType)
	}
	decoded, err := base64.StdEncoding.DecodeString(out.Image.Base64)
	if err != nil || !bytes.Equal(decoded, pngBytes) {
		t.Errorf("decoded image bytes diverge from the served body (err=%v)", err)
	}
}

// omp renderUrl: a 204 response carries the verbatim note and no body.
func TestExecute_URLFetch_NoContent204(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	out := fetchExecute(t, server.URL)

	if out.Content != "[note] HTTP 204 No Content\n\n" {
		t.Errorf("Content = %q, want the 204 note over an empty body", out.Content)
	}
}

// resolveImageMimeType ports omp's mime+extHint inference: image/* wins,
// generic binary mimes fall back to the extension map, everything else is
// not an image.
func TestResolveImageMimeType(t *testing.T) {
	cases := []struct {
		mime, ext, want string
	}{
		{"image/png", "", "image/png"},
		{"image/svg+xml", "", "image/svg+xml"},
		{"", ".png", "image/png"},
		{"", ".jpg", "image/jpeg"},
		{"", ".jpeg", "image/jpeg"},
		{"", ".gif", "image/gif"},
		{"", ".webp", "image/webp"},
		{"", ".pdf", ""},
		{"application/octet-stream", ".png", "image/png"},
		{"binary/octet-stream", ".webp", "image/webp"},
		{"unknown", ".jpg", "image/jpeg"},
		{"text/html", ".png", ""},
		{"application/pdf", ".png", ""},
	}
	for _, tc := range cases {
		if got := resolveImageMimeType(tc.mime, tc.ext); got != tc.want {
			t.Errorf("resolveImageMimeType(%q, %q) = %q, want %q", tc.mime, tc.ext, got, tc.want)
		}
	}
}

func TestSupportedInlineImageMime(t *testing.T) {
	for _, mime := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		if !supportedInlineImageMime(mime) {
			t.Errorf("supportedInlineImageMime(%q) = false, want true", mime)
		}
	}
	for _, mime := range []string{"image/svg+xml", "image/bmp", "image/tiff", "image/x-icon", "image/jpg", ""} {
		if supportedInlineImageMime(mime) {
			t.Errorf("supportedInlineImageMime(%q) = true, want false", mime)
		}
	}
}

// The sniff pair over the four magic types and the liars: DetectContentType
// decides from the bytes, so a lying Content-Type header cannot push a body
// onto the inline path and an empty body sniffs as text/plain.
func TestSniffedInlineImageMime(t *testing.T) {
	png := pngFixture(t)
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"png", png, "image/png"},
		{"jpeg", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}, "image/jpeg"},
		{"gif", []byte("GIF89a" + "\x00\x00"), "image/gif"},
		{"webp", append([]byte("RIFF\x00\x00\x00\x00"), []byte("WEBPVP8 ")...), "image/webp"},
		{"empty", nil, ""},
		{"html", []byte("<html><body>x</body></html>"), ""},
		{"bmp", append([]byte("BM"), make([]byte, 8)...), ""},
		{"text", []byte("plain text"), ""},
	}
	for _, tc := range cases {
		if got := sniffedInlineImageMime(tc.data); got != tc.want {
			t.Errorf("%s: sniffedInlineImageMime = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The 512-byte cap is load-bearing at the boundary: a NUL just inside the
// window makes the body binary, one just outside leaves it textual. The
// trailing padding keeps both bodies over 512 bytes so the trim itself runs
// (a ±1 on the slice bound flips exactly these two).
func TestTextuallyDecodable_Boundary(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"nul inside the cap", strings.Repeat("a", 511) + "\x00" + strings.Repeat("a", 100), false},
		{"nul at the cap edge", strings.Repeat("a", 512) + "\x00" + strings.Repeat("a", 100), true},
		{"svg text", "<svg xmlns=\"http://www.w3.org/2000/svg\"/>", true},
		{"binary", "\x00\x01\x02", false},
	}
	for _, tc := range cases {
		if got := textuallyDecodable(tc.content); got != tc.want {
			t.Errorf("%s: textuallyDecodable = %v, want %v", tc.name, got, tc.want)
		}
	}
}
