package wui

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newStaticTestServer mounts only the static routes (no artifact dir needed
// for decoder asset assertions).
func newStaticTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	RegisterStaticRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// decoderAssetCase pins one served decoder file: its exact Content-Type and
// that the body is byte-identical to the vendored source (not a truncated or
// substituted payload).
type decoderAssetCase struct {
	url  string
	path string // embed.FS path of the vendored source
	ct   string
}

// readAllAndClose drains and closes a response body, failing the test on
// any transport defect so assertions never run on a partial body.
func readAllAndClose(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s body: %v", resp.Request.URL.Path, err)
	}
	return body
}

func TestStaticRoutesServeDecoderAssets(t *testing.T) {
	srv := newStaticTestServer(t)
	cases := []decoderAssetCase{
		{"/assets/draco/draco_decoder.wasm", "assets/draco/draco_decoder.wasm", "application/wasm"},
		{"/assets/draco/draco_wasm_wrapper.js", "assets/draco/draco_wasm_wrapper.js", "text/javascript; charset=utf-8"},
		{"/assets/basis/basis_transcoder.js", "assets/basis/basis_transcoder.js", "text/javascript; charset=utf-8"},
		{"/assets/basis/basis_transcoder.wasm", "assets/basis/basis_transcoder.wasm", "application/wasm"},
	}
	for _, tc := range cases {
		resp, err := http.Get(srv.URL + tc.url)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.url, err)
		}
		body := readAllAndClose(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", tc.url, resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Type"); got != tc.ct {
			t.Errorf("%s Content-Type = %q, want %q", tc.url, got, tc.ct)
		}
		src, err := fs.ReadFile(decoderAssets, tc.path)
		if err != nil {
			t.Fatalf("read vendored %s: %v", tc.path, err)
		}
		if len(body) != len(src) {
			t.Errorf("%s body = %d bytes, want the vendored %d", tc.url, len(body), len(src))
		}
		if string(body) != string(src) {
			t.Errorf("%s body differs from the vendored bytes", tc.url)
		}
		if etag := resp.Header.Get("ETag"); etag == "" {
			t.Errorf("%s has no ETag, want a content hash for 304 revalidation", tc.url)
		}
	}
}

// The decoders are immutable for the process lifetime — a matching
// If-None-Match must save the 929 KB re-download.
func TestStaticRoutesDecoderAssetETag304(t *testing.T) {
	srv := newStaticTestServer(t)
	resp, err := http.Get(srv.URL + "/assets/draco/draco_decoder.wasm")
	if err != nil {
		t.Fatal(err)
	}
	etag := resp.Header.Get("ETag")
	readAllAndClose(t, resp)
	if etag == "" {
		t.Fatal("first GET carried no ETag")
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/assets/draco/draco_decoder.wasm", nil)
	req.Header.Set("If-None-Match", etag)
	reval, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer reval.Body.Close()
	if reval.StatusCode != http.StatusNotModified {
		t.Fatalf("INM revalidation = %d, want 304", reval.StatusCode)
	}
	if body, _ := io.ReadAll(reval.Body); len(body) != 0 {
		t.Fatalf("304 body = %d bytes, want empty", len(body))
	}
}

func TestStaticRoutesDecoderUnknownFile404s(t *testing.T) {
	srv := newStaticTestServer(t)
	for _, url := range []string{
		"/assets/draco/missing_decoder.wasm",
		"/assets/basis/not_a_file.js",
	} {
		resp, err := http.Get(srv.URL + url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		body := readAllAndClose(t, resp)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", url, resp.StatusCode)
		}
		// http.NotFound's fixed plaintext payload (net/http writes exactly
		// this), not the SPA index.html the catch-all would emit.
		if string(body) != "404 page not found\n" {
			t.Errorf("%s body = %q, want the http.NotFound payload", url, body)
		}
	}
}
