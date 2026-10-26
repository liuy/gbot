package mcp

// Tests for the auth.go production fixes (callback channel sends, code-only
// fallback, swallowed revocation errors) and for the escapes those fixes make
// observable.
//
// Three probes do the work:
//   - withMetadataTransport routes the https:// URL of one httptest TLS server
//     through that server's own client. RevokeServerTokens fetches its metadata
//     with a plain http.Client, so a self-signed test certificate is rejected,
//     metadata stays nil and every assertion below the fetch is vacuous.
//   - withCapturedLogs keeps the slog records a call emits (the captureHandler
//     harness from transport_stderr_test.go), which is the only way to see a
//     log-only failure path.
//   - the scripted transport answers without a network, so a client timeout can
//     be observed inside a synctest bubble instead of waiting in real time.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// ---------------------------------------------------------------------------
// Probes
// ---------------------------------------------------------------------------

type hostRoutedTransport struct {
	host   string
	routed http.RoundTripper
	rest   http.RoundTripper
}

func (h *hostRoutedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == h.host {
		return h.routed.RoundTrip(req)
	}
	return h.rest.RoundTrip(req)
}

// withMetadataTransport makes the package default transport trust one httptest
// TLS server, and only that server's host: every other request keeps using the
// transport that was installed before the probe, so unrelated tests in this
// package (httptest users included) are not affected.
func withMetadataTransport(t *testing.T, srv *httptest.Server) {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = &hostRoutedTransport{
		host:   srv.Listener.Addr().String(),
		routed: srv.Client().Transport,
		rest:   original,
	}
	t.Cleanup(func() { http.DefaultTransport = original })
}

// withCapturedLogs runs fn with the default logger pointed at a captureHandler
// and returns every record it saw as msg + attrs.
func withCapturedLogs(t *testing.T, fn func()) []map[string]string {
	t.Helper()
	handler := &captureHandler{}
	original := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(original) })
	fn()
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return handler.attrs
}

// logsFor narrows captured records down to one message.
func logsFor(records []map[string]string, msg string) []map[string]string {
	var out []map[string]string
	for _, r := range records {
		if r["msg"] == msg {
			out = append(out, r)
		}
	}
	return out
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Errorf("closing the port probe listener: %v", err)
	}
	return port
}

// dialCallback opens a connection to the flow's callback server, retrying until
// it accepts: ListenAndServe runs in the flow's own goroutine, so the port is
// not necessarily listening when OnAuthURL has already fired.
func dialCallback(t *testing.T, port int) net.Conn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second) // REAL-TIME
	for {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return conn
		}
		if time.Now().After(deadline) { // REAL-TIME
			t.Fatalf("dialing the callback server on port %d: %v", port, err)
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
}

// revokeRecorder is the authorization server RevokeServerTokens talks to: an
// https:// metadata document plus the revocation endpoint it advertises. The
// requests it answered are recorded, because "the endpoint was never called"
// and "the endpoint was called with this token" are the only facts that tell
// the branches of RevokeServerTokens apart.
type revokeRecorder struct {
	srv     *httptest.Server
	mu      sync.Mutex
	paths   []string
	bodies  []string
	headers []http.Header
}

// newRevokeServer answers /metadata with metadataBody ({{host}} is replaced by
// this server's host, so a test can point the revocation endpoint at itself)
// and every other path with revokeStatus.
func newRevokeServer(t *testing.T, metadataBody string, metadataStatus, revokeStatus int) *revokeRecorder {
	t.Helper()
	rec := &revokeRecorder{}
	rec.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the %s request body: %v", r.URL.Path, err)
		}
		rec.mu.Lock()
		rec.paths = append(rec.paths, r.URL.Path)
		rec.bodies = append(rec.bodies, string(body))
		rec.headers = append(rec.headers, r.Header.Clone())
		rec.mu.Unlock()
		if r.URL.Path != "/metadata" {
			w.WriteHeader(revokeStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(metadataStatus)
		if metadataStatus == http.StatusOK {
			if _, err := fmt.Fprint(w, strings.ReplaceAll(metadataBody, "{{host}}", r.Host)); err != nil {
				t.Errorf("writing the metadata body: %v", err)
			}
		}
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

// metadataURL is the https:// URL RevokeServerTokens requires.
func (r *revokeRecorder) metadataURL() string { return r.srv.URL + "/metadata" }

func (r *revokeRecorder) pathLog() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.paths, ",")
}

// bodyLog joins the request bodies, skipping the metadata GET (no body).
func (r *revokeRecorder) bodyLog() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var bodies []string
	for i, b := range r.bodies {
		if r.paths[i] == "/metadata" {
			continue
		}
		bodies = append(bodies, b)
	}
	return strings.Join(bodies, "|")
}

// headerFor returns the given header of the first request that hit path.
func (r *revokeRecorder) headerFor(path, key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, p := range r.paths {
		if p == path {
			return r.headers[i].Get(key)
		}
	}
	return ""
}

