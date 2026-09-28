package wui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"sync"
	"testing"
)

// embeddedAssetCase pins one /assets/ route: the URL, the exact bytes the
// route must deliver (the embed source), and the Content-Type it must carry
// under both encodings.
type embeddedAssetCase struct {
	url  string
	body []byte
	ct   string
}

func newEmbeddedAssetCases(t *testing.T) []embeddedAssetCase {
	t.Helper()
	mustEmbed := func(p string) []byte {
		b, err := fs.ReadFile(decoderAssets, p)
		if err != nil {
			t.Fatalf("read vendored %s: %v", p, err)
		}
		return b
	}
	return []embeddedAssetCase{
		{"/assets/novnc.esm.js", novncESM, "text/javascript; charset=utf-8"},
		{"/assets/model-viewer.esm.js", modelViewerESM, "text/javascript; charset=utf-8"},
		{"/assets/draco/draco_decoder.wasm", mustEmbed("assets/draco/draco_decoder.wasm"), "application/wasm"},
		{"/assets/draco/draco_wasm_wrapper.js", mustEmbed("assets/draco/draco_wasm_wrapper.js"), "text/javascript; charset=utf-8"},
		{"/assets/basis/basis_transcoder.js", mustEmbed("assets/basis/basis_transcoder.js"), "text/javascript; charset=utf-8"},
		{"/assets/basis/basis_transcoder.wasm", mustEmbed("assets/basis/basis_transcoder.wasm"), "application/wasm"},
	}
}

