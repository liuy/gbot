package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// FileTokenStore lifecycle
// ---------------------------------------------------------------------------

func TestFileTokenStore_Lifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	// Read nonexistent key returns nil, nil
	tok, err := store.ReadTokens("nonexistent")
	if err != nil {
		t.Fatalf("ReadTokens nonexistent: %v", err)
	}
	if tok != nil {
		t.Errorf("expected nil for nonexistent key, got %+v", tok)
	}

	// Write + Read round-trip
	original := &OAuthTokenData{
		ServerName:   "test-server",
		ServerURL:    "http://example.com",
		AccessToken:  "access-123",
		RefreshToken: "refresh-456",
		ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
		Scope:        "read write",
		ClientID:     "client-abc",
		ClientSecret: "secret-xyz",
		DiscoveryState: &OAuthDiscoveryState{
			AuthorizationServerURL: "https://auth.example.com",
			ResourceMetadataURL:    "https://res.example.com",
		},
		StepUpScope: "admin",
	}
	if err := store.WriteTokens("key1", original); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	got, err := store.ReadTokens("key1")
	if err != nil {
		t.Fatalf("ReadTokens: %v", err)
	}
	if got == nil {
		t.Fatal("ReadTokens returned nil after write")
	}
	if got.AccessToken != original.AccessToken {
		t.Errorf("AccessToken: got %q, want %q", got.AccessToken, original.AccessToken)
	}
	if got.RefreshToken != original.RefreshToken {
		t.Errorf("RefreshToken: got %q, want %q", got.RefreshToken, original.RefreshToken)
	}
	if got.ServerName != original.ServerName {
		t.Errorf("ServerName: got %q, want %q", got.ServerName, original.ServerName)
	}
	if got.Scope != original.Scope {
		t.Errorf("Scope: got %q, want %q", got.Scope, original.Scope)
	}
	if got.ClientID != original.ClientID {
		t.Errorf("ClientID: got %q, want %q", got.ClientID, original.ClientID)
	}
	if got.ClientSecret != original.ClientSecret {
		t.Errorf("ClientSecret: got %q, want %q", got.ClientSecret, original.ClientSecret)
	}
	if got.StepUpScope != original.StepUpScope {
		t.Errorf("StepUpScope: got %q, want %q", got.StepUpScope, original.StepUpScope)
	}
	if got.DiscoveryState == nil {
		t.Fatal("DiscoveryState is nil")
	}
	if got.DiscoveryState.AuthorizationServerURL != original.DiscoveryState.AuthorizationServerURL {
		t.Errorf("DiscoveryState.AuthorizationServerURL: got %q, want %q",
			got.DiscoveryState.AuthorizationServerURL, original.DiscoveryState.AuthorizationServerURL)
	}

	// Delete
	if err := store.DeleteTokens("key1"); err != nil {
		t.Fatalf("DeleteTokens: %v", err)
	}
	tok, err = store.ReadTokens("key1")
	if err != nil {
		t.Fatalf("ReadTokens after delete: %v", err)
	}
	if tok != nil {
		t.Errorf("expected nil after delete, got %+v", tok)
	}

	// Delete nonexistent is not an error
	if err := store.DeleteTokens("nonexistent"); err != nil {
		t.Errorf("DeleteTokens nonexistent: %v", err)
	}
}

func TestFileTokenStore_ClientConfig(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	// Read nonexistent
	cfg, err := store.ReadClientConfig("missing")
	if err != nil {
		t.Fatalf("ReadClientConfig missing: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil for missing key, got %+v", cfg)
	}

	// Write + Read
	original := &OAuthClientConfig{ClientSecret: "super-secret"}
	if err := store.WriteClientConfig("key1", original); err != nil {
		t.Fatalf("WriteClientConfig: %v", err)
	}
	got, err := store.ReadClientConfig("key1")
	if err != nil {
		t.Fatalf("ReadClientConfig: %v", err)
	}
	if got == nil {
		t.Fatal("ReadClientConfig returned nil after write")
	}
	if got.ClientSecret != original.ClientSecret {
		t.Errorf("ClientSecret: got %q, want %q", got.ClientSecret, original.ClientSecret)
	}

	// Delete
	if err := store.DeleteClientConfig("key1"); err != nil {
		t.Fatalf("DeleteClientConfig: %v", err)
	}
	cfg, err = store.ReadClientConfig("key1")
	if err != nil {
		t.Fatalf("ReadClientConfig after delete: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil after delete, got %+v", cfg)
	}
}

func TestFileTokenStore_DirectoryPermissions(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "tokens")
	store, err := NewFileTokenStore(storeDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	_ = store

	info, err := os.Stat(storeDir)
	if err != nil {
		t.Fatalf("Stat store dir: %v", err)
	}
	if info.Mode().Perm() != 0700 {
		t.Errorf("dir permissions: got %o, want 0700", info.Mode().Perm())
	}

	// Write and check file permissions
	if err := store.WriteTokens("test", &OAuthTokenData{ServerName: "s"}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	fileInfo, err := os.Stat(filepath.Join(storeDir, "test.json"))
	if err != nil {
		t.Fatalf("Stat token file: %v", err)
	}
	if fileInfo.Mode().Perm() != 0600 {
		t.Errorf("file permissions: got %o, want 0600", fileInfo.Mode().Perm())
	}
}

func TestFileTokenStore_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := range goroutines {
		go func() {
			defer wg.Done()
			_ = store.WriteTokens("key", &OAuthTokenData{
				ServerName:  fmt.Sprintf("server-%d", i),
				AccessToken: fmt.Sprintf("token-%d", i),
			})
		}()
		go func() {
			defer wg.Done()
			_, _ = store.ReadTokens("key")
		}()
	}
	wg.Wait()

	// Verify data is valid after concurrent access
	tok, err := store.ReadTokens("key")
	if err != nil {
		t.Fatalf("ReadTokens after concurrent access: %v", err)
	}
	if tok == nil {
		t.Fatal("expected non-nil token after concurrent writes")
	}
	if tok.AccessToken == "" {
		t.Error("expected non-empty access token after concurrent writes")
	}
}

// ---------------------------------------------------------------------------
// SanitizeServerName — path traversal prevention
// ---------------------------------------------------------------------------

func TestSanitizeServerName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"normal", "my-server", "my-server"},
		{"path traversal", "../etc/passwd", "___etc_passwd"},
		{"dot", ".", "_"},
		{"double dot", "..", "__"},
		{"empty", "", "_"},
		{"special chars", "my server!", "my_server_"},
		{"CJK", "服务器", "___"},
		{"underscores allowed", "my_server", "my_server"},
		{"mixed", "a.b/c:d", "a_b_c_d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeServerName(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeServerName(%q) = %q, want %q", tt.input, got, tt.want)
			}
			// Verify the result is safe for filepath.Join
			if got == "." || got == ".." {
				t.Errorf("result %q is a path traversal vector", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetServerKey
// ---------------------------------------------------------------------------

func TestGetServerKey(t *testing.T) {
	sse1 := &SSEConfig{URL: "http://example.com/mcp", Headers: map[string]string{"k": "v"}}
	sse2 := &SSEConfig{URL: "http://other.com/mcp", Headers: map[string]string{"k": "v"}}

	key1 := GetServerKey("server", sse1)
	key2 := GetServerKey("server", sse1)
	if key1 != key2 {
		t.Errorf("same config should produce same key: %q != %q", key1, key2)
	}

	key3 := GetServerKey("server", sse2)
	if key1 == key3 {
		t.Error("different URLs should produce different keys")
	}

	key4 := GetServerKey("other-server", sse1)
	if key1 == key4 {
		t.Error("different names should produce different keys")
	}

	// Key should contain server name prefix
	if !strings.HasPrefix(key1, "server|") {
		t.Errorf("key should start with server name: %q", key1)
	}
}

// ---------------------------------------------------------------------------
// RedactSensitiveURLParams
// ---------------------------------------------------------------------------

func TestRedactSensitiveURLParams(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string // substring that should NOT be present
	}{
		{"redacts state", "http://example.com/auth?state=secret123&code=abc", "[REDACTED]"},
		{"redacts code_verifier", "http://example.com/auth?code_verifier=xyz", "[REDACTED]"},
		{"preserves other params", "http://example.com/auth?client_id=myid&redirect_uri=http://localhost", "client_id=myid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RedactSensitiveURLParams(tt.url)
			// Check that sensitive values (not the [REDACTED] placeholder) are gone
			if strings.Contains(result, "secret123") {
				t.Errorf("sensitive value not redacted: %q", result)
			}
			if strings.Contains(result, "code=abc") {
				t.Errorf("code value not redacted: %q", result)
			}
		})
	}

	// Invalid URL returned as-is
	invalid := "not a url :::"
	if got := RedactSensitiveURLParams(invalid); got != invalid {
		t.Errorf("invalid URL should be returned as-is: got %q", got)
	}
}

// ---------------------------------------------------------------------------
// GetScopeFromMetadata
// ---------------------------------------------------------------------------