const revokeEndpointMetadata = `{"revocation_endpoint": "https://{{host}}/revoke"}`

// parkedTransport never answers: only the client's own deadline can end the
// request. Inside a synctest bubble the wait is simulated time, so a 30s
// timeout costs no wall clock while a ±1s change in it still shows up.
type parkedTransport struct {
	mu    sync.Mutex
	paths []string
}

func (p *parkedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.paths = append(p.paths, req.URL.Path)
	p.mu.Unlock()
	// The only way a parked request can end is somebody's deadline: the client
	// Timeout these tests measure, or the caller's context. Answering earlier
	// would hide the timeout completely.
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func (p *parkedTransport) pathLog() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.paths, ",")
}

// swapDefaultTransport points the package default transport at rt for the
// duration of the test, restoring whatever was installed afterwards.
func swapDefaultTransport(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = rt
	t.Cleanup(func() { http.DefaultTransport = original })
}

// ---------------------------------------------------------------------------
// auth.go:1108,1109 — a metadata body that fails to decode can still leave
// fields behind: encoding/json assigns members in document order and stops at
// the first bad one. Treating "decode returned an error" as usable metadata
// would then run revocation against a half-parsed endpoint.
// ---------------------------------------------------------------------------

func TestRevokeServerTokens_MetadataDecodeErrorIgnoresPartialFields(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "access",
		RefreshToken: "refresh",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	// revocation_endpoint decodes, scope does not: the error arrives after the
	// endpoint is already in the struct.
	as := newRevokeServer(t, `{"revocation_endpoint": "https://{{host}}/revoke", "scope": 123}`, http.StatusOK, http.StatusOK)
	withMetadataTransport(t, as.srv)

	logs := withCapturedLogs(t, func() {
		if err := RevokeServerTokens(context.Background(), store, "key", "http://example.com", as.metadataURL()); err != nil {
			t.Errorf("RevokeServerTokens = %v, want nil: an undecodable metadata document is best-effort", err)
		}
	})
	if want := "/metadata"; as.pathLog() != want {
		t.Fatalf("server saw %q, want %q", as.pathLog(), want)
	}
	if warned := logsFor(logs, "mcp: token revocation failed"); len(warned) != 0 {
		t.Errorf("captured %d revocation failures from a half-parsed endpoint, want 0: %v", len(warned), warned)
	}
	tokens, err := store.ReadTokens("key")
	if err != nil {
		t.Fatalf("ReadTokens: %v", err)
	}
	if tokens != nil {
		t.Errorf("stored tokens = %+v, want the record deleted", tokens)
	}
}

// ---------------------------------------------------------------------------
// auth.go:630,1053,1102 — the request timeouts. Every other test server answers
// at once, so a 30s budget is invisible until the transport parks. Inside a
// synctest bubble the wait is simulated time: no wall clock, but a ±1s shift or
// a dropped Timeout field changes what the caller sees. The caller's own context
// gets a far larger budget so the only deadline that can fire is the client's.
// ---------------------------------------------------------------------------

func TestExchangeAuthCodeRequestHonorsClientTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		tr := &parkedTransport{}
		swapDefaultTransport(t, tr)

		start := time.Now()
		tokens, err := ExchangeAuthCode(ctx, "https://auth.example.com/oauth/token", "code-1", "verifier-1", "http://localhost:3118/callback", "client-1")
		elapsed := time.Since(start)

		if tokens != nil {
			t.Errorf("ExchangeAuthCode tokens = %+v, want nil", tokens)
		}
		if elapsed != 30*time.Second {
			t.Errorf("ExchangeAuthCode gave up after %v, want exactly the 30s its client sets", elapsed)
		}
		// The wrapper is stable; only the deadline wording after it depends on
		// which internal path reports the cancellation.
		if err == nil || !strings.HasPrefix(err.Error(), `mcp: token exchange request: Post "https://auth.example.com/oauth/token": `) {
			t.Errorf("ExchangeAuthCode error = %v, want it to name the failed POST", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("ExchangeAuthCode error = %v, want it to be the deadline the client set", err)
		}
		if got := tr.pathLog(); got != "/oauth/token" {
			t.Errorf("transport saw %q, want %q", got, "/oauth/token")
		}
	})
}

func TestRevokeTokenRequestHonorsAuthRequestTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		tr := &parkedTransport{}
		swapDefaultTransport(t, tr)

		start := time.Now()
		err := revokeToken(ctx, "https://auth.example.com/revoke", "token-1", "access_token", "", "", "")
		elapsed := time.Since(start)

		if elapsed != 30*time.Second {
			t.Errorf("revokeToken gave up after %v, want exactly AuthRequestTimeoutMs (30000ms)", elapsed)
		}
		if err == nil || !strings.HasPrefix(err.Error(), `mcp: revocation request failed: Post "https://auth.example.com/revoke": `) {
			t.Errorf("revokeToken error = %v, want it to name the failed POST", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("revokeToken error = %v, want it to be the deadline the client set", err)
		}
		if got := tr.pathLog(); got != "/revoke" {
			t.Errorf("transport saw %q, want %q", got, "/revoke")
		}
	})
}

