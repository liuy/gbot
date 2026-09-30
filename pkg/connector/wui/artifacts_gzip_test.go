package wui

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// bigGLBRepeat x "glTF-payload\n" = 1,571,856 bytes, above the 1 MiB gzip
// threshold but far below any test timeout cost; the payload is text so it
// sniffs compressible (not a blocked medium).
const bigGLBRepeat = 120912

func bigGLB() string {
	return strings.Repeat("glTF-payload\n", bigGLBRepeat)
}

// The tags are quoted strong validators: "hex-hex" (identity) and
// "gz-hex-hex" (gzip). The plan's sketch omitted the closing quote, which
// can never match a real ETag, so both patterns anchor on both quotes.
var identityETagRe = regexp.MustCompile(`^"[0-9a-f]+-[0-9a-f]+"$`)
var gzipETagRe = regexp.MustCompile(`^"gz-[0-9a-f]+-[0-9a-f]+"$`)

// probeArtifact fetches url with optional request headers and returns the
// status, response headers, raw body bytes and resp.ContentLength. The Go
// client injects Accept-Encoding: gzip on its own when the header is absent,
// which would both trigger the gzip path invisibly and transparently
// decompress the response, so every probe sets an explicit Accept-Encoding.
// ContentLength is returned separately because net/http hoists the wire
// Content-Length out of the Header map (a Header.Get assertion on it can
// never fail).
func probeArtifact(t *testing.T, url, acceptEncoding string, headers map[string]string) (int, http.Header, []byte, int64) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", acceptEncoding)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, body, resp.ContentLength
}

// gunzipAll decompresses a raw gzip stream, failing the test on any defect.
func gunzipAll(t *testing.T, raw []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gunzipped body: %v", err)
	}
	if err := zr.Close(); err != nil {
		t.Fatalf("close gzip reader: %v", err)
	}
	return out
}

func TestArtifactGzipIdentityResponse(t *testing.T) {
	dir := t.TempDir()
	content := bigGLB()
	writeArtifactFile(t, dir, "big.glb", content)
	srv := newArtifactTestServer(t, dir)

	status, h, body, _ := probeArtifact(t, srv.URL+"/artifacts/big.glb", "identity", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := h.Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want empty on identity", got)
	}
	if got := h.Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q, want Accept-Encoding on every file response", got)
	}
	if !identityETagRe.MatchString(h.Get("ETag")) {
		t.Errorf("ETag = %q, want identity format \"<hex>-<hex>\"", h.Get("ETag"))
	}
	if got := h.Get("Content-Type"); got != "model/gltf-binary" {
		t.Errorf("Content-Type = %q, want model/gltf-binary", got)
	}
	if !bytes.Equal(body, []byte(content)) {
		t.Errorf("body = %d bytes, want the exact file content (%d)", len(body), len(content))
	}

	// Range stays ServeContent's job on the identity path.
	status, _, rangeBody, _ := probeArtifact(t, srv.URL+"/artifacts/big.glb", "identity", map[string]string{"Range": "bytes=0-9"})
	if status != http.StatusPartialContent {
		t.Fatalf("Range status = %d, want 206", status)
	}
	if !bytes.Equal(rangeBody, []byte(content[:10])) {
		t.Errorf("Range body = %q, want the first 10 bytes", string(rangeBody))
	}
}

