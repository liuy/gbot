package mcp

// Tests here pin the escape mutants found by the go-mutesting census batch on
// auth.go (2026-10-25: 910 mutants, 603 killed, 306 escaped). Each test is
// written against specific census instances (line + mutator + orig→repl), so
// applying that mutant turns the assertion red while unmutated code stays green.
//
// The exactness (full messages, error chains, every struct field, boundary
// values) is the point: the escapes survived the pre-existing suite almost
// entirely because those tests assert only "an error happened" or
// Contains(substring), which a mutant that changes the message shape, the
// wrapped chain, or one field of a returned struct still satisfies.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

// Tests outside a synctest bubble must not call time.Now() (weak-assertion rule), so
// expiries there use fixed epoch values: epochExpiredMs is always in the past and
// farFutureMs is always in the future.
const (
	epochExpiredMs = 1
	farFutureMs    = 4102444800000
)

// ---------------------------------------------------------------------------
// auth.go:39,43,47,51,59,60 — numbers/decrementer + numbers/incrementer
// These are protocol/persistence constants copied from auth.ts; ±1 shifts are
// invisible to the behavioural tests but change timeout and retry semantics.
// ---------------------------------------------------------------------------

func TestAuthConstantsExactValues(t *testing.T) {
	if AuthRequestTimeoutMs != 30000 {
		t.Errorf("AuthRequestTimeoutMs = %d, want 30000 (auth.ts:65)", AuthRequestTimeoutMs)
	}
	if MaxLockRetries != 5 {
		t.Errorf("MaxLockRetries = %d, want 5 (auth.ts:94)", MaxLockRetries)
	}
	if OAuthCallbackTimeout != 5*time.Minute {
		t.Errorf("OAuthCallbackTimeout = %v, want 5m (auth.ts:1209)", OAuthCallbackTimeout)
	}
	if OAuthTokenExpiryBuffer != 300*time.Second {
		t.Errorf("OAuthTokenExpiryBuffer = %v, want 300s (auth.ts:1650)", OAuthTokenExpiryBuffer)
	}
	if lockRetryBase != 1000 {
		t.Errorf("lockRetryBase = %d, want 1000 (auth.ts:2122 retry base)", lockRetryBase)
	}
	if lockRetryInc != 300 {
		t.Errorf("lockRetryInc = %d, want 300 (auth.ts:2122 retry increment)", lockRetryInc)
	}
	if redirectPortFallback != 3118 {
		t.Errorf("redirectPortFallback = %d, want 3118 (oauthPort.ts fallback)", redirectPortFallback)
	}
}

// ---------------------------------------------------------------------------
// auth.go:117,120,123 — composite/field-clear + branch/case in GetServerKey.
// The key is the on-disk identity of a credential file: dropping a field of the
// marshalled config changes the SHA-256 digest and silently orphans the stored
// token. A golden derivation is the only assertion that can see a dropped
// "type" field — no two configs collide when only that field is missing.
// ---------------------------------------------------------------------------

func TestGetServerKeyIsSHA256OfCanonicalConfigJSON(t *testing.T) {
	sse := &SSEConfig{URL: "http://example.com/mcp", Headers: map[string]string{"k": "v"}}
	httpc := &HTTPConfig{URL: "http://example.com/mcp", Headers: map[string]string{"k": "v"}}

	// json.Marshal sorts map keys, so the canonical bytes are fully known.
	canonicalSSE := `{"headers":{"k":"v"},"type":"sse","url":"http://example.com/mcp"}`
	canonicalHTTP := `{"headers":{"k":"v"},"type":"http","url":"http://example.com/mcp"}`

	if got := GetServerKey("srv", sse); got != wantServerKey("srv", canonicalSSE) {
		t.Errorf("GetServerKey(sse) = %q, want %q (digest of %s)", got, wantServerKey("srv", canonicalSSE), canonicalSSE)
	}
	if got := GetServerKey("srv", httpc); got != wantServerKey("srv", canonicalHTTP) {
		t.Errorf("GetServerKey(http) = %q, want %q (digest of %s)", got, wantServerKey("srv", canonicalHTTP), canonicalHTTP)
	}

	// Differential guards for the url / headers clears.
	otherURL := GetServerKey("srv", &SSEConfig{URL: "http://other.example.com/mcp", Headers: map[string]string{"k": "v"}})
	if otherURL == GetServerKey("srv", sse) {
		t.Error("SSE configs with different URLs must produce different keys")
	}
	otherHeaders := GetServerKey("srv", &SSEConfig{URL: "http://example.com/mcp", Headers: map[string]string{"k": "other"}})
	if otherHeaders == GetServerKey("srv", sse) {
		t.Error("SSE configs with different headers must produce different keys")
	}
	if GetServerKey("srv", sse) == GetServerKey("srv", httpc) {
		t.Error("sse and http configs with the same url and headers must produce different keys")
	}

	// Default branch (branch/case 123): non-sse/http configs hash their %v
	// rendering, so two stdio configs must not collide.
	stdio1 := &StdioConfig{Command: "first-cmd", Args: []string{"arg1"}}
	stdio2 := &StdioConfig{Command: "second-cmd", Args: []string{"arg1"}}
	key1 := GetServerKey("stdio-server", stdio1)
	if key1 != wantServerKey("stdio-server", fmt.Sprintf("%v", stdio1)) {
		t.Errorf("GetServerKey(stdio) = %q, want digest of %q", key1, fmt.Sprintf("%v", stdio1))
	}
	if key1 == GetServerKey("stdio-server", stdio2) {
		t.Error("stdio configs with different commands must produce different keys")
	}
	if key1 == GetServerKey("stdio-server", sse) {
		t.Error("stdio key must not collide with an sse key")
	}
}

func wantServerKey(serverName, canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf("%s|%x", serverName, sum)[:len(serverName)+17]
}

// ---------------------------------------------------------------------------
// auth.go:164,189,194,210,237,242,258,270 — expression/errorf-wrap (%w→%v).
// %w→%v leaves the message text identical; only errors.Is/errors.As through the
// wrapped chain can tell them apart, so the assertions go through the chain.
// ---------------------------------------------------------------------------

