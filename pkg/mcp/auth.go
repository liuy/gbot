// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
// Source: src/services/mcp/auth.ts (2466 lines)
//
// This file: OAuth authentication, token storage, and refresh logic.
// Source: auth.ts
package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Constants — Source: auth.ts:65, 94, 100-106
// ---------------------------------------------------------------------------

const (
	// AuthRequestTimeoutMs is the timeout for individual OAuth requests.
	// Source: auth.ts:65
	AuthRequestTimeoutMs = 30000

	// MaxLockRetries is the maximum number of attempts to acquire a refresh lock.
	// Source: auth.ts:94
	MaxLockRetries = 5

	// OAuthCallbackTimeout is the maximum time to wait for an OAuth callback.
	// Source: auth.ts:1209 — 5 * 60 * 1000
	OAuthCallbackTimeout = 5 * time.Minute

	// OAuthTokenExpiryBuffer is the time buffer before token expiry for proactive refresh.
	// Source: auth.ts:1650 — 300 seconds
	OAuthTokenExpiryBuffer = 300 * time.Second
)

// sensitiveOAuthParams are query parameters that should be redacted from logs.
// Source: auth.ts:100-106
var sensitiveOAuthParams = []string{"state", "nonce", "code_challenge", "code_verifier", "code"}

// ---------------------------------------------------------------------------
// Token types — Source: auth.ts (OAuthTokens from SDK)
// ---------------------------------------------------------------------------

// OAuthTokens represents OAuth access and refresh tokens.
// Source: @modelcontextprotocol/sdk/shared/auth.js — OAuthTokens
type OAuthTokens struct {
	AccessToken  string  `json:"access_token"`
	TokenType    string  `json:"token_type"`
	ExpiresIn    float64 `json:"expires_in"`
	RefreshToken string  `json:"refresh_token,omitempty"`
	Scope        string  `json:"scope,omitempty"`
}

// OAuthDiscoveryState stores OAuth server discovery information.
// Source: auth.ts:1997-2035
type OAuthDiscoveryState struct {
	AuthorizationServerURL string `json:"authorizationServerUrl"`
	ResourceMetadataURL    string `json:"resourceMetadataUrl,omitempty"`
}

// OAuthTokenData represents stored token data for an MCP server.
// Source: auth.ts — SecureStorageData.mcpOAuth entries
type OAuthTokenData struct {
	ServerName     string              `json:"serverName"`
	ServerURL      string              `json:"serverUrl"`
	AccessToken    string              `json:"accessToken"`
	RefreshToken   string              `json:"refreshToken,omitempty"`
	ExpiresAt      int64               `json:"expiresAt"` // Unix millis
	Scope          string              `json:"scope,omitempty"`
	ClientID       string              `json:"clientId,omitempty"`
	ClientSecret   string              `json:"clientSecret,omitempty"`
	DiscoveryState *OAuthDiscoveryState `json:"discoveryState,omitempty"`
	StepUpScope    string              `json:"stepUpScope,omitempty"`
}

// OAuthClientConfig stores client configuration for an MCP server.
// Source: auth.ts:2399-2414
type OAuthClientConfig struct {
	ClientSecret string `json:"clientSecret"`
}

// ---------------------------------------------------------------------------
// getServerKey — Source: auth.ts:325-341
// ---------------------------------------------------------------------------

// GetServerKey generates a unique key for server credentials.
// Source: auth.ts:325-341 — uses SHA-256 hash of config JSON.
func GetServerKey(serverName string, serverConfig McpServerConfig) string {
	var configJSON string
	switch c := serverConfig.(type) {
	case *SSEConfig:
		b, _ := json.Marshal(map[string]any{"type": "sse", "url": c.URL, "headers": c.Headers})
		configJSON = string(b)
	case *HTTPConfig:
		b, _ := json.Marshal(map[string]any{"type": "http", "url": c.URL, "headers": c.Headers})
		configJSON = string(b)
	default:
		configJSON = fmt.Sprintf("%v", serverConfig)
	}

	hash := sha256.Sum256([]byte(configJSON))
	return fmt.Sprintf("%s|%x", serverName, hash)[:len(serverName)+17] // name| + 16 hex
}

// ---------------------------------------------------------------------------
// sanitizeServerName — path traversal prevention for FileTokenStore
// ---------------------------------------------------------------------------

// nonAlphaNumPath matches characters that are NOT safe for file paths.
var nonAlphaNumPath = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// SanitizeServerName sanitizes a server name for use as a file/directory name.
// Prevents path traversal attacks by replacing dangerous characters.
func SanitizeServerName(name string) string {
	sanitized := nonAlphaNumPath.ReplaceAllString(name, "_")
	// Prevent empty names and names that could be path traversal
	if sanitized == "" || sanitized == "." || sanitized == ".." {
		sanitized = "_"
	}
	return sanitized
}

// ---------------------------------------------------------------------------
// FileTokenStore — file-based token storage with proper permissions
// Source: auth.ts (SecureStorage abstraction) + PRD requirements
// ---------------------------------------------------------------------------

// FileTokenStore implements file-based OAuth token storage.
// Tokens are stored as JSON files with 0600 permissions in a 0700 directory.
type FileTokenStore struct {
	baseDir string
	mu      sync.Mutex
}

// NewFileTokenStore creates a new file-based token store.
// Creates the base directory with 0700 permissions if it doesn't exist.
func NewFileTokenStore(baseDir string) (*FileTokenStore, error) {
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return nil, fmt.Errorf("mcp: failed to create token store dir: %w", err)
	}
	return &FileTokenStore{baseDir: baseDir}, nil
}