func TestArtifactGzipServesCompressed(t *testing.T) {
	dir := t.TempDir()
	content := bigGLB()
	writeArtifactFile(t, dir, "big.glb", content)
	srv := newArtifactTestServer(t, dir)

	status, h, body, contentLength := probeArtifact(t, srv.URL+"/artifacts/big.glb", "gzip", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := h.Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", got)
	}
	if !gzipETagRe.MatchString(h.Get("ETag")) {
		t.Errorf("ETag = %q, want gzip format \"gz-<hex>-<hex>\"", h.Get("ETag"))
	}
	if got := h.Get("Content-Type"); got != "model/gltf-binary" {
		t.Errorf("Content-Type = %q, want model/gltf-binary", got)
	}
	if got := h.Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q, want Accept-Encoding", got)
	}
	// net/http hoists the wire Content-Length into resp.ContentLength. A
	// known-up-front size is the point of the cache: fetch progress needs a
	// denominator, so the compressed byte count must be on the wire.
	if contentLength != int64(len(body)) {
		t.Errorf("resp.ContentLength = %d, want %d (exact compressed size)", contentLength, len(body))
	}
	if got := h.Get("Last-Modified"); got == "" {
		t.Error("Last-Modified empty, want the modtime validator for IMS revalidation")
	}
	if plain := gunzipAll(t, body); !bytes.Equal(plain, []byte(content)) {
		t.Errorf("gunzipped body = %d bytes, want the exact file content (%d)", len(plain), len(content))
	}

	// Range is advisory and ignored on the compressed stream: the whole
	// gunzipped body must come back under a 200.
	status, _, rangeBody, _ := probeArtifact(t, srv.URL+"/artifacts/big.glb", "gzip", map[string]string{"Range": "bytes=0-99"})
	if status != http.StatusOK {
		t.Fatalf("Range+gzip status = %d, want 200", status)
	}
	if plain := gunzipAll(t, rangeBody); !bytes.Equal(plain, []byte(content)) {
		t.Errorf("Range+gzip gunzipped body = %d bytes, want the full content (%d)", len(plain), len(content))
	}
}

func TestArtifactGzipRevalidation(t *testing.T) {
	dir := t.TempDir()
	content := bigGLB()
	writeArtifactFile(t, dir, "big.glb", content)
	srv := newArtifactTestServer(t, dir)
	url := srv.URL + "/artifacts/big.glb"

	_, idH, _, _ := probeArtifact(t, url, "identity", nil)
	identityTag := idH.Get("ETag")
	_, gzH, _, _ := probeArtifact(t, url, "gzip", nil)
	gzTag := gzH.Get("ETag")

	// Same-encoding revalidation: 304, empty body, validator echoed.
	status, h, body, _ := probeArtifact(t, url, "gzip", map[string]string{"If-None-Match": gzTag})
	if status != http.StatusNotModified {
		t.Fatalf("INM(gz) status = %d, want 304", status)
	}
	if len(body) != 0 {
		t.Errorf("304 body = %d bytes, want 0", len(body))
	}
	if got := h.Get("ETag"); got != gzTag {
		t.Errorf("304 ETag = %q, want the echoed %q", got, gzTag)
	}
	if got := h.Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("304 Vary = %q, want Accept-Encoding", got)
	}
	if got := h.Get("Content-Encoding"); got != "" {
		t.Errorf("304 Content-Encoding = %q, want empty (writeNotModified semantics)", got)
	}
	if got := h.Get("Content-Type"); got != "" {
		t.Errorf("304 Content-Type = %q, want empty (writeNotModified semantics)", got)
	}
	// net/http writeNotModified drops Last-Modified whenever an ETag is
	// present: the 304 must echo exactly one validator so a client cannot
	// pair a fresh ETag with a stale modtime across representations.
	if got := h.Get("Last-Modified"); got != "" {
		t.Errorf("304 Last-Modified = %q, want empty (ETag present ⇒ dropped)", got)
	}

	// No false 304 across encodings: an identity-tagged cache under
	// Accept-Encoding: gzip must be re-served the gzip representation.
	status, h, body, _ = probeArtifact(t, url, "gzip", map[string]string{"If-None-Match": identityTag})
	if status != http.StatusOK {
		t.Fatalf("identity INM under gzip status = %d, want 200 (per-encoding tags)", status)
	}
	if plain := gunzipAll(t, body); !bytes.Equal(plain, []byte(content)) {
		t.Errorf("cross-encoding body gunzipped = %d bytes, want %d", len(plain), len(content))
	}
	if got := h.Get("ETag"); got != gzTag {
		t.Errorf("cross-encoding ETag = %q, want the gzip tag %q", got, gzTag)
	}

	// IMS alone negotiates 304; the file's own modtime is the deterministic
	// input (a moving clock could trail the mtime).
	fi := statArtifact(t, dir, "big.glb")
	status, _, body, _ = probeArtifact(t, url, "gzip", map[string]string{"If-Modified-Since": fi.ModTime().UTC().Format(http.TimeFormat)})
	if status != http.StatusNotModified {
		t.Fatalf("IMS status = %d, want 304", status)
	}
	if len(body) != 0 {
		t.Errorf("IMS 304 body = %d bytes, want 0", len(body))
	}

	// A malformed If-None-Match matches nothing and must not 304.
	status, _, body, _ = probeArtifact(t, url, "gzip", map[string]string{"If-None-Match": "garbage"})
	if status != http.StatusOK {
		t.Fatalf("malformed INM status = %d, want 200", status)
	}
	if plain := gunzipAll(t, body); !bytes.Equal(plain, []byte(content)) {
		t.Errorf("malformed INM body gunzipped = %d bytes, want %d", len(plain), len(content))
	}

	// Precedence guard: a stale INM plus a fresh IMS is a 200 in net/http
	// (INM present means IMS is ignored). An OR-combined precondition check
	// returns a wrong 304 here and fails this block.
	status, _, body, _ = probeArtifact(t, url, "gzip", map[string]string{
		"If-None-Match":     `"deadbeef-1"`,
		"If-Modified-Since": fi.ModTime().UTC().Format(http.TimeFormat),
	})
	if status != http.StatusOK {
		t.Fatalf("stale INM + fresh IMS status = %d, want 200 (INM decides, IMS ignored)", status)
	}
	if plain := gunzipAll(t, body); !bytes.Equal(plain, []byte(content)) {
		t.Errorf("stale INM + fresh IMS body gunzipped = %d bytes, want %d", len(plain), len(content))
	}
}

