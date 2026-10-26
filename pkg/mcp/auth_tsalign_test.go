package mcp

// TS semantic-alignment tests for revokeToken / RevokeServerTokens /
// PerformMCPOAuthFlow (auth.ts vs the Go port), each written red-first against
// the pre-alignment behavior.
//
// Probes reused from auth_fix4_test.go: withCapturedLogs, withMetadataTransport,
// withRandReader, withRoutedDefaultTransport, autoCallbackPort, dialCallback.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// revokeToken — TS auth.ts:430-458: axios.post rejects any non-2xx status, so
// every stage's status is checked. The Go port only looked at the 401 retry,
// letting a flat 500/403 first answer pass as success.
// ---------------------------------------------------------------------------

func TestRevokeTokenFirstResponseStatusIsChecked(t *testing.T) {
	for _, tt := range []struct {
		name         string
		status       int
		accessToken  string
		wantRequests int
		wantErrText  string
	}{
		// axios fails these outright; only 401 with an access token retries.
		{"503 is a failure", 503, "access-token", 1, "mcp: revocation failed with status 503"},
		{"403 is a failure", 403, "access-token", 1, "mcp: revocation failed with status 403"},
		{"401 without an access token is a failure", 401, "", 1, "mcp: revocation failed with status 401"},
		{"300 is a failure", 300, "access-token", 1, "mcp: revocation failed with status 300"},
		{"299 is a success", 299, "access-token", 1, ""},
		{"204 is a success", 204, "access-token", 1, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			seen := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				seen++
				w.WriteHeader(tt.status)
			}))
			defer server.Close()

			err := revokeToken(context.Background(), server.URL, "rt", "refresh_token", "client-1", "secret-1", tt.accessToken)
			if seen != tt.wantRequests {
				t.Errorf("revocation endpoint saw %d requests, want %d", seen, tt.wantRequests)
			}
			if tt.wantErrText == "" {
				if err != nil {
					t.Errorf("revokeToken error = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErrText {
				t.Errorf("revokeToken error = %v, want %q", err, tt.wantErrText)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// revokeToken — TS auth.ts:444-450: the Bearer retry must drop
// client_id/client_secret from the body (RFC 6749 §2.3.1: "must not send more
// than one auth method"). The Go port re-encoded the same form.
// ---------------------------------------------------------------------------

func TestRevokeTokenBearerRetryDropsClientCredentials(t *testing.T) {
	type recorded struct {
		body string
		auth string
	}
	var got []recorded
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading revocation body: %v", err)
		}
		got = append(got, recorded{body: string(payload), auth: r.Header.Get("Authorization")})
		if len(got) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := revokeToken(context.Background(), server.URL, "rt", "refresh_token", "client-1", "secret-1", "access-1"); err != nil {
		t.Fatalf("revokeToken: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("revocation endpoint saw %d requests, want 2 (initial + Bearer retry)", len(got))
	}
	if want := "client_id=client-1&client_secret=secret-1&token=rt&token_type_hint=refresh_token"; got[0].body != want {
		t.Errorf("initial body = %q, want %q", got[0].body, want)
	}
	if got[0].auth != "" {
		t.Errorf("initial Authorization = %q, want none", got[0].auth)
	}
	// RFC 6749 §2.3.1: the retry authenticates with Bearer only.
	if want := "token=rt&token_type_hint=refresh_token"; got[1].body != want {
		t.Errorf("retry body = %q, want %q without client credentials", got[1].body, want)
	}
	if got[1].auth != "Bearer access-1" {
		t.Errorf("retry Authorization = %q, want %q", got[1].auth, "Bearer access-1")
	}
}

// ---------------------------------------------------------------------------
// F3 — TS auth.ts:484-490 + fetchAuthServerMetadata (auth.ts:256-311): the
// revocation metadata comes from a discovery chain, not only from a configured
// URL: asUrl = tokenData.discoveryState.authorizationServerUrl ?? serverUrl,
// then RFC 9728 protected-resource discovery, then the RFC 8414/OIDC
// well-known candidates, then one path-aware legacy retry. The Go port had
// only the configured-URL branch.
// ---------------------------------------------------------------------------

// discoveryAnswer is what the fake authorization server replies to one path.
type discoveryAnswer struct {
	status int
	body   string
}

// discoveryRecorder answers discovery probes by exact path ({{host}} in a body
// becomes this server's host:port, {{origin}} its scheme://host:port) and
// records every hit in order — which candidate URL the chain tried is the
// fact that tells its branches apart.
type discoveryRecorder struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits []string
}

func newDiscoveryAS(t *testing.T, answers map[string]discoveryAnswer, missStatus int) *discoveryRecorder {
	t.Helper()
	rec := &discoveryRecorder{}
	rec.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		// RequestURI keeps the query string so probe-order assertions can
		// distinguish path-aware probes (query kept) from root retries
		// (query dropped); requests without a query record just the path.
		rec.hits = append(rec.hits, r.URL.RequestURI())
		rec.mu.Unlock()
		ans, ok := answers[r.URL.Path]
		if !ok {
			w.WriteHeader(missStatus)
			return
		}
		body := strings.ReplaceAll(ans.body, "{{origin}}", "https://"+r.Host)
		body = strings.ReplaceAll(body, "{{host}}", r.Host)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(ans.status)
		if _, err := fmt.Fprint(w, body); err != nil {
			t.Errorf("writing discovery answer for %s: %v", r.URL.Path, err)
		}
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func (d *discoveryRecorder) hitLog() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.hits, ",")
}

func tsalignStore(t *testing.T, tokenData *OAuthTokenData) (*FileTokenStore, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("srv", tokenData); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	return store, dir
}

func tsalignTokens() *OAuthTokenData {
	return &OAuthTokenData{
		ServerName:   "srv",
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		ClientID:     "client-1",
	}
}

const (
	prPath   = "/.well-known/oauth-protected-resource"
	asPath   = "/.well-known/oauth-authorization-server"
	oidcPath = "/.well-known/openid-configuration"
)

func TestRevokeServerTokensUsesDiscoveryStateASURL(t *testing.T) {
	// The MCP server URL is unreachable; only the persisted discoveryState
	// points at a live authorization server.
	as := newDiscoveryAS(t, map[string]discoveryAnswer{
		prPath: {200, `{"resource": "https://{{host}}/mcp", "authorization_servers": ["{{origin}}"]}`},
		asPath: {200, `{"revocation_endpoint": "https://{{host}}/revoke"}`},
	}, http.StatusOK)
	withMetadataTransport(t, as.srv)
	store, _ := tsalignStore(t, &OAuthTokenData{
		ServerName:     "srv",
		AccessToken:    "access-1",
		RefreshToken:   "refresh-1",
		ClientID:       "client-1",
		DiscoveryState: &OAuthDiscoveryState{AuthorizationServerURL: as.srv.URL},
	})

	if err := RevokeServerTokens(context.Background(), store, "srv", "https://127.0.0.1:1/mcp", ""); err != nil {
		t.Fatalf("RevokeServerTokens: %v", err)
	}
	if want := prPath + "," + asPath + ",/revoke,/revoke"; as.hitLog() != want {
		t.Fatalf("authorization server saw %q, want %q", as.hitLog(), want)
	}
	tok, err := store.ReadTokens("srv")
	if err != nil {
		t.Fatalf("ReadTokens: %v", err)
	}
	if tok != nil {
		t.Errorf("stored tokens = %+v, want the record deleted", tok)
	}
}

func TestRevokeServerTokensDiscoversViaProtectedResourceMetadata(t *testing.T) {
	// No discovery state: RFC 9728 probe on the MCP server URL itself, which
	// points the chain at the authorization server to revoke against.
	as := newDiscoveryAS(t, map[string]discoveryAnswer{
		prPath: {200, `{"resource": "https://{{host}}/", "authorization_servers": ["{{origin}}"]}`},
		asPath: {200, `{"revocation_endpoint": "https://{{host}}/revoke"}`},
	}, http.StatusOK)
	withMetadataTransport(t, as.srv)
	store, _ := tsalignStore(t, tsalignTokens())

	if err := RevokeServerTokens(context.Background(), store, "srv", as.srv.URL, ""); err != nil {
		t.Fatalf("RevokeServerTokens: %v", err)
	}
	if want := prPath + "," + asPath + ",/revoke,/revoke"; as.hitLog() != want {
		t.Fatalf("authorization server saw %q, want %q", as.hitLog(), want)
	}
}

func TestRevokeServerTokensLegacyPathAwareFallback(t *testing.T) {
	// RFC 9728 absent and the root well-knowns empty: a path-bearing server
	// URL gets the legacy RFC 8414 path-inserted probe (auth.ts:302-310).
	as := newDiscoveryAS(t, map[string]discoveryAnswer{
		asPath + "/mcp": {200, `{"revocation_endpoint": "https://{{host}}/revoke"}`},
	}, http.StatusNotFound)
	withMetadataTransport(t, as.srv)
	store, _ := tsalignStore(t, tsalignTokens())

	if err := RevokeServerTokens(context.Background(), store, "srv", as.srv.URL+"/mcp", ""); err != nil {
		t.Fatalf("RevokeServerTokens: %v", err)
	}
	// The PR probe is path-aware first, then falls back to the root well-known
	// (SDK discoverMetadataWithFallback); both miss before the AS chain runs.
	want := prPath + "/mcp," + prPath + "," + asPath + "," + oidcPath + "," + asPath + "/mcp,/revoke,/revoke"
	if as.hitLog() != want {
		t.Fatalf("authorization server saw %q, want %q", as.hitLog(), want)
	}
}

func TestRevokeServerTokensRootURLSkipsLegacyFallback(t *testing.T) {
	// auth.ts:304-307 — for root URLs the SDK's own fallback already probed
	// the same endpoints, so the legacy retry is skipped and no revocation
	// happens.
	as := newDiscoveryAS(t, nil, http.StatusNotFound)
	withMetadataTransport(t, as.srv)
	store, _ := tsalignStore(t, tsalignTokens())

	var revokeErr error
	logs := withCapturedLogs(t, func() {
		revokeErr = RevokeServerTokens(context.Background(), store, "srv", as.srv.URL, "")
	})
	if revokeErr != nil {
		t.Fatalf("RevokeServerTokens: %v", revokeErr)
	}
	if want := prPath + "," + asPath + "," + oidcPath; as.hitLog() != want {
		t.Fatalf("authorization server saw %q, want %q and no more probes", as.hitLog(), want)
	}
	if found := logsFor(logs, "mcp: no OAuth metadata found"); len(found) != 1 {
		t.Errorf("captured %d %q records, want 1: %v", len(found), "mcp: no OAuth metadata found", logs)
	}
	tok, err := store.ReadTokens("srv")
	if err != nil {
		t.Fatalf("ReadTokens: %v", err)
	}
	if tok != nil {
		t.Errorf("stored tokens = %+v, want the record deleted", tok)
	}
}

func TestRevokeServerTokensDiscovery5XXFallsBackToLegacy(t *testing.T) {
	// A 5xx anywhere in the RFC 9728 → 8414 chain aborts it (SDK auth.js:630)
	// and fetchAuthServerMetadata falls through to the legacy path-aware retry
	// (auth.ts:292-300).
	as := newDiscoveryAS(t, map[string]discoveryAnswer{
		asPath:          {500, "upstream exploded"},
		asPath + "/mcp": {200, `{"revocation_endpoint": "https://{{host}}/revoke"}`},
	}, http.StatusNotFound)
	withMetadataTransport(t, as.srv)
	store, _ := tsalignStore(t, tsalignTokens())

	var revokeErr error
	logs := withCapturedLogs(t, func() {
		revokeErr = RevokeServerTokens(context.Background(), store, "srv", as.srv.URL+"/mcp", "")
	})
	if revokeErr != nil {
		t.Fatalf("RevokeServerTokens: %v", revokeErr)
	}
	// PR probes (path-aware, then root) both 404; the root oauth probe answers
	// 500 and aborts the chain before the OIDC probe; the legacy path-inserted
	// candidate then revokes.
	if want := prPath + "/mcp," + prPath + "," + asPath + "," + asPath + "/mcp,/revoke,/revoke"; as.hitLog() != want {
		t.Fatalf("authorization server saw %q, want %q", as.hitLog(), want)
	}
	found := logsFor(logs, "mcp: RFC 9728 discovery failed, falling back")
	if len(found) != 1 {
		t.Fatalf("captured %d fallback records, want 1: %v", len(found), logs)
	}
	if !strings.Contains(found[0]["error"], "HTTP 500") {
		t.Errorf("fallback record error = %q, want the 5xx from the chain", found[0]["error"])
	}
}

func TestRevokeServerTokensConfiguredNon2xxReportsFetchFailure(t *testing.T) {
	// auth.ts:273-278 — a configured URL that answers non-2xx is an error
	// ("HTTP ${status} fetching configured auth server metadata from ..."),
	// logged by revokeServerTokens' catch, never a silent no-metadata.
	as := newDiscoveryAS(t, map[string]discoveryAnswer{"/cfg": {500, "nope"}}, http.StatusOK)
	withMetadataTransport(t, as.srv)
	store, _ := tsalignStore(t, tsalignTokens())

	var revokeErr error
	logs := withCapturedLogs(t, func() {
		revokeErr = RevokeServerTokens(context.Background(), store, "srv", "https://127.0.0.1:1/mcp", as.srv.URL+"/cfg")
	})
	if revokeErr != nil {
		t.Fatalf("RevokeServerTokens: %v, want nil (best-effort)", revokeErr)
	}
	if want := "/cfg"; as.hitLog() != want {
		t.Fatalf("server saw %q, want only the configured fetch %q", as.hitLog(), want)
	}
	found := logsFor(logs, "mcp: failed to revoke tokens")
	if len(found) != 1 {
		t.Fatalf("captured %d fetch-failure records, want 1: %v", len(found), logs)
	}
	wantErr := fmt.Sprintf("mcp: HTTP %d fetching configured auth server metadata from %s", 500, as.srv.URL+"/cfg")
	if found[0]["error"] != wantErr {
		t.Errorf("fetch-failure record error = %q, want %q", found[0]["error"], wantErr)
	}
	tok, err := store.ReadTokens("srv")
	if err != nil {
		t.Fatalf("ReadTokens: %v", err)
	}
	if tok != nil {
		t.Errorf("stored tokens = %+v, want the record deleted", tok)
	}
}

func TestRevokeServerTokensNonHTTPSConfiguredClearsTokens(t *testing.T) {
	// auth.ts:263-268 + 481-570 — the https check throws inside the revocation
	// try, so the catch logs it and local tokens are still cleared; the error
	// never reaches the caller.
	store, _ := tsalignStore(t, tsalignTokens())

	var revokeErr error
	logs := withCapturedLogs(t, func() {
		revokeErr = RevokeServerTokens(context.Background(), store, "srv", "https://127.0.0.1:1/mcp", "http://insecure.example.com/metadata")
	})
	if revokeErr != nil {
		t.Fatalf("RevokeServerTokens = %v, want nil: the check throws inside the best-effort block", revokeErr)
	}
	found := logsFor(logs, "mcp: failed to revoke tokens")
	if len(found) != 1 {
		t.Fatalf("captured %d fetch-failure records, want 1: %v", len(found), logs)
	}
	if !strings.Contains(found[0]["error"], "must use https://") ||
		!strings.Contains(found[0]["error"], "http://insecure.example.com/metadata") {
		t.Errorf("fetch-failure record error = %q, want the must-use-https message with the offending URL", found[0]["error"])
	}
	tok, err := store.ReadTokens("srv")
	if err != nil {
		t.Fatalf("ReadTokens: %v", err)
	}
	if tok != nil {
		t.Errorf("stored tokens = %+v, want the record deleted", tok)
	}
}

// ---------------------------------------------------------------------------
// F4 — TS auth.ts:1115/1130/1147: every first-callback branch (state mismatch,
// error, success) answers, then cleanup() closes the callback server, so a
// repeat callback meets a closed listener. The Go port kept listening until
// the flow's deferred Shutdown, answering repeats.
// ---------------------------------------------------------------------------

func TestPerformMCPOAuthFlowClosesCallbackServerOnFirstCallback(t *testing.T) {
	t.Run("authorized callback", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		// A parked exchange keeps the flow inside ExchangeAuthCode, so a
		// refusal observed during that window can only come from the callback
		// server closing on the first callback, not from the flow's own
		// Shutdown.
		withRoutedDefaultTransport(t, map[string]http.RoundTripper{"auth.example.com": &parkedTransport{}})
		t.Setenv("MCP_OAUTH_CALLBACK_PORT", "") // flow picks its own port (autoCallbackPort)

		var flowState string
		entered := make(chan string, 1)
		config := flowConfigForExchange(store, "f4-server", func(url string) { entered <- url })
		config.OnOAuthParams = func(state, _ string) { flowState = state }
		type outcome struct {
			result *OAuthFlowResult
			err    error
		}
		done := make(chan outcome, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			result, err := PerformMCPOAuthFlow(ctx, config)
			done <- outcome{result: result, err: err}
		}()

		port := autoCallbackPort(t, <-entered)
		conn := dialCallback(t, port)
		defer conn.Close()
		if _, err := fmt.Fprintf(conn, "GET /callback?code=f4-code&state=%s HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n", flowState); err != nil {
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
			t.Fatalf("callback answer = %d %q, want the success page for the first callback", resp.StatusCode, string(page))
		}

		// The exchange is parked, the flow is still alive: a repeat callback
		// must meet a closed listener, never a second answered page. The probe
		// carries only the matching state (no code): even when it slips into
		// the shutdown race window and gets answered, it leaves nothing in the
		// flow's channels.
		deadline := time.Now().Add(3 * time.Second) // REAL-TIME
		for i := 0; ; i++ {
			second, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				break // listener closed — the TS contract
			}
			connected := second
			if _, err := fmt.Fprintf(connected, "GET /callback?state=%s HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n", url.QueryEscape(flowState)); err != nil {
				_ = connected.Close()
				break // accepted then slammed shut — no answer was served
			}
			_ = connected.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // REAL-TIME
			secondResp, readErr := http.ReadResponse(bufio.NewReader(connected), nil)
			if readErr != nil {
				_ = connected.Close()
				break // closed without a response — no answer was served
			}
			secondPage, _ := io.ReadAll(secondResp.Body)
			_ = secondResp.Body.Close()
			_ = connected.Close()
			if i == 20 || time.Now().After(deadline) { // REAL-TIME
				t.Fatalf("repeat callback was answered with %d %q while the flow was still exchanging: the callback server must close on the first callback (auth.ts:1147)", secondResp.StatusCode, string(secondPage))
			}
			time.Sleep(100 * time.Millisecond) // REAL-TIME
		}

		// Let the parked exchange end and the flow unwind without leaks.
		cancel()
		watchdog := time.NewTimer(5 * time.Second) // REAL-TIME
		defer watchdog.Stop()
		select {
		case got := <-done:
			if got.err == nil {
				t.Fatalf("PerformMCPOAuthFlow = (%+v, nil), want the cancelled exchange", got.result)
			}
		case <-watchdog.C:
			t.Fatal("PerformMCPOAuthFlow never returned after the exchange was cancelled")
		}
	})
}