func TestRevokeServerTokensMetadataFetchHonorsClientTimeout(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "access",
		RefreshToken: "refresh",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		tr := &parkedTransport{}
		swapDefaultTransport(t, tr)

		start := time.Now()
		revokeErr := RevokeServerTokens(ctx, store, "key", "https://mcp.example.com", "https://auth.example.com/.well-known/oauth-authorization-server")
		elapsed := time.Since(start)

		if elapsed != 30*time.Second {
			t.Errorf("RevokeServerTokens gave up after %v, want exactly AuthRequestTimeoutMs (30000ms) for the metadata fetch", elapsed)
		}
		if revokeErr != nil {
			t.Errorf("RevokeServerTokens = %v, want nil: a timed out metadata fetch is best-effort", revokeErr)
		}
		if got := tr.pathLog(); got != "/.well-known/oauth-authorization-server" {
			t.Errorf("transport saw %q, want only the metadata request", got)
		}
		tokens, err := store.ReadTokens("key")
		if err != nil {
			t.Fatalf("ReadTokens: %v", err)
		}
		if tokens != nil {
			t.Errorf("stored tokens = %+v, want the record deleted even after a timed out fetch", tokens)
		}
	})
}

// ---------------------------------------------------------------------------
// auth.go:502,513,520 — bare sends from the callback handler into channels of
// capacity 1 were the original stall bug; the select/default arms stay as
// defense. With the close-on-first-callback alignment (auth.ts:1147) a repeat
// callback no longer even reaches a handler: this test pins that the first
// error callback is answered, the listener closes, and the flow returns its
// error without stalling in Shutdown.
// ---------------------------------------------------------------------------

func TestPerformMCPOAuthFlow_DuplicateCallbacksDoNotStallShutdown(t *testing.T) {
	port := freePort(t)
	t.Setenv("MCP_OAUTH_CALLBACK_PORT", strconv.Itoa(port))

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	// OnAuthURL is called before the flow reaches its select; holding it open
	// keeps the flow busy while the callback server lives on its own.
	entered := make(chan struct{})
	release := make(chan struct{})
	config := OAuthFlowConfig{
		ServerName:   "dup-callback-server",
		ServerConfig: &SSEConfig{URL: "http://example.com/mcp"},
		TokenStore:   store,
		// Configured for an exchange the flow never reaches: these callbacks
		// all carry an error, and the endpoint is never contacted.
		TokenEndpoint: "https://auth.example.com/oauth/token",
		ClientID:      "client-123",
		OnAuthURL: func(string) {
			close(entered)
			<-release
		},
	}

	type outcome struct {
		result *OAuthFlowResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := PerformMCPOAuthFlow(context.Background(), config)
		done <- outcome{result: res, err: err}
	}()

	<-entered

	// The first error callback is answered with the error page; every repeat
	// must meet a closed listener instead of parking a handler.
	query := "error=access_denied&error_description=duplicate+callback"
	first := dialCallback(t, port)
	defer first.Close()
	if _, err := fmt.Fprintf(first, "GET /callback?%s HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n", query); err != nil {
		t.Fatalf("writing the first callback: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(first), nil)
	if err != nil {
		t.Fatalf("reading the first callback response: %v", err)
	}
	page, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the first callback page: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing the first callback page: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), "access_denied: duplicate callback") {
		t.Fatalf("first callback answer = %d %q, want the error page", resp.StatusCode, string(page))
	}
	waitCallbackRefused(t, port)
	close(release)

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatalf("PerformMCPOAuthFlow = (%+v, nil), want the callback's OAuth error", got.result)
		}
		if got.err.Error() != "OAuth error: access_denied - duplicate callback" {
			t.Errorf("PerformMCPOAuthFlow error = %q, want the callback error verbatim", got.err)
		}
		if got.result != nil {
			t.Errorf("PerformMCPOAuthFlow result = %+v, want nil on error", got.result)
		}
	case <-time.After(3 * time.Second): // REAL-TIME
		t.Fatal("PerformMCPOAuthFlow is still inside Shutdown 3s after the callback closed the server: a handler is parked on a full channel and its connection keeps Shutdown waiting")
	}
}

// ---------------------------------------------------------------------------
// auth.go:1107,1112 — `_ = revokeToken(...)` hid every remote revocation
// failure. The contract stays best-effort (auth.go:1065-1067), so the one thing
// a failure can still do is say so; without a log line a server that refused to
// revoke a token is indistinguishable from one that has no revocation endpoint.
// ---------------------------------------------------------------------------