func TestFileTokenStoreErrorWrapping(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	key := "wrap-key"
	if err := store.WriteTokens(key, &OAuthTokenData{ServerName: key, AccessToken: "tok"}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	if err := store.WriteClientConfig(key, &OAuthClientConfig{ClientSecret: "sec"}); err != nil {
		t.Fatalf("WriteClientConfig: %v", err)
	}

	tokenPath := store.tokenPath(key)
	if err := os.Chmod(tokenPath, 0000); err != nil {
		t.Fatalf("chmod token file: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(tokenPath, 0600) })
	_, err = store.ReadTokens(key)
	if err == nil {
		t.Fatal("ReadTokens() = nil, want error for an unreadable token file")
	}
	if !strings.HasPrefix(err.Error(), "mcp: failed to read token data: ") {
		t.Errorf("ReadTokens error = %q, want prefix %q", err.Error(), "mcp: failed to read token data: ")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("ReadTokens error chain lost fs.ErrPermission: %v", err)
	}

	if err := os.Chmod(tokenPath, 0600); err != nil {
		t.Fatalf("chmod token file back: %v", err)
	}
	if err := os.WriteFile(tokenPath, []byte("{bad json"), 0600); err != nil {
		t.Fatalf("WriteFile corrupt token: %v", err)
	}
	_, err = store.ReadTokens(key)
	if err == nil {
		t.Fatal("ReadTokens() = nil, want error for corrupted token JSON")
	}
	if !strings.HasPrefix(err.Error(), "mcp: failed to parse token data: ") {
		t.Errorf("ReadTokens error = %q, want prefix %q", err.Error(), "mcp: failed to parse token data: ")
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("ReadTokens error chain lost *json.SyntaxError: %v", err)
	}

	if err := store.DeleteTokens("absent-key"); err != nil {
		t.Errorf("DeleteTokens(absent) = %v, want nil", err)
	}

	roDir := filepath.Join(dir, "ro-store")
	if err := os.MkdirAll(roDir, 0700); err != nil {
		t.Fatalf("MkdirAll ro store: %v", err)
	}
	wStore, err := NewFileTokenStore(roDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore(ro): %v", err)
	}
	if err := os.Chmod(roDir, 0500); err != nil {
		t.Fatalf("chmod ro store: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0700) })

	err = wStore.WriteTokens("k", &OAuthTokenData{AccessToken: "tok"})
	if err == nil {
		t.Fatal("WriteTokens() = nil, want error for a read-only store dir")
	}
	if !strings.HasPrefix(err.Error(), "mcp: failed to write token data: ") {
		t.Errorf("WriteTokens error = %q, want prefix %q", err.Error(), "mcp: failed to write token data: ")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("WriteTokens error chain lost fs.ErrPermission: %v", err)
	}

	err = wStore.WriteClientConfig("k", &OAuthClientConfig{ClientSecret: "s"})
	if err == nil {
		t.Fatal("WriteClientConfig() = nil, want error for a read-only store dir")
	}
	if !strings.HasPrefix(err.Error(), "mcp: failed to write client config: ") {
		t.Errorf("WriteClientConfig error = %q, want prefix %q", err.Error(), "mcp: failed to write client config: ")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("WriteClientConfig error chain lost fs.ErrPermission: %v", err)
	}

	if err := os.Chmod(roDir, 0700); err != nil {
		t.Fatalf("chmod ro store back: %v", err)
	}
	cfgPath := wStore.clientConfigPath("cfg")
	if err := os.WriteFile(cfgPath, []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile client cfg: %v", err)
	}
	if err := os.Chmod(cfgPath, 0000); err != nil {
		t.Fatalf("chmod client cfg: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfgPath, 0600) })
	cfg, err := wStore.ReadClientConfig("cfg")
	if err == nil {
		t.Fatalf("ReadClientConfig() = (%+v, nil), want error for an unreadable client config", cfg)
	}
	if !strings.HasPrefix(err.Error(), "mcp: failed to read client config: ") {
		t.Errorf("ReadClientConfig error = %q, want prefix %q", err.Error(), "mcp: failed to read client config: ")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("ReadClientConfig error chain lost fs.ErrPermission: %v", err)
	}

	badCfg := wStore.clientConfigPath("bad")
	if err := os.WriteFile(badCfg, []byte("{oops"), 0600); err != nil {
		t.Fatalf("WriteFile bad client cfg: %v", err)
	}
	if err := os.Chmod(badCfg, 0400); err != nil {
		t.Fatalf("chmod bad client cfg: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(badCfg, 0600) })
	_, err = wStore.ReadClientConfig("bad")
	if err == nil {
		t.Fatal("ReadClientConfig() = nil, want a parse error")
	}
	if !strings.HasPrefix(err.Error(), "mcp: failed to parse client config: ") {
		t.Errorf("ReadClientConfig error = %q, want prefix %q", err.Error(), "mcp: failed to parse client config: ")
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("ReadClientConfig error chain lost *json.SyntaxError: %v", err)
	}

	if err := os.Chmod(roDir, 0500); err != nil {
		t.Fatalf("chmod ro store read-only again: %v", err)
	}
	err = wStore.DeleteClientConfig("cfg")
	if err == nil {
		t.Fatal("DeleteClientConfig() = nil, want error for a read-only store dir")
	}
	if !strings.HasPrefix(err.Error(), "mcp: failed to delete client config: ") {
		t.Errorf("DeleteClientConfig error = %q, want prefix %q", err.Error(), "mcp: failed to delete client config: ")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("DeleteClientConfig error chain lost fs.ErrPermission: %v", err)
	}
}

func TestNewFileTokenStoreDirErrorWrapping(t *testing.T) {
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile blocker: %v", err)
	}
	store, err := NewFileTokenStore(filepath.Join(blocker, "sub"))
	if err == nil {
		t.Fatalf("NewFileTokenStore() = (%+v, nil), want an error when the parent is a file", store)
	}
	if !strings.HasPrefix(err.Error(), "mcp: failed to create token store dir: ") {
		t.Errorf("NewFileTokenStore error = %q, want prefix %q", err.Error(), "mcp: failed to create token store dir: ")
	}
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("NewFileTokenStore error chain lost syscall.ENOTDIR: %v", err)
	}
}

// ---------------------------------------------------------------------------
// auth.go:257 — numbers/decrementer + numbers/incrementer on the 0600 literal.
// 0600→0577 keeps owner rw and adds group rw; 0600→0601 adds other-execute.
// Both stay readable by the owner, so only the mode bits themselves can see it.
// ---------------------------------------------------------------------------

func TestFileTokenStoreFileModeBitsExact(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if info := mustStat(t, dir); info.Mode().Perm() != 0700 {
		t.Errorf("store dir mode = %04o, want 0700", info.Mode().Perm())
	}
	if err := store.WriteTokens("k", &OAuthTokenData{AccessToken: "a"}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	if info := mustStat(t, store.tokenPath("k")); info.Mode().Perm() != 0600 {
		t.Errorf("token file mode = %04o, want 0600", info.Mode().Perm())
	}
	if err := store.WriteClientConfig("k", &OAuthClientConfig{ClientSecret: "s"}); err != nil {
		t.Fatalf("WriteClientConfig: %v", err)
	}
	if info := mustStat(t, store.clientConfigPath("k")); info.Mode().Perm() != 0600 {
		t.Errorf("client config file mode = %04o, want 0600", info.Mode().Perm())
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	return info
}

// TestFileTokenStoreMutexHeldDuringBody pins statement/defer-remove on the
// store's defer mu.Unlock (182, 202, 230, 250). Dropping only the `defer`
// keyword still unlocks before return, so single-threaded behaviour is
// unchanged: what is lost is mutual exclusion *inside* the body. A FIFO at the
// token path parks the body in open() until the test opens the other end, which
// keeps the body inside the locked region for as long as the probe runs.
func TestFileTokenStoreMutexHeldDuringBody(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	t.Run("ReadTokens", func(t *testing.T) {
		path := store.tokenPath("fifo-read")
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatalf("Mkfifo: %v", err)
		}
		done := make(chan string, 1)
		go func() {
			data, err := store.ReadTokens("fifo-read")
			if err != nil {
				done <- "err:" + err.Error()
				return
			}
			if data == nil {
				done <- "nil"
				return
			}
			done <- data.AccessToken
		}()
		held, samples := probeStoreMutexHeld(store)
		w, err := os.OpenFile(path, os.O_WRONLY, 0600)
		if err != nil {
			t.Fatalf("open fifo writer: %v", err)
		}
		if _, err := w.Write([]byte(`{"accessToken":"fifo-token"}`)); err != nil {
			t.Fatalf("fifo write: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("fifo close: %v", err)
		}
		if got := <-done; got != "fifo-token" {
			t.Errorf("ReadTokens over fifo = %q, want fifo-token", got)
		}
		if held < samples*9/10 {
			t.Error(mutexNotHeld("ReadTokens", held, samples))
		}
	})

	t.Run("WriteTokens", func(t *testing.T) {
		path := store.tokenPath("fifo-write")
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatalf("Mkfifo: %v", err)
		}
		done := make(chan string, 1)
		go func() {
			if err := store.WriteTokens("fifo-write", &OAuthTokenData{AccessToken: "fifo-written"}); err != nil {
				done <- "err:" + err.Error()
				return
			}
			done <- "ok"
		}()
		held, samples := probeStoreMutexHeld(store)
		r, err := os.OpenFile(path, os.O_RDONLY, 0600)
		if err != nil {
			t.Fatalf("open fifo reader: %v", err)
		}
		body, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("fifo read: %v", err)
		}
		if err := r.Close(); err != nil {
			t.Fatalf("fifo close: %v", err)
		}
		if got := <-done; got != "ok" {
			t.Fatalf("WriteTokens over fifo = %q, want ok", got)
		}
		if !strings.Contains(string(body), "fifo-written") {
			t.Errorf("WriteTokens wrote %q, want it to contain the token", string(body))
		}
		if held < samples*9/10 {
			t.Error(mutexNotHeld("WriteTokens", held, samples))
		}
	})

	t.Run("ReadClientConfig", func(t *testing.T) {
		path := store.clientConfigPath("fifo-cfg-read")
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatalf("Mkfifo: %v", err)
		}
		done := make(chan string, 1)
		go func() {
			cfg, err := store.ReadClientConfig("fifo-cfg-read")
			if err != nil {
				done <- "err:" + err.Error()
				return
			}
			if cfg == nil {
				done <- "nil"
				return
			}
			done <- cfg.ClientSecret
		}()
		held, samples := probeStoreMutexHeld(store)
		w, err := os.OpenFile(path, os.O_WRONLY, 0600)
		if err != nil {
			t.Fatalf("open fifo writer: %v", err)
		}
		if _, err := w.Write([]byte(`{"clientSecret":"fifo-secret"}`)); err != nil {
			t.Fatalf("fifo write: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("fifo close: %v", err)
		}
		if got := <-done; got != "fifo-secret" {
			t.Errorf("ReadClientConfig over fifo = %q, want fifo-secret", got)
		}
		if held < samples*9/10 {
			t.Error(mutexNotHeld("ReadClientConfig", held, samples))
		}
	})

	t.Run("WriteClientConfig", func(t *testing.T) {
		path := store.clientConfigPath("fifo-cfg-write")
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatalf("Mkfifo: %v", err)
		}
		done := make(chan string, 1)
		go func() {
			if err := store.WriteClientConfig("fifo-cfg-write", &OAuthClientConfig{ClientSecret: "fifo-written"}); err != nil {
				done <- "err:" + err.Error()
				return
			}
			done <- "ok"
		}()
		held, samples := probeStoreMutexHeld(store)
		r, err := os.OpenFile(path, os.O_RDONLY, 0600)
		if err != nil {
			t.Fatalf("open fifo reader: %v", err)
		}
		body, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("fifo read: %v", err)
		}
		if err := r.Close(); err != nil {
			t.Fatalf("fifo close: %v", err)
		}
		if got := <-done; got != "ok" {
			t.Fatalf("WriteClientConfig over fifo = %q, want ok", got)
		}
		if !strings.Contains(string(body), "fifo-written") {
			t.Errorf("WriteClientConfig wrote %q, want it to contain the secret", string(body))
		}
		if held < samples*9/10 {
			t.Error(mutexNotHeld("WriteClientConfig", held, samples))
		}
	})
}

// probeStoreMutexHeld samples the store mutex while the store call is parked in
// a FIFO open and reports how many samples found it held. Sampling repeatedly is
// what makes this reliable: a mutant that unlocks up-front still owns the mutex
// for the couple of nanoseconds between Lock and Unlock, so a single sample can
// be fooled, while a real lock-holder fails nearly every sample. It has to run
// before the fifo's other end is opened: that open is what lets the parked store
// call finish and unlock.
func probeStoreMutexHeld(store *FileTokenStore) (held, samples int) {
	// Batches with a yield in between, instead of a wall-clock window: the yield is
	// what lets the store goroutine reach Lock(), and the sample ratio (not elapsed
	// time) is what decides the verdict.
	for range 200 {
		for range 1000 {
			if !store.mu.TryLock() {
				held++
			} else {
				store.mu.Unlock()
			}
			samples++
		}
		runtime.Gosched()
	}
	return held, samples
}

func mutexNotHeld(name string, held, samples int) string {
	return fmt.Sprintf("%s let go of the store mutex while its body was parked: only %d of %d probes found it held", name, held, samples)
}

// ---------------------------------------------------------------------------
// auth.go:293 — statement/return: the redacted URL becomes "".
// ---------------------------------------------------------------------------

func TestRedactSensitiveURLParamsExactOutput(t *testing.T) {
	got := RedactSensitiveURLParams("http://example.com/auth?state=secret&code=abc&client_id=pub")
	want := "http://example.com/auth?client_id=pub&code=%5BREDACTED%5D&state=%5BREDACTED%5D"
	if got != want {
		t.Errorf("RedactSensitiveURLParams = %q, want %q", got, want)
	}
	if got == "" {
		t.Error("RedactSensitiveURLParams returned an empty URL")
	}
	keep := "http://example.com/auth?client_id=pub"
	if got := RedactSensitiveURLParams(keep); got != keep {
		t.Errorf("RedactSensitiveURLParams(%q) = %q, want it unchanged", keep, got)
	}
}

// ---------------------------------------------------------------------------
// auth.go:325 — numbers/incrementer on `len(ScopesSupported) > 0` (0→1).
// ---------------------------------------------------------------------------

func TestGetScopeFromMetadataSingleScopeSupported(t *testing.T) {
	got := GetScopeFromMetadata(&OAuthMetadata{ScopesSupported: []string{"only-scope"}})
	if got != "only-scope" {
		t.Errorf("GetScopeFromMetadata(one scope) = %q, want %q", got, "only-scope")
	}
	got = GetScopeFromMetadata(&OAuthMetadata{ScopesSupported: []string{"a", "b"}})
	if got != "a b" {
		t.Errorf("GetScopeFromMetadata(two scopes) = %q, want %q", got, "a b")
	}
}

// ---------------------------------------------------------------------------
// auth.go:420 — numbers/incrementer on `configuredPort > 0` (0→1).
// A configured port of 1 is valid and must be returned verbatim.
// ---------------------------------------------------------------------------

func TestFindAvailablePortConfiguredPortOfOne(t *testing.T) {
	t.Setenv("MCP_OAUTH_CALLBACK_PORT", "1")
	port, err := FindAvailablePort()
	if err != nil {
		t.Fatalf("FindAvailablePort: %v", err)
	}
	if port != 1 {
		t.Errorf("FindAvailablePort with MCP_OAUTH_CALLBACK_PORT=1 returned %d, want 1", port)
	}
}

// TestFindAvailablePortAlwaysInPlatformRange pins branch/if 432, loop/condition
// 429 and conditional/negated 429: every one of those mutants leaves the random
// loop without a port, so the fallback (3118, outside the platform range) or a
// zero becomes the answer.
func TestFindAvailablePortAlwaysInPlatformRange(t *testing.T) {
	t.Setenv("MCP_OAUTH_CALLBACK_PORT", "")
	min, max := redirectPortRange()
	for i := range 3 {
		port, err := FindAvailablePort()
		if err != nil {
			t.Fatalf("FindAvailablePort call %d: %v", i+1, err)
		}
		if port < min || port > max {
			t.Errorf("FindAvailablePort call %d returned %d, want inside [%d,%d]", i+1, port, min, max)
		}
	}
}

// ---------------------------------------------------------------------------
// auth.go:531,532,533 — conditional/negated, branch/if, errorf-wrap and
// select/case-remove on the callback server's failure arm.
// ---------------------------------------------------------------------------

func TestPerformMCPOAuthFlow_CallbackServerBindFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	t.Setenv("MCP_OAUTH_CALLBACK_PORT", fmt.Sprintf("%d", port))

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	// A short parent deadline bounds the mutant run: with the send removed the
	// flow would otherwise sit in the select until the 5 minute callback timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	config := OAuthFlowConfig{
		ServerName:   "bind-failure-server",
		ServerConfig: &SSEConfig{URL: "http://example.com/mcp"},
		TokenStore:   store,
		OnAuthURL:    func(string) {},
		// The flow checks the exchange configuration before it touches a port;
		// this test needs the flow to get as far as ListenAndServe.
		TokenEndpoint: "https://auth.example.com/oauth/token",
		ClientID:      "client-123",
	}

	type outcome struct {
		result *OAuthFlowResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := PerformMCPOAuthFlow(ctx, config)
		done <- outcome{res, err}
	}()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("PerformMCPOAuthFlow never returned after a callback server bind failure")
	}

	if got.err == nil {
		t.Fatalf("PerformMCPOAuthFlow = (%+v, nil), want the bind failure", got.result)
	}
	if !strings.Contains(got.err.Error(), "OAuth callback server failed") {
		t.Errorf("PerformMCPOAuthFlow error = %v, want it to name the callback server failure", got.err)
	}
	if !strings.Contains(got.err.Error(), fmt.Sprintf("127.0.0.1:%d", port)) {
		t.Errorf("PerformMCPOAuthFlow error = %v, want it to name the occupied address", got.err)
	}
	if _, ok := errors.AsType[*net.OpError](got.err); !ok {
		t.Errorf("PerformMCPOAuthFlow error chain lost *net.OpError: %v", got.err)
	}
	if got.result != nil {
		t.Errorf("PerformMCPOAuthFlow result = %+v, want nil on error", got.result)
	}
	if err := ln.Close(); err != nil {
		t.Errorf("closing the occupying listener: %v", err)
	}
}

// ---------------------------------------------------------------------------
// auth.go:609,610,621,622,631,632,633,642,616 — ExchangeAuthCode error shapes.
// ---------------------------------------------------------------------------

func TestExchangeAuthCodeRequestCreationFailure(t *testing.T) {
	_, err := ExchangeAuthCode(context.Background(), "://bad-endpoint", "code", "verifier", "http://localhost/cb", "client")
	if err == nil {
		t.Fatal("ExchangeAuthCode() = nil, want an error for a malformed token endpoint")
	}
	if !strings.HasPrefix(err.Error(), "mcp: create token request: ") {
		t.Errorf("ExchangeAuthCode error = %q, want prefix %q", err.Error(), "mcp: create token request: ")
	}
	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Errorf("ExchangeAuthCode error chain lost *url.Error: %v", err)
	}
}

func TestExchangeAuthCodeTruncatedResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "60")
		_, _ = io.WriteString(w, `{"access_token":"trunc`)
	}))
	defer server.Close()

	_, err := ExchangeAuthCode(context.Background(), server.URL, "code", "verifier", "http://localhost/cb", "client")
	if err == nil {
		t.Fatal("ExchangeAuthCode() = nil, want an error for a truncated response body")
	}
	if !strings.HasPrefix(err.Error(), "mcp: read token response: ") {
		t.Errorf("ExchangeAuthCode error = %q, want prefix %q", err.Error(), "mcp: read token response: ")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("ExchangeAuthCode error chain lost io.ErrUnexpectedEOF: %v", err)
	}
}