func TestArtifactGzipIneligibleServesIdentity(t *testing.T) {
	dir := t.TempDir()
	small := strings.Repeat("s", 1024)
	// PNG magic forces the media sniff to classify the file as already
	// compressed; the filler pads past the size threshold.
	bigPNG := "\x89PNG\r\n\x1a\n" + strings.Repeat("P", 1572864-8)
	writeArtifactFile(t, dir, "small.glb", small)
	writeArtifactFile(t, dir, "big.png", bigPNG)
	srv := newArtifactTestServer(t, dir)

	for name, want := range map[string]string{
		"small.glb": small,
		"big.png":   bigPNG,
	} {
		status, h, body, _ := probeArtifact(t, srv.URL+"/artifacts/"+name, "gzip", nil)
		if status != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", name, status)
		}
		if got := h.Get("Content-Encoding"); got != "" {
			t.Errorf("%s Content-Encoding = %q, want empty", name, got)
		}
		// Full byte equality: the eligibility sniff consumes the first 512
		// bytes, and a missing rewind truncates exactly this body.
		if !bytes.Equal(body, []byte(want)) {
			t.Errorf("%s body = %d bytes, want the exact file content (%d)", name, len(body), len(want))
		}
	}
}

func TestArtifactGzipHEADStreamsNoBody(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "big.glb", bigGLB())
	srv := newArtifactTestServer(t, dir)

	req, err := http.NewRequest(http.MethodHead, srv.URL+"/artifacts/big.glb", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Errorf("HEAD body = %d bytes, want 0", len(body))
	}
	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", got)
	}
}

// resetArtifactGzipCache swaps in a fresh cache with the production caps so
// each cache test counts compressions and entries from a known zero state
// (the route reads the package var per request, so live servers pick the
// swap up).
func resetArtifactGzipCache(t *testing.T) {
	t.Helper()
	artifactGzipCache = newGzipCache(artifactGzipCacheMaxEntries, artifactGzipCacheMaxBytes)
}

func TestArtifactGzipCompressesOncePerFileIdentity(t *testing.T) {
	resetArtifactGzipCache(t)
	dir := t.TempDir()
	content := bigGLB()
	writeArtifactFile(t, dir, "big.glb", content)
	srv := newArtifactTestServer(t, dir)

	_, _, first, firstLen := probeArtifact(t, srv.URL+"/artifacts/big.glb", "gzip", nil)
	if got := artifactGzipCache.compressions.Load(); got != 1 {
		t.Fatalf("compressions after first GET = %d, want 1", got)
	}
	if firstLen != int64(len(first)) {
		t.Errorf("first Content-Length = %d, want %d", firstLen, len(first))
	}

	_, _, second, secondLen := probeArtifact(t, srv.URL+"/artifacts/big.glb", "gzip", nil)
	if got := artifactGzipCache.compressions.Load(); got != 1 {
		t.Errorf("compressions after second GET = %d, want 1 (cache hit, no re-compress)", got)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("second body = %d bytes, want identical cached bytes (%d)", len(second), len(first))
	}
	if secondLen != int64(len(second)) {
		t.Errorf("second Content-Length = %d, want %d", secondLen, len(second))
	}
}