func TestRevokeServerTokensLogsEachFailedRevocation(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	const serverKey = "revoke-log-server"
	if err := store.WriteTokens(serverKey, &OAuthTokenData{
		AccessToken:  "access-token-123",
		RefreshToken: "refresh-token-456",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	as := newRevokeServer(t, revokeEndpointMetadata, http.StatusOK, http.StatusUnauthorized)
	withMetadataTransport(t, as.srv)

	var revokeErr error
	logs := withCapturedLogs(t, func() {
		revokeErr = RevokeServerTokens(context.Background(), store, serverKey, "http://example.com", as.metadataURL())
	})

	if revokeErr != nil {
		t.Errorf("RevokeServerTokens = %v, want nil: remote revocation is best-effort", revokeErr)
	}
	tokens, err := store.ReadTokens(serverKey)
	if err != nil {
		t.Fatalf("ReadTokens: %v", err)
	}
	if tokens != nil {
		t.Errorf("stored tokens after revocation = %+v, want the record deleted", tokens)
	}

	if want := "/metadata,/revoke,/revoke,/revoke,/revoke"; as.pathLog() != want {
		t.Fatalf("server saw %q, want %q", as.pathLog(), want)
	}
	// url.Values.Encode sorts keys. The 401 answers trigger the Bearer retry,
	// which drops the client credentials (RFC 6749 §2.3.1 via auth.ts:444-450),
	// so attempts alternate between the credentialed form and the bare one.
	wantBodies := "client_id=client-id&client_secret=client-secret&token=refresh-token-456&token_type_hint=refresh_token" +
		"|token=refresh-token-456&token_type_hint=refresh_token" +
		"|client_id=client-id&client_secret=client-secret&token=access-token-123&token_type_hint=access_token" +
		"|token=access-token-123&token_type_hint=access_token"
	if got := as.bodyLog(); got != wantBodies {
		t.Errorf("revocation bodies = %q, want %q", got, wantBodies)
	}

	warned := logsFor(logs, "mcp: token revocation failed")
	if len(warned) != 2 {
		t.Fatalf("captured %d %q records, want one per failed revocation: %v", len(warned), "mcp: token revocation failed", logs)
	}
	wantHints := []string{"refresh_token", "access_token"}
	for i, rec := range warned {
		if rec["server"] != serverKey {
			t.Errorf("record %d server = %q, want %q", i, rec["server"], serverKey)
		}
		if rec["token_type_hint"] != wantHints[i] {
			t.Errorf("record %d token_type_hint = %q, want %q", i, rec["token_type_hint"], wantHints[i])
		}
		if rec["error"] != "mcp: revocation failed with status 401" {
			t.Errorf("record %d error = %q, want the status the server answered with", i, rec["error"])
		}
	}
}

// ---------------------------------------------------------------------------
// auth.ts:1216-1258 — the code goes to a real exchange, and a flow that does
// not end AUTHORIZED throws there (reason token_exchange_failed). TS has no
// entry-time guard: a flow without a token endpoint or client id runs the
// whole browser dance and fails at the exchange step. Callers persist whatever
// the flow returns (SaveTokens), so a code stored as a bearer credential is a
// broken credential, not a partial success — the failure must still be an
// error, just at the TS position.
// ---------------------------------------------------------------------------

func TestPerformMCPOAuthFlow_MissingExchangeConfigIsAFailure(t *testing.T) {
	for _, tc := range []struct {
		name          string
		tokenEndpoint string
		clientID      string
	}{
		{"no token endpoint", "", "client-123"},
		{"no client id", "https://auth.example.com/oauth/token", ""},
		{"neither", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := NewFileTokenStore(dir)
			if err != nil {
				t.Fatalf("NewFileTokenStore: %v", err)
			}
			// A scripted rand makes the state known, so the authorized
			// callback the flow must accept (TS timing: the browser dance runs
			// to completion) can be delivered.
			stateWant := base64.RawURLEncoding.EncodeToString(sequentialBytes(0, 32))
			withRandReader(t, &sequentialRand{})
			port := freePort(t)
			t.Setenv("MCP_OAUTH_CALLBACK_PORT", strconv.Itoa(port))

			entered := make(chan string, 1)
			config := OAuthFlowConfig{
				ServerName:    "unconfigured-server",
				ServerConfig:  &SSEConfig{URL: "http://example.com/mcp"},
				TokenStore:    store,
				TokenEndpoint: tc.tokenEndpoint,
				ClientID:      tc.clientID,
				OnAuthURL:     func(url string) { entered <- url },
			}
			type outcome struct {
				result *OAuthFlowResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				res, err := PerformMCPOAuthFlow(context.Background(), config)
				done <- outcome{result: res, err: err}
			}()

			// The flow must reach the browser dance: it binds a port and hands
			// out the redirect URL before any exchange can happen.
			select {
			case <-entered:
			case <-time.After(5 * time.Second): // REAL-TIME
				t.Fatal("PerformMCPOAuthFlow never started the browser dance: the missing-exchange guard fires before the callback server, unlike auth.ts")
			}

			// The authorized callback is answered like any successful flow.
			conn := dialCallback(t, port)
			defer conn.Close()
			if _, err := fmt.Fprintf(conn, "GET /callback?code=code-1&state=%s HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n", stateWant); err != nil {
				t.Fatalf("write callback request: %v", err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatalf("read callback response: %v", err)
			}
			page, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read callback response body: %v", err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatalf("close callback response body: %v", err)
			}
			if resp.StatusCode != http.StatusOK || string(page) != "<h1>Authentication Successful</h1><p>You can close this window.</p>" {
				t.Errorf("callback answer = %d %q, want the success page: the dance completed before the failure", resp.StatusCode, string(page))
			}

			var got outcome
			select {
			case got = <-done:
			case <-time.After(10 * time.Second): // REAL-TIME
				t.Fatal("PerformMCPOAuthFlow never returned for a flow that cannot exchange its code")
			}
			want := "mcp: unconfigured-server: OAuth flow needs a token endpoint and client id to exchange the authorization code"
			if got.err == nil {
				t.Fatalf("PerformMCPOAuthFlow = (%+v, nil), want the missing-exchange failure", got.result)
			}
			if got.err.Error() != want {
				t.Errorf("PerformMCPOAuthFlow error = %q, want %q", got.err.Error(), want)
			}
			if got.result != nil {
				t.Errorf("PerformMCPOAuthFlow result = %+v, want no tokens for a code that was never exchanged", got.result)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Reaching the OAuth state
//
// The flow draws both the CSRF state and the PKCE verifier from crypto/rand, so
// until now the callback those values validate had no observable input: every
// flow test could deliver an error or a mismatched state, and nothing could
// reach the authorized branch. crypto/rand.Reader is an exported variable and
// the documented seam for tests, so a scripted reader makes both values known
// and the whole flow, token exchange included, becomes testable.
//
// A *failing* reader is not a usable seam for rand.Read: crypto/rand aborts the
// process instead of returning the error. That is also why the "rand failed"
// guards in this package are unkillable from a test, and why RandomBytes, the
// one generator that reads rand.Reader through io.ReadFull, is the only place
// where a failing reader can be observed.
// ---------------------------------------------------------------------------

type sequentialRand struct {
	mu      sync.Mutex
	next    byte
	reads   int
	gate    chan struct{} // when non-nil, every Read blocks until it is closed
	failure error
}

func (r *sequentialRand) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gate != nil {
		<-r.gate
	}
	r.reads++
	if r.failure != nil {
		return 0, r.failure
	}
	for i := range p {
		p[i] = r.next
		r.next++
	}
	return len(p), nil
}

func (r *sequentialRand) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

func sequentialBytes(start byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(int(start) + i)
	}
	return out
}

func withRandReader(t *testing.T, r *sequentialRand) *sequentialRand {
	original := rand.Reader
	rand.Reader = r
	t.Cleanup(func() { rand.Reader = original })
	return r
}

// routedDefaultTransport sends the listed hosts to a scripted RoundTripper and
// everything else to the transport it replaced, so swapping http.DefaultTransport
// cannot redirect another test's httptest traffic.
type routedDefaultTransport struct {
	routes map[string]http.RoundTripper
	rest   http.RoundTripper
}

func (r *routedDefaultTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt, ok := r.routes[req.URL.Host]; ok {
		return rt.RoundTrip(req)
	}
	return r.rest.RoundTrip(req)
}

func withRoutedDefaultTransport(t *testing.T, routes map[string]http.RoundTripper) {
	original := http.DefaultTransport
	http.DefaultTransport = &routedDefaultTransport{routes: routes, rest: original}
	t.Cleanup(func() { http.DefaultTransport = original })
}

// exchangeRecorder answers as a token endpoint and keeps each request body.
type exchangeRecorder struct {
	status   int
	body     string
	requests []string
}

func (e *exchangeRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	payload := ""
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("read exchange request body: %w", err)
		}
		payload = string(raw)
	}
	e.requests = append(e.requests, req.Method+" "+req.URL.String()+" "+payload)
	return &http.Response{
		StatusCode: e.status,
		Body:       io.NopCloser(strings.NewReader(e.body)),
		Header:     make(http.Header),
	}, nil
}

func flowConfigForExchange(store *FileTokenStore, serverName string, onAuthURL func(string)) OAuthFlowConfig {
	return OAuthFlowConfig{
		ServerName:    serverName,
		ServerConfig:  &SSEConfig{URL: "http://example.com/mcp"},
		TokenStore:    store,
		TokenEndpoint: "https://auth.example.com/oauth/token",
		ClientID:      "client-123",
		OnAuthURL:     onAuthURL,
	}
}

func TestPerformMCPOAuthFlowExchangesTheCodeForTokens(t *testing.T) {
	const codeValue = "flow-auth-code"
	stateWant := base64.RawURLEncoding.EncodeToString(sequentialBytes(0, 32))
	verifierWant := base64.RawURLEncoding.EncodeToString(sequentialBytes(32, 32))

	t.Run("Authorized", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		reader := withRandReader(t, &sequentialRand{})
		endpoint := &exchangeRecorder{status: http.StatusOK, body: `{"access_token":"flow-access-token","refresh_token":"flow-refresh","expires_in":3600,"token_type":"Bearer"}`}
		withRoutedDefaultTransport(t, map[string]http.RoundTripper{"auth.example.com": endpoint})
		port := freePort(t)
		t.Setenv("MCP_OAUTH_CALLBACK_PORT", strconv.Itoa(port))

		entered := make(chan string, 1)
		config := flowConfigForExchange(store, "flow-server", func(url string) { entered <- url })
		type outcome struct {
			result *OAuthFlowResult
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			result, err := PerformMCPOAuthFlow(context.Background(), config)
			done <- outcome{result: result, err: err}
		}()

		authURL := <-entered
		conn := dialCallback(t, port)
		defer conn.Close()
		if _, err := fmt.Fprintf(conn, "GET /callback?code=%s&state=%s HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n", codeValue, stateWant); err != nil {
			t.Fatalf("write callback request: %v", err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("read callback response: %v", err)
		}
		page, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read callback response body: %v", err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close callback response body: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("callback status = %d, want 200 for a matching state and a code", resp.StatusCode)
		}
		if string(page) != "<h1>Authentication Successful</h1><p>You can close this window.</p>" {
			t.Errorf("callback page = %q, want the success page", string(page))
		}

		watchdog := time.NewTimer(5 * time.Second) // REAL-TIME
		defer watchdog.Stop()
		var got outcome
		select {
		case got = <-done:
		case <-watchdog.C:
			t.Fatalf("PerformMCPOAuthFlow did not finish 5s after a callback carrying state %q", stateWant)
		}
		if got.err != nil {
			t.Fatalf("PerformMCPOAuthFlow error = %v, want the exchanged tokens", got.err)
		}
		if got.result == nil {
			t.Fatalf("PerformMCPOAuthFlow result = nil, want tokens from https://auth.example.com/oauth/token")
		}
		if got.result.Tokens == nil {
			t.Fatalf("result.Tokens = nil, want the token response")
		}
		if got.result.Tokens.AccessToken != "flow-access-token" {
			t.Errorf("AccessToken = %q, want %q", got.result.Tokens.AccessToken, "flow-access-token")
		}
		if got.result.Tokens.RefreshToken != "flow-refresh" {
			t.Errorf("RefreshToken = %q, want %q", got.result.Tokens.RefreshToken, "flow-refresh")
		}
		if got.result.Tokens.ExpiresIn != 3600 {
			t.Errorf("ExpiresIn = %v, want 3600", got.result.Tokens.ExpiresIn)
		}
		if reader.count() != 2 {
			t.Errorf("rand reads = %d, want 2 (state then code verifier)", reader.count())
		}
		if len(endpoint.requests) != 1 {
			t.Fatalf("token endpoint saw %d requests, want 1: %v", len(endpoint.requests), endpoint.requests)
		}
		parts := strings.SplitN(endpoint.requests[0], " ", 3)
		if len(parts) != 3 {
			t.Fatalf("recorded exchange request = %q, want METHOD URL FORM", endpoint.requests[0])
		}
		method, target, form := parts[0], parts[1], parts[2]
		if method != http.MethodPost || target != "https://auth.example.com/oauth/token" {
			t.Errorf("exchange request = %s %s, want POST https://auth.example.com/oauth/token", method, target)
		}
		values, err := url.ParseQuery(form)
		if err != nil {
			t.Fatalf("parse exchange form %q: %v", form, err)
		}
		want := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {codeValue},
			"code_verifier": {verifierWant},
			"redirect_uri":  {BuildRedirectURI(port)},
			"client_id":     {"client-123"},
		}
		if values.Encode() != want.Encode() {
			t.Errorf("exchange form = %q, want %q", values.Encode(), want.Encode())
		}
		if authURL == "" {
			t.Error("OnAuthURL received an empty authorization URL")
		}
	})

	t.Run("ExchangeRefused", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		withRandReader(t, &sequentialRand{})
		endpoint := &exchangeRecorder{status: http.StatusInternalServerError, body: "upstream refused"}
		withRoutedDefaultTransport(t, map[string]http.RoundTripper{"auth.example.com": endpoint})
		port := freePort(t)
		t.Setenv("MCP_OAUTH_CALLBACK_PORT", strconv.Itoa(port))

		entered := make(chan string, 1)
		config := flowConfigForExchange(store, "flow-server", func(url string) { entered <- url })
		type outcome struct {
			result *OAuthFlowResult
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			result, err := PerformMCPOAuthFlow(context.Background(), config)
			done <- outcome{result: result, err: err}
		}()

		<-entered
		conn := dialCallback(t, port)
		defer conn.Close()
		if _, err := fmt.Fprintf(conn, "GET /callback?code=%s&state=%s HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n", codeValue, stateWant); err != nil {
			t.Fatalf("write callback request: %v", err)
		}

		watchdog := time.NewTimer(5 * time.Second) // REAL-TIME
		defer watchdog.Stop()
		var got outcome
		select {
		case got = <-done:
		case <-watchdog.C:
			t.Fatalf("PerformMCPOAuthFlow did not finish 5s after a refused token exchange")
		}
		wantErr := "mcp: flow-server: token exchange failed: mcp: token exchange failed: HTTP 500: upstream refused"
		if got.err == nil {
			t.Fatalf("PerformMCPOAuthFlow error = nil, want %q", wantErr)
		}
		if got.err.Error() != wantErr {
			t.Errorf("PerformMCPOAuthFlow error = %q, want %q", got.err.Error(), wantErr)
		}
		if got.result != nil {
			t.Errorf("result = %+v, want nil when the exchange is refused", got.result)
		}
		if got.result != nil && got.result.Tokens != nil {
			t.Errorf("result.Tokens = %+v, want nil: an authorization code is not a credential", got.result.Tokens)
		}
		unwrapped := errors.Unwrap(got.err)
		if unwrapped == nil {
			t.Fatalf("PerformMCPOAuthFlow error does not wrap the exchange failure: %v", got.err)
		}
		if unwrapped.Error() != "mcp: token exchange failed: HTTP 500: upstream refused" {
			t.Errorf("wrapped error = %q, want %q", unwrapped.Error(), "mcp: token exchange failed: HTTP 500: upstream refused")
		}
	})
}