// TestExchangeAuthCodeNon200MessageShapes pins conditional/negated ×2,
// expression/logical and expression/remove on the
// `unmarshal succeeded && error field non-empty` guard, plus the invalid_grant
// special case: exact messages separate the OAuth-error shape from the generic
// HTTP fallback.
func TestExchangeAuthCodeNon200MessageShapes(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{
			name:    "standard error body uses the OAuth error shape",
			status:  400,
			body:    `{"error":"invalid_request","error_description":"Missing code parameter"}`,
			wantErr: "mcp: OAuth error invalid_request: Missing code parameter",
		},
		{
			name:    "invalid_grant keeps its own prefix",
			status:  400,
			body:    `{"error":"invalid_grant","error_description":"Authorization code expired"}`,
			wantErr: "mcp: invalid_grant: Authorization code expired",
		},
		{
			name:    "valid JSON without an error field falls back to the HTTP shape",
			status:  400,
			body:    `{"foo":"bar"}`,
			wantErr: `mcp: token exchange failed: HTTP 400: {"foo":"bar"}`,
		},
		{
			name:    "empty error string falls back to the HTTP shape",
			status:  400,
			body:    `{"error":""}`,
			wantErr: `mcp: token exchange failed: HTTP 400: {"error":""}`,
		},
		{
			name:    "unparsable body keeps status and raw body",
			status:  500,
			body:    "internal server error",
			wantErr: "mcp: token exchange failed: HTTP 500: internal server error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()

			_, err := ExchangeAuthCode(context.Background(), server.URL, "code", "verifier", "http://localhost/cb", "client")
			if err == nil {
				t.Fatalf("ExchangeAuthCode() = nil, want an error for status %d body %q", tt.status, tt.body)
			}
			if err.Error() != tt.wantErr {
				t.Errorf("ExchangeAuthCode error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestExchangeAuthCodeParseFailureChain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `not json`)
	}))
	defer server.Close()

	_, err := ExchangeAuthCode(context.Background(), server.URL, "code", "verifier", "http://localhost/cb", "client")
	if err == nil {
		t.Fatal("ExchangeAuthCode() = nil, want an error for a non-JSON token body")
	}
	if !strings.HasPrefix(err.Error(), "mcp: parse token response: ") {
		t.Errorf("ExchangeAuthCode error = %q, want prefix %q", err.Error(), "mcp: parse token response: ")
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("ExchangeAuthCode error chain lost *json.SyntaxError: %v", err)
	}
}

