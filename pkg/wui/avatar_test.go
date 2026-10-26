package wui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pngFixture is a standard 1×1 PNG — the server never decodes pixels, so
// magic bytes + roundtrip identity are all that matters.
var pngFixture, _ = base64.StdEncoding.DecodeString(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

// jpegFixture carries the JFIF marker after the magic bytes so sniffing has
// something realistic to look at.
var jpegFixture = append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0},
	bytes.Repeat([]byte{0x00}, 64)...)

// newAvatarServer mounts only the avatar routes over an isolated HOME.
func newAvatarServer(t *testing.T) *httptest.Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	mux := http.NewServeMux()
	RegisterAvatarRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// avatarPath returns $HOME/.gbot/avatars/<who>.png under the isolated HOME.
func avatarPath(t *testing.T, who string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, ".gbot", "avatars", who+".png")
}

// putAvatar PUTs body with the given Content-Type and returns the response.
func putAvatar(t *testing.T, srv *httptest.Server, who, contentType string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/avatar/"+who, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// wantErrorBody asserts the {"error": msg} envelope byte-exactly — status
// plus message, so neither can drift.
func wantErrorBody(t *testing.T, resp *http.Response, status int, msg string) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("status = %d, want %d", resp.StatusCode, status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"error":"` + msg + `"}`; string(body) != want {
		t.Fatalf("body = %s, want %s", body, want)
	}
}

// chmodRO flips dir perms and restores them on cleanup, so write-dependent
// teardown (t.TempDir removal) still works.
func chmodRO(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	})
}

// want500HomeUnset re-issues one request per method with HOME empty, so
// config.ConfigDir fails inside the handler: every method must surface 500.
func TestAvatarConfigDirFailure500(t *testing.T) {
	srv := newAvatarServer(t)
	t.Setenv("HOME", "")
	do := func(method string) {
		t.Helper()
		var body io.Reader
		if method == http.MethodPut {
			body = bytes.NewReader(pngFixture)
		}
		req, err := http.NewRequest(method, srv.URL+"/api/avatar/agent", body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "image/png")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("%s status = %d, want 500", method, resp.StatusCode)
		}
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if want := `{"error":"$HOME is not defined"}`; string(b) != want {
			t.Fatalf("%s body = %s, want %s", method, b, want)
		}
	}
	do(http.MethodGet)
	do(http.MethodPut)
	do(http.MethodDelete)
}

// The who whitelist must be a name check, not a path lookup: a stray file
// named like an unknown identity must not become servable.
func TestAvatarGet_WhoWhitelistShadowsFile(t *testing.T) {
	srv := newAvatarServer(t)
	path := avatarPath(t, "bot")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pngFixture, 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(srv.URL + "/api/avatar/bot")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 despite bot.png existing on disk", resp.StatusCode)
	}
}

func TestAvatarPut_MkdirAllFailure(t *testing.T) {
	srv := newAvatarServer(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".gbot"), 0o755); err != nil {
		t.Fatal(err)
	}
	chmodRO(t, filepath.Join(home, ".gbot"))
	resp := putAvatar(t, srv, "agent", "image/png", pngFixture)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("PUT status = %d, want 500 with unwritable config dir", resp.StatusCode)
	}
	// Exact body distinguishes the MkdirAll failure from the CreateTemp
	// failure that would follow if the error were swallowed.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	avatars := filepath.Join(home, ".gbot", "avatars")
	want := `{"error":"mkdir ` + avatars + `: permission denied"}`
	if string(body) != want {
		t.Fatalf("body = %s, want %s", body, want)
	}
}

func TestAvatarPut_CreateTempFailure(t *testing.T) {
	srv := newAvatarServer(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	avatars := filepath.Join(home, ".gbot", "avatars")
	if err := os.MkdirAll(avatars, 0o755); err != nil {
		t.Fatal(err)
	}
	chmodRO(t, avatars)
	resp := putAvatar(t, srv, "agent", "image/png", pngFixture)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("PUT status = %d, want 500 with unwritable avatars dir", resp.StatusCode)
	}
}

// A directory squatting on the avatar name breaks the final rename; the
// failure must 500 and must not strand the temp file.
func TestAvatarPut_RenameFailure(t *testing.T) {
	srv := newAvatarServer(t)
	if err := os.MkdirAll(avatarPath(t, "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	resp := putAvatar(t, srv, "agent", "image/png", pngFixture)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("PUT status = %d, want 500 when target is a directory", resp.StatusCode)
	}
	entries, err := os.ReadDir(filepath.Dir(avatarPath(t, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "agent.png" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("failed rename left entries = %v, want only [agent.png]", names)
	}
}

func TestAvatarDelete_RemoveFailure(t *testing.T) {
	srv := newAvatarServer(t)
	if resp := putAvatar(t, srv, "agent", "image/png", pngFixture); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	chmodRO(t, filepath.Join(home, ".gbot", "avatars"))
	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/avatar/agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("DELETE status = %d, want 500 with unwritable avatars dir", resp.StatusCode)
	}
}

func TestAvatarGet_MissingFile404(t *testing.T) {
	srv := newAvatarServer(t)
	resp, err := http.Get(srv.URL + "/api/avatar/agent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Fatalf("body = %q, want empty", body)
	}
}

func TestAvatarInvalidWho404(t *testing.T) {
	srv := newAvatarServer(t)
	do := func(method string) *http.Response {
		var body io.Reader
		if method == http.MethodPut {
			body = bytes.NewReader(pngFixture)
		}
		req, err := http.NewRequest(method, srv.URL+"/api/avatar/bot", body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "image/png")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if resp := do(method); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", method, resp.StatusCode)
		}
	}
}

func TestAvatarGet_Directory404(t *testing.T) {
	srv := newAvatarServer(t)
	if err := os.MkdirAll(avatarPath(t, "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(srv.URL + "/api/avatar/agent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAvatarPut_WritesFileAndGetRoundtrips(t *testing.T) {
	srv := newAvatarServer(t)
	resp := putAvatar(t, srv, "agent", "image/png", pngFixture)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("PUT body = %s, want {\"ok\":true}", body)
	}
	st, err := os.Stat(avatarPath(t, "agent"))
	if err != nil {
		t.Fatalf("file missing after PUT: %v", err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Fatalf("file mode = %o, want 644", st.Mode().Perm())
	}

	gresp, err := http.Get(srv.URL + "/api/avatar/agent")
	if err != nil {
		t.Fatal(err)
	}
	defer gresp.Body.Close()
	if gresp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", gresp.StatusCode)
	}
	if ct := gresp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Fatalf("Content-Type = %q, want image/png prefix", ct)
	}
	if cc := gresp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
	etag := gresp.Header.Get("ETag")
	if matched, _ := regexp.MatchString(`^[0-9a-f]+-[0-9a-f]+$`, strings.Trim(etag, `"`)); !matched {
		t.Fatalf("ETag = %q, want mtimeNano-size hex form", etag)
	}
	got, err := io.ReadAll(gresp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pngFixture) {
		t.Fatalf("GET body differs from fixture: %d vs %d bytes", len(got), len(pngFixture))
	}
}