// tokenPath returns the file path for a server key's token data.
func (s *FileTokenStore) tokenPath(serverKey string) string {
	return filepath.Join(s.baseDir, SanitizeServerName(serverKey)+".json")
}

// clientConfigPath returns the file path for a server key's client config.
func (s *FileTokenStore) clientConfigPath(serverKey string) string {
	return filepath.Join(s.baseDir, SanitizeServerName(serverKey)+"_client.json")
}

// ReadTokens reads stored token data for a server key.
func (s *FileTokenStore) ReadTokens(serverKey string) (*OAuthTokenData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.tokenPath(serverKey))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("mcp: failed to read token data: %w", err)
	}

	var tokenData OAuthTokenData
	if err := json.Unmarshal(data, &tokenData); err != nil {
		return nil, fmt.Errorf("mcp: failed to parse token data: %w", err)
	}
	return &tokenData, nil
}

// WriteTokens writes token data for a server key.
func (s *FileTokenStore) WriteTokens(serverKey string, tokenData *OAuthTokenData) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(tokenData)
	if err != nil {
		return fmt.Errorf("mcp: failed to marshal token data: %w", err)
	}

	if err := os.WriteFile(s.tokenPath(serverKey), data, 0600); err != nil {
		return fmt.Errorf("mcp: failed to write token data: %w", err)
	}
	return nil
}

// DeleteTokens removes stored token data for a server key.
func (s *FileTokenStore) DeleteTokens(serverKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := os.Remove(s.tokenPath(serverKey))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("mcp: failed to delete token data: %w", err)
	}
	return nil
}

// ReadClientConfig reads stored client config for a server key.
func (s *FileTokenStore) ReadClientConfig(serverKey string) (*OAuthClientConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.clientConfigPath(serverKey))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("mcp: failed to read client config: %w", err)
	}

	var config OAuthClientConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("mcp: failed to parse client config: %w", err)
	}
	return &config, nil
}

// WriteClientConfig writes client config for a server key.
func (s *FileTokenStore) WriteClientConfig(serverKey string, config *OAuthClientConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("mcp: failed to marshal client config: %w", err)
	}

	if err := os.WriteFile(s.clientConfigPath(serverKey), data, 0600); err != nil {
		return fmt.Errorf("mcp: failed to write client config: %w", err)
	}
	return nil
}