func TestExchangeAuthCodeRequestCancellationChain(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"tok","token_type":"Bearer"}`)
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := ExchangeAuthCode(ctx, server.URL, "code", "verifier", "http://localhost/cb", "client")
	if err == nil {
		t.Fatal("ExchangeAuthCode() = nil, want an error for a cancelled request")
	}
	if !strings.HasPrefix(err.Error(), "mcp: token exchange request: ") {
		t.Errorf("ExchangeAuthCode error = %q, want prefix %q", err.Error(), "mcp: token exchange request: ")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ExchangeAuthCode error chain lost context.DeadlineExceeded: %v", err)
	}
}

// TestNormalizeOAuthErrorBodyStatusBoundary pins which statuses are returned
// verbatim: 400 is an error status, 399 is not, and re-marshalling (which
// reorders JSON keys) only happens on the non-error path.
func TestNormalizeOAuthErrorBodyStatusBoundary(t *testing.T) {
	body := []byte(`{"error_description":"d","error":"bad_request"}`)
	tests := []struct {
		name       string
		statusCode int
		wantStatus int
		wantBody   string
	}{
		{"400 stays verbatim in the original key order", 400, 400, `{"error_description":"d","error":"bad_request"}`},
		{"500 stays verbatim in the original key order", 500, 500, `{"error_description":"d","error":"bad_request"}`},
		{"399 is not an error status so it is re-parsed", 399, 400, `{"error":"bad_request","error_description":"d"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, gotBody := NormalizeOAuthErrorBody(tt.statusCode, body)
			if code != tt.wantStatus {
				t.Errorf("NormalizeOAuthErrorBody(%d) status = %d, want %d", tt.statusCode, code, tt.wantStatus)
			}
			if string(gotBody) != tt.wantBody {
				t.Errorf("NormalizeOAuthErrorBody(%d) body = %s, want %s", tt.statusCode, gotBody, tt.wantBody)
			}
		})
	}
}

func TestNormalizeOAuthErrorBodyPrefersAccessAndErrorFields(t *testing.T) {
	accessBody := `{"access_token":"a1","error":"weird"}`
	code, gotBody := NormalizeOAuthErrorBody(200, []byte(accessBody))
	if code != http.StatusOK || string(gotBody) != accessBody {
		t.Errorf("NormalizeOAuthErrorBody(200, %+q) = (%d, %s), want (%d, %s)", accessBody, code, gotBody, http.StatusOK, accessBody)
	}

	emptyErrBody := `{"error":""}`
	code, gotBody = NormalizeOAuthErrorBody(200, []byte(emptyErrBody))
	if code != http.StatusOK || string(gotBody) != emptyErrBody {
		t.Errorf("NormalizeOAuthErrorBody(200, %+q) = (%d, %s), want (%d, %s)", emptyErrBody, code, gotBody, http.StatusOK, emptyErrBody)
	}
}

func TestRefreshAuthorizationStillValidReturnsEveryField(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		stored := &OAuthTokenData{
			ServerName:   "srv",
			AccessToken:  "stored-access",
			RefreshToken: "stored-refresh",
			ExpiresAt:    time.Now().Truncate(time.Second).Add(time.Hour).UnixMilli(),
			Scope:        "read write",
		}
		if err := store.WriteTokens("srv|k", stored); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		tokens, err := RefreshAuthorization(context.Background(), "srv|k", store, nil)
		if err != nil {
			t.Fatalf("RefreshAuthorization: %v", err)
		}
		if tokens == nil {
			t.Fatal("RefreshAuthorization() = nil, want the stored tokens")
		}
		want := &OAuthTokens{
			AccessToken:  "stored-access",
			RefreshToken: "stored-refresh",
			ExpiresIn:    3600,
			Scope:        "read write",
			TokenType:    "Bearer",
		}
		if *tokens != *want {
			t.Errorf("RefreshAuthorization tokens = %+v, want %+v", tokens, want)
		}
	})
}

func TestRefreshAuthorizationAtExpiryBufferStillRefreshes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		stored := &OAuthTokenData{
			ServerName:   "srv",
			AccessToken:  "stored-access",
			RefreshToken: "stored-refresh",
			ExpiresAt:    time.Now().Truncate(time.Second).Add(OAuthTokenExpiryBuffer).UnixMilli(),
		}
		if err := store.WriteTokens("srv|k", stored); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		var refreshedToken string
		tokens, err := RefreshAuthorization(context.Background(), "srv|k", store,
			func(_ context.Context, refreshToken string) (*OAuthTokens, error) {
				refreshedToken = refreshToken
				return &OAuthTokens{AccessToken: "fresh", RefreshToken: "fresh-refresh", ExpiresIn: 3600}, nil
			})
		if err != nil {
			t.Fatalf("RefreshAuthorization: %v", err)
		}
		if refreshedToken != "stored-refresh" {
			t.Errorf("refreshFunc saw %q, want the stored refresh token: a token expiring exactly at the buffer must still be refreshed", refreshedToken)
		}
		if tokens == nil || tokens.AccessToken != "fresh" {
			t.Errorf("RefreshAuthorization tokens = %+v, want the refreshed tokens", tokens)
		}
	})
}

func TestRefreshAuthorizationRefreshFuncReceivesHandlerContext(t *testing.T) {
	type markerKey struct{}
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	stored := &OAuthTokenData{
		ServerName:   "srv",
		AccessToken:  "stored-access",
		RefreshToken: "stored-refresh",
		ExpiresAt:    epochExpiredMs,
	}
	if err := store.WriteTokens("srv|k", stored); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	ctx := context.WithValue(context.Background(), markerKey{}, "handler-ctx")
	_, err = RefreshAuthorization(ctx, "srv|k", store, func(c context.Context, _ string) (*OAuthTokens, error) {
		if c == nil {
			t.Error("refreshFunc received a nil context")
		} else if c.Value(markerKey{}) != "handler-ctx" {
			t.Errorf("refreshFunc context lost the caller value: %v", c.Value(markerKey{}))
		}
		return &OAuthTokens{AccessToken: "fresh", ExpiresIn: 3600}, nil
	})
	if err != nil {
		t.Fatalf("RefreshAuthorization: %v", err)
	}
}

func TestRefreshAuthorizationNilRefreshFuncReturnsNilNotStored(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	stored := &OAuthTokenData{
		ServerName:   "srv",
		AccessToken:  "stored-access",
		RefreshToken: "stored-refresh",
		ExpiresAt:    epochExpiredMs,
	}
	if err := store.WriteTokens("srv|k", stored); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	tokens, err := RefreshAuthorization(context.Background(), "srv|k", store, nil)
	if err != nil {
		t.Fatalf("RefreshAuthorization: %v", err)
	}
	if tokens != nil {
		t.Errorf("RefreshAuthorization(nil refreshFunc) tokens = %+v, want nil", tokens)
	}
}

func TestRefreshAuthorizationTransientFailureStopsAfterThreeAttempts(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	stored := &OAuthTokenData{
		ServerName:   "srv",
		AccessToken:  "stored-access",
		RefreshToken: "stored-refresh",
		ExpiresAt:    epochExpiredMs,
	}
	if err := store.WriteTokens("srv|k", stored); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	origBase, origInc := lockRetryBase, lockRetryInc
	lockRetryBase, lockRetryInc = 0, 0
	defer func() { lockRetryBase, lockRetryInc = origBase, origInc }()

	attempts := 0
	tokens, err := RefreshAuthorization(context.Background(), "srv|k", store,
		func(_ context.Context, _ string) (*OAuthTokens, error) {
			attempts++
			return nil, errors.New("temporary 503 failure")
		})
	if attempts != 3 {
		t.Errorf("refreshFunc called %d times, want exactly maxAttempts=3", attempts)
	}
	if tokens != nil {
		t.Errorf("RefreshAuthorization tokens = %+v, want nil", tokens)
	}
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("RefreshAuthorization error = %v, want the refresh failure", err)
	}
}

func TestRefreshAuthorizationInvalidGrantClearsExpiryToo(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	stored := &OAuthTokenData{
		ServerName:   "srv",
		AccessToken:  "stored-access",
		RefreshToken: "stored-refresh",
		ExpiresAt:    epochExpiredMs,
	}
	if err := store.WriteTokens("srv|k", stored); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	tokens, err := RefreshAuthorization(context.Background(), "srv|k", store,
		func(_ context.Context, _ string) (*OAuthTokens, error) {
			return nil, errors.New("oauth: invalid_grant: token revoked")
		})
	if err != nil {
		t.Fatalf("RefreshAuthorization: %v", err)
	}
	if tokens != nil {
		t.Errorf("RefreshAuthorization tokens = %+v, want nil after invalid_grant", tokens)
	}
	saved, err := store.ReadTokens("srv|k")
	if err != nil {
		t.Fatalf("ReadTokens: %v", err)
	}
	if saved == nil {
		t.Fatal("stored token record vanished, want the cleared record")
	}
	want := &OAuthTokenData{ServerName: "srv", ExpiresAt: 0}
	if *saved != *want {
		t.Errorf("stored record after invalid_grant = %+v, want %+v", saved, want)
	}
}

func TestRefreshAuthorizationSavedExpiryIsRefreshTimePlusLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		stored := &OAuthTokenData{
			ServerName:   "srv",
			AccessToken:  "stored-access",
			RefreshToken: "stored-refresh",
			ExpiresAt:    time.Now().Truncate(time.Second).Add(-time.Minute).UnixMilli(),
		}
		if err := store.WriteTokens("srv|k", stored); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		var refreshedAt time.Time
		_, err = RefreshAuthorization(context.Background(), "srv|k", store,
			func(_ context.Context, _ string) (*OAuthTokens, error) {
				refreshedAt = time.Now()
				return &OAuthTokens{AccessToken: "fresh", RefreshToken: "fresh-refresh", ExpiresIn: 3600}, nil
			})
		if err != nil {
			t.Fatalf("RefreshAuthorization: %v", err)
		}
		saved, err := store.ReadTokens("srv|k")
		if err != nil {
			t.Fatalf("ReadTokens: %v", err)
		}
		wantExpiry := refreshedAt.Add(3600 * time.Second).UnixMilli()
		if saved.ExpiresAt != wantExpiry {
			t.Errorf("saved ExpiresAt = %d, want %d (refresh time + ExpiresIn)", saved.ExpiresAt, wantExpiry)
		}
		if wantExpiry <= stored.ExpiresAt {
			t.Errorf("wantExpiry = %d <= stored %d: the refresh would have looked already expired", wantExpiry, stored.ExpiresAt)
		}
	})
}