// callbackRefused reports whether one callback attempt gets no full HTTP
// answer: the listener is closed, or the connection is accepted and then
// slammed shut. Used to pin the close-on-first-callback contract.
//
// The probe carries the flow's real state and no code, so it lands in the
// handler's empty-code branch, which writes nothing to the flow's channels. A
// probe raced into the window between the first callback's answer and the
// shutdown closing the listener would otherwise park a state mismatch (or a
// code) into a channel nobody has drained yet, and the flow's select can then
// answer the probe instead of the first callback.
func callbackRefused(port int, state string) bool {
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return true
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "GET /callback?state=%s HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n", url.QueryEscape(state)); err != nil {
		return true
	}
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // REAL-TIME
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return true
	}
	_ = resp.Body.Close()
	return false
}

// waitCallbackRefused polls until the callback server stops answering, and
// fails the test if it is still serving full pages at the deadline.
func waitCallbackRefused(t *testing.T, port int, state string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second) // REAL-TIME
	for !callbackRefused(port, state) {
		if time.Now().After(deadline) { // REAL-TIME
			t.Fatalf("the callback server on port %d is still answering repeat callbacks: it must close on the first callback (auth.ts:1147)", port)
		}
		time.Sleep(20 * time.Millisecond) // REAL-TIME
	}
}

func TestRevokeServerTokensRootRetryDropsQueryString(t *testing.T) {
	// SDK auth.js:496-501 — the path-aware PR probe keeps the issuer's query
	// string; the root retry resolves against the origin and drops it.
	as := newDiscoveryAS(t, map[string]discoveryAnswer{
		prPath: {200, `{"resource": "https://{{host}}/", "authorization_servers": ["{{origin}}"]}`},
		asPath: {200, `{"revocation_endpoint": "https://{{host}}/revoke"}`},
	}, http.StatusNotFound)
	withMetadataTransport(t, as.srv)
	store, _ := tsalignStore(t, tsalignTokens())

	if err := RevokeServerTokens(context.Background(), store, "srv", as.srv.URL+"/mcp?session=x", ""); err != nil {
		t.Fatalf("RevokeServerTokens: %v", err)
	}
	want := prPath + "/mcp?session=x," + prPath + "," + asPath + ",/revoke,/revoke"
	if as.hitLog() != want {
		t.Fatalf("authorization server saw %q, want %q (path-aware probe keeps ?session=x, root retry drops it)", as.hitLog(), want)
	}
}