func TestPerformMCPOAuthFlowDuplicateAuthorizedCallbacksDoNotStallShutdown(t *testing.T) {
	const codeValue = "duplicate-code"
	stateWant := base64.RawURLEncoding.EncodeToString(sequentialBytes(0, 32))
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	withRandReader(t, &sequentialRand{})
	endpoint := &exchangeRecorder{status: http.StatusOK, body: `{"access_token":"duplicate-access","token_type":"Bearer"}`}
	withRoutedDefaultTransport(t, map[string]http.RoundTripper{"auth.example.com": endpoint})
	port := freePort(t)
	t.Setenv("MCP_OAUTH_CALLBACK_PORT", strconv.Itoa(port))

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	config := flowConfigForExchange(store, "duplicate-server", func(_ string) {
		entered <- struct{}{}
		<-release
	})
	type outcome struct {
		result *OAuthFlowResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := PerformMCPOAuthFlow(context.Background(), config)
		done <- outcome{result: result, err: err}
	}()
	<-entered

	// The first authorized callback is answered with the success page and
	// closes the listener; every repeat must be refused (auth.ts:1147).
	first := dialCallback(t, port)
	defer first.Close()
	if _, err := fmt.Fprintf(first, "GET /callback?code=%s&state=%s HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n", codeValue, stateWant); err != nil {
		t.Fatalf("write the authorized callback: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(first), nil)
	if err != nil {
		t.Fatalf("read the authorized callback response: %v", err)
	}
	page, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the authorized callback page: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close the authorized callback page: %v", err)
	}
	if resp.StatusCode != http.StatusOK || string(page) != "<h1>Authentication Successful</h1><p>You can close this window.</p>" {
		t.Fatalf("authorized callback answer = %d %q, want the success page", resp.StatusCode, string(page))
	}
	waitCallbackRefused(t, port)
	close(release)

	watchdog := time.NewTimer(3 * time.Second) // REAL-TIME
	defer watchdog.Stop()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("PerformMCPOAuthFlow error = %v, want the exchanged tokens", got.err)
		}
		if got.result == nil || got.result.Tokens == nil {
			t.Fatalf("result = %+v, want tokens from the first authorized callback", got.result)
		}
		if got.result.Tokens.AccessToken != "duplicate-access" {
			t.Errorf("AccessToken = %q, want %q", got.result.Tokens.AccessToken, "duplicate-access")
		}
		if len(endpoint.requests) != 1 {
			t.Errorf("token endpoint saw %d requests, want 1", len(endpoint.requests))
		}
	case <-watchdog.C:
		t.Error("PerformMCPOAuthFlow is still inside Shutdown 3s after the callback closed the server " +
			"(the handler-triggered Shutdown must not stall the flow)")
	}
}