func TestStepUpDetectIgnoresScopeWithoutInsufficientScope(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}
	resp.Header.Set("WWW-Authenticate", `Bearer error="invalid_token", scope="read admin"`)
	if got := StepUpDetect(resp); got != "" {
		t.Errorf("StepUpDetect(scope only) = %q, want empty: only insufficient_scope names the missing scopes", got)
	}
}

// TestRefreshLockBackoffTotalIsExact pins the mkdir-retry loop. Acquire is
// best-effort (it returns nil even when it gives up), so the only observable is
// how much simulated time it spent: a free lock must return immediately and a
// contested one must spend exactly 0+200+400+600+800 ms.
func TestRefreshLockBackoffTotalIsExact(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		origBase, origInc := lockRetryBase, lockRetryInc
		lockRetryBase, lockRetryInc = 0, 200
		defer func() { lockRetryBase, lockRetryInc = origBase, origInc }()

		dir := t.TempDir()
		lock := NewRefreshLock(dir, "busy-key")

		start := time.Now()
		if err := lock.Acquire(); err != nil {
			t.Fatalf("Acquire on a free lock: %v", err)
		}
		freeElapsed := time.Since(start)
		if freeElapsed != 0 {
			t.Errorf("Acquire on a free lock took %v of simulated time, want 0: it must return once Mkdir succeeds", freeElapsed)
		}
		if _, err := os.Stat(lock.lockDir); err != nil {
			t.Fatalf("lock dir after a successful Acquire: %v", err)
		}
		if err := lock.Release(); err != nil {
			t.Fatalf("Release: %v", err)
		}

		if err := os.Mkdir(lock.lockDir, 0700); err != nil {
			t.Fatalf("pre-owning the lock dir: %v", err)
		}
		start = time.Now()
		if err := lock.Acquire(); err != nil {
			t.Fatalf("Acquire on a held lock: %v", err)
		}
		elapsed := time.Since(start)
		if want := time.Duration((MaxLockRetries*lockRetryBase + 200*(0+1+2+3+4)) * int(time.Millisecond)); elapsed != want {
			t.Errorf("Acquire on a held lock retried for %v, want exactly %v (sleeps of 0,200,400,600,800ms)", elapsed, want)
		}
	})
}

func TestRefreshLockNonExistErrorReturnsBeforeAnyBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		blocker := filepath.Join(dir, "not-a-directory")
		if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
			t.Fatalf("creating the blocker file: %v", err)
		}
		lock := NewRefreshLock(dir, "blocked-key")
		lock.lockDir = filepath.Join(blocker, "mcp-refresh-blocked-key.lock")

		start := time.Now()
		if err := lock.Acquire(); err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		elapsed := time.Since(start)
		if elapsed >= time.Second {
			t.Errorf("Acquire took %v of simulated time after a mkdir failure that is not EEXIST, want an immediate give-up", elapsed)
		}
	})
}

func TestRefreshLockDirectoryPermissions(t *testing.T) {
	dir := t.TempDir()
	lock := NewRefreshLock(dir, "perm-key")
	if err := lock.Acquire(); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	info, err := os.Stat(lock.lockDir)
	if err != nil {
		t.Fatalf("Stat(lock dir): %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("lock dir mode = 0%o, want 0700", perm)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestRevokeTokenSendsFormEncodedRevocationFields(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("revocation request Content-Type = %q, want application/x-www-form-urlencoded", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		want := url.Values{
			"token":           {"token-to-revoke"},
			"token_type_hint": {"refresh_token"},
			"client_id":       {"client-1"},
			"client_secret":   {"client-secret-1"},
		}
		if len(r.PostForm) != len(want) {
			t.Errorf("revocation form has %d fields (%v), want exactly %d", len(r.PostForm), r.PostForm, len(want))
		}
		for field, wantValues := range want {
			gotValues := r.PostForm[field]
			if len(gotValues) != 1 || gotValues[0] != wantValues[0] {
				t.Errorf("revocation form field %q = %v, want %q", field, gotValues, wantValues[0])
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := revokeToken(context.Background(), server.URL, "token-to-revoke", "refresh_token",
		"client-1", "client-secret-1", "access-token-1"); err != nil {
		t.Fatalf("revokeToken: %v", err)
	}
	if requests != 1 {
		t.Errorf("revocation endpoint saw %d requests, want exactly 1", requests)
	}
}

func TestRevokeTokenRequestFailureKeepsTransportCause(t *testing.T) {
	tests := []struct {
		name       string
		revokeURL  string
		wantPrefix string
	}{
		{"unparsable revoke endpoint", "://bad-revoke-endpoint", "mcp: failed to create revocation request: "},
		{"unreachable revoke endpoint", "http://127.0.0.1:1/revoke", "mcp: revocation request failed: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := revokeToken(context.Background(), tt.revokeURL, "token", "refresh_token", "", "", "access")
			if err == nil {
				t.Fatalf("revokeToken(%s) = nil, want an error", tt.revokeURL)
			}
			if !strings.HasPrefix(err.Error(), tt.wantPrefix) {
				t.Errorf("revokeToken error = %q, want prefix %q", err.Error(), tt.wantPrefix)
			}
			if _, ok := errors.AsType[*url.Error](err); !ok {
				if !errors.Is(err, errors.New(strings.TrimSuffix(err.Error(), tt.wantPrefix))) {
					t.Errorf("revokeToken error chain lost the transport cause: %v", err)
				}
			}
		})
	}
}

func TestRevokeTokenRetryOnlyWhenStatusAndTokenAllowIt(t *testing.T) {
	tests := []struct {
		name         string
		statuses     []int
		accessToken  string
		wantRequests int
		wantErrText  string
	}{
		// TS auth.ts:430-458: axios rejects any non-2xx; only 401 with an
		// access token retries, anything else is an error the caller sees.
		{"403 never retries even with an access token", []int{http.StatusForbidden}, "access-token", 1, "mcp: revocation failed with status 403"},
		{"401 never retries without an access token", []int{http.StatusUnauthorized}, "", 1, "mcp: revocation failed with status 401"},
		{"401 retries once with an access token", []int{http.StatusUnauthorized, http.StatusOK}, "access-token", 2, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := 0
			contentTypes := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contentTypes = append(contentTypes, r.Header.Get("Content-Type"))
				w.WriteHeader(tt.statuses[min(seen, len(tt.statuses)-1)])
				seen++
			}))
			defer server.Close()

			err := revokeToken(context.Background(), server.URL, "refresh-token", "refresh_token", "", "", tt.accessToken)
			if seen != tt.wantRequests {
				t.Errorf("revocation endpoint saw %d requests, want %d", seen, tt.wantRequests)
			}
			for i, ct := range contentTypes {
				if ct != "application/x-www-form-urlencoded" {
					t.Errorf("revocation request %d Content-Type = %q, want application/x-www-form-urlencoded", i+1, ct)
				}
			}
			if tt.wantErrText == "" {
				if err != nil {
					t.Errorf("revokeToken error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErrText) {
				t.Errorf("revokeToken error = %v, want it to contain %q", err, tt.wantErrText)
			}
		})
	}
}

func TestRevokeTokenRetryStatusBoundary(t *testing.T) {
	tests := []struct {
		name        string
		retryStatus int
		wantErrText string
	}{
		// The retry answer goes through the same axios non-2xx rule, so the
		// boundary is 2xx, not "under 400" (auth.ts:448-450).
		{"retry answering 400 is an error", http.StatusBadRequest, "mcp: revocation failed with status 400"},
		{"retry answering 300 is an error", 300, "mcp: revocation failed with status 300"},
		{"retry answering 299 is not an error", 299, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if seen == 0 {
					w.WriteHeader(http.StatusUnauthorized)
				} else {
					w.WriteHeader(tt.retryStatus)
				}
				seen++
			}))
			defer server.Close()

			err := revokeToken(context.Background(), server.URL, "refresh-token", "refresh_token", "", "", "access-token")
			if tt.wantErrText == "" {
				if err != nil {
					t.Errorf("revokeToken error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErrText) {
				t.Errorf("revokeToken error = %v, want it to contain %q", err, tt.wantErrText)
			}
		})
	}
}

func TestRevokeTokenRetryTransportFailureKeepsCause(t *testing.T) {
	seen := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if seen == 0 {
			w.WriteHeader(http.StatusUnauthorized)
			seen++
			return
		}
		panic(http.ErrAbortHandler)
	}))
	defer server.Close()

	err := revokeToken(context.Background(), server.URL, "refresh-token", "refresh_token", "", "", "access-token")
	if err == nil {
		t.Fatal("revokeToken = nil, want the failed retry error")
	}
	if !strings.HasPrefix(err.Error(), "mcp: revocation retry failed: ") {
		t.Errorf("revokeToken error = %q, want the retry-failed prefix", err.Error())
	}
	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Errorf("revokeToken error chain lost the transport error: %v", err)
	}
}