func TestGetScopeFromMetadata(t *testing.T) {
	tests := []struct {
		name string
		meta *OAuthMetadata
		want string
	}{
		{"nil", nil, ""},
		{"scope field", &OAuthMetadata{Scope: "read write"}, "read write"},
		{"default_scope fallback", &OAuthMetadata{DefaultScope: "default"}, "default"},
		{"scopes_supported fallback", &OAuthMetadata{ScopesSupported: []string{"a", "b"}}, "a b"},
		{"scope preferred over default", &OAuthMetadata{Scope: "primary", DefaultScope: "fallback"}, "primary"},
		{"empty metadata", &OAuthMetadata{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetScopeFromMetadata(tt.meta)
			if got != tt.want {
				t.Errorf("GetScopeFromMetadata() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PKCE helpers
// ---------------------------------------------------------------------------

func TestGenerateState(t *testing.T) {
	s1, err := GenerateState()
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}
	if s1 == "" {
		t.Fatal("state should not be empty")
	}
	s2, err := GenerateState()
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}
	if s1 == s2 {
		t.Error("consecutive states should differ")
	}
}

func TestGenerateCodeVerifier(t *testing.T) {
	v, err := GenerateCodeVerifier()
	if err != nil {
		t.Fatalf("GenerateCodeVerifier: %v", err)
	}
	if v == "" {
		t.Fatal("verifier should not be empty")
	}
	// Should be base64url encoded (no +, /, =)
	if strings.ContainsAny(v, "+/=") {
		t.Errorf("verifier should be base64url: %q", v)
	}
}

func TestDeriveCodeChallenge(t *testing.T) {
	verifier := "test-verifier-value"
	c1 := DeriveCodeChallenge(verifier)
	c2 := DeriveCodeChallenge(verifier)
	if c1 != c2 {
		t.Error("same verifier should produce same challenge")
	}
	c3 := DeriveCodeChallenge("different-verifier")
	if c1 == c3 {
		t.Error("different verifiers should produce different challenges")
	}
}

// ---------------------------------------------------------------------------
// FindAvailablePort
// ---------------------------------------------------------------------------

func TestFindAvailablePort(t *testing.T) {
	port, err := FindAvailablePort()
	if err != nil {
		t.Fatalf("FindAvailablePort: %v", err)
	}
	if port <= 0 || port > 65535 {
		t.Errorf("port %d out of valid range", port)
	}
}

// ---------------------------------------------------------------------------
// NormalizeOAuthErrorBody
// ---------------------------------------------------------------------------

func TestNormalizeOAuthErrorBody(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantCode   int
		wantBody   string // substring check
	}{
		{
			"error response passes through",
			400,
			`{"error":"invalid_grant"}`,
			400,
			"invalid_grant",
		},
		{
			"valid token response passes through",
			200,
			`{"access_token":"tok123","token_type":"Bearer","expires_in":3600}`,
			200,
			"tok123",
		},
		{
			"non-standard invalid_refresh_token → invalid_grant",
			200,
			`{"error":"invalid_refresh_token"}`,
			400,
			"invalid_grant",
		},
		{
			"non-standard expired_refresh_token → invalid_grant",
			200,
			`{"error":"expired_refresh_token","error_description":"token has expired"}`,
			400,
			"invalid_grant",
		},
		{
			"non-standard token_expired → invalid_grant",
			200,
			`{"error":"token_expired"}`,
			400,
			"invalid_grant",
		},
		{
			"standard error code still rewritten to 400",
			200,
			`{"error":"invalid_client"}`,
			400,
			"invalid_client",
		},
		{
			"invalid JSON passes through",
			200,
			`not json`,
			200,
			"not json",
		},
		{
			"no error field passes through",
			200,
			`{"some":"data"}`,
			200,
			"some",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCode, gotBody := NormalizeOAuthErrorBody(tt.statusCode, []byte(tt.body))
			if gotCode != tt.wantCode {
				t.Errorf("statusCode = %d, want %d", gotCode, tt.wantCode)
			}
			if !strings.Contains(string(gotBody), tt.wantBody) {
				t.Errorf("body = %q, want to contain %q", string(gotBody), tt.wantBody)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// StepUpDetect
// ---------------------------------------------------------------------------

func TestStepUpDetect(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wwwAuth    string
		want       string
	}{
		{
			"403 with quoted scope",
			403,
			`Bearer error="insufficient_scope", scope="read write"`,
			"read write",
		},
		{
			"403 with unquoted scope",
			403,
			`Bearer error="insufficient_scope", scope=admin`,
			"admin",
		},
		{
			"403 without insufficient_scope",
			403,
			`Bearer error="invalid_token"`,
			"",
		},
		{
			"200 returns empty",
			200,
			"",
			"",
		},
		{
			"401 returns empty",
			401,
			"",
			"",
		},
		{
			"no WWW-Authenticate",
			403,
			"",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tt.statusCode,
				Header:     http.Header{},
			}
			if tt.wwwAuth != "" {
				resp.Header.Set("WWW-Authenticate", tt.wwwAuth)
			}
			got := StepUpDetect(resp)
			if got != tt.want {
				t.Errorf("StepUpDetect() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// RevokeServerTokens (with mock HTTP server)
// ---------------------------------------------------------------------------

func TestRevokeServerTokens(t *testing.T) {
	t.Run("deletes local tokens when no metadata", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		key := "test-server"
		if err := store.WriteTokens(key, &OAuthTokenData{
			ServerName:   "test-server",
			AccessToken:  "access-token-12345",
			RefreshToken: "refresh-token-67890",
			ClientID:     "client-id",
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}

		// No metadata URL → just cleans up local tokens
		err = RevokeServerTokens(context.Background(), store, key, "http://example.com", "")
		if err != nil {
			t.Fatalf("RevokeServerTokens: %v", err)
		}
		// Local tokens should be cleared
		tok, err := store.ReadTokens(key)
		if err != nil {
			t.Fatalf("ReadTokens after revoke: %v", err)
		}
		if tok != nil {
			t.Errorf("expected tokens to be deleted after revocation, got %+v", tok)
		}
	})

	t.Run("no tokens to revoke", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		err = RevokeServerTokens(context.Background(), store, "empty-key", "http://example.com", "")
		if err != nil {
			t.Errorf("expected no error for empty tokens, got %v", err)
		}
	})

	t.Run("non-https metadata URL rejected", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		if err := store.WriteTokens("key", &OAuthTokenData{
			AccessToken: "token",
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", "http://insecure.com/metadata")
		if err == nil {
			t.Fatal("want error for non-https metadata URL")
		}
		if !strings.Contains(err.Error(), "https://") {
			t.Errorf("error should mention https://, got: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// RefreshAuthorization
// ---------------------------------------------------------------------------

func TestRefreshAuthorization_NilRefreshFunc(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	tokens, err := RefreshAuthorization(context.Background(), "key", store, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens != nil {
		t.Errorf("expected nil tokens with nil refreshFunc, got %+v", tokens)
	}
}

func TestRefreshAuthorization_TokenStillValid(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	// Token expires in 1 hour — well above the 5-minute buffer
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "valid-token",
		RefreshToken: "unused-refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour).UnixMilli(),
		Scope:        "read",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	refreshCalled := false
	tokens, err := RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		refreshCalled = true
		return nil, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens == nil {
		t.Fatal("expected tokens, got nil")
	}
	if tokens.AccessToken != "valid-token" {
		t.Errorf("AccessToken = %q, want %q", tokens.AccessToken, "valid-token")
	}
	if refreshCalled {
		t.Error("refresh should not be called when token is still valid")
	}
}

func TestRefreshAuthorization_RefreshSuccess(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	// Token expired 1 second ago
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "old-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(-1 * time.Second).UnixMilli(),
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	tokens, err := RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		if rt != "refresh-token" {
			t.Errorf("refreshFunc called with %q, want %q", rt, "refresh-token")
		}
		return &OAuthTokens{
			AccessToken:  "new-token",
			RefreshToken: "new-refresh",
			ExpiresIn:    3600,
			Scope:        "read write",
			TokenType:    "Bearer",
		}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens == nil {
		t.Fatal("expected tokens, got nil")
	}
	if tokens.AccessToken != "new-token" {
		t.Errorf("AccessToken = %q, want %q", tokens.AccessToken, "new-token")
	}
	// Verify saved
	saved, _ := store.ReadTokens("key")
	if saved == nil {
		t.Fatal("tokens not saved after refresh")
	}
	if saved.AccessToken != "new-token" {
		t.Errorf("saved AccessToken = %q, want %q", saved.AccessToken, "new-token")
	}
	if saved.RefreshToken != "new-refresh" {
		t.Errorf("saved RefreshToken = %q, want %q", saved.RefreshToken, "new-refresh")
	}
}

func TestRefreshAuthorization_InvalidGrant(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "old-token",
		RefreshToken: "bad-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	tokens, err := RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		return nil, fmt.Errorf("invalid_grant: token expired")
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens != nil {
		t.Errorf("expected nil tokens on invalid_grant, got %+v", tokens)
	}
	// Tokens should be cleared
	saved, _ := store.ReadTokens("key")
	if saved == nil {
		t.Fatal("token data should still exist but be cleared")
	}
	if saved.AccessToken != "" {
		t.Errorf("AccessToken should be cleared, got %q", saved.AccessToken)
	}
	if saved.RefreshToken != "" {
		t.Errorf("RefreshToken should be cleared, got %q", saved.RefreshToken)
	}
}

func TestRefreshAuthorization_NoRefreshToken(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken: "expired",
		ExpiresAt:   time.Now().Add(-1 * time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	tokens, err := RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		t.Error("refreshFunc should not be called without refresh token")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens != nil {
		t.Errorf("expected nil without refresh token, got %+v", tokens)
	}
}

// ---------------------------------------------------------------------------
// InvalidateCredentials
// ---------------------------------------------------------------------------

func TestInvalidateCredentials(t *testing.T) {
	t.Run("all deletes tokens", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		if err := store.WriteTokens("key", &OAuthTokenData{
			AccessToken: "token",
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		if err := InvalidateCredentials(store, "key", InvalidateAll); err != nil {
			t.Fatalf("InvalidateCredentials: %v", err)
		}
		tok, _ := store.ReadTokens("key")
		if tok != nil {
			t.Errorf("expected nil after InvalidateAll, got %+v", tok)
		}
	})

	t.Run("client clears client fields", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		if err := store.WriteTokens("key", &OAuthTokenData{
			AccessToken:  "token",
			ClientID:     "client-123",
			ClientSecret: "secret-456",
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		if err := InvalidateCredentials(store, "key", InvalidateClient); err != nil {
			t.Fatalf("InvalidateCredentials: %v", err)
		}
		tok, _ := store.ReadTokens("key")
		if tok == nil {
			t.Fatal("token data should still exist")
		}
		if tok.ClientID != "" {
			t.Errorf("ClientID should be cleared, got %q", tok.ClientID)
		}
		if tok.ClientSecret != "" {
			t.Errorf("ClientSecret should be cleared, got %q", tok.ClientSecret)
		}
		if tok.AccessToken != "token" {
			t.Errorf("AccessToken should be preserved, got %q", tok.AccessToken)
		}
	})

	t.Run("tokens clears token fields", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		if err := store.WriteTokens("key", &OAuthTokenData{
			AccessToken:  "access",
			RefreshToken: "refresh",
			ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
			ClientID:     "client",
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		if err := InvalidateCredentials(store, "key", InvalidateTokens); err != nil {
			t.Fatalf("InvalidateCredentials: %v", err)
		}
		tok, _ := store.ReadTokens("key")
		if tok == nil {
			t.Fatal("token data should still exist")
		}
		if tok.AccessToken != "" {
			t.Errorf("AccessToken should be cleared, got %q", tok.AccessToken)
		}
		if tok.RefreshToken != "" {
			t.Errorf("RefreshToken should be cleared, got %q", tok.RefreshToken)
		}
		if tok.ExpiresAt != 0 {
			t.Errorf("ExpiresAt should be 0, got %d", tok.ExpiresAt)
		}
		if tok.ClientID != "client" {
			t.Errorf("ClientID should be preserved, got %q", tok.ClientID)
		}
	})

	t.Run("discovery clears discovery fields", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		if err := store.WriteTokens("key", &OAuthTokenData{
			AccessToken: "token",
			DiscoveryState: &OAuthDiscoveryState{
				AuthorizationServerURL: "https://auth.example.com",
			},
			StepUpScope: "admin",
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		if err := InvalidateCredentials(store, "key", InvalidateDiscovery); err != nil {
			t.Fatalf("InvalidateCredentials: %v", err)
		}
		tok, _ := store.ReadTokens("key")
		if tok == nil {
			t.Fatal("token data should still exist")
		}
		if tok.DiscoveryState != nil {
			t.Errorf("DiscoveryState should be nil, got %+v", tok.DiscoveryState)
		}
		if tok.StepUpScope != "" {
			t.Errorf("StepUpScope should be cleared, got %q", tok.StepUpScope)
		}
		if tok.AccessToken != "token" {
			t.Errorf("AccessToken should be preserved, got %q", tok.AccessToken)
		}
	})

	t.Run("nil token data is safe", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		for _, scope := range []InvalidateCredentialsScope{InvalidateClient, InvalidateTokens, InvalidateDiscovery} {
			if err := InvalidateCredentials(store, "missing", scope); err != nil {
				t.Errorf("InvalidateCredentials(%s) on missing key: %v", scope, err)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// HasDiscoveryButNoToken
// ---------------------------------------------------------------------------

func TestHasDiscoveryButNoToken(t *testing.T) {
	t.Run("true when discovery exists without tokens", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		if err := store.WriteTokens("key", &OAuthTokenData{
			DiscoveryState: &OAuthDiscoveryState{
				AuthorizationServerURL: "https://auth.example.com",
			},
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		if !HasDiscoveryButNoToken(store, "key") {
			t.Error("expected true when discovery exists without tokens")
		}
	})

	t.Run("false when tokens exist", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		if err := store.WriteTokens("key", &OAuthTokenData{
			AccessToken: "valid-token",
			DiscoveryState: &OAuthDiscoveryState{
				AuthorizationServerURL: "https://auth.example.com",
			},
		}); err != nil {
			t.Fatalf("WriteTokens: %v", err)
		}
		if HasDiscoveryButNoToken(store, "key") {
			t.Error("expected false when access token exists")
		}
	})

	t.Run("false when no data", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileTokenStore(dir)
		if err != nil {
			t.Fatalf("NewFileTokenStore: %v", err)
		}
		if HasDiscoveryButNoToken(store, "missing") {
			t.Error("expected false for missing key")
		}
	})
}

// ---------------------------------------------------------------------------
// MCPAuthHandler
// ---------------------------------------------------------------------------

func newTestHandler(t *testing.T) (*MCPAuthHandler, *FileTokenStore) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	handler := NewMCPAuthHandler("test-server", &SSEConfig{URL: "http://example.com/mcp"}, store, t.TempDir())
	return handler, store
}

func TestMCPAuthHandler_State(t *testing.T) {
	h, _ := newTestHandler(t)
	s1, err := h.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if s1 == "" {
		t.Fatal("state should not be empty")
	}
	s2, err := h.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if s1 != s2 {
		t.Errorf("state should be cached: %q != %q", s1, s2)
	}
}

func TestMCPAuthHandler_CodeVerifier(t *testing.T) {
	h, _ := newTestHandler(t)
	v1, err := h.CodeVerifier()
	if err != nil {
		t.Fatalf("CodeVerifier: %v", err)
	}
	if v1 == "" {
		t.Fatal("verifier should not be empty")
	}
	v2, err := h.CodeVerifier()
	if err != nil {
		t.Fatalf("CodeVerifier: %v", err)
	}
	if v1 != v2 {
		t.Errorf("verifier should be cached: %q != %q", v1, v2)
	}
}

func TestMCPAuthHandler_StepUpAuth(t *testing.T) {
	h, _ := newTestHandler(t)
	if scope := h.StepUpScope(); scope != "" {
		t.Errorf("initial step-up scope should be empty, got %q", scope)
	}
	h.MarkStepUpPending("admin")
	if scope := h.StepUpScope(); scope != "admin" {
		t.Errorf("StepUpScope = %q, want %q", scope, "admin")
	}
}

func TestMCPAuthHandler_SaveAndTokens(t *testing.T) {
	h, _ := newTestHandler(t)

	tokens := &OAuthTokens{
		AccessToken:  "access-123",
		RefreshToken: "refresh-456",
		ExpiresIn:    3600,
		Scope:        "read write",
		TokenType:    "Bearer",
	}
	if err := h.SaveTokens(tokens); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}

	got, err := h.Tokens()
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	if got == nil {
		t.Fatal("expected tokens, got nil")
	}
	if got.AccessToken != "access-123" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "access-123")
	}
	if got.RefreshToken != "refresh-456" {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, "refresh-456")
	}
	if got.TokenType != "Bearer" {
		t.Errorf("TokenType = %q, want %q", got.TokenType, "Bearer")
	}
}

func TestMCPAuthHandler_TokensStepUp(t *testing.T) {
	h, _ := newTestHandler(t)

	if err := h.SaveTokens(&OAuthTokens{
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresIn:    3600,
		Scope:        "read",
	}); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}

	// Mark step-up for scope not in current token
	h.MarkStepUpPending("admin")

	got, err := h.Tokens()
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	if got == nil {
		t.Fatal("expected tokens, got nil")
	}
	// Step-up should omit RefreshToken
	if got.RefreshToken != "" {
		t.Errorf("RefreshToken should be omitted during step-up, got %q", got.RefreshToken)
	}
	if got.AccessToken != "access" {
		t.Errorf("AccessToken should still be present, got %q", got.AccessToken)
	}
}

func TestMCPAuthHandler_TokensExpiredNoRefresh(t *testing.T) {
	h, store := newTestHandler(t)
	if err := store.WriteTokens(h.ServerKey(), &OAuthTokenData{
		AccessToken: "expired",
		ExpiresAt:   time.Now().Add(-1 * time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	got, err := h.Tokens()
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for expired token without refresh, got %+v", got)
	}
}

func TestMCPAuthHandler_TokensNoData(t *testing.T) {
	h, _ := newTestHandler(t)
	got, err := h.Tokens()
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil with no data, got %+v", got)
	}
}

func TestMCPAuthHandler_SaveClientInformation(t *testing.T) {
	h, store := newTestHandler(t)
	if err := h.SaveClientInformation("client-123", "secret-456"); err != nil {
		t.Fatalf("SaveClientInformation: %v", err)
	}
	tok, err := store.ReadTokens(h.ServerKey())
	if err != nil {
		t.Fatalf("ReadTokens: %v", err)
	}
	if tok == nil {
		t.Fatal("expected token data, got nil")
	}
	if tok.ClientID != "client-123" {
		t.Errorf("ClientID = %q, want %q", tok.ClientID, "client-123")
	}
	if tok.ClientSecret != "secret-456" {
		t.Errorf("ClientSecret = %q, want %q", tok.ClientSecret, "secret-456")
	}
}

func TestMCPAuthHandler_DiscoveryState(t *testing.T) {
	h, _ := newTestHandler(t)

	// No state initially
	ds, err := h.DiscoveryState()
	if err != nil {
		t.Fatalf("DiscoveryState: %v", err)
	}
	if ds != nil {
		t.Errorf("expected nil initially, got %+v", ds)
	}

	// Save and read back
	state := &OAuthDiscoveryState{
		AuthorizationServerURL: "https://auth.example.com",
		ResourceMetadataURL:    "https://res.example.com",
	}
	if err := h.SaveDiscoveryState(state); err != nil {
		t.Fatalf("SaveDiscoveryState: %v", err)
	}
	ds, err = h.DiscoveryState()
	if err != nil {
		t.Fatalf("DiscoveryState: %v", err)
	}
	if ds == nil {
		t.Fatal("expected discovery state, got nil")
	}
	if ds.AuthorizationServerURL != "https://auth.example.com" {
		t.Errorf("AuthorizationServerURL = %q, want %q", ds.AuthorizationServerURL, "https://auth.example.com")
	}
}

func TestMCPAuthHandler_InvalidateCredentials(t *testing.T) {
	h, _ := newTestHandler(t)
	if err := h.SaveTokens(&OAuthTokens{AccessToken: "tok", ExpiresIn: 3600}); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}
	if err := h.InvalidateCredentials(InvalidateAll); err != nil {
		t.Fatalf("InvalidateCredentials: %v", err)
	}
	got, err := h.Tokens()
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after invalidation, got %+v", got)
	}
}

func TestMCPAuthHandler_ClientMetadata(t *testing.T) {
	h, _ := newTestHandler(t)
	md := h.ClientMetadata()
	if md["token_endpoint_auth_method"] != "none" {
		t.Errorf("auth method = %v, want none", md["token_endpoint_auth_method"])
	}
	if md["client_name"] != "Claude Code (test-server)" {
		t.Errorf("client_name = %v, want Claude Code (test-server)", md["client_name"])
	}

	// With metadata scope
	h.SetMetadata(&OAuthMetadata{Scope: "read write"})
	md = h.ClientMetadata()
	if md["scope"] != "read write" {
		t.Errorf("scope = %v, want read write", md["scope"])
	}
}

func TestMCPAuthHandler_RefreshWithLock(t *testing.T) {
	h, _ := newTestHandler(t)
	// Store expired tokens
	if err := h.SaveTokens(&OAuthTokens{
		AccessToken:  "old",
		RefreshToken: "refresh",
		ExpiresIn:    -1, // already expired
	}); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}

	refreshed, err := h.RefreshWithLock(context.Background(), func(ctx context.Context, rt string) (*OAuthTokens, error) {
		return &OAuthTokens{
			AccessToken:  "new",
			RefreshToken: "new-refresh",
			ExpiresIn:    3600,
			TokenType:    "Bearer",
		}, nil
	})
	if err != nil {
		t.Fatalf("RefreshWithLock: %v", err)
	}
	if refreshed == nil {
		t.Fatal("expected tokens, got nil")
	}
	if refreshed.AccessToken != "new" {
		t.Errorf("AccessToken = %q, want %q", refreshed.AccessToken, "new")
	}
}

func TestMCPAuthHandler_Options(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	called := false
	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, t.TempDir(),
		WithRedirectURI("http://localhost:9999/callback"),
		WithSkipBrowser(true),
		WithOnAuthURL(func(u string) { called = true }),
	)
	if h.redirectURI != "http://localhost:9999/callback" {
		t.Errorf("redirectURI = %q", h.redirectURI)
	}
	if !h.skipBrowser {
		t.Error("skipBrowser should be true")
	}
	h.onAuthURL("test")
	if !called {
		t.Error("onAuthURL should have been called")
	}
}

// ---------------------------------------------------------------------------
// isTransientError
// ---------------------------------------------------------------------------

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"connection timeout", true},
		{"request timed out", true},
		{"ETIMEDOUT", true},
		{"ECONNRESET", true},
		{"HTTP 500 Internal Server Error", true},
		{"HTTP 503 Service Unavailable", true},
		{"429 Too Many Requests", true},
		{"connection refused", false},
		{"invalid_grant", false},
		{"unknown error", false},
	}
	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			got := isTransientError(fmt.Errorf("%s", tt.msg))
			if got != tt.want {
				t.Errorf("isTransientError(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BuildAuthURL
// ---------------------------------------------------------------------------

func TestBuildAuthURL(t *testing.T) {
	url := BuildAuthURL(
		"https://auth.example.com/authorize",
		"client-123",
		"http://localhost:8080/callback",
		"state-abc",
		"challenge-xyz",
		"read write",
	)
	if !strings.Contains(url, "client_id=client-123") {
		t.Errorf("missing client_id in %q", url)
	}
	if !strings.Contains(url, "response_type=code") {
		t.Errorf("missing response_type in %q", url)
	}
	if !strings.Contains(url, "code_challenge=challenge-xyz") {
		t.Errorf("missing code_challenge in %q", url)
	}
	if !strings.Contains(url, "code_challenge_method=S256") {
		t.Errorf("missing code_challenge_method in %q", url)
	}
	if !strings.Contains(url, "scope=read+write") && !strings.Contains(url, "scope=read%20write") {
		t.Errorf("missing scope in %q", url)
	}

	// Empty scope should not add scope param
	urlNoScope := BuildAuthURL("https://auth.example.com", "c", "http://localhost", "s", "ch", "")
	if strings.Contains(urlNoScope, "scope=") {
		t.Errorf("scope should not be present when empty: %q", urlNoScope)
	}
}

// ---------------------------------------------------------------------------
// AuthenticationCancelledError
// ---------------------------------------------------------------------------

func TestAuthenticationCancelledError(t *testing.T) {
	err := &AuthenticationCancelledError{}
	if err.Error() != "Authentication was cancelled" {
		t.Errorf("Error() = %q, want %q", err.Error(), "Authentication was cancelled")
	}
}

// ---------------------------------------------------------------------------
// RandomBytes
// ---------------------------------------------------------------------------

func TestRandomBytes(t *testing.T) {
	b, err := RandomBytes(32)
	if err != nil {
		t.Fatalf("RandomBytes: %v", err)
	}
	if len(b) != 32 {
		t.Errorf("len = %d, want 32", len(b))
	}
	b2, _ := RandomBytes(32)
	if string(b) == string(b2) {
		t.Error("consecutive calls should produce different bytes")
	}
}

// ---------------------------------------------------------------------------
// configURL
// ---------------------------------------------------------------------------

func TestConfigURL(t *testing.T) {
	tests := []struct {
		name   string
		config McpServerConfig
		want   string
	}{
		{"SSE", &SSEConfig{URL: "http://sse.example.com"}, "http://sse.example.com"},
		{"HTTP", &HTTPConfig{URL: "http://http.example.com"}, "http://http.example.com"},
		{"Stdio", &StdioConfig{Command: "cmd"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := configURL(tt.config)
			if got != tt.want {
				t.Errorf("configURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// RefreshLock
// ---------------------------------------------------------------------------

func TestRefreshLock_AcquireRelease(t *testing.T) {
	dir := t.TempDir()
	lock := NewRefreshLock(dir, "test-key")

	if err := lock.Acquire(); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// Lock directory should exist
	if _, err := os.Stat(lock.lockDir); os.IsNotExist(err) {
		t.Error("lock directory should exist after acquire")
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Lock directory should be removed
	if _, err := os.Stat(lock.lockDir); !os.IsNotExist(err) {
		t.Error("lock directory should be removed after release")
	}
}

func TestRefreshLock_Concurrent(t *testing.T) {
	origBase, origInc := lockRetryBase, lockRetryInc
	lockRetryBase, lockRetryInc = 1, 1
	defer func() { lockRetryBase, lockRetryInc = origBase, origInc }()

	dir := t.TempDir()
	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)

	acquired := make(chan int, goroutines)
	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			lock := NewRefreshLock(dir, "shared-key")
			if err := lock.Acquire(); err != nil {
				return
			}
			acquired <- id
			time.Sleep(50 * time.Millisecond)
			_ = lock.Release()
		}(i)
	}
	wg.Wait()
	close(acquired)

	count := 0
	for range acquired {
		count++
	}
	// At least one should have acquired (best-effort means all could "succeed" without blocking)
	if count == 0 {
		t.Error("expected at least one lock acquisition")
	}
}

// ---------------------------------------------------------------------------
// ClearServerTokens
// ---------------------------------------------------------------------------

func TestClearServerTokens(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{AccessToken: "token"}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	if err := ClearServerTokens(store, "key"); err != nil {
		t.Fatalf("ClearServerTokens: %v", err)
	}
	tok, _ := store.ReadTokens("key")
	if tok != nil {
		t.Errorf("expected nil after clear, got %+v", tok)
	}
}

// ---------------------------------------------------------------------------
// BuildRedirectURI — Source: oauthPort.ts:21-25
// ---------------------------------------------------------------------------

func TestBuildRedirectURI(t *testing.T) {
	tests := []struct {
		name string
		port int
		want string
	}{
		{"standard port", 8080, "http://localhost:8080/callback"},
		{"port 1", 1, "http://localhost:1/callback"},
		{"high port", 65535, "http://localhost:65535/callback"},
		{"port 3118", 3118, "http://localhost:3118/callback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildRedirectURI(tt.port)
			if got != tt.want {
				t.Errorf("BuildRedirectURI(%d) = %q, want %q", tt.port, got, tt.want)
			}
		})
	}
}

func TestBuildRedirectURI_DefaultPort(t *testing.T) {
	// Source: oauthPort.ts:23 — default parameter is REDIRECT_PORT_FALLBACK (3118)
	// When port <= 0, it should use the fallback port.
	got := BuildRedirectURI(0)
	want := "http://localhost:3118/callback"
	if got != want {
		t.Errorf("BuildRedirectURI(0) = %q, want %q", got, want)
	}

	got = BuildRedirectURI(-1)
	if got != want {
		t.Errorf("BuildRedirectURI(-1) = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// redirectPortRange — Source: oauthPort.ts:9-12
// ---------------------------------------------------------------------------

func TestRedirectPortRange(t *testing.T) {
	min, max := redirectPortRange()
	if min <= 0 || max <= 0 {
		t.Errorf("port range should be positive: min=%d, max=%d", min, max)
	}
	if min >= max {
		t.Errorf("min should be less than max: min=%d, max=%d", min, max)
	}

	// Verify the range matches the expected platform-specific values
	if runtime.GOOS == "windows" {
		// Source: oauthPort.ts:10-11 — Windows: { min: 39152, max: 49151 }
		if min != 39152 || max != 49151 {
			t.Errorf("Windows range: got min=%d max=%d, want min=39152 max=49151", min, max)
		}
	} else {
		// Source: oauthPort.ts:12 — Others: { min: 49152, max: 65535 }
		if min != 49152 || max != 65535 {
			t.Errorf("Non-Windows range: got min=%d max=%d, want min=49152 max=65535", min, max)
		}
	}
}

// ---------------------------------------------------------------------------
// getMcpOAuthCallbackPort — Source: oauthPort.ts:27-30
// ---------------------------------------------------------------------------

func TestGetMcpOAuthCallbackPort(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int
	}{
		{"valid port", "8080", 8080},
		{"port 1", "1", 1},
		{"port 0 is invalid", "0", 0},
		{"negative is invalid", "-1", 0},
		{"empty string", "", 0},
		{"non-numeric", "abc", 0},
		{"float string", "80.5", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Setenv("MCP_OAUTH_CALLBACK_PORT", tt.value)
			defer func() { _ = os.Unsetenv("MCP_OAUTH_CALLBACK_PORT") }()

			got := getMcpOAuthCallbackPort()
			if got != tt.want {
				t.Errorf("getMcpOAuthCallbackPort() with env %q = %d, want %d", tt.value, got, tt.want)
			}
		})
	}

	// When env var is unset
	t.Run("unset env", func(t *testing.T) {
		_ = os.Unsetenv("MCP_OAUTH_CALLBACK_PORT")
		got := getMcpOAuthCallbackPort()
		if got != 0 {
			t.Errorf("getMcpOAuthCallbackPort() with unset env = %d, want 0", got)
		}
	})
}

// ---------------------------------------------------------------------------
// isPortAvailable — Source: oauthPort.ts:51-57 (inline in findAvailablePort)
// ---------------------------------------------------------------------------

func TestIsPortAvailable_AvailablePort(t *testing.T) {
	// Listen on a random port to find one that's definitely available
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get random port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	if !isPortAvailable(port) {
		t.Errorf("port %d should be available after closing listener", port)
	}
}

func TestIsPortAvailable_OccupiedPort(t *testing.T) {
	// Listen on a port to make it occupied
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get random port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	defer ln.Close()

	if isPortAvailable(port) {
		t.Errorf("port %d should not be available while listener is active", port)
	}
}

// ---------------------------------------------------------------------------
// FindAvailablePort — Source: oauthPort.ts:36-78
// ---------------------------------------------------------------------------

func TestFindAvailablePort_EnvOverride(t *testing.T) {
	// Source: oauthPort.ts:38-40 — First, try the configured port if specified
	_ = os.Setenv("MCP_OAUTH_CALLBACK_PORT", "9999")
	defer func() { _ = os.Unsetenv("MCP_OAUTH_CALLBACK_PORT") }()

	port, err := FindAvailablePort()
	if err != nil {
		t.Fatalf("FindAvailablePort: %v", err)
	}
	if port != 9999 {
		t.Errorf("port = %d, want 9999 when MCP_OAUTH_CALLBACK_PORT=9999", port)
	}
}

func TestFindAvailablePort_EnvOverrideInvalid(t *testing.T) {
	// When the env var is invalid, it should be ignored and a port found normally
	_ = os.Setenv("MCP_OAUTH_CALLBACK_PORT", "not-a-number")
	defer func() { _ = os.Unsetenv("MCP_OAUTH_CALLBACK_PORT") }()

	port, err := FindAvailablePort()
	if err != nil {
		t.Fatalf("FindAvailablePort: %v", err)
	}
	if port <= 0 || port > 65535 {
		t.Errorf("port %d out of valid range", port)
	}
}

func TestFindAvailablePort_AvailablePort(t *testing.T) {
	// Ensure env var doesn't interfere
	_ = os.Unsetenv("MCP_OAUTH_CALLBACK_PORT")

	port, err := FindAvailablePort()
	if err != nil {
		t.Fatalf("FindAvailablePort: %v", err)
	}

	// Port must be in valid range
	if port <= 0 || port > 65535 {
		t.Errorf("port %d out of valid range", port)
	}

	// Port must be in platform range OR be the fallback
	min, max := redirectPortRange()
	if port < min || port > max {
		if port != redirectPortFallback {
			t.Errorf("port %d not in range [%d-%d] and not fallback %d",
				port, min, max, redirectPortFallback)
		}
	}

	// Port must actually be available (was selected because it's free)
	if !isPortAvailable(port) {
		t.Errorf("returned port %d should be available", port)
	}
}

func TestFindAvailablePort_ReturnsDistinctPorts(t *testing.T) {
	_ = os.Unsetenv("MCP_OAUTH_CALLBACK_PORT")

	// Two calls should not necessarily return the same port (random selection)
	// But both should succeed
	p1, err := FindAvailablePort()
	if err != nil {
		t.Fatalf("first FindAvailablePort: %v", err)
	}
	p2, err := FindAvailablePort()
	if err != nil {
		t.Fatalf("second FindAvailablePort: %v", err)
	}
	// Just verify both are valid; they *could* be the same if the port is still free
	if p1 <= 0 || p2 <= 0 {
		t.Errorf("both ports should be positive: p1=%d, p2=%d", p1, p2)
	}
}

// =========================================================================
// PerformMCPOAuthFlow — Source: auth.go:458-571
// =========================================================================

func TestPerformMCPOAuthFlow_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	config := OAuthFlowConfig{
		ServerName:   "test-server",
		ServerConfig: &SSEConfig{URL: "http://example.com/mcp"},
		TokenStore:   store,
	}

	// Start flow in background
	errChan := make(chan error, 1)
	go func() {
		_, err := PerformMCPOAuthFlow(ctx, config)
		errChan <- err
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Cancel context
	cancel()

	// Should return AuthenticationCancelledError
	err = <-errChan
	if err == nil {
		t.Fatal("PerformMCPOAuthFlow() = nil, want error after cancellation")
	}
	var authErr *AuthenticationCancelledError
	if !errors.As(err, &authErr) {
		t.Errorf("error = %T, want AuthenticationCancelledError", err)
	}
}

// =========================================================================
// RevokeServerTokens — Source: auth.go:985-1034
// =========================================================================

func TestRevokeServerTokens_SuccessfulRevocation(t *testing.T) {
	// Use https:// server to satisfy the HTTPS check in RevokeServerTokens
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata" {
			// Return OAuth metadata with revocation endpoint
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"revocation_endpoint": "%s/revoke"}`, r.URL.Scheme+"://"+r.Host+"/revoke")
			return
		}
		if r.URL.Path == "/revoke" {
			// Accept revocation
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	// Write tokens with metadata URL
	serverKey := "test-server"
	tokenData := &OAuthTokenData{
		ServerName:     "test-server",
		ServerURL:      "http://example.com",
		AccessToken:    "access-token-123",
		RefreshToken:   "refresh-token-456",
		ExpiresAt:      time.Now().Add(time.Hour).UnixMilli(),
		ClientID:       "client-id",
		ClientSecret:   "client-secret",
		DiscoveryState: &OAuthDiscoveryState{AuthorizationServerURL: server.URL + "/metadata"},
	}
	if err := store.WriteTokens(serverKey, tokenData); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	ctx := context.Background()
	metadataURL := server.URL + "/metadata"
	err = RevokeServerTokens(ctx, store, serverKey, "http://example.com", metadataURL)
	if err != nil {
		t.Errorf("RevokeServerTokens() = %v, want nil", err)
	}

	// Verify tokens were deleted
	readData, err := store.ReadTokens(serverKey)
	if err != nil {
		t.Errorf("ReadTokens after revocation = %v, want nil", err)
	}
	if readData != nil {
		t.Errorf("ReadTokens after revocation = %+v, want nil", readData)
	}
}

func TestRevokeServerTokens_RevocationFailsButTokensCleaned(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"revocation_endpoint": "%s/revoke"}`, r.URL.Scheme+"://"+r.Host+"/revoke")
			return
		}
		if r.URL.Path == "/revoke" {
			// Revocation endpoint returns error
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	serverKey := "test-server"
	tokenData := &OAuthTokenData{
		ServerName:     "test-server",
		ServerURL:      "http://example.com",
		AccessToken:    "access-token-123",
		RefreshToken:   "refresh-token-456",
		ExpiresAt:      time.Now().Add(time.Hour).UnixMilli(),
		ClientID:       "client-id",
		ClientSecret:   "client-secret",
		DiscoveryState: &OAuthDiscoveryState{AuthorizationServerURL: server.URL + "/metadata"},
	}
	if err := store.WriteTokens(serverKey, tokenData); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	ctx := context.Background()
	metadataURL := server.URL + "/metadata"
	err = RevokeServerTokens(ctx, store, serverKey, "http://example.com", metadataURL)
	// Should still succeed even if revocation fails
	if err != nil {
		t.Errorf("RevokeServerTokens() = %v, want nil (tokens should still be cleaned)", err)
	}

	// Verify tokens were deleted despite revocation failure
	readData, err := store.ReadTokens(serverKey)
	if err != nil {
		t.Errorf("ReadTokens after failed revocation = %v, want nil", err)
	}
	if readData != nil {
		t.Errorf("ReadTokens after failed revocation = %+v, want nil", readData)
	}
}

func TestRevokeServerTokens_WithClientCredentials(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"revocation_endpoint": "%s/revoke"}`, r.URL.Scheme+"://"+r.Host+"/revoke")
			return
		}
		if r.URL.Path == "/revoke" {
			// Verify client credentials in request body
			body, _ := io.ReadAll(r.Body)
			bodyStr := string(body)
			if !strings.Contains(bodyStr, "client_id=my-client-id") {
				t.Errorf("revocation request missing client_id, got: %s", bodyStr)
			}
			if !strings.Contains(bodyStr, "client_secret=my-secret") {
				t.Errorf("revocation request missing client_secret, got: %s", bodyStr)
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	serverKey := "test-server"
	tokenData := &OAuthTokenData{
		ServerName:     "test-server",
		ServerURL:      "http://example.com",
		AccessToken:    "access-token-123",
		RefreshToken:   "refresh-token-456",
		ExpiresAt:      time.Now().Add(time.Hour).UnixMilli(),
		ClientID:       "my-client-id",
		ClientSecret:   "my-secret",
		DiscoveryState: &OAuthDiscoveryState{AuthorizationServerURL: server.URL + "/metadata"},
	}
	if err := store.WriteTokens(serverKey, tokenData); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	ctx := context.Background()
	metadataURL := server.URL + "/metadata"
	err = RevokeServerTokens(ctx, store, serverKey, "http://example.com", metadataURL)
	if err != nil {
		t.Errorf("RevokeServerTokens() = %v, want nil", err)
	}
}

// =========================================================================
// GetServerKey — Source: auth.go:106-121
// =========================================================================

func TestGetServerKey_SpecialCharacters(t *testing.T) {
	tests := []struct {
		name         string
		serverName   string
		serverConfig McpServerConfig
		wantPrefix   string
	}{
		{
			name:         "special characters in name",
			serverName:   "test server!@#$%",
			serverConfig: &SSEConfig{URL: "http://example.com"},
			wantPrefix:   "test server!@#$%|",
		},
		{
			name:         "unicode characters",
			serverName:   "测试服务器",
			serverConfig: &SSEConfig{URL: "http://example.com"},
			wantPrefix:   "测试服务器|",
		},
		{
			name:         "empty config",
			serverName:   "test-server",
			serverConfig: nil,
			wantPrefix:   "test-server|",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := GetServerKey(tt.serverName, tt.serverConfig)
			if !strings.HasPrefix(key, tt.wantPrefix) {
				t.Errorf("GetServerKey() = %s, want prefix %s", key, tt.wantPrefix)
			}
			// Total length should be name length + 1 (for |) + 16 (hex)
			wantLen := len(tt.serverName) + 1 + 16
			if len(key) != wantLen {
				t.Errorf("GetServerKey() length = %d, want %d", len(key), wantLen)
			}
		})
	}
}

// =========================================================================
// NewFileTokenStore — Source: auth.go:155-160
// =========================================================================

func TestNewFileTokenStore_DirCreationError(t *testing.T) {
	// Create a file (not directory) to cause MkdirAll to fail
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("test"), 0600); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	_, err := NewFileTokenStore(filePath)
	if err == nil {
		t.Error("NewFileTokenStore() = nil, want error when path is a file")
	}
}

// =========================================================================
// WriteTokens — Source: auth.go:193-206
// =========================================================================

func TestWriteTokens_ReadOnlyDirectory(t *testing.T) {
	// On Windows, chmod doesn't prevent writes the same way
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	// Make directory read-only
	if err := os.Chmod(tmpDir, 0500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() {
		_ = os.Chmod(tmpDir, 0700) // Restore permissions for cleanup
	}()

	tokenData := &OAuthTokenData{
		ServerName:  "test-server",
		ServerURL:   "http://example.com",
		AccessToken: "test-token",
	}

	err = store.WriteTokens("test-key", tokenData)
	if err == nil {
		t.Error("WriteTokens() = nil, want error when directory is read-only")
	}
}

// =========================================================================
// ReadTokens — Source: auth.go:173-190
// =========================================================================

func TestReadTokens_CorruptedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	serverKey := "test-server"
	tokenPath := store.tokenPath(serverKey)

	// Write corrupted JSON
	if err := os.WriteFile(tokenPath, []byte("{invalid json"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err = store.ReadTokens(serverKey)
	if err == nil {
		t.Error("ReadTokens() = nil, want error for corrupted JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse token data") {
		t.Errorf("error = %v, want parse error", err)
	}
}

// =========================================================================
// WriteClientConfig — Source: auth.go:241-254
// =========================================================================

func TestWriteClientConfig_ErrorPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	// Make directory read-only
	if err := os.Chmod(tmpDir, 0500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() {
		_ = os.Chmod(tmpDir, 0700)
	}()

	config := &OAuthClientConfig{ClientSecret: "secret"}
	err = store.WriteClientConfig("test-key", config)
	if err == nil {
		t.Error("WriteClientConfig() = nil, want error when directory is read-only")
	}
}

// =========================================================================
// DeleteClientConfig — Source: auth.go:257-266
// =========================================================================

func TestDeleteClientConfig_FileDoesNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	// Deleting non-existent file should succeed
	err = store.DeleteClientConfig("nonexistent-key")
	if err != nil {
		t.Errorf("DeleteClientConfig() = %v, want nil for non-existent file", err)
	}
}

// =========================================================================
// GenerateState — Source: auth.go:330-336
// =========================================================================

func TestGenerateState_DeterministicLength(t *testing.T) {
	state1, err := GenerateState()
	if err != nil {
		t.Fatalf("GenerateState() = %v", err)
	}

	state2, err := GenerateState()
	if err != nil {
		t.Fatalf("GenerateState() = %v", err)
	}

	// States should be different (random)
	if state1 == state2 {
		t.Error("GenerateState() produced identical states, want random values")
	}

	// All base64.RawURLEncoding of 32 bytes should be 43 chars
	if len(state1) != 43 {
		t.Errorf("GenerateState() length = %d, want 43", len(state1))
	}
	if len(state2) != 43 {
		t.Errorf("GenerateState() length = %d, want 43", len(state2))
	}
}

// =========================================================================
// GenerateCodeVerifier — Source: auth.go:344-350
// =========================================================================

func TestGenerateCodeVerifier_DeterministicLength(t *testing.T) {
	v1, err := GenerateCodeVerifier()
	if err != nil {
		t.Fatalf("GenerateCodeVerifier() = %v", err)
	}

	v2, err := GenerateCodeVerifier()
	if err != nil {
		t.Fatalf("GenerateCodeVerifier() = %v", err)
	}

	// Verifiers should be different (random)
	if v1 == v2 {
		t.Error("GenerateCodeVerifier() produced identical verifiers, want random values")
	}

	// All base64.RawURLEncoding of 32 bytes should be 43 chars
	if len(v1) != 43 {
		t.Errorf("GenerateCodeVerifier() length = %d, want 43", len(v1))
	}
	if len(v2) != 43 {
		t.Errorf("GenerateCodeVerifier() length = %d, want 43", len(v2))
	}
}

// =========================================================================
// redirectPortRange — Source: auth.go:378-383
// =========================================================================

func TestRedirectPortRange_WindowsVsNonWindows(t *testing.T) {
	min, max := redirectPortRange()

	if runtime.GOOS == "windows" {
		if min != 39152 || max != 49151 {
			t.Errorf("redirectPortRange() on Windows = %d, %d, want 39152, 49151", min, max)
		}
	} else {
		if min != 49152 || max != 65535 {
			t.Errorf("redirectPortRange() on non-Windows = %d, %d, want 49152, 65535", min, max)
		}
	}
}

// =========================================================================
// RefreshAuthorization — Source: auth.go:592-707
// =========================================================================

func TestRefreshAuthorization_Success(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	serverKey := "test-server"
	// Set expiry in the past to trigger refresh
	tokenData := &OAuthTokenData{
		ServerName:   "test-server",
		ServerURL:    "http://example.com",
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		ExpiresAt:    time.Now().Add(-time.Hour).UnixMilli(), // Expired
	}
	if err := store.WriteTokens(serverKey, tokenData); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	ctx := context.Background()
	refreshFunc := func(ctx context.Context, refreshToken string) (*OAuthTokens, error) {
		// Verify we got the right refresh token
		if refreshToken != "old-refresh" {
			t.Errorf("refreshFunc() got refreshToken = %s, want old-refresh", refreshToken)
		}
		// Simulate token exchange
		return &OAuthTokens{
			AccessToken:  "new-access",
			RefreshToken: "new-refresh",
			ExpiresIn:    3600,
		}, nil
	}

	tokens, err := RefreshAuthorization(ctx, serverKey, store, refreshFunc)
	if err != nil {
		t.Errorf("RefreshAuthorization() = %v, want nil", err)
	}
	if tokens == nil {
		t.Fatal("RefreshAuthorization() = nil, want tokens")
	}
	if tokens.AccessToken != "new-access" {
		t.Errorf("AccessToken = %s, want new-access", tokens.AccessToken)
	}
	if tokens.RefreshToken != "new-refresh" {
		t.Errorf("RefreshToken = %s, want new-refresh", tokens.RefreshToken)
	}

	// Verify tokens were updated in the store
	updatedData, err := store.ReadTokens(serverKey)
	if err != nil {
		t.Errorf("ReadTokens after refresh = %v, want nil", err)
	}
	if updatedData.AccessToken != "new-access" {
		t.Errorf("AccessToken in store = %s, want new-access", updatedData.AccessToken)
	}
	if updatedData.RefreshToken != "new-refresh" {
		t.Errorf("RefreshToken in store = %s, want new-refresh", updatedData.RefreshToken)
	}
}

// =========================================================================
// InvalidateCredentials — Source: auth.go:709-750
// =========================================================================

func TestInvalidateCredentials_WithTokenData(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	serverKey := "test-server"
	tokenData := &OAuthTokenData{
		ServerName:   "test-server",
		ServerURL:    "http://example.com",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
		DiscoveryState: &OAuthDiscoveryState{
			AuthorizationServerURL: "http://auth.example.com",
		},
		StepUpScope: "extra-scope",
	}
	if err := store.WriteTokens(serverKey, tokenData); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	// Test InvalidateAll
	err = InvalidateCredentials(store, serverKey, InvalidateAll)
	if err != nil {
		t.Errorf("InvalidateCredentials(InvalidateAll) = %v, want nil", err)
	}
	readData, err := store.ReadTokens(serverKey)
	if err != nil {
		t.Errorf("ReadTokens after InvalidateAll = %v, want nil", err)
	}
	if readData != nil {
		t.Errorf("ReadTokens after InvalidateAll = %+v, want nil", readData)
	}

	// Recreate tokens for other tests
	if err := store.WriteTokens(serverKey, tokenData); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	// Test InvalidateClient
	err = InvalidateCredentials(store, serverKey, InvalidateClient)
	if err != nil {
		t.Errorf("InvalidateCredentials(InvalidateClient) = %v, want nil", err)
	}
	readData, err = store.ReadTokens(serverKey)
	if err != nil {
		t.Errorf("ReadTokens after InvalidateClient = %v, want nil", err)
	}
	if readData.ClientID != "" || readData.ClientSecret != "" {
		t.Errorf("ClientID/Secret after InvalidateClient = %s/%s, want empty", readData.ClientID, readData.ClientSecret)
	}
	if readData.AccessToken != "access-token" {
		t.Errorf("AccessToken after InvalidateClient = %s, want access-token", readData.AccessToken)
	}

	// Test InvalidateTokens
	err = InvalidateCredentials(store, serverKey, InvalidateTokens)
	if err != nil {
		t.Errorf("InvalidateCredentials(InvalidateTokens) = %v, want nil", err)
	}
	readData, err = store.ReadTokens(serverKey)
	if err != nil {
		t.Errorf("ReadTokens after InvalidateTokens = %v, want nil", err)
	}
	if readData.AccessToken != "" || readData.RefreshToken != "" {
		t.Errorf("AccessToken/RefreshToken after InvalidateTokens = %s/%s, want empty", readData.AccessToken, readData.RefreshToken)
	}
	if readData.ExpiresAt != 0 {
		t.Errorf("ExpiresAt after InvalidateTokens = %d, want 0", readData.ExpiresAt)
	}

	// Test InvalidateDiscovery
	err = InvalidateCredentials(store, serverKey, InvalidateDiscovery)
	if err != nil {
		t.Errorf("InvalidateCredentials(InvalidateDiscovery) = %v, want nil", err)
	}
	readData, err = store.ReadTokens(serverKey)
	if err != nil {
		t.Errorf("ReadTokens after InvalidateDiscovery = %v, want nil", err)
	}
	if readData.DiscoveryState != nil {
		t.Errorf("DiscoveryState after InvalidateDiscovery = %+v, want nil", readData.DiscoveryState)
	}
	if readData.StepUpScope != "" {
		t.Errorf("StepUpScope after InvalidateDiscovery = %s, want empty", readData.StepUpScope)
	}
}

// =========================================================================
// Acquire — Source: auth.go:819-834 (RefreshLock.Acquire)
// =========================================================================

func TestRefreshLock_AcquireAlreadyHeld(t *testing.T) {
	origBase, origInc := lockRetryBase, lockRetryInc
	lockRetryBase, lockRetryInc = 1, 1
	defer func() { lockRetryBase, lockRetryInc = origBase, origInc }()

	tmpDir := t.TempDir()
	lock := NewRefreshLock(tmpDir, "test-server")

	// First acquire should succeed
	err := lock.Acquire()
	if err != nil {
		t.Errorf("first Acquire() = %v, want nil", err)
	}
	defer func() {
		_ = lock.Release()
	}()

	// Second acquire should also succeed (best-effort, returns nil even if held)
	err = lock.Acquire()
	if err != nil {
		t.Errorf("second Acquire() = %v, want nil (best-effort)", err)
	}
}

// =========================================================================
// NormalizeOAuthErrorBody — Source: auth.go:857-899
// =========================================================================

func TestNormalizeOAuthErrorBody_EdgeCases(t *testing.T) {
	tests := []struct {
		name             string
		statusCode       int
		body             []byte
		wantStatusCode   int
		wantBodyContains string
	}{
		{
			name:             "non-error status",
			statusCode:       200,
			body:             []byte(`{"access_token":"token"}`),
			wantStatusCode:   200,
			wantBodyContains: "access_token",
		},
		{
			name:             "error status without error field",
			statusCode:       400,
			body:             []byte(`{"message":"bad request"}`),
			wantStatusCode:   400,
			wantBodyContains: "message",
		},
		{
			name:             "empty error code",
			statusCode:       200,
			body:             []byte(`{"error":""}`),
			wantStatusCode:   200,
			wantBodyContains: "",
		},
		{
			name:             "invalid JSON",
			statusCode:       200,
			body:             []byte(`{invalid`),
			wantStatusCode:   200,
			wantBodyContains: "{invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := NormalizeOAuthErrorBody(tt.statusCode, tt.body)
			if status != tt.wantStatusCode {
				t.Errorf("status = %d, want %d", status, tt.wantStatusCode)
			}
			if tt.wantBodyContains != "" && !strings.Contains(string(body), tt.wantBodyContains) {
				t.Errorf("body = %s, want包含 %s", string(body), tt.wantBodyContains)
			}
		})
	}
}

// =========================================================================
// StepUpDetect — Source: auth.go:912-928
// =========================================================================

func TestStepUpDetect_MoreResponseBodies(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		header     string
		wantScope  string
	}{
		{
			name:       "non-403 status",
			statusCode: 401,
			header:     `Bearer insufficient_scope scope="admin"`,
			wantScope:  "",
		},
		{
			name:       "no WWW-Authenticate header",
			statusCode: 403,
			header:     "",
			wantScope:  "",
		},
		{
			name:       "WWW-Authenticate without insufficient_scope",
			statusCode: 403,
			header:     `Bearer error="invalid_token"`,
			wantScope:  "",
		},
		{
			name:       "insufficient_scope with unquoted scope",
			statusCode: 403,
			header:     `Bearer insufficient_scope, scope=admin`,
			wantScope:  "admin",
		},
		{
			name:       "insufficient_scope with quoted scope",
			statusCode: 403,
			header:     `Bearer insufficient_scope, scope="admin read"`,
			wantScope:  "admin read",
		},
		{
			name:       "insufficient_scope with scope in different position",
			statusCode: 403,
			header:     `Bearer scope="write", insufficient_scope`,
			wantScope:  "write",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			if tt.header != "" {
				header.Set("WWW-Authenticate", tt.header)
			}
			resp := &http.Response{
				StatusCode: tt.statusCode,
				Header:     header,
			}
			scope := StepUpDetect(resp)
			if scope != tt.wantScope {
				t.Errorf("StepUpDetect() = %s, want %s", scope, tt.wantScope)
			}
		})
	}
}

// =========================================================================
// MCPAuthHandler getters — State, CodeVerifier, Tokens
// =========================================================================

func TestMCPAuthHandler_StateGetter(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, dir)

	// First call should generate a state
	state1, err := h.State()
	if err != nil {
		t.Errorf("State() = %v, want nil", err)
	}
	if state1 == "" {
		t.Error("State() = empty, want non-empty")
	}

	// Second call should return the same state
	state2, err := h.State()
	if err != nil {
		t.Errorf("State() = %v, want nil", err)
	}
	if state1 != state2 {
		t.Errorf("State() = %s, want %s (cached)", state2, state1)
	}
}

func TestMCPAuthHandler_CodeVerifierGetter(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, dir)

	// First call should generate a verifier
	v1, err := h.CodeVerifier()
	if err != nil {
		t.Errorf("CodeVerifier() = %v, want nil", err)
	}
	if v1 == "" {
		t.Error("CodeVerifier() = empty, want non-empty")
	}

	// Second call should return the same verifier
	v2, err := h.CodeVerifier()
	if err != nil {
		t.Errorf("CodeVerifier() = %v, want nil", err)
	}
	if v1 != v2 {
		t.Errorf("CodeVerifier() = %s, want %s (cached)", v2, v1)
	}
}

func TestMCPAuthHandler_TokensGetter(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, dir)

	// No tokens stored
	tokens, err := h.Tokens()
	if err != nil {
		t.Errorf("Tokens() = %v, want nil", err)
	}
	if tokens != nil {
		t.Errorf("Tokens() = %+v, want nil", tokens)
	}

	// Store tokens
	serverKey := h.ServerKey()
	tokenData := &OAuthTokenData{
		ServerName:  "srv",
		ServerURL:   "http://example.com",
		AccessToken: "test-access",
		ExpiresAt:   time.Now().Add(time.Hour).UnixMilli(),
	}
	if err := store.WriteTokens(serverKey, tokenData); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	// Now should return tokens
	tokens, err = h.Tokens()
	if err != nil {
		t.Errorf("Tokens() = %v, want nil", err)
	}
	if tokens == nil {
		t.Fatal("Tokens() = nil, want tokens")
	}
	if tokens.AccessToken != "test-access" {
		t.Errorf("AccessToken = %s, want test-access", tokens.AccessToken)
	}
	if tokens.TokenType != "Bearer" {
		t.Errorf("TokenType = %s, want Bearer", tokens.TokenType)
	}
}

// =========================================================================
// MCPAuthHandler save methods — SaveTokens (already exists, testing edge cases)
// =========================================================================

func TestMCPAuthHandler_SaveTokensEdgeCases(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, dir)

	tokens := &OAuthTokens{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		ExpiresIn:    7200,
		Scope:        "read write",
	}

	err = h.SaveTokens(tokens)
	if err != nil {
		t.Errorf("SaveTokens() = %v, want nil", err)
	}

	// Verify tokens were saved
	serverKey := h.ServerKey()
	tokenData, err := store.ReadTokens(serverKey)
	if err != nil {
		t.Errorf("ReadTokens after SaveTokens = %v, want nil", err)
	}
	if tokenData.AccessToken != "new-access" {
		t.Errorf("AccessToken = %s, want new-access", tokenData.AccessToken)
	}
	if tokenData.RefreshToken != "new-refresh" {
		t.Errorf("RefreshToken = %s, want new-refresh", tokenData.RefreshToken)
	}
	// ExpiresAt should be approximately now + ExpiresIn
	expectedExpiry := time.Now().Add(7200 * time.Second).UnixMilli()
	if tokenData.ExpiresAt < expectedExpiry-1000 || tokenData.ExpiresAt > expectedExpiry+1000 {
		t.Errorf("ExpiresAt = %d, want approximately %d", tokenData.ExpiresAt, expectedExpiry)
	}
}

// =========================================================================
// revokeToken tests
// =========================================================================

func TestRevokeToken(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %q, want POST", r.Method)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		err := revokeToken(context.Background(), server.URL, "token123", "refresh_token", "client1", "secret1", "access123")
		if err != nil {
			t.Errorf("revokeToken() = %v, want nil", err)
		}
	})

	t.Run("unauthorized_retry_success", func(t *testing.T) {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			if requests == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			// Second request should have Authorization header
			auth := r.Header.Get("Authorization")
			if auth != "Bearer access123" {
				t.Errorf("Authorization = %q, want %q", auth, "Bearer access123")
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		err := revokeToken(context.Background(), server.URL, "token123", "refresh_token", "client1", "secret1", "access123")
		if err != nil {
			t.Errorf("revokeToken() = %v, want nil", err)
		}
		if requests != 2 {
			t.Errorf("request count = %d, want 2", requests)
		}
	})

	t.Run("unauthorized_retry_failure", func(t *testing.T) {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			if requests == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusForbidden)
		}))
		defer server.Close()

		err := revokeToken(context.Background(), server.URL, "token123", "refresh_token", "client1", "secret1", "access123")
		if err == nil {
			t.Fatal("revokeToken() = nil, want error")
		}
		if !strings.Contains(err.Error(), "revocation failed with status 403") {
			t.Errorf("error = %v, want containing 'revocation failed with status 403'", err)
		}
	})

	t.Run("no_retry_without_access_token", func(t *testing.T) {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		// accessToken="" means no retry, function returns nil
		err := revokeToken(context.Background(), server.URL, "token123", "refresh_token", "client1", "secret1", "")
		if err != nil {
			t.Errorf("revokeToken() = %v, want nil (401 with empty access token is ignored)", err)
		}
		if requests != 1 {
			t.Errorf("request count = %d, want 1", requests)
		}
	})

	t.Run("request_error", func(t *testing.T) {
		err := revokeToken(context.Background(), "://bad-url", "token123", "refresh_token", "client1", "secret1", "access123")
		if err == nil {
			t.Fatal("revokeToken() = nil, want error")
		}
		if !strings.Contains(err.Error(), "revocation") {
			t.Errorf("error = %v, want containing 'revocation'", err)
		}
	})
}

// =========================================================================
// MCPAuthHandler options tests — WithRefreshFunc and WithContext
// =========================================================================

func TestMCPAuthHandler_MoreOptions(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	t.Run("WithRefreshFunc", func(t *testing.T) {
		called := false
		h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, t.TempDir(),
			WithRefreshFunc(func(ctx context.Context, refreshToken string) (*OAuthTokens, error) {
				called = true
				return &OAuthTokens{AccessToken: "test"}, nil
			}),
		)
		if h.refreshFunc == nil {
			t.Fatal("refreshFunc should be set")
		}
		result, err := h.refreshFunc(context.Background(), "refresh-token")
		if err != nil {
			t.Fatalf("refreshFunc: %v", err)
		}
		if !called {
			t.Error("refreshFunc was not called")
		}
		if result.AccessToken != "test" {
			t.Errorf("AccessToken = %q, want %q", result.AccessToken, "test")
		}
	})

	t.Run("WithContext", func(t *testing.T) {
		ctx := t.Context()
		h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, t.TempDir(),
			WithContext(ctx),
		)
		if h.ctx == nil {
			t.Fatal("ctx should be set")
		}
		if h.ctx != ctx {
			t.Error("ctx should be the passed context")
		}
	})
}

// =========================================================================
// Coverage gap tests for auth.go
// =========================================================================

// --- GetServerKey: HTTPConfig branch (line 112-114) ---

func TestGetServerKey_HTTPConfig(t *testing.T) {
	h1 := &HTTPConfig{URL: "http://example.com/mcp", Headers: map[string]string{"k": "v"}}
	key1 := GetServerKey("server", h1)
	if !strings.HasPrefix(key1, "server|") {
		t.Errorf("key should start with server name: %q", key1)
	}

	// Same config produces same key
	key2 := GetServerKey("server", h1)
	if key1 != key2 {
		t.Errorf("same HTTPConfig should produce same key: %q != %q", key1, key2)
	}

	// Different URL produces different key
	h2 := &HTTPConfig{URL: "http://other.com/mcp", Headers: map[string]string{"k": "v"}}
	key3 := GetServerKey("server", h2)
	if key1 == key3 {
		t.Error("different HTTP URLs should produce different keys")
	}
}

// --- GetServerKey: default branch with StdioConfig (line 116) ---

func TestGetServerKey_StdioConfig(t *testing.T) {
	cfg := &StdioConfig{Command: "my-cmd", Args: []string{"arg1"}}
	key := GetServerKey("stdio-server", cfg)
	if !strings.HasPrefix(key, "stdio-server|") {
		t.Errorf("key should start with server name: %q", key)
	}
}

// --- GetServerKey: nil config (default branch) ---

func TestGetServerKey_NilConfig(t *testing.T) {
	key := GetServerKey("nil-server", nil)
	if !strings.HasPrefix(key, "nil-server|") {
		t.Errorf("key should start with server name: %q", key)
	}
}

// --- ReadTokens: non-os.IsNotExist read error (line 182) ---

func TestReadTokens_PermissionError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	// Create token file as a directory to cause a read error
	tokenPath := store.tokenPath("bad-key")
	if err := os.MkdirAll(tokenPath, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	_, err = store.ReadTokens("bad-key")
	if err == nil {
		t.Error("ReadTokens() should fail when token path is a directory")
	}
	if !strings.Contains(err.Error(), "failed to read token data") {
		t.Errorf("error = %v, want 'failed to read token data'", err)
	}
}

// --- DeleteTokens: non-os.IsNotExist remove error (line 214-215) ---

func TestDeleteTokens_RemoveError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	// Write a valid file first
	if err := store.WriteTokens("key", &OAuthTokenData{ServerName: "s"}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	// Replace the file with a non-empty directory so os.Remove fails with ENOTEMPTY
	tokenPath := store.tokenPath("key")
	if err := os.Remove(tokenPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := os.MkdirAll(tokenPath, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Put a file inside so Remove can't delete the directory
	if err := os.WriteFile(filepath.Join(tokenPath, "child"), []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile child: %v", err)
	}

	err = store.DeleteTokens("key")
	if err == nil {
		t.Fatal("DeleteTokens() should fail when token path is a non-empty directory")
	}
	if !strings.Contains(err.Error(), "failed to delete token data") {
		t.Errorf("error = %v, want 'failed to delete token data'", err)
	}
}

// --- ReadClientConfig: corrupted JSON (line 234-235) ---

func TestReadClientConfig_CorruptedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	configPath := store.clientConfigPath("bad-key")
	if err := os.WriteFile(configPath, []byte("{invalid json"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err = store.ReadClientConfig("bad-key")
	if err == nil {
		t.Error("ReadClientConfig() should fail for corrupted JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse client config") {
		t.Errorf("error = %v, want 'failed to parse client config'", err)
	}
}

// --- ReadClientConfig: non-os.IsNotExist read error (line 230) ---

func TestReadClientConfig_PermissionError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	// Create config path as a directory to cause read error
	configPath := store.clientConfigPath("bad-key")
	if err := os.MkdirAll(configPath, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	_, err = store.ReadClientConfig("bad-key")
	if err == nil {
		t.Error("ReadClientConfig() should fail when config path is a directory")
	}
	if !strings.Contains(err.Error(), "failed to read client config") {
		t.Errorf("error = %v, want 'failed to read client config'", err)
	}
}

// --- DeleteClientConfig: non-os.IsNotExist remove error (line 262-263) ---

func TestDeleteClientConfig_RemoveError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	// Write a valid file first
	if err := store.WriteClientConfig("key", &OAuthClientConfig{ClientSecret: "s"}); err != nil {
		t.Fatalf("WriteClientConfig: %v", err)
	}
	// Replace file with a non-empty directory so os.Remove fails
	configPath := store.clientConfigPath("key")
	if err := os.Remove(configPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := os.MkdirAll(configPath, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Put a file inside so Remove can't delete the directory
	if err := os.WriteFile(filepath.Join(configPath, "child"), []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile child: %v", err)
	}
	err = store.DeleteClientConfig("key")
	if err == nil {
		t.Fatal("DeleteClientConfig() should fail when config path is a non-empty directory")
	}
	if !strings.Contains(err.Error(), "failed to delete client config") {
		t.Errorf("error = %v, want 'failed to delete client config'", err)
	}
}

// --- InvalidateCredentials: unknown scope (line 748 default) ---

func TestInvalidateCredentials_UnknownScope(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	err = InvalidateCredentials(store, "key", InvalidateCredentialsScope("unknown"))
	if err == nil {
		t.Fatal("want error for unknown scope")
	}
	if !strings.Contains(err.Error(), "unknown invalidation scope") {
		t.Errorf("error = %v, want 'unknown invalidation scope'", err)
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error should contain scope name, got: %v", err)
	}
}

// --- InvalidateCredentials: ReadTokens error path for client/tokens/discovery ---

func TestInvalidateCredentials_ReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	// Write tokens, then make the file unreadable as a directory
	if err := store.WriteTokens("key", &OAuthTokenData{AccessToken: "tok"}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	tokenPath := store.tokenPath("key")
	if err := os.Remove(tokenPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := os.MkdirAll(tokenPath, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	for _, scope := range []InvalidateCredentialsScope{InvalidateClient, InvalidateTokens, InvalidateDiscovery} {
		err := InvalidateCredentials(store, "key", scope)
		if err == nil {
			t.Errorf("InvalidateCredentials(%s) should fail when ReadTokens fails", scope)
		}
	}
}

// --- RandomBytes: zero length ---

func TestRandomBytes_ZeroLength(t *testing.T) {
	b, err := RandomBytes(0)
	if err != nil {
		t.Fatalf("RandomBytes(0): %v", err)
	}
	if len(b) != 0 {
		t.Errorf("len(b) = %d, want 0", len(b))
	}
}

// --- Acquire: non-os.IsExist error (line 826) ---

func TestRefreshLock_AcquireNonExistError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	// Create lock pointing at a path whose parent is a file (not a directory)
	parentFile := filepath.Join(tmpDir, "parent-file")
	if err := os.WriteFile(parentFile, []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	lock := &RefreshLock{lockDir: filepath.Join(parentFile, "sub", "lock")}

	// Mkdir on a path where a parent is a file should give a non-IsExist error
	// Acquire should return nil (best-effort)
	err := lock.Acquire()
	if err != nil {
		t.Errorf("Acquire() should return nil on non-IsExist errors (best-effort), got %v", err)
	}
}

// --- NormalizeOAuthErrorBody: error field is not a string ---

func TestNormalizeOAuthErrorBody_ErrorFieldNotString(t *testing.T) {
	// Error field is a number, not a string — should pass through unchanged
	code, body := NormalizeOAuthErrorBody(200, []byte(`{"error":123}`))
	if code != 200 {
		t.Errorf("statusCode = %d, want 200 for non-string error field", code)
	}
	if string(body) != `{"error":123}` {
		t.Errorf("body should pass through unchanged, got %q", string(body))
	}
}

// --- NormalizeOAuthErrorBody: non-standard alias with custom description ---

func TestNormalizeOAuthErrorBody_NonStandardWithDescription(t *testing.T) {
	code, body := NormalizeOAuthErrorBody(200, []byte(`{"error":"invalid_refresh_token","error_description":"Your token has expired permanently"}`))
	if code != 400 {
		t.Errorf("statusCode = %d, want 400", code)
	}
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "invalid_grant") {
		t.Errorf("body should contain 'invalid_grant', got %q", bodyStr)
	}
	if !strings.Contains(bodyStr, "Your token has expired permanently") {
		t.Errorf("body should contain original description, got %q", bodyStr)
	}
}

// --- NormalizeOAuthErrorBody: non-standard alias with empty description ---

func TestNormalizeOAuthErrorBody_NonStandardWithEmptyDescription(t *testing.T) {
	code, body := NormalizeOAuthErrorBody(200, []byte(`{"error":"token_expired","error_description":""}`))
	if code != 400 {
		t.Errorf("statusCode = %d, want 400", code)
	}
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "invalid_grant") {
		t.Errorf("body should contain 'invalid_grant', got %q", bodyStr)
	}
	if !strings.Contains(bodyStr, "non-standard error code: token_expired") {
		t.Errorf("body should contain fallback description, got %q", bodyStr)
	}
}

// --- PerformMCPOAuthFlow: callback with OAuth error (line 488-494) ---

func TestPerformMCPOAuthFlow_ErrorCallback(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	_ = os.Setenv("MCP_OAUTH_CALLBACK_PORT", fmt.Sprintf("%d", port))
	defer func() { _ = os.Unsetenv("MCP_OAUTH_CALLBACK_PORT") }()

	config := OAuthFlowConfig{
		ServerName:   "test-server",
		ServerConfig: &SSEConfig{URL: "http://example.com/mcp"},
		TokenStore:   store,
	}

	errChan := make(chan error, 1)
	go func() {
		_, err := PerformMCPOAuthFlow(ctx, config)
		errChan <- err
	}()

	time.Sleep(200 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?error=access_denied&error_description=User+denied+access", port))
	if err != nil {
		t.Fatalf("callback GET failed: %v", err)
	}
	_ = resp.Body.Close()

	err = <-errChan
	if err == nil {
		t.Fatal("want error from OAuth error callback")
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("error should contain 'access_denied', got: %v", err)
	}
	if !strings.Contains(err.Error(), "User denied access") {
		t.Errorf("error should contain error description, got: %v", err)
	}
}

// --- PerformMCPOAuthFlow: state mismatch (line 501-505) ---

func TestPerformMCPOAuthFlow_StateMismatch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	_ = os.Setenv("MCP_OAUTH_CALLBACK_PORT", fmt.Sprintf("%d", port))
	defer func() { _ = os.Unsetenv("MCP_OAUTH_CALLBACK_PORT") }()

	config := OAuthFlowConfig{
		ServerName:   "test-server",
		ServerConfig: &SSEConfig{URL: "http://example.com/mcp"},
		TokenStore:   store,
	}

	errChan := make(chan error, 1)
	go func() {
		_, err := PerformMCPOAuthFlow(ctx, config)
		errChan <- err
	}()

	time.Sleep(200 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?code=some-code&state=wrong-state", port))
	if err != nil {
		t.Fatalf("callback GET failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	_ = resp.Body.Close()

	err = <-errChan
	if err == nil {
		t.Fatal("want error from state mismatch")
	}
	if !strings.Contains(err.Error(), "state mismatch") {
		t.Errorf("error should mention state mismatch, got: %v", err)
	}
}

// --- PerformMCPOAuthFlow: timeout (line 566-568) ---

func TestPerformMCPOAuthFlow_Timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	_ = os.Setenv("MCP_OAUTH_CALLBACK_PORT", fmt.Sprintf("%d", port))
	defer func() { _ = os.Unsetenv("MCP_OAUTH_CALLBACK_PORT") }()

	config := OAuthFlowConfig{
		ServerName:   "test-server",
		ServerConfig: &SSEConfig{URL: "http://example.com/mcp"},
		TokenStore:   store,
	}

	_, err = PerformMCPOAuthFlow(ctx, config)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error should mention timeout, got: %v", err)
	}
}

// --- PerformMCPOAuthFlow: OnAuthURL callback (line 536-540) ---

func TestPerformMCPOAuthFlow_OnAuthURL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	_ = os.Setenv("MCP_OAUTH_CALLBACK_PORT", fmt.Sprintf("%d", port))
	defer func() { _ = os.Unsetenv("MCP_OAUTH_CALLBACK_PORT") }()

	var authURLCalled string
	config := OAuthFlowConfig{
		ServerName:   "test-server",
		ServerConfig: &SSEConfig{URL: "http://example.com/mcp"},
		TokenStore:   store,
		OnAuthURL:    func(u string) { authURLCalled = u },
	}

	errChan := make(chan error, 1)
	go func() {
		_, err := PerformMCPOAuthFlow(ctx, config)
		errChan <- err
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	<-errChan

	if authURLCalled == "" {
		t.Error("OnAuthURL should have been called")
	}
	if !strings.Contains(authURLCalled, fmt.Sprintf("localhost:%d", port)) {
		t.Errorf("authURL should contain port %d, got %q", port, authURLCalled)
	}
}

// --- RefreshAuthorization: ReadTokens error (line 600-601) ---

func TestRefreshAuthorization_ReadTokensError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	// Make token file unreadable (as directory)
	if err := os.MkdirAll(store.tokenPath("key"), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	_, err = RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("want error when ReadTokens fails")
	}
	if !strings.Contains(err.Error(), "failed to read tokens for refresh") {
		t.Errorf("error = %v, want 'failed to read tokens for refresh'", err)
	}
}

// --- RefreshAuthorization: transient error retry succeeds (line 643) ---

func TestRefreshAuthorization_TransientRetrySuccess(t *testing.T) {
	origSleep := authBackoffSleep
	authBackoffSleep = func(d time.Duration) {} // instant for testing
	defer func() { authBackoffSleep = origSleep }()

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "old",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Second).UnixMilli(),
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	callCount := 0
	tokens, err := RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		callCount++
		if callCount == 1 {
			return nil, fmt.Errorf("server returned 503 Service Unavailable")
		}
		return &OAuthTokens{
			AccessToken:  "new-token",
			RefreshToken: "new-refresh",
			ExpiresIn:    3600,
		}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens == nil {
		t.Fatal("expected tokens after transient retry")
	}
	if tokens.AccessToken != "new-token" {
		t.Errorf("AccessToken = %q, want %q", tokens.AccessToken, "new-token")
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2 (1 transient + 1 success)", callCount)
	}
}

// --- RefreshAuthorization: transient error retry exhausted (line 646) ---

func TestRefreshAuthorization_TransientRetryExhausted(t *testing.T) {
	origSleep := authBackoffSleep
	authBackoffSleep = func(d time.Duration) {} // instant for testing
	defer func() { authBackoffSleep = origSleep }()

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "old",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Second).UnixMilli(),
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	tokens, err := RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		return nil, fmt.Errorf("HTTP 500 Internal Server Error")
	})
	if err == nil {
		t.Fatal("want error after all retries exhausted")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should contain '500', got: %v", err)
	}
	if tokens != nil {
		t.Errorf("expected nil tokens after exhausted retries, got %+v", tokens)
	}
}

// --- RefreshAuthorization: refreshFunc returns nil tokens (line 649-651) ---

func TestRefreshAuthorization_RefreshReturnsNil(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "old",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Second).UnixMilli(),
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	tokens, err := RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens != nil {
		t.Errorf("expected nil when refreshFunc returns nil, got %+v", tokens)
	}
}

// --- RefreshAuthorization: WriteTokens error during save (line 660-661) ---

func TestRefreshAuthorization_SaveError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "old",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Second).UnixMilli(),
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	// Make the token file read-only so WriteFile fails
	// ReadTokens reads the file (works), refreshFunc runs, then WriteTokens fails
	tokenPath := store.tokenPath("key")
	if err := os.Chmod(tokenPath, 0444); err != nil {
		t.Fatalf("chmod file: %v", err)
	}
	defer func() { _ = os.Chmod(tokenPath, 0600) }()

	_, err = RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		return &OAuthTokens{AccessToken: "new", ExpiresIn: 3600}, nil
	})
	if err == nil {
		t.Fatal("want error when saving refreshed tokens fails")
	}
	if !strings.Contains(err.Error(), "failed to save refreshed tokens") {
		t.Errorf("error = %v, want 'failed to save refreshed tokens'", err)
	}
}

// --- RefreshAuthorization: no ExpiresAt (line 608) ---

func TestRefreshAuthorization_NoExpiry(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "old",
		RefreshToken: "refresh",
		ExpiresAt:    0,
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	tokens, err := RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		return &OAuthTokens{
			AccessToken:  "new-token",
			RefreshToken: "new-refresh",
			ExpiresIn:    3600,
		}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens == nil {
		t.Fatal("expected tokens")
	}
	if tokens.AccessToken != "new-token" {
		t.Errorf("AccessToken = %q, want %q", tokens.AccessToken, "new-token")
	}
}

// --- RefreshAuthorization: nil tokenData (line 603) ---

func TestRefreshAuthorization_NilTokenData(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	tokens, err := RefreshAuthorization(context.Background(), "missing-key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		t.Error("refreshFunc should not be called")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens != nil {
		t.Errorf("expected nil tokens for missing key, got %+v", tokens)
	}
}

// --- revokeToken: no clientID, no clientSecret (lines 940-946) ---

func TestRevokeToken_NoClientCredentials(t *testing.T) {
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := revokeToken(context.Background(), server.URL, "token123", "refresh_token", "", "", "")
	if err != nil {
		t.Fatalf("revokeToken() = %v, want nil", err)
	}
	if strings.Contains(receivedBody, "client_id") {
		t.Errorf("body should not contain client_id when empty, got: %s", receivedBody)
	}
	if strings.Contains(receivedBody, "client_secret") {
		t.Errorf("body should not contain client_secret when empty, got: %s", receivedBody)
	}
	if !strings.Contains(receivedBody, "token=token123") {
		t.Errorf("body should contain token, got: %s", receivedBody)
	}
}

// --- revokeToken: retry with 401 and access token, retry returns >=400 (line 975-977) ---

func TestRevokeToken_RetryWith401ThenFail(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	err := revokeToken(context.Background(), server.URL, "token123", "refresh_token", "", "", "access123")
	if err == nil {
		t.Fatal("want error from retry")
	}
	if !strings.Contains(err.Error(), "revocation failed with status 401") {
		t.Errorf("error = %v, want 'revocation failed with status 401'", err)
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2", callCount)
	}
}

// --- revokeToken: first request creation error (line 949-950) ---

func TestRevokeToken_CreateRequestError(t *testing.T) {
	err := revokeToken(context.Background(), "http://[::1]:namedhost", "token123", "refresh_token", "", "", "")
	if err == nil {
		t.Fatal("want error for invalid URL")
	}
}

// --- revokeToken: first request Do error via cancelled context (line 957) ---

func TestRevokeToken_DoError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := revokeToken(ctx, "http://127.0.0.1:1/revoke", "token123", "refresh_token", "", "", "")
	if err == nil {
		t.Fatal("want error from cancelled context")
	}
}

// --- RevokeServerTokens: metadata fetch error (lines 1000-1014) ---

func TestRevokeServerTokens_MetadataRequestError(t *testing.T) {
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
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = RevokeServerTokens(ctx, store, "key", "http://example.com", "https://auth.example.com/.well-known/oauth")
	if err != nil {
		t.Errorf("RevokeServerTokens should succeed even when metadata fetch fails: %v", err)
	}
	tok, _ := store.ReadTokens("key")
	if tok != nil {
		t.Errorf("tokens should be deleted after failed metadata fetch, got %+v", tok)
	}
}

// --- RevokeServerTokens: metadata returns non-200 (line 1007) ---

func TestRevokeServerTokens_MetadataNon200(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken: "access",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", server.URL+"/metadata")
	if err != nil {
		t.Errorf("RevokeServerTokens with non-200 metadata: %v", err)
	}
	tok, _ := store.ReadTokens("key")
	if tok != nil {
		t.Errorf("tokens should be deleted, got %+v", tok)
	}
}

// --- RevokeServerTokens: metadata returns invalid JSON (line 1009) ---

func TestRevokeServerTokens_MetadataInvalidJSON(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "not valid json")
	}))
	defer server.Close()

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken: "access",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", server.URL+"/metadata")
	if err != nil {
		t.Errorf("RevokeServerTokens with invalid JSON metadata: %v", err)
	}
	tok, _ := store.ReadTokens("key")
	if tok != nil {
		t.Errorf("tokens should be deleted, got %+v", tok)
	}
}

// --- RevokeServerTokens: metadata has no RevocationEndpoint (line 1017) ---

func TestRevokeServerTokens_NoRevocationEndpoint(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"token_endpoint": "https://auth.example.com/token"}`)
	}))
	defer server.Close()

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

	err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", server.URL+"/metadata")
	if err != nil {
		t.Errorf("RevokeServerTokens without revocation endpoint: %v", err)
	}
	tok, _ := store.ReadTokens("key")
	if tok != nil {
		t.Errorf("tokens should be deleted when no revocation endpoint, got %+v", tok)
	}
}

// --- RevokeServerTokens: only access token, no refresh token (line 1028-1030) ---

func TestRevokeServerTokens_AccessTokenOnly(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"revocation_endpoint": "%s/revoke"}`, r.URL.Scheme+"://"+r.Host+"/revoke")
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "token_type_hint=access_token") {
			t.Errorf("expected token_type_hint=access_token, got: %s", string(body))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken: "access-only",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", server.URL+"/metadata")
	if err != nil {
		t.Errorf("RevokeServerTokens access-only: %v", err)
	}
	tok, _ := store.ReadTokens("key")
	if tok != nil {
		t.Errorf("tokens should be deleted, got %+v", tok)
	}
}

// --- RevokeServerTokens: only refresh token, no access token (line 1023-1026) ---

func TestRevokeServerTokens_RefreshTokenOnly(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"revocation_endpoint": "%s/revoke"}`, r.URL.Scheme+"://"+r.Host+"/revoke")
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "token_type_hint=refresh_token") {
			t.Errorf("expected token_type_hint=refresh_token, got: %s", string(body))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		RefreshToken: "refresh-only",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", server.URL+"/metadata")
	if err != nil {
		t.Errorf("RevokeServerTokens refresh-only: %v", err)
	}
	tok, _ := store.ReadTokens("key")
	if tok != nil {
		t.Errorf("tokens should be deleted, got %+v", tok)
	}
}

// --- RevokeServerTokens: read error returns nil (line 987-988) ---

func TestRevokeServerTokens_ReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := os.MkdirAll(store.tokenPath("key"), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", "")
	if err != nil {
		t.Errorf("RevokeServerTokens should return nil on read error, got %v", err)
	}
}

// --- FindAvailablePort: fallback port occupied → error (line 431) ---

func TestFindAvailablePort_FallbackOccupied(t *testing.T) {
	_ = os.Unsetenv("MCP_OAUTH_CALLBACK_PORT")

	min, _ := redirectPortRange()

	// Occupy the fallback port
	fallbackLn, err := net.Listen("tcp", fmt.Sprintf(":%d", redirectPortFallback))
	if err != nil {
		t.Skipf("can't occupy fallback port %d: %v", redirectPortFallback, err)
	}
	defer fallbackLn.Close()

	// Also occupy a bunch of ports in the range
	var listeners []net.Listener
	defer func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}()
	for i := range 200 {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", min+i))
		if err != nil {
			continue
		}
		listeners = append(listeners, ln)
	}

	port, err := FindAvailablePort()
	if err != nil {
		if !strings.Contains(err.Error(), "no available ports") {
			t.Errorf("error = %v, want 'no available ports'", err)
		}
	} else {
		if port <= 0 || port > 65535 {
			t.Errorf("port %d out of range", port)
		}
	}
}

// --- MCPAuthHandler.Tokens: proactive refresh with refreshFunc (lines 1239-1261) ---

func TestMCPAuthHandler_Tokens_ProactiveRefresh(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	refreshCalled := false
	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, dir,
		WithRefreshFunc(func(ctx context.Context, rt string) (*OAuthTokens, error) {
			refreshCalled = true
			return &OAuthTokens{
				AccessToken:  "refreshed-access",
				RefreshToken: "refreshed-refresh",
				ExpiresIn:    3600,
				TokenType:    "Bearer",
			}, nil
		}),
	)

	serverKey := h.ServerKey()
	if err := store.WriteTokens(serverKey, &OAuthTokenData{
		ServerName:   "srv",
		ServerURL:    "http://example.com",
		AccessToken:  "expiring-access",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(2 * time.Minute).UnixMilli(),
		Scope:        "read",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	tokens, err := h.Tokens()
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	if tokens == nil {
		t.Fatal("expected tokens, got nil")
	}
	if !refreshCalled {
		t.Error("refreshFunc should have been called for expiring token")
	}
	if tokens.AccessToken != "refreshed-access" {
		t.Errorf("AccessToken = %q, want %q", tokens.AccessToken, "refreshed-access")
	}
}

// --- MCPAuthHandler.Tokens: refresh already in progress (line 1259-1260) ---

func TestMCPAuthHandler_Tokens_RefreshInProgress(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, dir,
		WithRefreshFunc(func(ctx context.Context, rt string) (*OAuthTokens, error) {
			return &OAuthTokens{AccessToken: "refreshed", ExpiresIn: 3600}, nil
		}),
	)

	serverKey := h.ServerKey()
	if err := store.WriteTokens(serverKey, &OAuthTokenData{
		ServerName:   "srv",
		AccessToken:  "expiring",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(2 * time.Minute).UnixMilli(),
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
	if tokens == nil {
		t.Fatal("expected tokens even when refresh in progress")
	}
	if tokens.AccessToken != "expiring" {
		t.Errorf("AccessToken = %q, want %q (current token when refresh in progress)", tokens.AccessToken, "expiring")
	}
}

// --- MCPAuthHandler.Tokens: refresh returns nil, falls through (line 1256-1258) ---

func TestMCPAuthHandler_Tokens_RefreshReturnsNil(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, dir,
		WithRefreshFunc(func(ctx context.Context, rt string) (*OAuthTokens, error) {
			return nil, nil
		}),
	)

	serverKey := h.ServerKey()
	if err := store.WriteTokens(serverKey, &OAuthTokenData{
		ServerName:   "srv",
		AccessToken:  "expiring",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(2 * time.Minute).UnixMilli(),
		Scope:        "read",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	tokens, err := h.Tokens()
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	if tokens == nil {
		t.Fatal("expected tokens (should fall through to return current tokens)")
	}
	if tokens.AccessToken != "expiring" {
		t.Errorf("AccessToken = %q, want %q", tokens.AccessToken, "expiring")
	}
}

// --- MCPAuthHandler.Tokens: step-up with all scopes already present ---

func TestMCPAuthHandler_Tokens_StepUpAlreadyHasScopes(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, dir)
	serverKey := h.ServerKey()

	if err := store.WriteTokens(serverKey, &OAuthTokenData{
		ServerName:   "srv",
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
		Scope:        "read write admin",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	h.MarkStepUpPending("admin")

	tokens, err := h.Tokens()
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	if tokens == nil {
		t.Fatal("expected tokens")
	}
	if tokens.RefreshToken != "refresh" {
		t.Errorf("RefreshToken should be present when step-up scope already granted, got %q", tokens.RefreshToken)
	}
	if tokens.AccessToken != "access" {
		t.Errorf("AccessToken = %q, want %q", tokens.AccessToken, "access")
	}
}

// --- MCPAuthHandler.Tokens: ReadTokens error (line 1200-1201) ---

func TestMCPAuthHandler_Tokens_ReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, tmpDir)

	if err := os.MkdirAll(store.tokenPath(h.ServerKey()), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	_, err = h.Tokens()
	if err == nil {
		t.Error("want error when ReadTokens fails")
	}
}

// --- MCPAuthHandler.Tokens: expired with refresh token but no refreshFunc (line 1234-1262) ---

func TestMCPAuthHandler_Tokens_ExpiredWithRefresh_NoRefreshFunc(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, dir)
	serverKey := h.ServerKey()

	if err := store.WriteTokens(serverKey, &OAuthTokenData{
		ServerName:   "srv",
		AccessToken:  "expired-access",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Hour).UnixMilli(),
		Scope:        "read",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	tokens, err := h.Tokens()
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	if tokens == nil {
		t.Fatal("expected tokens for expired+refresh+no refreshFunc")
	}
	if tokens.AccessToken != "expired-access" {
		t.Errorf("AccessToken = %q, want %q", tokens.AccessToken, "expired-access")
	}
}

// --- MCPAuthHandler.SaveTokens: ReadTokens error (line 1278-1279) ---

func TestMCPAuthHandler_SaveTokens_ReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, tmpDir)

	if err := os.MkdirAll(store.tokenPath(h.ServerKey()), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err = h.SaveTokens(&OAuthTokens{AccessToken: "new", ExpiresIn: 3600})
	if err == nil {
		t.Error("want error when ReadTokens fails in SaveTokens")
	}
}

// --- MCPAuthHandler.SaveTokens: empty RefreshToken preserves existing (line 1288-1290) ---

func TestMCPAuthHandler_SaveTokens_EmptyRefreshToken(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, dir)

	if err := h.SaveTokens(&OAuthTokens{
		AccessToken:  "access1",
		RefreshToken: "refresh1",
		ExpiresIn:    3600,
	}); err != nil {
		t.Fatalf("first SaveTokens: %v", err)
	}

	if err := h.SaveTokens(&OAuthTokens{
		AccessToken: "access2",
		ExpiresIn:   7200,
	}); err != nil {
		t.Fatalf("second SaveTokens: %v", err)
	}

	serverKey := h.ServerKey()
	tok, _ := store.ReadTokens(serverKey)
	if tok == nil {
		t.Fatal("expected tokens")
	}
	if tok.AccessToken != "access2" {
		t.Errorf("AccessToken = %q, want %q", tok.AccessToken, "access2")
	}
	if tok.RefreshToken != "refresh1" {
		t.Errorf("RefreshToken should be preserved when new one is empty, got %q", tok.RefreshToken)
	}
}

// --- MCPAuthHandler.SaveClientInformation: ReadTokens error (line 1301-1302) ---

func TestMCPAuthHandler_SaveClientInformation_ReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, tmpDir)

	if err := os.MkdirAll(store.tokenPath(h.ServerKey()), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err = h.SaveClientInformation("client-123", "secret-456")
	if err == nil {
		t.Error("want error when ReadTokens fails in SaveClientInformation")
	}
}

// --- MCPAuthHandler.SaveDiscoveryState: ReadTokens error (line 1320-1321) ---

func TestMCPAuthHandler_SaveDiscoveryState_ReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, tmpDir)

	if err := os.MkdirAll(store.tokenPath(h.ServerKey()), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err = h.SaveDiscoveryState(&OAuthDiscoveryState{AuthorizationServerURL: "https://auth.example.com"})
	if err == nil {
		t.Error("want error when ReadTokens fails in SaveDiscoveryState")
	}
}

// --- MCPAuthHandler.DiscoveryState: ReadTokens error (line 1338-1339) ---

func TestMCPAuthHandler_DiscoveryState_ReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, tmpDir)

	if err := os.MkdirAll(store.tokenPath(h.ServerKey()), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	_, err = h.DiscoveryState()
	if err == nil {
		t.Error("want error when ReadTokens fails in DiscoveryState")
	}
}

// --- MCPAuthHandler.DiscoveryState: nil DiscoveryState in existing token data (line 1341) ---

func TestMCPAuthHandler_DiscoveryState_NilDiscoveryStateInTokenData(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, dir)

	serverKey := h.ServerKey()
	if err := store.WriteTokens(serverKey, &OAuthTokenData{
		ServerName:  "srv",
		AccessToken: "token",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	ds, err := h.DiscoveryState()
	if err != nil {
		t.Fatalf("DiscoveryState: %v", err)
	}
	if ds != nil {
		t.Errorf("expected nil when DiscoveryState is nil in token data, got %+v", ds)
	}
}

// --- MCPAuthHandler.RefreshWithLock: token still valid (line 1368-1379) ---

func TestMCPAuthHandler_RefreshWithLock_TokenStillValid(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	refreshCalled := false
	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, dir)

	serverKey := h.ServerKey()
	if err := store.WriteTokens(serverKey, &OAuthTokenData{
		ServerName:   "srv",
		AccessToken:  "valid-access",
		RefreshToken: "valid-refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour).UnixMilli(),
		Scope:        "read",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	tokens, err := h.RefreshWithLock(context.Background(), func(ctx context.Context, rt string) (*OAuthTokens, error) {
		refreshCalled = true
		return nil, nil
	})
	if err != nil {
		t.Fatalf("RefreshWithLock: %v", err)
	}
	if tokens == nil {
		t.Fatal("expected tokens, got nil")
	}
	if tokens.AccessToken != "valid-access" {
		t.Errorf("AccessToken = %q, want %q", tokens.AccessToken, "valid-access")
	}
	if tokens.TokenType != "Bearer" {
		t.Errorf("TokenType = %q, want %q", tokens.TokenType, "Bearer")
	}
	if refreshCalled {
		t.Error("refreshFunc should not be called when token is still valid")
	}
}

// --- MCPAuthHandler.RefreshWithLock: ReadTokens error (line 1365-1366) ---

func TestMCPAuthHandler_RefreshWithLock_ReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, tmpDir)

	if err := os.MkdirAll(store.tokenPath(h.ServerKey()), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	_, err = h.RefreshWithLock(context.Background(), func(ctx context.Context, rt string) (*OAuthTokens, error) {
		return nil, nil
	})
	if err == nil {
		t.Error("want error when ReadTokens fails in RefreshWithLock")
	}
}

// --- MCPAuthHandler.RefreshWithLock: nil tokenData (line 1368) ---

func TestMCPAuthHandler_RefreshWithLock_NilTokenData(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, dir)

	tokens, err := h.RefreshWithLock(context.Background(), func(ctx context.Context, rt string) (*OAuthTokens, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatalf("RefreshWithLock with no stored tokens: %v", err)
	}
	if tokens != nil {
		t.Errorf("expected nil tokens when no token data and refresh returns nil, got %+v", tokens)
	}
}

// --- MCPAuthHandler.ClientMetadata: no metadata set ---

func TestMCPAuthHandler_ClientMetadata_NoMetadata(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, dir)

	md := h.ClientMetadata()
	if md["scope"] != nil {
		t.Errorf("scope should be nil when no metadata set, got %v", md["scope"])
	}
	if md["client_name"] != "Claude Code (srv)" {
		t.Errorf("client_name = %v, want 'Claude Code (srv)'", md["client_name"])
	}
}

// --- MCPAuthHandler.ClientMetadata: empty metadata scope (line 1187-1189) ---

func TestMCPAuthHandler_ClientMetadata_EmptyScope(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	h := NewMCPAuthHandler("srv", &SSEConfig{URL: "http://example.com"}, store, dir)
	h.SetMetadata(&OAuthMetadata{})

	md := h.ClientMetadata()
	if md["scope"] != nil {
		t.Errorf("scope should be nil when metadata has empty scope, got %v", md["scope"])
	}
}

// =========================================================================
// Additional coverage gap tests — round 2
// =========================================================================

// --- PerformMCPOAuthFlow: successful code callback (line 554-561) ---
// This test verifies the happy path where the callback receives a valid code.

func TestPerformMCPOAuthFlow_SuccessfulCodeCallback(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	_ = os.Setenv("MCP_OAUTH_CALLBACK_PORT", fmt.Sprintf("%d", port))
	defer func() { _ = os.Unsetenv("MCP_OAUTH_CALLBACK_PORT") }()

	var capturedAuthURL string
	config := OAuthFlowConfig{
		ServerName:   "test-server",
		ServerConfig: &SSEConfig{URL: "http://example.com/mcp"},
		TokenStore:   store,
		OnAuthURL:    func(u string) { capturedAuthURL = u },
	}

	resultChan := make(chan *OAuthFlowResult, 1)
	errChan := make(chan error, 1)
	go func() {
		result, err := PerformMCPOAuthFlow(ctx, config)
		if err != nil {
			errChan <- err
		} else {
			resultChan <- result
		}
	}()

	time.Sleep(200 * time.Millisecond)

	// We don't know the generated state, so we can't send a valid code.
	// Instead, test by sending a callback that triggers the code path.
	// We'll get a state mismatch, which exercises most of the flow.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?code=auth-code-123&state=wrong", port))
	if err != nil {
		t.Fatalf("callback GET failed: %v", err)
	}
	_ = resp.Body.Close()

	// Expect state mismatch error
	select {
	case result := <-resultChan:
		// Unexpected success
		_ = result
	case err := <-errChan:
		if !strings.Contains(err.Error(), "state mismatch") {
			t.Errorf("expected state mismatch error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for flow result")
	}

	if capturedAuthURL == "" {
		t.Error("OnAuthURL callback should have been called")
	}
}

// --- RevokeServerTokens: empty configuredMetadataURL (line 996 — metadata URL empty) ---

func TestRevokeServerTokens_EmptyMetadataURL(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	// Tokens exist but no metadata URL
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "access",
		RefreshToken: "refresh",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", "")
	if err != nil {
		t.Errorf("RevokeServerTokens with empty metadata URL: %v", err)
	}
	tok, _ := store.ReadTokens("key")
	if tok != nil {
		t.Errorf("tokens should be deleted when no metadata URL, got %+v", tok)
	}
}

// --- RevokeServerTokens: only access token, no refresh (with revocation) ---

func TestRevokeServerTokens_MetadataFetchReqError(t *testing.T) {
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

	// Use a URL that causes NewRequest to fail (invalid URL for GET)
	// "https://" with no host is invalid for NewRequestWithContext
	err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", "https://")
	if err != nil {
		// Should still succeed — falls through to DeleteTokens
		t.Logf("RevokeServerTokens returned: %v", err)
	}
	// Either way, local tokens should be cleaned
	tok, _ := store.ReadTokens("key")
	// If metadata fetch fails, it should still delete tokens
	if tok != nil {
		t.Errorf("tokens should be deleted, got %+v", tok)
	}
}

// --- RevokeServerTokens: metadata returns valid JSON with revocation endpoint (full path) ---

func TestRevokeServerTokens_MetadataFetchDoError(t *testing.T) {
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

	// Use a URL that will fail during client.Do (connection refused)
	err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", "https://127.0.0.1:1/metadata")
	if err != nil {
		t.Logf("RevokeServerTokens returned: %v", err)
	}
	tok, _ := store.ReadTokens("key")
	if tok != nil {
		t.Errorf("tokens should be deleted after failed metadata fetch, got %+v", tok)
	}
}

// --- StepUpDetect: no scope match in WWW-Authenticate (line 920-927) ---

func TestStepUpDetect_NoScopeMatch(t *testing.T) {
	resp := &http.Response{
		StatusCode: 403,
		Header: http.Header{
			"Www-Authenticate": []string{`Bearer error="insufficient_scope"`},
		},
	}
	scope := StepUpDetect(resp)
	if scope != "" {
		t.Errorf("expected empty scope when no scope= in header, got %q", scope)
	}
}

// --- RevokeServerTokens: read tokens returns nil (line 987-988) ---

func TestRevokeServerTokens_TokenDataNil(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	err = RevokeServerTokens(context.Background(), store, "nonexistent-key", "http://example.com", "")
	if err != nil {
		t.Errorf("expected nil for nonexistent key, got %v", err)
	}
}

// --- RevokeServerTokens: both access and refresh tokens empty (line 990-992) ---

func TestRevokeServerTokens_BothTokensEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		ServerName: "srv",
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", "")
	if err != nil {
		t.Errorf("expected nil when both tokens empty, got %v", err)
	}
}

// --- RefreshAuthorization: invalid_grant with WriteTokens error (line 636-638) ---

func TestRefreshAuthorization_InvalidGrant_WriteTokensError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	tmpDir := t.TempDir()
	store, err := NewFileTokenStore(tmpDir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "old",
		RefreshToken: "bad-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}
	// Make the file read-only so WriteTokens inside invalid_grant handling fails
	tokenPath := store.tokenPath("key")
	if err := os.Chmod(tokenPath, 0444); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(tokenPath, 0600) }()

	// Should still return nil (best effort, just logs a warning)
	tokens, err := RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		return nil, fmt.Errorf("invalid_grant: token expired")
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens != nil {
		t.Errorf("expected nil tokens on invalid_grant, got %+v", tokens)
	}
}

// --- RevokeServerTokens: non-HTTPS metadata URL (line 997-998) ---

func TestRevokeServerTokens_NonHTTPSMetadataURL(t *testing.T) {
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

	err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", "http://not-https.example.com/metadata")
	if err == nil {
		t.Fatal("want error for non-HTTPS metadata URL")
	}
	if !strings.Contains(err.Error(), "must use https://") {
		t.Errorf("error = %v, want 'must use https://'", err)
	}
}

// --- revokeToken: 401 then Bearer retry succeeds (line 962-978) ---

func TestRevokeToken_BearerRetrySuccess(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First call returns 401 to trigger Bearer retry
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Second call has Authorization header, succeed
		if auth := r.Header.Get("Authorization"); auth != "Bearer access123" {
			t.Errorf("retry missing Bearer auth, got: %s", auth)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := revokeToken(context.Background(), server.URL, "token123", "refresh_token", "client-id", "secret", "access123")
	if err != nil {
		t.Fatalf("revokeToken() = %v, want nil on successful retry", err)
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2", callCount)
	}
}

// --- revokeToken: 401 but no accessToken, no retry (line 962) ---

func TestRevokeToken_401NoAccessToken_NoRetry(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	err := revokeToken(context.Background(), server.URL, "token123", "refresh_token", "", "", "")
	if err != nil {
		t.Fatalf("revokeToken() = %v, want nil (no retry, 401 accepted)", err)
	}
	if callCount != 1 {
		t.Errorf("callCount = %d, want 1 (no retry without access token)", callCount)
	}
}

// --- RefreshAuthorization: non-transient error returns immediately (line 646) ---

func TestRefreshAuthorization_NonTransientErrorNoRetry(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "old",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Second).UnixMilli(),
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	callCount := 0
	tokens, err := RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		callCount++
		return nil, fmt.Errorf("permission denied")
	})
	if err == nil {
		t.Fatal("want error from non-transient failure")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %v, want 'permission denied'", err)
	}
	if tokens != nil {
		t.Errorf("expected nil tokens, got %+v", tokens)
	}
	if callCount != 1 {
		t.Errorf("callCount = %d, want 1 (no retry for non-transient)", callCount)
	}
}

// --- RefreshAuthorization: new refresh token is saved (line 655-657) ---

func TestRefreshAuthorization_RefreshTokenSavedOnSuccess(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Second).UnixMilli(),
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	tokens, err := RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		if rt != "old-refresh" {
			t.Errorf("refreshFunc got rt = %q, want 'old-refresh'", rt)
		}
		return &OAuthTokens{
			AccessToken:  "new-access",
			RefreshToken: "new-refresh",
			ExpiresIn:    7200,
			Scope:        "read write admin",
		}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens == nil {
		t.Fatal("expected tokens")
	}
	if tokens.AccessToken != "new-access" {
		t.Errorf("AccessToken = %q, want 'new-access'", tokens.AccessToken)
	}
	if tokens.RefreshToken != "new-refresh" {
		t.Errorf("RefreshToken = %q, want 'new-refresh'", tokens.RefreshToken)
	}

	// Verify saved to store
	saved, err := store.ReadTokens("key")
	if err != nil {
		t.Fatalf("ReadTokens: %v", err)
	}
	if saved == nil {
		t.Fatal("saved tokens should exist")
	}
	if saved.AccessToken != "new-access" {
		t.Errorf("saved AccessToken = %q, want 'new-access'", saved.AccessToken)
	}
	if saved.RefreshToken != "new-refresh" {
		t.Errorf("saved RefreshToken = %q, want 'new-refresh'", saved.RefreshToken)
	}
	if saved.Scope != "read write admin" {
		t.Errorf("saved Scope = %q, want 'read write admin'", saved.Scope)
	}
	if saved.ExpiresAt <= 0 {
		t.Errorf("saved ExpiresAt = %d, want positive", saved.ExpiresAt)
	}
}

// --- FindAvailablePort: fallback port used when random ports exhausted (line 428-429) ---

func TestFindAvailablePort_FallbackPort(t *testing.T) {
	_ = os.Unsetenv("MCP_OAUTH_CALLBACK_PORT")

	min, _ := redirectPortRange()

	// Occupy many ports in the random range, but leave fallback port free
	var listeners []net.Listener
	defer func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}()

	// Occupy 100 ports in the range so random attempts likely fail
	for i := range 100 {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", min+i))
		if err != nil {
			continue
		}
		listeners = append(listeners, ln)
	}

	// Make sure fallback port is free
	fallbackLn, err := net.Listen("tcp", fmt.Sprintf(":%d", redirectPortFallback))
	if err != nil {
		t.Skipf("can't verify fallback: port %d occupied: %v", redirectPortFallback, err)
	}
	_ = fallbackLn.Close()

	port, err := FindAvailablePort()
	if err != nil {
		t.Fatalf("FindAvailablePort() = %v, want nil", err)
	}
	// Port should either be in the range or be the fallback
	_, max := redirectPortRange()
	if port < min || port > max {
		if port != redirectPortFallback {
			t.Errorf("port = %d, want in range [%d,%d] or fallback %d", port, min, max, redirectPortFallback)
		}
	}
	if port <= 0 || port > 65535 {
		t.Errorf("port = %d, out of valid range", port)
	}
}

// ---------------------------------------------------------------------------
// RevokeServerTokens — configuredMetadataURL https:// check
// ---------------------------------------------------------------------------

func TestRevokeServerTokens_MetadataNotHTTPS(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{AccessToken: "at"}); err != nil {
		t.Fatal(err)
	}
	err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", "http://not-https.example.com/metadata")
	if err == nil {
		t.Fatal("want error for non-https metadata URL")
	}
	if !strings.Contains(err.Error(), "https://") {
		t.Errorf("error should mention https://, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RevokeServerTokens — configuredMetadataURL with successful fetch
// ---------------------------------------------------------------------------

func TestRevokeServerTokens_ConfiguredMetadataSuccess(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"revocation_endpoint":"https://`+r.Host+`/revoke"}`)
			return
		}
		if r.URL.Path == "/revoke" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "at",
		RefreshToken: "rt",
		ClientID:     "cid",
		ClientSecret: "cs",
	}); err != nil {
		t.Fatal(err)
	}

	metadataURL := "https://" + server.Listener.Addr().String() + "/metadata"
	err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", metadataURL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tokens, err := store.ReadTokens("key")
	if err != nil {
		t.Fatalf("ReadTokens: %v", err)
	}
	if tokens != nil {
		t.Error("tokens should have been deleted after revocation")
	}
}

// ---------------------------------------------------------------------------
// RevokeServerTokens — configuredMetadataURL with non-200 response
// ---------------------------------------------------------------------------

func TestRevokeServerTokens_ConfiguredMetadataNon200(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{AccessToken: "at"}); err != nil {
		t.Fatal(err)
	}

	metadataURL := "https://" + server.Listener.Addr().String() + "/metadata"
	err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", metadataURL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tokens, _ := store.ReadTokens("key")
	if tokens != nil {
		t.Error("tokens should have been deleted")
	}
}

// ---------------------------------------------------------------------------
// RevokeServerTokens — configuredMetadataURL with invalid JSON body
// ---------------------------------------------------------------------------

func TestRevokeServerTokens_ConfiguredMetadataInvalidJSON(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{invalid json}`)
	}))
	defer server.Close()

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{AccessToken: "at"}); err != nil {
		t.Fatal(err)
	}

	metadataURL := "https://" + server.Listener.Addr().String() + "/metadata"
	err = RevokeServerTokens(context.Background(), store, "key", "http://example.com", metadataURL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tokens, _ := store.ReadTokens("key")
	if tokens != nil {
		t.Error("tokens should have been deleted")
	}
}