func TestAvatarPut_JpegAcceptedAndServedSniffed(t *testing.T) {
	srv := newAvatarServer(t)
	resp := putAvatar(t, srv, "user", "image/jpeg", jpegFixture)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}
	gresp, err := http.Get(srv.URL + "/api/avatar/user")
	if err != nil {
		t.Fatal(err)
	}
	defer gresp.Body.Close()
	if gresp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", gresp.StatusCode)
	}
	if ct := gresp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/jpeg") {
		t.Fatalf("Content-Type = %q, want sniffed image/jpeg despite .png filename", ct)
	}
}

func TestAvatarPut_LazyDirCreation(t *testing.T) {
	srv := newAvatarServer(t)
	resp := putAvatar(t, srv, "agent", "image/png", pngFixture)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}
	if _, err := os.Stat(avatarPath(t, "agent")); err != nil {
		t.Fatalf("file missing after lazy-dir PUT: %v", err)
	}
	// Lazy creation is the only writer of the dir, so its perms are
	// observable contract (MkdirAll applies umask; tests run with 022).
	dst, err := os.Stat(filepath.Dir(avatarPath(t, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	if dst.Mode().Perm() != 0o755 {
		t.Fatalf("avatars dir mode = %o, want 755", dst.Mode().Perm())
	}
}

func TestAvatarPut_RejectsWrongContentType(t *testing.T) {
	srv := newAvatarServer(t)
	cases := []struct {
		name        string
		contentType string
		body        []byte
		status      int
		msg         string
	}{
		{"text/plain", "text/plain", pngFixture, http.StatusUnsupportedMediaType, "unsupported content type"},
		{"missing header", "", pngFixture, http.StatusUnsupportedMediaType, "unsupported content type"},
		// ParseMediaType returns the base type even when a parameter is
		// malformed — the header must still be rejected as a whole.
		{"malformed params", "image/png; charset", pngFixture, http.StatusUnsupportedMediaType, "unsupported content type"},
		{"empty body", "image/png", nil, http.StatusBadRequest, "empty body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantErrorBody(t, putAvatar(t, srv, "agent", tc.contentType, tc.body), tc.status, tc.msg)
		})
	}
}

func TestAvatarPut_RejectsMagicMismatch(t *testing.T) {
	srv := newAvatarServer(t)
	cases := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{"jpeg declared png bytes", "image/jpeg", pngFixture},
		{"png declared jpeg bytes", "image/png", jpegFixture},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantErrorBody(t, putAvatar(t, srv, "agent", tc.contentType, tc.body),
				http.StatusBadRequest, "content does not match declared image type")
		})
	}
}