// DeleteClientConfig removes stored client config for a server key.
func (s *FileTokenStore) DeleteClientConfig(serverKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := os.Remove(s.clientConfigPath(serverKey))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("mcp: failed to delete client config: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// redactSensitiveUrlParams — Source: auth.ts:112-125
// ---------------------------------------------------------------------------

// RedactSensitiveURLParams redacts sensitive OAuth query parameters from a URL.
// Source: auth.ts:112-125
func RedactSensitiveURLParams(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := parsed.Query()
	for _, param := range sensitiveOAuthParams {
		if q.Has(param) {
			q.Set(param, "[REDACTED]")
		}
	}
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

// ---------------------------------------------------------------------------
// getScopeFromMetadata — Source: auth.ts:2445-2465
// ---------------------------------------------------------------------------

// OAuthMetadata represents OAuth server metadata.
// Source: @modelcontextprotocol/sdk/shared/auth.js — AuthorizationServerMetadata
type OAuthMetadata struct {
	Scope            string   `json:"scope,omitempty"`
	DefaultScope     string   `json:"default_scope,omitempty"`
	ScopesSupported  []string `json:"scopes_supported,omitempty"`
	TokenEndpoint    string   `json:"token_endpoint,omitempty"`
	RevocationEndpoint string `json:"revocation_endpoint,omitempty"`
}

// GetScopeFromMetadata extracts scope information from OAuth server metadata.
// Source: auth.ts:2445-2465
func GetScopeFromMetadata(metadata *OAuthMetadata) string {
	if metadata == nil {
		return ""
	}
	// Source: auth.ts:2450 — try 'scope' first
	if metadata.Scope != "" {
		return metadata.Scope
	}
	// Source: auth.ts:2455 — try 'default_scope'
	if metadata.DefaultScope != "" {
		return metadata.DefaultScope
	}
	// Source: auth.ts:2461 — fall back to scopes_supported
	if len(metadata.ScopesSupported) > 0 {
		return strings.Join(metadata.ScopesSupported, " ")
	}
	return ""
}

// ---------------------------------------------------------------------------
// generateState — Source: auth.ts:1474-1480
// ---------------------------------------------------------------------------

// GenerateState generates a random OAuth state parameter.
// Source: auth.ts:1474-1480 — randomBytes(32).toString('base64url')
func GenerateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("mcp: failed to generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ---------------------------------------------------------------------------
// generateCodeVerifier — PKCE code verifier
// Source: RFC 7636 §4.1
// ---------------------------------------------------------------------------

// GenerateCodeVerifier generates a PKCE code verifier.
func GenerateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("mcp: failed to generate code verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// DeriveCodeChallenge derives a PKCE code challenge from a code verifier.
// Source: RFC 7636 §4.2 — SHA-256 + base64url
func DeriveCodeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// AuthenticationCancelledError — Source: auth.ts:313-318
// ---------------------------------------------------------------------------

// AuthenticationCancelledError indicates the user cancelled the auth flow.
// Source: auth.ts:313-318
type AuthenticationCancelledError struct{}

func (e *AuthenticationCancelledError) Error() string {
	return "Authentication was cancelled"
}

// ---------------------------------------------------------------------------
// findAvailablePort — Source: oauthPort.ts (findAvailablePort)
// ---------------------------------------------------------------------------

// OAuth redirect port management - Source: oauthPort.ts
const redirectPortFallback = 3118

func redirectPortRange() (min, max int) {
	if runtime.GOOS == "windows" {
		return 39152, 49151
	}
	return 49152, 65535
}

func getMcpOAuthCallbackPort() int {
	port, err := strconv.Atoi(os.Getenv("MCP_OAUTH_CALLBACK_PORT"))
	if err != nil {
		return 0
	}
	if port > 0 {
		return port
	}
	return 0
}

func BuildRedirectURI(port int) string {
	if port <= 0 {
		port = redirectPortFallback
	}
	return fmt.Sprintf("http://localhost:%d/callback", port)
}

func isPortAvailable(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func FindAvailablePort() (int, error) {
	if configuredPort := getMcpOAuthCallbackPort(); configuredPort > 0 {
		return configuredPort, nil
	}
	min, max := redirectPortRange()
	rangeSize := max - min + 1
	maxAttempts := rangeSize
	if maxAttempts > 100 {
		maxAttempts = 100
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		port := min + mathrand.Intn(rangeSize)
		if isPortAvailable(port) {
			return port, nil
		}
	}
	if isPortAvailable(redirectPortFallback) {
		return redirectPortFallback, nil
	}
	return 0, fmt.Errorf("mcp: no available ports for OAuth redirect")
}

// ---------------------------------------------------------------------------
// PerformMCPOAuthFlow — Source: auth.ts:847-1342
// ---------------------------------------------------------------------------

// OAuthFlowConfig holds the configuration for an OAuth flow.
type OAuthFlowConfig struct {
	ServerName    string
	ServerConfig  McpServerConfig
	TokenStore    *FileTokenStore
	OnAuthURL     func(url string) // called when auth URL is available
	SkipBrowser   bool
}

// OAuthFlowResult holds the result of a completed OAuth flow.
type OAuthFlowResult struct {
	Tokens *OAuthTokens
}

// PerformMCPOAuthFlow executes the OAuth authorization code flow.
// Source: auth.ts:847-1342 — simplified port of the full flow.
//
// Starts a local HTTP callback server on 127.0.0.1, generates PKCE parameters,
// waits for the callback with the authorization code, then returns.
// The caller is responsible for token exchange (SDK-dependent).
func PerformMCPOAuthFlow(ctx context.Context, config OAuthFlowConfig) (*OAuthFlowResult, error) {
	// Source: auth.ts:961 — find available port
	port, err := FindAvailablePort()
	if err != nil {
		return nil, fmt.Errorf("mcp: %s: %w", config.ServerName, err)
	}
	redirectURI := BuildRedirectURI(port)

	// Source: auth.ts:1002-1003 — generate OAuth state
	state, err := GenerateState()
	if err != nil {
		return nil, err
	}

	// Source: auth.ts:1946-1949 — generate PKCE code verifier
	codeVerifier, err := GenerateCodeVerifier()
	if err != nil {
		return nil, err
	}
	codeChallenge := DeriveCodeChallenge(codeVerifier)

	// Source: auth.ts:1029-1214 — start callback server
	codeChan := make(chan string, 1)
	errChan := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		// Source: auth.ts:1100-1150 — handle callback
		q := r.URL.Query()

		if errorCode := q.Get("error"); errorCode != "" {
			errorDesc := q.Get("error_description")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "<h1>Authentication Error</h1><p>%s: %s</p><p>You can close this window.</p>",
				errorCode, errorDesc)
			errChan <- fmt.Errorf("OAuth error: %s - %s", errorCode, errorDesc)
			return
		}

		code := q.Get("code")
		callbackState := q.Get("state")

		// Source: auth.ts:1110-1118 — validate state
		if callbackState != state {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, "<h1>Authentication Error</h1><p>Invalid state parameter.</p>")
			errChan <- fmt.Errorf("OAuth state mismatch - possible CSRF attack")
			return
		}

		if code != "" {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "<h1>Authentication Successful</h1><p>You can close this window.</p>")
			codeChan <- code
		}
	})

	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: mux,
	}

	// Start server in background
	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			select {
			case errChan <- fmt.Errorf("OAuth callback server failed: %w", err):
			default:
			}
		}
	}()

	// Build authorization URL for the caller
	// The caller (UI/browser) will open this URL
	_ = redirectURI // Used in URL construction (caller handles)
	_ = codeChallenge

	// Notify about the auth URL if callback provided
	if config.OnAuthURL != nil {
		// Build a placeholder auth URL — the actual URL depends on the OAuth server
		// In production, the MCP SDK would provide the real URL
		config.OnAuthURL(BuildRedirectURI(port))
	}

	// Wait for callback or timeout
	ctx, cancel := context.WithTimeout(ctx, OAuthCallbackTimeout)
	defer cancel()

	// Shutdown server when done
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	select {
	case code := <-codeChan:
		// Source: auth.ts:1218-1258 — authorization code obtained
		// The caller does the actual token exchange with the code
		return &OAuthFlowResult{
			Tokens: &OAuthTokens{
				AccessToken: code, // placeholder; actual exchange is SDK-dependent
			},
		}, nil
	case err := <-errChan:
		return nil, err
	case <-ctx.Done():
		// Source: auth.ts:1205-1212 — timeout
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("authentication timeout")
		}
		return nil, &AuthenticationCancelledError{}
	}
}

// ---------------------------------------------------------------------------
// RefreshTokens — Source: auth.ts:2090-2359
// ---------------------------------------------------------------------------