func TestPerformMCPOAuthFlowDuplicateStateMismatchCallbacksDoNotStallShutdown(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	port := freePort(t)
	t.Setenv("MCP_OAUTH_CALLBACK_PORT", strconv.Itoa(port))

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	config := flowConfigForExchange(store, "csrf-server", func(_ string) {
		entered <- struct{}{}
		<-release
	})
	type outcome struct {
		result *OAuthFlowResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := PerformMCPOAuthFlow(context.Background(), config)
		done <- outcome{result: result, err: err}
	}()
	<-entered

	// The first state-mismatch callback is answered with the 400 page and
	// closes the listener; every repeat must be refused (auth.ts:1111-1116).
	first := dialCallback(t, port)
	defer first.Close()
	if _, err := first.Write([]byte("GET /callback?code=predictable-code&state=injected-state HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("write the state mismatch callback: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(first), nil)
	if err != nil {
		t.Fatalf("read the state mismatch response: %v", err)
	}
	page, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the state mismatch page: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close the state mismatch page: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(page), "Invalid state parameter") {
		t.Fatalf("state mismatch answer = %d %q, want the 400 error page", resp.StatusCode, string(page))
	}
	waitCallbackRefused(t, port)
	close(release)

	watchdog := time.NewTimer(3 * time.Second) // REAL-TIME
	defer watchdog.Stop()
	select {
	case got := <-done:
		if got.err == nil {
			t.Fatalf("PerformMCPOAuthFlow error = nil, want the state mismatch rejection")
		}
		if got.err.Error() != "OAuth state mismatch - possible CSRF attack" {
			t.Errorf("PerformMCPOAuthFlow error = %q, want %q", got.err.Error(), "OAuth state mismatch - possible CSRF attack")
		}
		if got.result != nil {
			t.Errorf("result = %+v, want nil for a state mismatch", got.result)
		}
	case <-watchdog.C:
		t.Error("PerformMCPOAuthFlow is still inside Shutdown 3s after the callback closed the server " +
			"(the handler-triggered Shutdown must not stall the flow)")
	}
}