// pngMagic + arbitrary payload builds a PNG-declared body of any size: the
// server validates magic bytes, never decodes pixels.
var pngMagic = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

func paddedPNG(n int) []byte {
	return append(append([]byte{}, pngMagic...), bytes.Repeat([]byte{0x00}, n)...)
}

func TestAvatarPut_UnderLimitAccepted(t *testing.T) {
	srv := newAvatarServer(t)
	// 1.5MB sits between the mutated half-size cap and the real cap — it
	// pins the limit from below (the oversize test pins it from above).
	resp := putAvatar(t, srv, "agent", "image/png", paddedPNG(1536*1024))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}
}

func TestAvatarPut_RejectsOversize(t *testing.T) {
	srv := newAvatarServer(t)
	// Literal 2MB+1: referencing avatarMaxBytes here would move the goalpost
	// together with any mutation of the constant.
	body := paddedPNG(2*1024*1024 + 1)
	wantErrorBody(t, putAvatar(t, srv, "agent", "image/png", body),
		http.StatusRequestEntityTooLarge, "avatar exceeds 2MB limit")
	if _, err := os.Stat(avatarPath(t, "agent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversize PUT must not leave a file, stat err = %v", err)
	}
}

// Magic-only bodies pin the length boundaries: the guard accepts a body
// that is exactly the magic bytes and rejects one byte shorter.
func TestAvatarPut_MagicLengthBoundaries(t *testing.T) {
	srv := newAvatarServer(t)
	cases := []struct {
		name        string
		contentType string
		body        []byte
		status      int
	}{
		{"png exactly magic", "image/png", pngMagic, http.StatusOK},
		{"png one byte short", "image/png", pngMagic[:7], http.StatusBadRequest},
		{"jpeg three bytes", "image/jpeg", []byte{0xFF, 0xD8, 0xFF}, http.StatusOK},
		{"jpeg two bytes", "image/jpeg", []byte{0xFF, 0xD8}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := putAvatar(t, srv, "agent", tc.contentType, tc.body)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
		})
	}
}