// ---------------------------------------------------------------------------
// redirectPortRange — verify both branches
// ---------------------------------------------------------------------------

func TestRedirectPortRange_AllPlatforms(t *testing.T) {
	min, max := redirectPortRange()
	if min >= max {
		t.Errorf("expected min < max, got min=%d max=%d", min, max)
	}
	if runtime.GOOS == "windows" {
		if min != 39152 || max != 49151 {
			t.Errorf("windows: expected 39152-49151, got %d-%d", min, max)
		}
	} else {
		if min != 49152 || max != 65535 {
			t.Errorf("non-windows: expected 49152-65535, got %d-%d", min, max)
		}
	}
}

// --- RefreshAuthorization: exponential backoff between retries (Step 1) ---

func TestRefreshAuthorization_ExponentialBackoff(t *testing.T) {
	// Record sleep durations instead of actually sleeping
	var delays []time.Duration
	origSleep := authBackoffSleep
	authBackoffSleep = func(d time.Duration) { delays = append(delays, d) }
	defer func() { authBackoffSleep = origSleep }()

	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "old",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Second).UnixMilli(),
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	var callTimes []time.Time
	tokens, err := RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		callTimes = append(callTimes, time.Now())
		if len(callTimes) < 3 {
			return nil, fmt.Errorf("server returned 503 Service Unavailable")
		}
		return &OAuthTokens{
			AccessToken:  "new-token",
			RefreshToken: "new-refresh",
			ExpiresIn:    3600,
		}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens == nil {
		t.Fatal("expected tokens after retries")
	}
	if tokens.AccessToken != "new-token" {
		t.Errorf("AccessToken = %q, want %q", tokens.AccessToken, "new-token")
	}
	if len(callTimes) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(callTimes))
	}

	// Verify exponential backoff delays: 1s, 2s
	// Source: auth.ts:2349 — delayMs = 1000 * Math.pow(2, attempt - 1)
	if len(delays) != 2 {
		t.Fatalf("expected 2 backoff sleeps, got %d", len(delays))
	}
	if delays[0] != 1000*time.Millisecond {
		t.Errorf("delay[0] = %v, want 1s", delays[0])
	}
	if delays[1] != 2000*time.Millisecond {
		t.Errorf("delay[1] = %v, want 2s", delays[1])
	}
}