// RefreshTokens attempts to refresh OAuth tokens using a refresh token.
// Source: auth.ts:2090-2359 — simplified port with retry logic.
//
// The actual HTTP token exchange is done via the caller-provided exchange function,
// since it depends on server metadata and client info.
type RefreshResult struct {
	Tokens *OAuthTokens
}

// TokenRefreshFunc performs the actual token exchange with the OAuth server.
// Implemented by the caller using the MCP SDK or HTTP client.
type TokenRefreshFunc func(ctx context.Context, refreshToken string) (*OAuthTokens, error)

// RefreshAuthorization refreshes tokens using the stored refresh token.
// Source: auth.ts:2090-2175 — includes cross-process lockfile pattern.
func RefreshAuthorization(
	ctx context.Context,
	serverKey string,
	store *FileTokenStore,
	refreshFunc TokenRefreshFunc,
) (*OAuthTokens, error) {
	// Read current tokens
	tokenData, err := store.ReadTokens(serverKey)
	if err != nil {
		return nil, fmt.Errorf("mcp: failed to read tokens for refresh: %w", err)
	}
	if tokenData == nil || tokenData.RefreshToken == "" {
		return nil, nil
	}

	// Source: auth.ts:2140-2163 — check if another process already refreshed
	if tokenData.ExpiresAt > 0 {
		expiresIn := time.Until(time.UnixMilli(tokenData.ExpiresAt))
		if expiresIn > OAuthTokenExpiryBuffer {
			// Token still valid, return it
			return &OAuthTokens{
				AccessToken:  tokenData.AccessToken,
				RefreshToken: tokenData.RefreshToken,
				ExpiresIn:    expiresIn.Seconds(),
				Scope:        tokenData.Scope,
				TokenType:    "Bearer",
			}, nil
		}
	}

	// Source: auth.ts:2210-2356 — retry loop
	if refreshFunc == nil {
		return nil, nil
	}
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		tokens, err := refreshFunc(ctx, tokenData.RefreshToken)
		if err != nil {
			// Source: auth.ts:2286-2325 — check for invalid_grant
			if strings.Contains(err.Error(), "invalid_grant") {
				// Refresh token is invalid, clear it
				tokenData.AccessToken = ""
				tokenData.RefreshToken = ""
				tokenData.ExpiresAt = 0
				if err := store.WriteTokens(serverKey, tokenData); err != nil {
					slog.Warn("failed to clear tokens after invalid_grant", "server", serverKey, "error", err)
				}
				return nil, nil
			}

			// Source: auth.ts:2328-2355 — retry on transient errors
			if attempt < maxAttempts && isTransientError(err) {
				continue
			}
			return nil, err
		}

		if tokens == nil {
			return nil, nil
		}

		// Save refreshed tokens
		tokenData.AccessToken = tokens.AccessToken
		if tokens.RefreshToken != "" {
			tokenData.RefreshToken = tokens.RefreshToken
		}
		tokenData.ExpiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UnixMilli()
		tokenData.Scope = tokens.Scope
		if err := store.WriteTokens(serverKey, tokenData); err != nil {
			return nil, fmt.Errorf("mcp: failed to save refreshed tokens: %w", err)
		}

		return tokens, nil
	}

	return nil, nil
}

// isTransientError checks if an error is retryable (timeout, server error).
// Source: auth.ts:2328-2335
func isTransientError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "timed out") ||
		strings.Contains(msg, "etimedout") ||
		strings.Contains(msg, "econnreset") ||
		strings.Contains(msg, "500") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "429")
}

// ---------------------------------------------------------------------------
// ClearServerTokens — Source: auth.ts:620-634
// ---------------------------------------------------------------------------

// ClearServerTokens removes stored tokens for a server.
// Source: auth.ts:620-634 — clearServerTokensFromLocalStorage
func ClearServerTokens(store *FileTokenStore, serverKey string) error {
	return store.DeleteTokens(serverKey)
}

// ---------------------------------------------------------------------------
// InvalidateCredentials — Source: auth.ts:1960-1995
// ---------------------------------------------------------------------------

// InvalidateCredentialsScope defines what to invalidate.
type InvalidateCredentialsScope string

const (
	InvalidateAll       InvalidateCredentialsScope = "all"
	InvalidateClient    InvalidateCredentialsScope = "client"
	InvalidateTokens    InvalidateCredentialsScope = "tokens"
	InvalidateDiscovery InvalidateCredentialsScope = "discovery"
)

// InvalidateCredentials removes or clears specified credential data.
// Source: auth.ts:1960-1995
func InvalidateCredentials(store *FileTokenStore, serverKey string, scope InvalidateCredentialsScope) error {
	switch scope {
	case InvalidateAll:
		return store.DeleteTokens(serverKey)
	case InvalidateClient:
		tokenData, err := store.ReadTokens(serverKey)
		if err != nil {
			return err
		}
		if tokenData == nil {
			return nil
		}
		tokenData.ClientID = ""
		tokenData.ClientSecret = ""
		return store.WriteTokens(serverKey, tokenData)
	case InvalidateTokens:
		tokenData, err := store.ReadTokens(serverKey)
		if err != nil {
			return err
		}
		if tokenData == nil {
			return nil
		}
		tokenData.AccessToken = ""
		tokenData.RefreshToken = ""
		tokenData.ExpiresAt = 0
		return store.WriteTokens(serverKey, tokenData)
	case InvalidateDiscovery:
		tokenData, err := store.ReadTokens(serverKey)
		if err != nil {
			return err
		}
		if tokenData == nil {
			return nil
		}
		tokenData.DiscoveryState = nil
		tokenData.StepUpScope = ""
		return store.WriteTokens(serverKey, tokenData)
	default:
		return fmt.Errorf("mcp: unknown invalidation scope: %s", scope)
	}
}