func TestAvatarPut_NoTempLeftovers(t *testing.T) {
	srv := newAvatarServer(t)
	if resp := putAvatar(t, srv, "agent", "image/png", pngFixture); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}
	entries, err := os.ReadDir(filepath.Dir(avatarPath(t, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "agent.png" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("avatars dir entries = %v, want exactly [agent.png]", names)
	}
}

func TestAvatarGet_Conditional304(t *testing.T) {
	srv := newAvatarServer(t)
	if resp := putAvatar(t, srv, "agent", "image/png", pngFixture); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}
	gresp, err := http.Get(srv.URL + "/api/avatar/agent")
	if err != nil {
		t.Fatal(err)
	}
	etag := gresp.Header.Get("ETag")
	_, _ = io.Copy(io.Discard, gresp.Body)
	_ = gresp.Body.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/avatar/agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("If-None-Match", etag)
	condResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer condResp.Body.Close()
	if condResp.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional GET status = %d, want 304", condResp.StatusCode)
	}
	if got := condResp.Header.Get("ETag"); got != etag {
		t.Fatalf("304 ETag = %q, want %q", got, etag)
	}
	body, err := io.ReadAll(condResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Fatalf("304 body = %q, want empty", body)
	}
}

func TestAvatarGet_HEADReturnsHeadersNoBody(t *testing.T) {
	srv := newAvatarServer(t)
	if resp := putAvatar(t, srv, "agent", "image/png", pngFixture); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}
	resp, err := http.Head(srv.URL + "/api/avatar/agent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatal("HEAD ETag missing")
	}
	if resp.ContentLength <= 0 {
		t.Fatalf("HEAD Content-Length = %d, want > 0", resp.ContentLength)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Fatalf("HEAD body = %q, want empty", body)
	}
}

func TestAvatarDelete_RemovesThenIdempotent(t *testing.T) {
	srv := newAvatarServer(t)
	if resp := putAvatar(t, srv, "agent", "image/png", pngFixture); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}
	for i := 0; i < 2; i++ {
		req, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/avatar/agent", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("DELETE #%d status = %d, want 204", i+1, resp.StatusCode)
		}
		if len(body) != 0 {
			t.Fatalf("DELETE #%d body = %q, want empty", i+1, body)
		}
	}
	gresp, err := http.Get(srv.URL + "/api/avatar/agent")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, gresp.Body)
	_ = gresp.Body.Close()
	if gresp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after DELETE status = %d, want 404", gresp.StatusCode)
	}
}

func TestAvatarLifecycle(t *testing.T) {
	srv := newAvatarServer(t)
	if resp := putAvatar(t, srv, "user", "image/png", pngFixture); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", resp.StatusCode)
	}

	gresp, err := http.Get(srv.URL + "/api/avatar/user")
	if err != nil {
		t.Fatal(err)
	}
	etag := gresp.Header.Get("ETag")
	got, err := io.ReadAll(gresp.Body)
	_ = gresp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if gresp.StatusCode != http.StatusOK || !bytes.Equal(got, pngFixture) {
		t.Fatalf("GET status = %d bytes = %d, want 200 with fixture bytes", gresp.StatusCode, len(got))
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/avatar/user", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("If-None-Match", etag)
	condResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = condResp.Body.Close()
	if condResp.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional GET status = %d, want 304", condResp.StatusCode)
	}

	dreq, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/avatar/user", nil)
	if err != nil {
		t.Fatal(err)
	}
	dresp, err := http.DefaultClient.Do(dreq)
	if err != nil {
		t.Fatal(err)
	}
	_ = dresp.Body.Close()
	if dresp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", dresp.StatusCode)
	}

	fresp, err := http.Get(srv.URL + "/api/avatar/user")
	if err != nil {
		t.Fatal(err)
	}
	_ = fresp.Body.Close()
	if fresp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after DELETE status = %d, want 404", fresp.StatusCode)
	}
}