// TestMCPAuthHandlerGeneratorBodiesHoldTheLock pins statement/defer-remove on
// MCPAuthHandler.State and MCPAuthHandler.CodeVerifier. Dropping only the
// `defer` keyword still unlocks before the body runs, so single threaded
// behaviour is unchanged: what is lost is exclusion inside the body, where the
// generated value is cached. crypto/rand is gated so every goroutine is inside
// the generator before any of them can finish, which turns the lost exclusion
// into a count: the cache must be filled by exactly one draw.
func TestMCPAuthHandlerGeneratorBodiesHoldTheLock(t *testing.T) {
	for _, probe := range []struct {
		name   string
		called func(*MCPAuthHandler) (string, error)
	}{
		{name: "State", called: func(h *MCPAuthHandler) (string, error) { return h.State() }},
		{name: "CodeVerifier", called: func(h *MCPAuthHandler) (string, error) { return h.CodeVerifier() }},
	} {
		t.Run(probe.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := NewFileTokenStore(dir)
			if err != nil {
				t.Fatalf("NewFileTokenStore: %v", err)
			}
			handler := NewMCPAuthHandler("lock-probe", &SSEConfig{URL: "http://example.com/mcp"}, store, dir)
			gate := make(chan struct{})
			reader := withRandReader(t, &sequentialRand{gate: gate})

			concurrency := 4
			results := make(chan string, concurrency)
			for range concurrency {
				go func() {
					value, err := probe.called(handler)
					if err != nil {
						results <- "error: " + err.Error()
						return
					}
					results <- value
				}()
			}
			settle := time.NewTimer(150 * time.Millisecond) // REAL-TIME
			<-settle.C
			close(gate)

			seen := make([]string, 0, concurrency)
			collect := time.NewTimer(5 * time.Second) // REAL-TIME
			defer collect.Stop()
			for range concurrency {
				select {
				case value := <-results:
					seen = append(seen, value)
				case <-collect.C:
					t.Fatalf("%s did not return for all %d concurrent calls (draws so far: %d)", probe.name, concurrency, reader.count())
				}
			}
			if draws := reader.count(); draws != 1 {
				t.Errorf("%s drew %d random values for %d concurrent calls, want 1: the lock was released before the body finished, so each call generated its own value", probe.name, draws, concurrency)
			}
			for _, value := range seen {
				if value != seen[0] {
					t.Errorf("%s returned %q, want the cached %q", probe.name, value, seen[0])
				}
			}
		})
	}
}

func TestRandomBytesReportsReaderFailure(t *testing.T) {
	reader := withRandReader(t, &sequentialRand{failure: errors.New("rand reader refused")})
	value, err := RandomBytes(24)
	if err == nil {
		t.Fatalf("RandomBytes(24) error = nil, want the reader failure (draws: %d)", reader.count())
	}
	if err.Error() != "rand reader refused" {
		t.Errorf("RandomBytes error = %q, want %q", err.Error(), "rand reader refused")
	}
	if value != nil {
		t.Errorf("RandomBytes value = %d bytes, want nil when the reader fails", len(value))
	}
	if reader.count() != 1 {
		t.Errorf("rand reads = %d, want 1", reader.count())
	}
}