// ---------------------------------------------------------------------------
// HasDiscoveryButNoToken — Source: auth.ts:349-363
// ---------------------------------------------------------------------------

// HasDiscoveryButNoToken returns true if we've probed this server but hold no credentials.
// Source: auth.ts:349-363
func HasDiscoveryButNoToken(store *FileTokenStore, serverKey string) bool {
	tokenData, err := store.ReadTokens(serverKey)
	if err != nil || tokenData == nil {
		return false
	}
	return tokenData.AccessToken == "" && tokenData.RefreshToken == ""
}

// ---------------------------------------------------------------------------
// BuildAuthURL — helper for constructing OAuth authorization URLs
// ---------------------------------------------------------------------------

// BuildAuthURL constructs an OAuth authorization URL.
func BuildAuthURL(authEndpoint, clientID, redirectURI, state, codeChallenge, scope string) string {
	u, _ := url.Parse(authEndpoint)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	if scope != "" {
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// RandomBytes generates cryptographically random bytes.
func RandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// RefreshLock — cross-process file locking for token refresh dedup
// Source: auth.ts:2094-2136 — mkdir-based lock with retry
// ---------------------------------------------------------------------------

// RefreshLock implements cross-process file locking using mkdir.
// Source: auth.ts:2094-2136 — prevents concurrent token refresh across processes.
type RefreshLock struct {
	lockDir string
}

// NewRefreshLock creates a refresh lock for the given server key.
// Source: auth.ts:2094-2098 — lockfile path includes sanitized server key.
func NewRefreshLock(configDir, serverKey string) *RefreshLock {
	sanitized := nonAlphaNumPath.ReplaceAllString(serverKey, "_")
	return &RefreshLock{
		lockDir: filepath.Join(configDir, fmt.Sprintf("mcp-refresh-%s.lock", sanitized)),
	}
}

// Acquire attempts to acquire the lock with retry.
// Source: auth.ts:2100-2130 — MAX_LOCK_RETRIES attempts with backoff.
// Returns nil even if lock acquisition fails (best-effort, matching TS).
func (l *RefreshLock) Acquire() error {
	for retry := range MaxLockRetries {
		err := os.Mkdir(l.lockDir, 0700)
		if err == nil {
			return nil
		}
		if !os.IsExist(err) {
			// Source: auth.ts:2124-2128 — non-ELOCKED error, proceed without lock
			return nil
		}
		// Source: auth.ts:2122 — sleep 1000 + random * 1000 ms
		time.Sleep(time.Duration(1000+retry*300) * time.Millisecond)
	}
	// Source: auth.ts:2131-2135 — proceed without lock after max retries
	return nil
}

// Release removes the lock directory.
func (l *RefreshLock) Release() error {
	return os.Remove(l.lockDir)
}

// ---------------------------------------------------------------------------
// NormalizeOAuthErrorBody — Source: auth.ts:147-190
// ---------------------------------------------------------------------------

// nonStandardInvalidGrantAliases maps non-standard OAuth error codes to invalid_grant.
// Source: auth.ts:147-151 — Slack returns invalid_refresh_token, expired_refresh_token, etc.
var nonStandardInvalidGrantAliases = map[string]bool{
	"invalid_refresh_token": true,
	"expired_refresh_token": true,
	"token_expired":         true,
}

// NormalizeOAuthErrorBody checks a 2xx response for non-standard OAuth error bodies.
// Source: auth.ts:157-190 — Slack returns HTTP 200 with {"error":"invalid_grant"} in body.
// Some OAuth servers return HTTP 200 for all responses, signaling errors via JSON body.
// Returns (newStatusCode, newBody) — may rewrite 200→400 for error responses.
func NormalizeOAuthErrorBody(statusCode int, body []byte) (int, []byte) {
	if statusCode >= 400 {
		return statusCode, body
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil {
		return statusCode, body
	}
	// If it has access_token, it's a valid token response
	if _, ok := parsed["access_token"]; ok {
		return statusCode, body
	}
	// Check for error field
	errRaw, ok := parsed["error"]
	if !ok {
		return statusCode, body
	}
	var errCode string
	if err := json.Unmarshal(errRaw, &errCode); err != nil {
		return statusCode, body
	}
	if errCode == "" {
		return statusCode, body
	}
	// Source: auth.ts:177-184 — normalize non-standard codes to invalid_grant
	if nonStandardInvalidGrantAliases[errCode] {
		desc := fmt.Sprintf("Server returned non-standard error code: %s", errCode)
		if descRaw, ok := parsed["error_description"]; ok {
			var descStr string
			if json.Unmarshal(descRaw, &descStr) == nil && descStr != "" {
				desc = descStr
			}
		}
		normalized, _ := json.Marshal(map[string]string{
			"error":             "invalid_grant",
			"error_description": desc,
		})
		return 400, normalized
	}
	// Has error field but not a non-standard code — rewrite to 400
	normalized, _ := json.Marshal(parsed)
	return 400, normalized
}

// ---------------------------------------------------------------------------
// StepUpDetect — Source: auth.ts:1354-1374
// ---------------------------------------------------------------------------

// scopePattern matches scope= value in WWW-Authenticate header, both quoted and unquoted.
// Source: auth.ts:1365 — matches both quoted and unquoted values per RFC 6750 §3.
var scopePattern = regexp.MustCompile(`scope=(?:"([^"]+)"|([^\s,]+))`)

// StepUpDetect checks an HTTP response for 403 insufficient_scope and returns
// the required scope if step-up auth is needed.
// Source: auth.ts:1354-1374 — wrapFetchWithStepUpDetection
func StepUpDetect(resp *http.Response) string {
	if resp.StatusCode != http.StatusForbidden {
		return ""
	}
	wwwAuth := resp.Header.Get("WWW-Authenticate")
	if !strings.Contains(wwwAuth, "insufficient_scope") {
		return ""
	}
	matches := scopePattern.FindStringSubmatch(wwwAuth)
	if len(matches) >= 3 {
		if matches[1] != "" {
			return matches[1]
		}
		return matches[2]
	}
	return ""
}

// ---------------------------------------------------------------------------
// RevokeServerTokens — Source: auth.ts:467-590
// ---------------------------------------------------------------------------

// revokeToken revokes a single token on the OAuth server via RFC 7009.
// Source: auth.ts:381-460 — tries client_id in body first, falls back to Bearer auth.
func revokeToken(ctx context.Context, endpoint, token, tokenTypeHint, clientID, clientSecret, accessToken string) error {
	form := url.Values{}
	form.Set("token", token)
	form.Set("token_type_hint", tokenTypeHint)
	if clientID != "" {
		form.Set("client_id", clientID)
	}
	// Source: auth.ts:411-413 — client_secret for client_secret_post auth method
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("mcp: failed to create revocation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: time.Duration(AuthRequestTimeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("mcp: revocation request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Source: auth.ts:433-460 — fallback to Bearer auth for non-compliant servers
	if resp.StatusCode == http.StatusUnauthorized && accessToken != "" {
		req2, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return fmt.Errorf("mcp: failed to create revocation retry: %w", err)
		}
		req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req2.Header.Set("Authorization", "Bearer "+accessToken)

		resp2, err := client.Do(req2)
		if err != nil {
			return fmt.Errorf("mcp: revocation retry failed: %w", err)
		}
		defer func() { _ = resp2.Body.Close() }()
		if resp2.StatusCode >= 400 {
			return fmt.Errorf("mcp: revocation failed with status %d", resp2.StatusCode)
		}
	}
	return nil
}

// RevokeServerTokens revokes tokens on the OAuth server if a revocation endpoint
// is available. Best-effort — errors do not prevent local token cleanup.
// Source: auth.ts:467-590
func RevokeServerTokens(ctx context.Context, store *FileTokenStore, serverKey, serverURL string, configuredMetadataURL string) error {
	tokenData, err := store.ReadTokens(serverKey)
	if err != nil || tokenData == nil {
		return nil
	}
	if tokenData.AccessToken == "" && tokenData.RefreshToken == "" {
		return nil
	}

	// Source: auth.ts:484-566 — fetch metadata and find revocation endpoint
	var metadata *OAuthMetadata
	if configuredMetadataURL != "" {
		if !strings.HasPrefix(configuredMetadataURL, "https://") {
			return fmt.Errorf("mcp: authServerMetadataUrl must use https://")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, configuredMetadataURL, nil)
		if err == nil {
			req.Header.Set("Accept", "application/json")
			client := &http.Client{Timeout: time.Duration(AuthRequestTimeoutMs) * time.Millisecond}
			resp, err := client.Do(req)
			if err == nil {
				defer func() { _ = resp.Body.Close() }()
				if resp.StatusCode == http.StatusOK {
					var m OAuthMetadata
					if json.NewDecoder(resp.Body).Decode(&m) == nil {
						metadata = &m
					}
				}
			}
		}
	}

	if metadata == nil || metadata.RevocationEndpoint == "" {
		// Source: auth.ts:499-500 — server does not support token revocation
		return store.DeleteTokens(serverKey)
	}

	// Source: auth.ts:524-543 — revoke refresh token first (more important)
	if tokenData.RefreshToken != "" {
		_ = revokeToken(ctx, metadata.RevocationEndpoint, tokenData.RefreshToken, "refresh_token",
			tokenData.ClientID, tokenData.ClientSecret, tokenData.AccessToken)
	}
	// Source: auth.ts:546-564 — then revoke access token
	if tokenData.AccessToken != "" {
		_ = revokeToken(ctx, metadata.RevocationEndpoint, tokenData.AccessToken, "access_token",
			tokenData.ClientID, tokenData.ClientSecret, tokenData.AccessToken)
	}
	// Source: auth.ts:576 — always clear local tokens
	return store.DeleteTokens(serverKey)
}

// ---------------------------------------------------------------------------
// MCPAuthHandler — Source: auth.ts:1376-2360 ClaudeAuthProvider
// ---------------------------------------------------------------------------

// MCPAuthHandler implements OAuth authentication for an MCP server.
// Source: auth.ts:1376 — ClaudeAuthProvider.
// Manages per-server auth state, proactive refresh, step-up auth detection,
// and cross-process refresh dedup via RefreshLock.
type MCPAuthHandler struct {
	serverName   string
	serverConfig McpServerConfig // SSEConfig or HTTPConfig
	tokenStore   *FileTokenStore
	redirectURI  string
	configDir    string // for refresh lock files
	ctx          context.Context // lifetime context, cancelled when handler is discarded

	mu                 sync.Mutex
	codeVerifier       string
	state              string
	metadata           *OAuthMetadata
	refreshInProgress  bool
	pendingStepUpScope string
	refreshFunc        TokenRefreshFunc // optional, for proactive refresh

	onAuthURL   func(url string)
	skipBrowser bool
}

// MCPAuthOption configures an MCPAuthHandler.
type MCPAuthOption func(*MCPAuthHandler)

// WithRedirectURI sets the OAuth redirect URI.
func WithRedirectURI(uri string) MCPAuthOption {
	return func(h *MCPAuthHandler) { h.redirectURI = uri }
}

// WithOnAuthURL sets the callback for when an auth URL is available.
func WithOnAuthURL(fn func(string)) MCPAuthOption {
	return func(h *MCPAuthHandler) { h.onAuthURL = fn }
}

// WithSkipBrowser sets whether to skip opening the browser.
func WithSkipBrowser(skip bool) MCPAuthOption {
	return func(h *MCPAuthHandler) { h.skipBrowser = skip }
}

// WithRefreshFunc sets the token refresh function for proactive refresh.
func WithRefreshFunc(fn TokenRefreshFunc) MCPAuthOption {
	return func(h *MCPAuthHandler) { h.refreshFunc = fn }
}

// WithContext sets the lifetime context for the auth handler.
// When cancelled, proactive refresh operations will abort.
func WithContext(ctx context.Context) MCPAuthOption {
	return func(h *MCPAuthHandler) { h.ctx = ctx }
}

// NewMCPAuthHandler creates a new auth handler for an MCP server.
// Source: auth.ts:1393-1407
func NewMCPAuthHandler(
	serverName string,
	serverConfig McpServerConfig,
	tokenStore *FileTokenStore,
	configDir string,
	opts ...MCPAuthOption,
) *MCPAuthHandler {
	h := &MCPAuthHandler{
		serverName:   serverName,
		serverConfig: serverConfig,
		tokenStore:   tokenStore,
		redirectURI:  BuildRedirectURI(0),
		configDir:    configDir,
		ctx:          context.Background(),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// ServerKey returns the unique credential key for this server.
// Source: auth.ts:325-341
func (h *MCPAuthHandler) ServerKey() string {
	return GetServerKey(h.serverName, h.serverConfig)
}

// State generates and caches an OAuth state parameter.
// Source: auth.ts:1473-1480
func (h *MCPAuthHandler) State() (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == "" {
		s, err := GenerateState()
		if err != nil {
			return "", err
		}
		h.state = s
	}
	return h.state, nil
}

// CodeVerifier generates and caches a PKCE code verifier.
func (h *MCPAuthHandler) CodeVerifier() (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.codeVerifier == "" {
		v, err := GenerateCodeVerifier()
		if err != nil {
			return "", err
		}
		h.codeVerifier = v
	}
	return h.codeVerifier, nil
}

// MarkStepUpPending marks step-up auth as needed for the given scope.
// Source: auth.ts:1468-1471 — causes Tokens() to omit refresh_token.
func (h *MCPAuthHandler) MarkStepUpPending(scope string) {
	h.mu.Lock()
	h.pendingStepUpScope = scope
	h.mu.Unlock()
}

// StepUpScope returns the pending step-up scope if any.
func (h *MCPAuthHandler) StepUpScope() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pendingStepUpScope
}

// SetMetadata caches OAuth server metadata.
// Source: auth.ts:1454-1458
func (h *MCPAuthHandler) SetMetadata(metadata *OAuthMetadata) {
	h.mu.Lock()
	h.metadata = metadata
	h.mu.Unlock()
}

// ClientMetadata returns OAuth client metadata for Dynamic Client Registration.
// Source: auth.ts:1417-1437
func (h *MCPAuthHandler) ClientMetadata() map[string]any {
	md := map[string]any{
		"client_name":                 fmt.Sprintf("Claude Code (%s)", h.serverName),
		"redirect_uris":              []string{h.redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
	h.mu.Lock()
	if h.metadata != nil {
		scope := GetScopeFromMetadata(h.metadata)
		if scope != "" {
			md["scope"] = scope
		}
	}
	h.mu.Unlock()
	return md
}

// Tokens reads stored tokens, checking expiry and triggering proactive refresh.
// Source: auth.ts:1540-1690
func (h *MCPAuthHandler) Tokens() (*OAuthTokens, error) {
	serverKey := h.ServerKey()
	tokenData, err := h.tokenStore.ReadTokens(serverKey)
	if err != nil {
		return nil, err
	}
	if tokenData == nil || tokenData.AccessToken == "" {
		return nil, nil
	}

	expiresIn := time.Until(time.UnixMilli(tokenData.ExpiresAt))

	// Source: auth.ts:1625-1637 — step-up check
	h.mu.Lock()
	pending := h.pendingStepUpScope
	h.mu.Unlock()
	if pending != "" {
		currentScopes := strings.Fields(tokenData.Scope)
		needsStepUp := false
		for s := range strings.FieldsSeq(pending) {
			if !slices.Contains(currentScopes, s) {
				needsStepUp = true
				break
			}
		}
		if needsStepUp {
			// Source: auth.ts:1632-1637 — omit refresh_token so SDK falls through to PKCE
			return &OAuthTokens{
				AccessToken: tokenData.AccessToken,
				TokenType:   "Bearer",
				ExpiresIn:   expiresIn.Seconds(),
				Scope:       tokenData.Scope,
			}, nil
		}
	}

	// Source: auth.ts:1640-1643 — expired without refresh token
	if expiresIn <= 0 && tokenData.RefreshToken == "" {
		return nil, nil
	}

	// Source: auth.ts:1650-1680 — proactive refresh within 5-minute buffer
	if expiresIn <= OAuthTokenExpiryBuffer && tokenData.RefreshToken != "" && pending == "" && h.refreshFunc != nil {
		h.mu.Lock()
		if !h.refreshInProgress {
			h.refreshInProgress = true
			h.mu.Unlock()

			tokens, _ := RefreshAuthorization(
				h.ctx,
				serverKey,
				h.tokenStore,
				h.refreshFunc,
			)

			h.mu.Lock()
			h.refreshInProgress = false
			h.mu.Unlock()

			if tokens != nil {
				return tokens, nil
			}
		} else {
			h.mu.Unlock()
		}
	}

	return &OAuthTokens{
		AccessToken:  tokenData.AccessToken,
		RefreshToken: tokenData.RefreshToken,
		ExpiresIn:    expiresIn.Seconds(),
		Scope:        tokenData.Scope,
		TokenType:    "Bearer",
	}, nil
}

// SaveTokens persists OAuth tokens.
// Source: auth.ts:1718-1755
func (h *MCPAuthHandler) SaveTokens(tokens *OAuthTokens) error {
	serverKey := h.ServerKey()
	tokenData, err := h.tokenStore.ReadTokens(serverKey)
	if err != nil {
		return err
	}
	if tokenData == nil {
		tokenData = &OAuthTokenData{
			ServerName: h.serverName,
			ServerURL:  configURL(h.serverConfig),
		}
	}
	tokenData.AccessToken = tokens.AccessToken
	if tokens.RefreshToken != "" {
		tokenData.RefreshToken = tokens.RefreshToken
	}
	tokenData.ExpiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UnixMilli()
	tokenData.Scope = tokens.Scope
	return h.tokenStore.WriteTokens(serverKey, tokenData)
}

// SaveClientInformation saves client credentials from Dynamic Client Registration.
// Source: auth.ts:1513-1538
func (h *MCPAuthHandler) SaveClientInformation(clientID, clientSecret string) error {
	serverKey := h.ServerKey()
	tokenData, err := h.tokenStore.ReadTokens(serverKey)
	if err != nil {
		return err
	}
	if tokenData == nil {
		tokenData = &OAuthTokenData{
			ServerName: h.serverName,
			ServerURL:  configURL(h.serverConfig),
		}
	}
	tokenData.ClientID = clientID
	tokenData.ClientSecret = clientSecret
	return h.tokenStore.WriteTokens(serverKey, tokenData)
}

// SaveDiscoveryState persists OAuth discovery state.
// Source: auth.ts:1997-2035 — only persists URLs, not full metadata blobs.
func (h *MCPAuthHandler) SaveDiscoveryState(state *OAuthDiscoveryState) error {
	serverKey := h.ServerKey()
	tokenData, err := h.tokenStore.ReadTokens(serverKey)
	if err != nil {
		return err
	}
	if tokenData == nil {
		tokenData = &OAuthTokenData{
			ServerName: h.serverName,
			ServerURL:  configURL(h.serverConfig),
		}
	}
	tokenData.DiscoveryState = state
	return h.tokenStore.WriteTokens(serverKey, tokenData)
}

// DiscoveryState reads stored OAuth discovery state.
// Source: auth.ts:2037-2088
func (h *MCPAuthHandler) DiscoveryState() (*OAuthDiscoveryState, error) {
	serverKey := h.ServerKey()
	tokenData, err := h.tokenStore.ReadTokens(serverKey)
	if err != nil {
		return nil, err
	}
	if tokenData == nil || tokenData.DiscoveryState == nil {
		return nil, nil
	}
	return tokenData.DiscoveryState, nil
}

// InvalidateCredentials removes specified credential data.
// Source: auth.ts:1960-1995
func (h *MCPAuthHandler) InvalidateCredentials(scope InvalidateCredentialsScope) error {
	return InvalidateCredentials(h.tokenStore, h.ServerKey(), scope)
}

// RefreshWithLock refreshes tokens with cross-process lock.
// Source: auth.ts:2090-2359 — lock-based refresh with dedup.
func (h *MCPAuthHandler) RefreshWithLock(ctx context.Context, refreshFunc TokenRefreshFunc) (*OAuthTokens, error) {
	serverKey := h.ServerKey()
	lock := NewRefreshLock(h.configDir, serverKey)

	// Source: auth.ts:2100-2130 — acquire lock (best-effort)
	_ = lock.Acquire()
	defer func() { _ = lock.Release() }()

	// Source: auth.ts:2139-2158 — re-read tokens after acquiring lock
	tokenData, err := h.tokenStore.ReadTokens(serverKey)
	if err != nil {
		return nil, err
	}
	if tokenData != nil && tokenData.ExpiresAt > 0 {
		expiresIn := time.Until(time.UnixMilli(tokenData.ExpiresAt))
		if expiresIn > OAuthTokenExpiryBuffer {
			// Another process already refreshed
			return &OAuthTokens{
				AccessToken:  tokenData.AccessToken,
				RefreshToken: tokenData.RefreshToken,
				ExpiresIn:    expiresIn.Seconds(),
				Scope:        tokenData.Scope,
				TokenType:    "Bearer",
			}, nil
		}
	}

	return RefreshAuthorization(ctx, serverKey, h.tokenStore, refreshFunc)
}

// configURL extracts the URL from an SSEConfig or HTTPConfig.
func configURL(config McpServerConfig) string {
	switch c := config.(type) {
	case *SSEConfig:
		return c.URL
	case *HTTPConfig:
		return c.URL
	default:
		return ""
	}
}