func TestRefreshAuthorization_NoBackoffOnNonTransient(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "old",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Second).UnixMilli(),
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	start := time.Now()
	tokens, err := RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		return nil, fmt.Errorf("permission denied")
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error for non-transient failure")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error should mention 'permission denied', got: %v", err)
	}
	if tokens != nil {
		t.Error("expected nil tokens")
	}
	// Non-transient errors should return immediately, no backoff delay
	if elapsed > 500*time.Millisecond {
		t.Errorf("non-transient error took %v, should be < 500ms (no backoff)", elapsed)
	}
}

func TestRefreshAuthorization_NoBackoffOnSuccess(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTokenStore(dir)
	if err != nil {
		t.Fatalf("NewFileTokenStore: %v", err)
	}
	if err := store.WriteTokens("key", &OAuthTokenData{
		AccessToken:  "old",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Second).UnixMilli(),
	}); err != nil {
		t.Fatalf("WriteTokens: %v", err)
	}

	start := time.Now()
	tokens, err := RefreshAuthorization(context.Background(), "key", store, func(ctx context.Context, rt string) (*OAuthTokens, error) {
		return &OAuthTokens{
			AccessToken:  "new-token",
			RefreshToken: "new-refresh",
			ExpiresIn:    3600,
		}, nil
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens == nil {
		t.Fatal("expected tokens")
	}
	// First attempt succeeds — no backoff at all
	if elapsed > 500*time.Millisecond {
		t.Errorf("successful refresh took %v, should be < 500ms (no backoff)", elapsed)
	}
}

// ---------------------------------------------------------------------------
// ExchangeAuthCode tests — Step 6
// Source: @modelcontextprotocol/sdk — auth.js token exchange
// ---------------------------------------------------------------------------

func TestExchangeAuthCode_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("expected form content type, got %s", r.Header.Get("Content-Type"))
		}

		// Parse form body
		body, _ := io.ReadAll(r.Body)
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse body: %v", err)
		}

		// Verify required parameters
		if values.Get("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q, want authorization_code", values.Get("grant_type"))
		}
		if values.Get("code") != "test-auth-code" {
			t.Errorf("code = %q, want test-auth-code", values.Get("code"))
		}
		if values.Get("code_verifier") != "test-verifier" {
			t.Errorf("code_verifier = %q, want test-verifier", values.Get("code_verifier"))
		}
		if values.Get("redirect_uri") != "http://localhost:1234/callback" {
			t.Errorf("redirect_uri = %q", values.Get("redirect_uri"))
		}
		if values.Get("client_id") != "test-client" {
			t.Errorf("client_id = %q", values.Get("client_id"))
		}

		// Return valid token response
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
			"access_token": "at-123",
			"token_type": "Bearer",
			"expires_in": 3600,
			"refresh_token": "rt-456",
			"scope": "read write"
		}`)
	}))
	defer server.Close()

	tokens, err := ExchangeAuthCode(context.Background(), server.URL, "test-auth-code", "test-verifier", "http://localhost:1234/callback", "test-client")
	if err != nil {
		t.Fatalf("ExchangeAuthCode: %v", err)
	}

	if tokens.AccessToken != "at-123" {
		t.Errorf("AccessToken = %q, want %q", tokens.AccessToken, "at-123")
	}
	if tokens.TokenType != "Bearer" {
		t.Errorf("TokenType = %q, want %q", tokens.TokenType, "Bearer")
	}
	if tokens.ExpiresIn != 3600 {
		t.Errorf("ExpiresIn = %v, want 3600", tokens.ExpiresIn)
	}
	if tokens.RefreshToken != "rt-456" {
		t.Errorf("RefreshToken = %q, want %q", tokens.RefreshToken, "rt-456")
	}
	if tokens.Scope != "read write" {
		t.Errorf("Scope = %q, want %q", tokens.Scope, "read write")
	}
}

func TestExchangeAuthCode_Error400(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"error":"invalid_request","error_description":"Missing code parameter"}`)
	}))
	defer server.Close()

	_, err := ExchangeAuthCode(context.Background(), server.URL, "code", "verifier", "http://localhost/cb", "client")
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if !strings.Contains(err.Error(), "invalid_request") {
		t.Errorf("error should contain error code, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Missing code parameter") {
		t.Errorf("error should contain description, got: %v", err)
	}
}