func TestRevokeServerTokensKeepsRecordWhenBothTokensEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	stored := &OAuthTokenData{ServerName: "srv|k", ExpiresAt: 123456}
	if err := store.WriteTokens("srv|k", stored); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	if err := RevokeServerTokens(context.Background(), store, "srv|k", "http://localhost:1/mcp", "http://localhost:1/revoke"); err != nil {
		t.Fatalf("RevokeServerTokens: %v", err)
	}
	saved, err := store.ReadTokens("srv|k")
	if err != nil {
		t.Fatalf("ReadTokens: %v", err)
	}
	if saved == nil {
		t.Fatal("token record was deleted although no token was revoked")
	}
	if saved.ExpiresAt != 123456 || saved.ServerName != "srv|k" {
		t.Errorf("stored record = %+v, want it unchanged", saved)
	}
}

func TestRevokeServerTokensReportsLocalDeleteFailure(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("srv|k", &OAuthTokenData{ServerName: "srv|k", AccessToken: "access-token"}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatalf("Chmod(store dir): %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

	err = RevokeServerTokens(context.Background(), store, "srv|k", "http://localhost:1/mcp", "")
	if err == nil {
		t.Fatal("RevokeServerTokens = nil, want the local delete failure")
	}
	if !strings.HasPrefix(err.Error(), "mcp: failed to delete token data: ") {
		t.Errorf("RevokeServerTokens error = %q, want the delete-failure prefix", err.Error())
	}
	if _, ok := errors.AsType[*os.PathError](err); !ok {
		t.Errorf("RevokeServerTokens error chain lost the delete cause: %v", err)
	}
}

func TestHasDiscoveryButNoTokenRequiresBothTokensEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	tests := []struct {
		name   string
		key    string
		stored *OAuthTokenData
		want   bool
	}{
		{"only a refresh token held", "only-refresh", &OAuthTokenData{AccessToken: "", RefreshToken: "refresh"}, false},
		{"only an access token held", "only-access", &OAuthTokenData{AccessToken: "access", RefreshToken: ""}, false},
		{"both empty", "both-empty", &OAuthTokenData{AccessToken: "", RefreshToken: ""}, true},
		{"both held", "both-held", &OAuthTokenData{AccessToken: "access", RefreshToken: "refresh"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.WriteTokens(tt.key, tt.stored); err != nil {
				t.Fatalf("WriteTokens(%s): %v", tt.key, err)
			}
			if got := HasDiscoveryButNoToken(store, tt.key); got != tt.want {
				t.Errorf("HasDiscoveryButNoToken(%s) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
	if got := HasDiscoveryButNoToken(store, "never-written"); got != false {
		t.Errorf("HasDiscoveryButNoToken(unknown key) = %v, want false", got)
	}
}

func TestBuildAuthURLCarriesEveryOAuthParameter(t *testing.T) {
	got := BuildAuthURL("https://auth.example.com/authorize", "client-1",
		"http://localhost:3118/callback", "state-1", "challenge-1", "read write")
	want := "https://auth.example.com/authorize?client_id=client-1&code_challenge=challenge-1" +
		"&code_challenge_method=S256&redirect_uri=http%3A%2F%2Flocalhost%3A3118%2Fcallback" +
		"&response_type=code&scope=read+write&state=state-1"
	if got != want {
		t.Errorf("BuildAuthURL() = %q, want %q", got, want)
	}

	got = BuildAuthURL("https://auth.example.com/authorize", "client-1",
		"http://localhost:3118/callback", "state-1", "challenge-1", "")
	if strings.Contains(got, "scope=") {
		t.Errorf("BuildAuthURL with an empty scope = %q, want no scope parameter", got)
	}
}

func newMutationHandler(t *testing.T, dir, name string, opts ...MCPAuthOption) (*MCPAuthHandler, *FileTokenStore) {
	t.Helper()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	h := NewMCPAuthHandler(name, &SSEConfig{URL: "http://mcp.example.com/mcp"}, store, dir, opts...)
	return h, store
}

func TestMCPAuthHandlerClientMetadataExactFields(t *testing.T) {
	dir := t.TempDir()
	h, _ := newMutationHandler(t, dir, "meta-server")
	md := h.ClientMetadata()
	if len(md) != 5 {
		t.Fatalf("ClientMetadata() has %d entries (%v), want exactly 5", len(md), md)
	}
	if got := md["client_name"]; got != "Claude Code (meta-server)" {
		t.Errorf("client_name = %v, want %q", got, "Claude Code (meta-server)")
	}
	uris, ok := md["redirect_uris"].([]string)
	if !ok || len(uris) != 1 || uris[0] != "http://localhost:3118/callback" {
		t.Errorf("redirect_uris = %v, want [http://localhost:3118/callback]", md["redirect_uris"])
	}
	grants, ok := md["grant_types"].([]string)
	if !ok || len(grants) != 2 || grants[0] != "authorization_code" || grants[1] != "refresh_token" {
		t.Errorf("grant_types = %v, want [authorization_code refresh_token]", md["grant_types"])
	}
	responseTypes, ok := md["response_types"].([]string)
	if !ok || len(responseTypes) != 1 || responseTypes[0] != "code" {
		t.Errorf("response_types = %v, want [code]", md["response_types"])
	}
	if got := md["token_endpoint_auth_method"]; got != "none" {
		t.Errorf("token_endpoint_auth_method = %v, want none", got)
	}
}

func TestMCPAuthHandlerServerKeyIsTheConfiguredIdentity(t *testing.T) {
	dir := t.TempDir()
	h, _ := newMutationHandler(t, dir, "key-server")
	key := h.ServerKey()
	if want := GetServerKey("key-server", &SSEConfig{URL: "http://mcp.example.com/mcp"}); key != want {
		t.Errorf("ServerKey() = %q, want %q", key, want)
	}
	if !strings.HasPrefix(key, "key-server|") || len(key) != len("key-server|")+16 {
		t.Errorf("ServerKey() = %q, want %q-prefixed plus a 16 hex digest", key, "key-server|")
	}
}

func TestMCPAuthHandlerTokensNilWithoutAccessToken(t *testing.T) {
	dir := t.TempDir()
	h, store := newMutationHandler(t, dir, "no-access")
	if err := store.WriteTokens(h.ServerKey(), &OAuthTokenData{
		ServerName:   "no-access",
		AccessToken:  "",
		RefreshToken: "stored-refresh",
		ExpiresAt:    farFutureMs,
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	tokens, err := h.Tokens()
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	if tokens != nil {
		t.Errorf("Tokens() = %+v, want nil while no access token is stored", tokens)
	}
}

func TestMCPAuthHandlerTokensStepUpOmitsRefreshTokenAndKeepsTheRest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		h, store := newMutationHandler(t, dir, "step-up")
		if err := store.WriteTokens(h.ServerKey(), &OAuthTokenData{
			ServerName:   "step-up",
			AccessToken:  "stored-access",
			RefreshToken: "stored-refresh",
			ExpiresAt:    time.Now().Truncate(time.Second).Add(time.Hour).UnixMilli(),
			Scope:        "read write",
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		h.MarkStepUpPending("admin")
		tokens, err := h.Tokens()
		if err != nil {
			t.Fatalf("Tokens: %v", err)
		}
		want := &OAuthTokens{
			AccessToken:  "stored-access",
			RefreshToken: "",
			ExpiresIn:    3600,
			Scope:        "read write",
			TokenType:    "Bearer",
		}
		if tokens == nil || *tokens != *want {
			t.Errorf("Tokens() = %+v, want %+v", tokens, want)
		}
	})
}

func TestMCPAuthHandlerTokensExpiredWithoutRefreshToken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		now := time.Now().Truncate(time.Second)
		cases := []struct {
			name      string
			expiresAt time.Time
			wantNil   bool
		}{
			{"long expired", now.Add(-time.Hour), true},
			{"exactly at the moment of expiry", now, true},
			{"one second short of a second left", now.Add(500 * time.Millisecond), false},
		}
		for _, c := range cases {
			h := NewMCPAuthHandler("expiry-"+c.name, &SSEConfig{URL: "http://mcp.example.com/mcp"}, store, dir)
			if err := store.WriteTokens(h.ServerKey(), &OAuthTokenData{
				ServerName:   "expiry-" + c.name,
				AccessToken:  "stored-access",
				RefreshToken: "",
				ExpiresAt:    c.expiresAt.UnixMilli(),
			}); err != nil {
				t.Fatalf("WriteTokens: %v", err)
			}
			tokens, err := h.Tokens()
			if err != nil {
				t.Fatalf("Tokens: %v", err)
			}
			if c.wantNil {
				if tokens != nil {
					t.Errorf("Tokens(%s) = %+v, want nil", c.name, tokens)
				}
				continue
			}
			if tokens == nil {
				t.Fatalf("Tokens(%s) = nil, want the stored tokens", c.name)
			}
			if tokens.AccessToken != "stored-access" || tokens.ExpiresIn != 0.5 {
				t.Errorf("Tokens(%s) = %+v, want the stored token with ExpiresIn 0.5", c.name, tokens)
			}
		}
	})
}

func TestMCPAuthHandlerTokensProactiveRefreshGuards(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		cases := []struct {
			name       string
			expiresIn  time.Duration
			scope      string
			pending    string
			refreshSet bool
		}{
			{"token still outside the refresh buffer", time.Hour, "read", "", true},
			{"pending step-up scope already granted", time.Minute, "read", "read", true},
			{"no refresh function configured", time.Minute, "read", "", false},
		}
		for _, c := range cases {
			refreshCalls := 0
			h, store := newMutationHandler(t, dir, "guard-"+c.name)
			if c.refreshSet {
				h = NewMCPAuthHandler("guard-"+c.name, &SSEConfig{URL: "http://mcp.example.com/mcp"}, store, dir,
					WithRefreshFunc(func(_ context.Context, _ string) (*OAuthTokens, error) {
						refreshCalls++
						return &OAuthTokens{AccessToken: "refreshed", RefreshToken: "refreshed-refresh", ExpiresIn: 3600}, nil
					}))
			}
			if err := store.WriteTokens(h.ServerKey(), &OAuthTokenData{
				ServerName:   "guard-" + c.name,
				AccessToken:  "stored-access",
				RefreshToken: "stored-refresh",
				ExpiresAt:    time.Now().Truncate(time.Second).Add(c.expiresIn).UnixMilli(),
				Scope:        c.scope,
			}); err != nil {
				t.Fatalf("WriteTokens: %v", err)
			}
			if c.pending != "" {
				h.MarkStepUpPending(c.pending)
			}
			tokens, err := h.Tokens()
			if err != nil {
				t.Fatalf("Tokens: %v", err)
			}
			if refreshCalls != 0 {
				t.Errorf("%s: proactive refresh ran %d times, want 0 (the guard must hold)", c.name, refreshCalls)
			}
			if tokens == nil || tokens.AccessToken != "stored-access" {
				t.Errorf("%s: Tokens() = %+v, want the stored tokens", c.name, tokens)
			}
		}
	})
}

func TestMCPAuthHandlerTokensProactiveRefreshAtBufferBoundaryUsesHandlerContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type markerKey struct{}
		dir := t.TempDir()
		ctx := context.WithValue(context.Background(), markerKey{}, "handler-ctx")
		refreshCalls := 0
		h, store := newMutationHandler(t, dir, "boundary", WithContext(ctx),
			WithRefreshFunc(func(c context.Context, _ string) (*OAuthTokens, error) {
				refreshCalls++
				if c == nil {
					t.Error("proactive refresh received a nil context")
				} else if c.Value(markerKey{}) != "handler-ctx" {
					t.Errorf("proactive refresh context lost the handler context: %v", c.Value(markerKey{}))
				}
				return &OAuthTokens{AccessToken: "refreshed", RefreshToken: "refreshed-refresh", ExpiresIn: 3600}, nil
			}))
		if err := store.WriteTokens(h.ServerKey(), &OAuthTokenData{
			ServerName:   "boundary",
			AccessToken:  "stored-access",
			RefreshToken: "stored-refresh",
			ExpiresAt:    time.Now().Truncate(time.Second).Add(OAuthTokenExpiryBuffer).UnixMilli(),
			Scope:        "read",
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		tokens, err := h.Tokens()
		if err != nil {
			t.Fatalf("Tokens: %v", err)
		}
		if refreshCalls != 1 {
			t.Errorf("proactive refresh ran %d times, want 1 at the exact buffer boundary", refreshCalls)
		}
		if tokens == nil || tokens.AccessToken != "refreshed" {
			t.Errorf("Tokens() = %+v, want the refreshed tokens", tokens)
		}
	})
}

func TestMCPAuthHandlerTokensProactiveRefreshUsesDefaultContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		refreshCalls := 0
		h, store := newMutationHandler(t, dir, "default-ctx",
			WithRefreshFunc(func(c context.Context, _ string) (*OAuthTokens, error) {
				refreshCalls++
				if c == nil {
					t.Error("proactive refresh received a nil context: the handler must carry a context of its own")
				} else if c.Err() != nil {
					t.Errorf("proactive refresh context is already done: %v", c.Err())
				}
				return &OAuthTokens{AccessToken: "refreshed", RefreshToken: "refreshed-refresh", ExpiresIn: 3600}, nil
			}))
		if err := store.WriteTokens(h.ServerKey(), &OAuthTokenData{
			ServerName:   "default-ctx",
			AccessToken:  "stored-access",
			RefreshToken: "stored-refresh",
			ExpiresAt:    time.Now().Truncate(time.Second).Add(time.Minute).UnixMilli(),
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		tokens, err := h.Tokens()
		if err != nil {
			t.Fatalf("Tokens: %v", err)
		}
		if refreshCalls != 1 {
			t.Errorf("proactive refresh ran %d times, want 1", refreshCalls)
		}
		if tokens == nil || tokens.AccessToken != "refreshed" {
			t.Errorf("Tokens() = %+v, want the refreshed tokens", tokens)
		}
	})
}