func TestArtifactGzipCacheInvalidatedByMtime(t *testing.T) {
	resetArtifactGzipCache(t)
	dir := t.TempDir()
	writeArtifactFile(t, dir, "big.glb", bigGLB())
	full := filepath.Join(dir, "big.glb")
	// Fixed timestamps keep the test clock-free; only their ordering matters.
	before := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	after := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(full, before, before); err != nil {
		t.Fatal(err)
	}
	srv := newArtifactTestServer(t, dir)
	url := srv.URL + "/artifacts/big.glb"

	_, hA, _, _ := probeArtifact(t, url, "gzip", nil)
	tagA := hA.Get("ETag")
	if got := artifactGzipCache.compressions.Load(); got != 1 {
		t.Fatalf("compressions after first GET = %d, want 1", got)
	}

	if err := os.Chtimes(full, after, after); err != nil {
		t.Fatal(err)
	}
	_, hB, bodyB, _ := probeArtifact(t, url, "gzip", nil)
	if got := artifactGzipCache.compressions.Load(); got != 2 {
		t.Errorf("compressions after mtime change = %d, want 2 (re-compress)", got)
	}
	if tagB := hB.Get("ETag"); tagB == tagA {
		t.Errorf("ETag after mtime change = %q, want a new validator (was %q)", tagB, tagA)
	}
	if plain := gunzipAll(t, bodyB); !bytes.Equal(plain, []byte(bigGLB())) {
		t.Errorf("re-compressed body gunzipped = %d bytes, want %d", len(plain), len(bigGLB()))
	}

	// Unchanged identity again: the refreshed entry must be a cache hit
	// (probeArtifact itself fails the test on any transport error).
	probeArtifact(t, url, "gzip", nil)
	if got := artifactGzipCache.compressions.Load(); got != 2 {
		t.Errorf("compressions after third GET = %d, want 2 (new identity cached)", got)
	}
}

func TestArtifactGzipCacheSingleFlight(t *testing.T) {
	resetArtifactGzipCache(t)
	dir := t.TempDir()
	content := bigGLB()
	writeArtifactFile(t, dir, "big.glb", content)
	srv := newArtifactTestServer(t, dir)

	const requests = 8
	start := make(chan struct{})
	type result struct {
		status int
		body   []byte
		err    error
	}
	results := make(chan result, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/artifacts/big.glb", nil)
			if err != nil {
				results <- result{err: err}
				return
			}
			req.Header.Set("Accept-Encoding", "gzip")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- result{err: err}
				return
			}
			body, err := io.ReadAll(resp.Body)
			if closeErr := resp.Body.Close(); closeErr != nil {
				t.Errorf("close response body: %v", closeErr)
			}
			results <- result{status: resp.StatusCode, body: body, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	seen := []byte(nil)
	for res := range results {
		if res.err != nil {
			t.Fatalf("concurrent GET: %v", res.err)
		}
		if res.status != http.StatusOK {
			t.Fatalf("concurrent GET status = %d, want 200", res.status)
		}
		if seen == nil {
			seen = res.body
			continue
		}
		if !bytes.Equal(res.body, seen) {
			t.Errorf("concurrent body = %d bytes, want identical representation (%d bytes)", len(res.body), len(seen))
		}
	}
	if got := artifactGzipCache.compressions.Load(); got != 1 {
		t.Errorf("compressions for %d parallel first-hits = %d, want 1 (single flight)", requests, got)
	}
}

func TestArtifactGzipCacheEvictsAtEntryCap(t *testing.T) {
	resetArtifactGzipCache(t)
	dir := t.TempDir()
	for _, name := range []string{"a.glb", "b.glb", "c.glb", "d.glb"} {
		writeArtifactFile(t, dir, name, bigGLB())
	}
	srv := newArtifactTestServer(t, dir)
	get := func(name string) {
		t.Helper()
		// probeArtifact itself fails the test on any transport error.
		status, _, _, _ := probeArtifact(t, srv.URL+"/artifacts/"+name, "gzip", nil)
		if status != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", name, status)
		}
	}

	// Four distinct identities against a three-entry cache: each is a miss.
	for _, name := range []string{"a.glb", "b.glb", "c.glb", "d.glb"} {
		get(name)
	}
	if got := artifactGzipCache.compressions.Load(); got != 4 {
		t.Fatalf("compressions after four first-hits = %d, want 4", got)
	}

	// b,c,d are the surviving entries; a.glb was the LRU victim.
	get("b.glb")
	if got := artifactGzipCache.compressions.Load(); got != 4 {
		t.Errorf("compressions after cached re-GET = %d, want 4 (b.glb still resident)", got)
	}

	get("a.glb")
	if got := artifactGzipCache.compressions.Load(); got != 5 {
		t.Errorf("compressions after evicted re-GET = %d, want 5 (a.glb was evicted)", got)
	}
}