// contentETagTag is the bare content hash both validators derive from: the
// identity ETag quotes it, the gzip ETag quotes it behind a "gz-" prefix.
func contentETagTag(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func TestEmbeddedAssetsGzipNegotiation(t *testing.T) {
	srv := newStaticTestServer(t)
	for _, tc := range newEmbeddedAssetCases(t) {
		tag := contentETagTag(tc.body)

		status, h, gzBody, clen := probeArtifact(t, srv.URL+tc.url, "gzip", nil)
		if status != http.StatusOK {
			t.Fatalf("%s gzip status = %d, want 200", tc.url, status)
		}
		if got := h.Get("Content-Encoding"); got != "gzip" {
			t.Errorf("%s Content-Encoding = %q, want gzip", tc.url, got)
		}
		if got := h.Get("ETag"); got != `"gz-`+tag+`"` {
			t.Errorf("%s gzip ETag = %q, want %q", tc.url, got, `"gz-`+tag+`"`)
		}
		if got := h.Get("Content-Type"); got != tc.ct {
			t.Errorf("%s gzip Content-Type = %q, want %q", tc.url, got, tc.ct)
		}
		if got := h.Get("Vary"); got != "Accept-Encoding" {
			t.Errorf("%s Vary = %q, want Accept-Encoding", tc.url, got)
		}
		if clen != int64(len(gzBody)) {
			t.Errorf("%s Content-Length = %d, want the exact compressed size %d", tc.url, clen, len(gzBody))
		}
		if plain := gunzipAll(t, gzBody); !bytes.Equal(plain, tc.body) {
			t.Errorf("%s gunzipped body = %d bytes, want the embed source (%d)", tc.url, len(plain), len(tc.body))
		}

		status, h, body, clen := probeArtifact(t, srv.URL+tc.url, "identity", nil)
		if status != http.StatusOK {
			t.Fatalf("%s identity status = %d, want 200", tc.url, status)
		}
		if got := h.Get("Content-Encoding"); got != "" {
			t.Errorf("%s identity Content-Encoding = %q, want empty", tc.url, got)
		}
		if got := h.Get("ETag"); got != `"`+tag+`"` {
			t.Errorf("%s identity ETag = %q, want the hash quoted without the gz- prefix (%q)", tc.url, got, `"`+tag+`"`)
		}
		if got := h.Get("Content-Type"); got != tc.ct {
			t.Errorf("%s identity Content-Type = %q, want %q", tc.url, got, tc.ct)
		}
		if got := h.Get("Vary"); got != "Accept-Encoding" {
			t.Errorf("%s identity Vary = %q, want Accept-Encoding", tc.url, got)
		}
		if clen != int64(len(tc.body)) {
			t.Errorf("%s identity Content-Length = %d, want %d", tc.url, clen, len(tc.body))
		}
		if !bytes.Equal(body, tc.body) {
			t.Errorf("%s identity body = %d bytes, want the exact embed source (%d)", tc.url, len(body), len(tc.body))
		}
	}
}

// A client that sends no Accept-Encoding at all (no auto-injection) must get
// the identity representation, not a guessed gzip one.
func TestEmbeddedAssetsAbsentAEIdentity(t *testing.T) {
	srv := newStaticTestServer(t)
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Get(srv.URL + "/assets/novnc.esm.js")
	if err != nil {
		t.Fatal(err)
	}
	body := readAllAndClose(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want empty without Accept-Encoding", got)
	}
	if !bytes.Equal(body, novncESM) {
		t.Errorf("body = %d bytes, want the exact embed source (%d)", len(body), len(novncESM))
	}
}

func TestEmbeddedAssetsRevalidation(t *testing.T) {
	srv := newStaticTestServer(t)
	for _, tc := range newEmbeddedAssetCases(t) {
		url := srv.URL + tc.url
		idTag := `"` + contentETagTag(tc.body) + `"`
		gzTag := `"gz-` + contentETagTag(tc.body) + `"`

		status, h, body, _ := probeArtifact(t, url, "gzip", map[string]string{"If-None-Match": gzTag})
		if status != http.StatusNotModified {
			t.Fatalf("%s INM(gz)+gzip status = %d, want 304", tc.url, status)
		}
		if len(body) != 0 {
			t.Errorf("%s 304 body = %d bytes, want 0", tc.url, len(body))
		}
		if got := h.Get("ETag"); got != gzTag {
			t.Errorf("%s 304 ETag = %q, want the echoed %q", tc.url, got, gzTag)
		}
		if got := h.Get("Content-Encoding"); got != "" {
			t.Errorf("%s 304 Content-Encoding = %q, want empty", tc.url, got)
		}
		if got := h.Get("Vary"); got != "Accept-Encoding" {
			t.Errorf("%s 304 Vary = %q, want Accept-Encoding", tc.url, got)
		}

		status, _, body, _ = probeArtifact(t, url, "identity", map[string]string{"If-None-Match": idTag})
		if status != http.StatusNotModified {
			t.Fatalf("%s INM(identity)+identity status = %d, want 304", tc.url, status)
		}
		if len(body) != 0 {
			t.Errorf("%s identity 304 body = %d bytes, want 0", tc.url, len(body))
		}

		// The gz- prefix keeps validators distinct: an identity-tagged cache
		// under Accept-Encoding: gzip must be re-served, never false-304'd.
		status, h, body, _ = probeArtifact(t, url, "gzip", map[string]string{"If-None-Match": idTag})
		if status != http.StatusOK {
			t.Fatalf("%s identity INM under gzip status = %d, want 200", tc.url, status)
		}
		if got := h.Get("ETag"); got != gzTag {
			t.Errorf("%s cross-encoding ETag = %q, want the gzip tag %q", tc.url, got, gzTag)
		}
		if plain := gunzipAll(t, body); !bytes.Equal(plain, tc.body) {
			t.Errorf("%s cross-encoding body gunzipped = %d bytes, want %d", tc.url, len(plain), len(tc.body))
		}
	}
}

func TestEmbeddedAssetsHEAD(t *testing.T) {
	srv := newStaticTestServer(t)
	for _, tc := range newEmbeddedAssetCases(t) {
		_, _, gzBody, _ := probeArtifact(t, srv.URL+tc.url, "gzip", nil)

		req, err := http.NewRequest(http.MethodHead, srv.URL+tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body := readAllAndClose(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s HEAD status = %d, want 200", tc.url, resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
			t.Errorf("%s HEAD Content-Encoding = %q, want gzip", tc.url, got)
		}
		if resp.ContentLength != int64(len(gzBody)) {
			t.Errorf("%s HEAD Content-Length = %d, want the compressed size %d", tc.url, resp.ContentLength, len(gzBody))
		}
		if len(body) != 0 {
			t.Errorf("%s HEAD body = %d bytes, want 0", tc.url, len(body))
		}
	}
}

// The embeds are process-immutable: the compressed representation is computed
// at most once per asset, and never for identity-only clients. The memo is
// process-global and earlier tests warm it, so the test starts from a cold
// map (the route reads the package var per request, so live servers pick the
// swap up).
func TestEmbeddedAssetsGzipCompressesOnce(t *testing.T) {
	embeddedGzips = &sync.Map{}
	srv := newStaticTestServer(t)
	before := embeddedGzipCompressions.Load()

	status, _, _, _ := probeArtifact(t, srv.URL+"/assets/novnc.esm.js", "gzip", nil)
	if status != http.StatusOK {
		t.Fatalf("gzip GET status = %d, want 200", status)
	}
	afterFirst := embeddedGzipCompressions.Load()
	if afterFirst != before+1 {
		t.Fatalf("compressions after first GET = %d, want %d", afterFirst, before+1)
	}

	status, _, _, secondLen := probeArtifact(t, srv.URL+"/assets/novnc.esm.js", "gzip", nil)
	if status != http.StatusOK {
		t.Fatalf("second gzip GET status = %d, want 200", status)
	}
	if got := embeddedGzipCompressions.Load(); got != afterFirst {
		t.Errorf("compressions after second GET = %d, want %d (memoized, no re-compress)", got, afterFirst)
	}
	if secondLen <= 0 {
		t.Errorf("second Content-Length = %d, want a positive compressed size", secondLen)
	}

	status, _, body, _ := probeArtifact(t, srv.URL+"/assets/novnc.esm.js", "identity", nil)
	if status != http.StatusOK {
		t.Fatalf("identity GET status = %d, want 200", status)
	}
	if !bytes.Equal(body, novncESM) {
		t.Errorf("identity body = %d bytes, want the embed source (%d)", len(body), len(novncESM))
	}
	if got := embeddedGzipCompressions.Load(); got != afterFirst {
		t.Errorf("compressions after identity GET = %d, want %d (identity never compresses)", got, afterFirst)
	}
}