func TestMCPAuthHandlerTokensRefreshInProgressFlagGuardsConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		refreshCalls := 0
		flagDuringRefresh := -1
		var h *MCPAuthHandler
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		h = NewMCPAuthHandler("in-progress", &SSEConfig{URL: "http://mcp.example.com/mcp"}, store, dir,
			WithRefreshFunc(func(_ context.Context, _ string) (*OAuthTokens, error) {
				refreshCalls++
				h.mu.Lock()
				if h.refreshInProgress {
					flagDuringRefresh = 1
				} else {
					flagDuringRefresh = 0
				}
				h.mu.Unlock()
				return &OAuthTokens{AccessToken: "refreshed", RefreshToken: "refreshed-refresh", ExpiresIn: 60}, nil
			}))
		if err := store.WriteTokens(h.ServerKey(), &OAuthTokenData{
			ServerName:   "in-progress",
			AccessToken:  "stored-access",
			RefreshToken: "stored-refresh",
			ExpiresAt:    time.Now().Truncate(time.Second).Add(time.Minute).UnixMilli(),
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		for i := range 2 {
			tokens, err := h.Tokens()
			if err != nil {
				t.Fatalf("Tokens: %v", err)
			}
			if tokens == nil || tokens.AccessToken != "refreshed" {
				t.Fatalf("Tokens() call %d = %+v, want the refreshed tokens", i+1, tokens)
			}
		}
		if flagDuringRefresh != 1 {
			t.Errorf("refreshInProgress during the refresh = %d, want 1 (set before the refresh runs)", flagDuringRefresh)
		}
		if refreshCalls != 2 {
			t.Errorf("proactive refresh ran %d times, want 2: the flag must be cleared after each refresh", refreshCalls)
		}
		if !h.mu.TryLock() {
			t.Error("handler mutex still held after Tokens(): an unlock path is missing")
		}
		h.mu.Unlock()
	})
}

func TestMCPAuthHandlerTokensReleasesMutexWhenRefreshAlreadyRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		refreshCalls := 0
		h, store := newMutationHandler(t, dir, "already-running",
			WithRefreshFunc(func(_ context.Context, _ string) (*OAuthTokens, error) {
				refreshCalls++
				return &OAuthTokens{AccessToken: "refreshed", ExpiresIn: 3600}, nil
			}))
		if err := store.WriteTokens(h.ServerKey(), &OAuthTokenData{
			ServerName:   "already-running",
			AccessToken:  "stored-access",
			RefreshToken: "stored-refresh",
			ExpiresAt:    time.Now().Truncate(time.Second).Add(time.Minute).UnixMilli(),
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		h.mu.Lock()
		h.refreshInProgress = true
		h.mu.Unlock()

		tokens, err := h.Tokens()
		if err != nil {
			t.Fatalf("Tokens: %v", err)
		}
		if refreshCalls != 0 {
			t.Errorf("proactive refresh ran %d times, want 0 while another refresh is in progress", refreshCalls)
		}
		if tokens == nil || tokens.AccessToken != "stored-access" {
			t.Errorf("Tokens() = %+v, want the stored tokens", tokens)
		}
		if !h.mu.TryLock() {
			t.Error("handler mutex still held after Tokens() skipped the refresh: the else-branch unlock is missing")
		}
		h.mu.Unlock()
	})
}

func TestMCPAuthHandlerTokensReturnsEveryStoredField(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		h, store := newMutationHandler(t, dir, "fields")
		if err := store.WriteTokens(h.ServerKey(), &OAuthTokenData{
			ServerName:   "fields",
			AccessToken:  "stored-access",
			RefreshToken: "stored-refresh",
			ExpiresAt:    time.Now().Truncate(time.Second).Add(time.Hour).UnixMilli(),
			Scope:        "read write",
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		tokens, err := h.Tokens()
		if err != nil {
			t.Fatalf("Tokens: %v", err)
		}
		want := &OAuthTokens{
			AccessToken:  "stored-access",
			RefreshToken: "stored-refresh",
			ExpiresIn:    3600,
			Scope:        "read write",
			TokenType:    "Bearer",
		}
		if tokens == nil || *tokens != *want {
			t.Errorf("Tokens() = %+v, want %+v", tokens, want)
		}
	})
}