func TestGzipCacheByteCapEvictsLRU(t *testing.T) {
	// Entry cap far above three; three six-byte payloads against a
	// twelve-byte cap force byte-driven eviction, which the route-level
	// tests cannot reach without padding hundreds of megabytes. The cap
	// fits two entries, so inserting the third must evict the
	// least-recently-used one — b, not the just-touched a.
	c := newGzipCache(10, 12)
	calls := map[string]int{}
	payload := func(key string) func() ([]byte, error) {
		return func() ([]byte, error) {
			calls[key]++
			return make([]byte, 6), nil
		}
	}

	if _, err := c.get("a", 1, 1, payload("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.get("b", 1, 1, payload("b")); err != nil {
		t.Fatal(err)
	}
	if calls["a"] != 1 || calls["b"] != 1 {
		t.Fatalf("compress calls = %v, want a=1 b=1", calls)
	}
	// Touch a: b becomes the LRU.
	if _, err := c.get("a", 1, 1, payload("a")); err != nil {
		t.Fatal(err)
	}
	if calls["a"] != 1 {
		t.Fatalf("compress calls for warm a = %d, want 1 (identity hit)", calls["a"])
	}

	if _, err := c.get("c", 1, 1, payload("c")); err != nil {
		t.Fatal(err)
	}
	if calls["c"] != 1 {
		t.Fatalf("compress calls for c = %d, want 1", calls["c"])
	}

	if _, err := c.get("a", 1, 1, payload("a")); err != nil {
		t.Fatal(err)
	}
	if calls["a"] != 1 {
		t.Errorf("compress calls for touched a = %d, want 1 (byte cap evicted b, the LRU, not a)", calls["a"])
	}
	if _, err := c.get("c", 1, 1, payload("c")); err != nil {
		t.Fatal(err)
	}
	if calls["c"] != 1 {
		t.Errorf("compress calls for c = %d, want 1 (inserted alongside a within the cap)", calls["c"])
	}
	if _, err := c.get("b", 1, 1, payload("b")); err != nil {
		t.Fatal(err)
	}
	if calls["b"] != 2 {
		t.Errorf("compress calls for evicted b = %d, want 2 (byte cap evicted it)", calls["b"])
	}
}

func TestGzipCacheCompressErrorPropagates(t *testing.T) {
	c := newGzipCache(3, 1000)
	sentinel := errors.New("compress boom")
	calls := 0
	failing := func() ([]byte, error) {
		calls++
		return nil, sentinel
	}

	if _, err := c.get("a", 1, 1, failing); !errors.Is(err, sentinel) {
		t.Fatalf("get err = %v, want the compress error", err)
	}
	if calls != 1 {
		t.Fatalf("compress calls = %d, want 1", calls)
	}
	// A failure must not poison the key: the next caller retries — which
	// also proves the failed flight was removed from the calls map (a stale
	// entry would hand back the old error without recompressing).
	if _, err := c.get("a", 1, 1, failing); !errors.Is(err, sentinel) {
		t.Fatalf("retry err = %v, want the compress error", err)
	}
	if calls != 2 {
		t.Errorf("compress calls after retry = %d, want 2 (errors are not cached)", calls)
	}
}

// statArtifact stats a file under dir, failing the test when the fixture is
// missing so assertions never run against a nonexistent validator source.
func statArtifact(t *testing.T, dir, name string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("stat %s: %v", name, err)
	}
	return fi
}