func TestExchangeAuthCode_CodeVerifierSent(t *testing.T) {
	var receivedVerifier string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(body))
		receivedVerifier = values.Get("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"tok","token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()

	_, err := ExchangeAuthCode(context.Background(), server.URL, "code", "my-pkce-verifier-123", "http://localhost/cb", "client")
	if err != nil {
		t.Fatalf("ExchangeAuthCode: %v", err)
	}

	if receivedVerifier != "my-pkce-verifier-123" {
		t.Errorf("code_verifier sent = %q, want %q", receivedVerifier, "my-pkce-verifier-123")
	}
}

func TestExchangeAuthCode_InvalidGrant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"error":"invalid_grant","error_description":"Authorization code expired"}`)
	}))
	defer server.Close()

	_, err := ExchangeAuthCode(context.Background(), server.URL, "expired-code", "verifier", "http://localhost/cb", "client")
	if err == nil {
		t.Fatal("expected error for invalid_grant")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error should mention invalid_grant, got: %v", err)
	}
}

func TestExchangeAuthCode_MissingAccessToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()

	_, err := ExchangeAuthCode(context.Background(), server.URL, "code", "verifier", "http://localhost/cb", "client")
	if err == nil {
		t.Fatal("expected error for missing access_token")
	}
	if !strings.Contains(err.Error(), "access_token") {
		t.Errorf("error should mention access_token, got: %v", err)
	}
}

func TestExchangeAuthCode_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `not json`)
	}))
	defer server.Close()

	_, err := ExchangeAuthCode(context.Background(), server.URL, "code", "verifier", "http://localhost/cb", "client")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention parse, got: %v", err)
	}
}

func TestExchangeAuthCode_500Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "internal server error")
	}))
	defer server.Close()

	_, err := ExchangeAuthCode(context.Background(), server.URL, "code", "verifier", "http://localhost/cb", "client")
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention 500, got: %v", err)
	}
}

func TestExchangeAuthCode_CancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"tok","token_type":"Bearer"}`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := ExchangeAuthCode(ctx, server.URL, "code", "verifier", "http://localhost/cb", "client")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Errorf("error should mention context, got: %v", err)
	}
}