func TestMCPAuthHandlerSaveSeedsIdentityFieldsOnAFreshRecord(t *testing.T) {
	tests := []struct {
		name  string
		save  func(h *MCPAuthHandler) error
		check func(t *testing.T, saved *OAuthTokenData)
	}{
		{"SaveTokens", func(h *MCPAuthHandler) error {
			return h.SaveTokens(&OAuthTokens{AccessToken: "a1", RefreshToken: "r1", ExpiresIn: 3600, Scope: "read write"})
		}, func(t *testing.T, saved *OAuthTokenData) {
			if saved.Scope != "read write" {
				t.Errorf("saved Scope = %q, want read write", saved.Scope)
			}
			if saved.AccessToken != "a1" || saved.RefreshToken != "r1" {
				t.Errorf("saved tokens = %q/%q, want a1/r1", saved.AccessToken, saved.RefreshToken)
			}
		}},
		{"SaveClientInformation", func(h *MCPAuthHandler) error {
			return h.SaveClientInformation("client-1", "client-secret-1")
		}, func(t *testing.T, saved *OAuthTokenData) {
			if saved.ClientID != "client-1" || saved.ClientSecret != "client-secret-1" {
				t.Errorf("saved client info = %q/%q, want client-1/client-secret-1", saved.ClientID, saved.ClientSecret)
			}
		}},
		{"SaveDiscoveryState", func(h *MCPAuthHandler) error {
			return h.SaveDiscoveryState(&OAuthDiscoveryState{AuthorizationServerURL: "https://auth.example.com/authorize"})
		}, func(t *testing.T, saved *OAuthTokenData) {
			if saved.DiscoveryState == nil || saved.DiscoveryState.AuthorizationServerURL != "https://auth.example.com/authorize" {
				t.Errorf("saved discovery state = %+v, want the AuthorizationServerURL we stored", saved.DiscoveryState)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			h, store := newMutationHandler(t, dir, "seed-"+tt.name)
			if err := tt.save(h); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			saved, err := store.ReadTokens(h.ServerKey())
			if err != nil {
				t.Fatalf("ReadTokens: %v", err)
			}
			if saved == nil {
				t.Fatalf("%s left no token record", tt.name)
			}
			if saved.ServerName != "seed-"+tt.name {
				t.Errorf("%s: saved ServerName = %q, want the handler's server name", tt.name, saved.ServerName)
			}
			if saved.ServerURL != "http://mcp.example.com/mcp" {
				t.Errorf("%s: saved ServerURL = %q, want the configured server URL", tt.name, saved.ServerURL)
			}
			tt.check(t, saved)
		})
	}
}

func TestMCPAuthHandlerRefreshWithLockHoldsTheLockWhileRefreshing(t *testing.T) {
	dir := t.TempDir()
	h, store := newMutationHandler(t, dir, "locked")
	serverKey := h.ServerKey()
	if err := store.WriteTokens(serverKey, &OAuthTokenData{
		ServerName:   "locked",
		AccessToken:  "old-access",
		RefreshToken: "stored-refresh",
		ExpiresAt:    epochExpiredMs,
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	lockDir := NewRefreshLock(dir, serverKey).lockDir
	var statErr error
	tokens, err := h.RefreshWithLock(context.Background(), func(_ context.Context, _ string) (*OAuthTokens, error) {
		_, statErr = os.Stat(lockDir)
		return &OAuthTokens{AccessToken: "fresh", RefreshToken: "fresh-refresh", ExpiresIn: 3600}, nil
	})
	if err != nil {
		t.Fatalf("RefreshWithLock: %v", err)
	}
	if tokens == nil || tokens.AccessToken != "fresh" {
		t.Errorf("RefreshWithLock tokens = %+v, want the refreshed tokens", tokens)
	}
	if statErr != nil {
		t.Errorf("refresh lock %s was not held while the refresh ran: %v", lockDir, statErr)
	}
	if _, err := os.Stat(lockDir); err == nil {
		t.Errorf("refresh lock %s still exists after RefreshWithLock returned", lockDir)
	}
}

func TestMCPAuthHandlerRefreshWithLockReturnsEveryStillValidField(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		refreshCalls := 0
		h, store := newMutationHandler(t, dir, "still-valid", WithRefreshFunc(func(_ context.Context, _ string) (*OAuthTokens, error) {
			refreshCalls++
			return &OAuthTokens{AccessToken: "fresh", ExpiresIn: 3600}, nil
		}))
		if err := store.WriteTokens(h.ServerKey(), &OAuthTokenData{
			ServerName:   "still-valid",
			AccessToken:  "stored-access",
			RefreshToken: "stored-refresh",
			ExpiresAt:    time.Now().Truncate(time.Second).Add(time.Hour).UnixMilli(),
			Scope:        "read write",
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		tokens, err := h.RefreshWithLock(context.Background(), func(_ context.Context, _ string) (*OAuthTokens, error) {
			refreshCalls++
			return &OAuthTokens{AccessToken: "fresh", ExpiresIn: 3600}, nil
		})
		if err != nil {
			t.Fatalf("RefreshWithLock: %v", err)
		}
		want := &OAuthTokens{
			AccessToken:  "stored-access",
			RefreshToken: "stored-refresh",
			ExpiresIn:    3600,
			Scope:        "read write",
			TokenType:    "Bearer",
		}
		if tokens == nil || *tokens != *want {
			t.Errorf("RefreshWithLock tokens = %+v, want %+v", tokens, want)
		}
		if refreshCalls != 0 {
			t.Errorf("refreshFunc ran %d times, want 0: another process' fresh token must be reused", refreshCalls)
		}
	})
}

func TestMCPAuthHandlerRefreshWithLockKeepsFreshTokenWithoutRefreshToken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		refreshCalls := 0
		h, store := newMutationHandler(t, dir, "no-refresh-token")
		if err := store.WriteTokens(h.ServerKey(), &OAuthTokenData{
			ServerName:   "no-refresh-token",
			AccessToken:  "stored-access",
			RefreshToken: "",
			ExpiresAt:    time.Now().Truncate(time.Second).Add(time.Hour).UnixMilli(),
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		tokens, err := h.RefreshWithLock(context.Background(), func(_ context.Context, _ string) (*OAuthTokens, error) {
			refreshCalls++
			return &OAuthTokens{AccessToken: "fresh", ExpiresIn: 3600}, nil
		})
		if err != nil {
			t.Fatalf("RefreshWithLock: %v", err)
		}
		if tokens == nil {
			t.Fatal("RefreshWithLock = nil, want the stored tokens (nothing expired)")
		}
		if tokens.AccessToken != "stored-access" {
			t.Errorf("RefreshWithLock tokens = %+v, want the stored access token", tokens)
		}
		if refreshCalls != 0 {
			t.Errorf("refreshFunc ran %d times, want 0", refreshCalls)
		}
	})
}

func TestMCPAuthHandlerRefreshWithLockZeroExpiryIsNotStillValid(t *testing.T) {
	dir := t.TempDir()
	refreshCalls := 0
	h, store := newMutationHandler(t, dir, "zero-expiry")
	if err := store.WriteTokens(h.ServerKey(), &OAuthTokenData{
		ServerName:   "zero-expiry",
		AccessToken:  "old-access",
		RefreshToken: "stored-refresh",
		ExpiresAt:    0,
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	tokens, err := h.RefreshWithLock(context.Background(), func(_ context.Context, _ string) (*OAuthTokens, error) {
		refreshCalls++
		return &OAuthTokens{AccessToken: "fresh", RefreshToken: "fresh-refresh", ExpiresIn: 3600}, nil
	})
	if err != nil {
		t.Fatalf("RefreshWithLock: %v", err)
	}
	if refreshCalls != 1 {
		t.Errorf("refreshFunc ran %d times, want 1: a zero ExpiresAt means no expiry info at all", refreshCalls)
	}
	if tokens == nil || tokens.AccessToken != "fresh" {
		t.Errorf("RefreshWithLock tokens = %+v, want the refreshed tokens", tokens)
	}
}

func TestMCPAuthHandlerRefreshWithLockBufferBoundaryStillRefreshes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		refreshCalls := 0
		h, store := newMutationHandler(t, dir, "lock-boundary")
		if err := store.WriteTokens(h.ServerKey(), &OAuthTokenData{
			ServerName:   "lock-boundary",
			AccessToken:  "old-access",
			RefreshToken: "stored-refresh",
			ExpiresAt:    time.Now().Truncate(time.Second).Add(OAuthTokenExpiryBuffer).UnixMilli(),
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		tokens, err := h.RefreshWithLock(context.Background(), func(_ context.Context, _ string) (*OAuthTokens, error) {
			refreshCalls++
			return &OAuthTokens{AccessToken: "fresh", RefreshToken: "fresh-refresh", ExpiresIn: 3600}, nil
		})
		if err != nil {
			t.Fatalf("RefreshWithLock: %v", err)
		}
		if refreshCalls != 1 {
			t.Errorf("refreshFunc ran %d times, want 1 at the exact buffer boundary", refreshCalls)
		}
		if tokens == nil || tokens.AccessToken != "fresh" {
			t.Errorf("RefreshWithLock tokens = %+v, want the refreshed tokens", tokens)
		}
	})
}

func TestMCPAuthHandlerRefreshWithLockWithoutStoredTokens(t *testing.T) {
	dir := t.TempDir()
	h, _ := newMutationHandler(t, dir, "no-tokens")
	tokens, err := h.RefreshWithLock(context.Background(), func(_ context.Context, _ string) (*OAuthTokens, error) {
		return &OAuthTokens{AccessToken: "fresh", ExpiresIn: 3600}, nil
	})
	if err != nil {
		t.Fatalf("RefreshWithLock: %v", err)
	}
	if tokens != nil {
		t.Errorf("RefreshWithLock tokens = %+v, want nil when nothing is stored", tokens)
	}
}

func TestMCPAuthHandlerRefreshWithLockPropagatesCallerContext(t *testing.T) {
	type markerKey struct{}
	dir := t.TempDir()
	h, store := newMutationHandler(t, dir, "caller-ctx")
	if err := store.WriteTokens(h.ServerKey(), &OAuthTokenData{
		ServerName:   "caller-ctx",
		AccessToken:  "old-access",
		RefreshToken: "stored-refresh",
		ExpiresAt:    0,
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), markerKey{}, "caller-ctx"))
	defer cancel()

	refreshCalls := 0
	tokens, err := h.RefreshWithLock(ctx, func(c context.Context, _ string) (*OAuthTokens, error) {
		refreshCalls++
		if c == nil {
			t.Error("refreshFunc received a nil context")
		} else if c.Value(markerKey{}) != "caller-ctx" {
			t.Errorf("refreshFunc context lost the caller value: %v", c.Value(markerKey{}))
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("RefreshWithLock: %v", err)
	}
	if tokens != nil {
		t.Errorf("RefreshWithLock tokens = %+v, want nil", tokens)
	}
	if refreshCalls != 1 {
		t.Errorf("refreshFunc ran %d times, want 1", refreshCalls)
	}
}
