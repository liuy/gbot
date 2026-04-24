// Package mcp — config: MCP server configuration loading, policy engine, and dedup.
//
// Source: TS services/mcp/config.ts (1578 lines)
//
// Faithful translation of all config functions. Settings-dependent functions
// accept a McpConfigProvider interface for dependency injection (TS uses global state).
package mcp

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ---------------------------------------------------------------------------
// Types — Source: config.ts + settings/types.ts
// ---------------------------------------------------------------------------

// ValidationError represents a config validation error.
// Source: utils/settings/validation.ts + config.ts parseMcpConfig errors
type ValidationError struct {
	File       string `json:"file,omitempty"`
	Path       string `json:"path"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

// SuppressedServer records a server suppressed during dedup.
// Source: config.ts:229 dedupPluginMcpServers return type
type SuppressedServer struct {
	Name        string
	DuplicateOf string
}

// McpPolicyEntry is a discriminated union for policy allowlist/denylist entries.
// Source: utils/settings/types.ts — McpServerNameEntry | McpServerCommandEntry | McpServerUrlEntry
type McpPolicyEntry interface {
	policyEntryMarker()
}

// McpNameEntry matches by server name.
// Source: settings/types.ts isMcpServerNameEntry
type McpNameEntry struct {
	ServerName string `json:"serverName"`
}

// McpCommandEntry matches by command array (stdio servers).
// Source: settings/types.ts isMcpServerCommandEntry
type McpCommandEntry struct {
	ServerCommand []string `json:"serverCommand"`
}

// McpUrlEntry matches by URL pattern (remote servers).
// Source: settings/types.ts isMcpServerUrlEntry
type McpUrlEntry struct {
	ServerUrl string `json:"serverUrl"`
}

func (*McpNameEntry) policyEntryMarker()    {}
func (*McpCommandEntry) policyEntryMarker() {}
func (*McpUrlEntry) policyEntryMarker()     {}

// UnmarshalMcpPolicyEntry detects entry type from JSON fields and unmarshals
// to the correct concrete type. Source: same pattern as UnmarshalServerConfig.
func UnmarshalMcpPolicyEntry(raw json.RawMessage) (McpPolicyEntry, error) {
	var probe struct {
		ServerName   *string  `json:"serverName"`
		ServerCommand []string `json:"serverCommand"`
		ServerUrl    *string  `json:"serverUrl"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("invalid policy entry: %w", err)
	}

	switch {
	case probe.ServerName != nil:
		var e McpNameEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		return &e, nil
	case len(probe.ServerCommand) > 0:
		var e McpCommandEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		return &e, nil
	case probe.ServerUrl != nil:
		var e McpUrlEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		return &e, nil
	default:
		return nil, fmt.Errorf("invalid policy entry: must have serverName, serverCommand, or serverUrl")
	}
}

// McpPolicySettings holds allowlist/denylist policy entries.
// Source: config.ts getMcpAllowlistSettings / getMcpDenylistSettings
type McpPolicySettings struct {
	AllowedMcpServers []json.RawMessage `json:"allowedMcpServers,omitempty"`
	DeniedMcpServers  []json.RawMessage `json:"deniedMcpServers,omitempty"`
}

// McpConfigProvider supplies MCP configuration from the application's settings system.
// TS reads settings via global state; Go uses dependency injection for testability.
type McpConfigProvider interface {
	// UserMcpServers returns user-scoped MCP server configs (from ~/.gbot/settings.json).
	UserMcpServers() map[string]McpServerConfig
	// LocalMcpServers returns project-local MCP server configs (from .gbot/settings.json).
	LocalMcpServers() map[string]McpServerConfig
	// ProjectDisabledServers returns the list of disabled MCP server names.
	ProjectDisabledServers() []string
	// ProjectEnabledServers returns the list of explicitly enabled MCP server names.
	ProjectEnabledServers() []string
	// PolicyDeniedServers returns denied MCP server policy entries (parsed).
	PolicyDeniedServers() []McpPolicyEntry
	// PolicyAllowedServers returns allowed MCP server policy entries (parsed).
	// Returns nil if no allowlist is configured.
	PolicyAllowedServers() []McpPolicyEntry
	// IsManagedOnly returns true when only managed MCP servers are allowed.
	IsManagedOnly() bool
	// IsPluginOnly returns true when MCP is restricted to plugins only.
	IsPluginOnly() bool
	// SaveProjectDisabledServers saves the disabled servers list.
	SaveProjectDisabledServers(names []string) error
	// SaveProjectEnabledServers saves the enabled servers list.
	SaveProjectEnabledServers(names []string) error
	// SaveUserMcpServers saves user-level MCP server configs.
	SaveUserMcpServers(servers map[string]McpServerConfig) error
	// SaveLocalMcpServers saves project-local MCP server configs.
	SaveLocalMcpServers(servers map[string]McpServerConfig) error
	// ManagedMcpFilePath returns the path to the managed MCP config file.
	ManagedMcpFilePath() string
}

// Compile-time interface checks.
var (
	_ McpPolicyEntry = (*McpNameEntry)(nil)
	_ McpPolicyEntry = (*McpCommandEntry)(nil)
	_ McpPolicyEntry = (*McpUrlEntry)(nil)
)

// ---------------------------------------------------------------------------
// Pure utilities — Source: config.ts:69-212
// ---------------------------------------------------------------------------

// AddScopeToServers adds scope information to each server config.
// Source: config.ts:69-81 addScopeToServers
func AddScopeToServers(
	servers map[string]McpServerConfig,
	scope ConfigScope,
) map[string]ScopedMcpServerConfig {
	if servers == nil {
		return map[string]ScopedMcpServerConfig{}
	}
	result := make(map[string]ScopedMcpServerConfig, len(servers))
	for name, cfg := range servers {
		result[name] = ScopedMcpServerConfig{
			Config: cfg,
			Scope:  scope,
		}
	}
	return result
}

// GetServerCommandArray extracts the command array from a stdio server config.
// Returns nil for non-stdio servers.
// Source: config.ts:137-144 getServerCommandArray
func GetServerCommandArray(cfg McpServerConfig) []string {
	// Non-stdio servers don't have commands.
	// TS: config.type !== undefined && config.type !== 'stdio' → return null
	if t := cfg.GetTransport(); t != TransportStdio && t != "" {
		return nil
	}
	stdio, ok := cfg.(*StdioConfig)
	if !ok {
		return nil
	}
	args := stdio.Args
	if args == nil {
		args = []string{}
	}
	result := make([]string, 0, 1+len(args))
	result = append(result, stdio.Command)
	result = append(result, args...)
	return result
}

// CommandArraysMatch checks if two command arrays are identical.
// Source: config.ts:149-154 commandArraysMatch
func CommandArraysMatch(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// GetServerUrl extracts the URL from a remote server config.
// Returns empty string for stdio/sdk servers.
// Source: config.ts:160-162 getServerUrl
func GetServerUrl(cfg McpServerConfig) string {
	switch c := cfg.(type) {
	case *SSEConfig:
		return c.URL
	case *HTTPConfig:
		return c.URL
	case *WSConfig:
		return c.URL
	case *SSEIDEConfig:
		return c.URL
	case *WSIDEConfig:
		return c.URL
	default:
		return ""
	}
}

// ccrProxyPathMarkers — Source: config.ts:171-174
var ccrProxyPathMarkers = []string{
	"/v2/session_ingress/shttp/mcp/",
	"/v2/ccr-sessions/",
}

// UnwrapCcrProxyUrl extracts the original vendor URL from a CCR proxy URL.
// If the URL is not a CCR proxy URL, returns it unchanged.
// Source: config.ts:182-193 unwrapCcrProxyUrl
func UnwrapCcrProxyUrl(rawURL string) string {
	isProxy := false
	for _, marker := range ccrProxyPathMarkers {
		if strings.Contains(rawURL, marker) {
			isProxy = true
			break
		}
	}
	if !isProxy {
		return rawURL
	}

	// Extract mcp_url query parameter.
	_, query, _ := strings.Cut(rawURL, "?")
	for pair := range strings.SplitSeq(query, "&") {
		if u, ok := strings.CutPrefix(pair, "mcp_url="); ok && u != "" {
			return u
		}
	}
	return rawURL
}

// GetMcpServerSignature computes a dedup signature for an MCP server config.
// Two configs with the same signature are considered "the same server".
// Returns nil for configs with neither command nor URL (sdk type).
// Source: config.ts:202-212 getMcpServerSignature
func GetMcpServerSignature(cfg McpServerConfig) string {
	cmd := GetServerCommandArray(cfg)
	if cmd != nil {
		// TS: `stdio:${jsonStringify(cmd)}`
		b, _ := json.Marshal(cmd)
		return "stdio:" + string(b)
	}
	url := GetServerUrl(cfg)
	if url != "" {
		return "url:" + UnwrapCcrProxyUrl(url)
	}
	return ""
}

// ---------------------------------------------------------------------------
// URL pattern matching — Source: config.ts:320-334
// ---------------------------------------------------------------------------

// UrlPatternToRegex converts a URL pattern with wildcards to a regexp string.
// Supports * as wildcard matching any characters.
// Source: config.ts:320-326 urlPatternToRegex
func UrlPatternToRegex(pattern string) string {
	// Escape regex special characters except *
	escaped := regexp.QuoteMeta(pattern)
	// Replace escaped \* with .*
	regexStr := strings.ReplaceAll(escaped, `\*`, ".*")
	return "^" + regexStr + "$"
}

// UrlMatchesPattern checks if a URL matches a pattern with wildcard support.
// Source: config.ts:331-334 urlMatchesPattern
func UrlMatchesPattern(url, pattern string) bool {
	re, err := regexp.Compile(UrlPatternToRegex(pattern))
	if err != nil {
		return false
	}
	return re.MatchString(url)
}

// ---------------------------------------------------------------------------
// Dedup — Source: config.ts:223-310
// ---------------------------------------------------------------------------

// DedupPluginMcpServers filters plugin MCP servers, dropping any whose signature
// matches a manually-configured server or an earlier-loaded plugin server.
// Manual wins over plugin; between plugins, first-loaded wins.
// Source: config.ts:223-266 dedupPluginMcpServers
func DedupPluginMcpServers(
	pluginServers map[string]ScopedMcpServerConfig,
	manualServers map[string]ScopedMcpServerConfig,
) (servers map[string]ScopedMcpServerConfig, suppressed []SuppressedServer) {
	// Map signature → server name so we can report which server a dup matches.
	manualSigs := make(map[string]string)
	for name, cfg := range manualServers {
		sig := GetMcpServerSignature(cfg.Config)
		if sig != "" {
			if _, exists := manualSigs[sig]; !exists {
				manualSigs[sig] = name
			}
		}
	}

	servers = make(map[string]ScopedMcpServerConfig)
	seenPluginSigs := make(map[string]string)

	for name, cfg := range pluginServers {
		sig := GetMcpServerSignature(cfg.Config)
		if sig == "" {
			// No signature (sdk type) — always include.
			servers[name] = cfg
			continue
		}

		if manualDup, ok := manualSigs[sig]; ok {
			suppressed = append(suppressed, SuppressedServer{
				Name:        name,
				DuplicateOf: manualDup,
			})
			continue
		}

		if pluginDup, ok := seenPluginSigs[sig]; ok {
			suppressed = append(suppressed, SuppressedServer{
				Name:        name,
				DuplicateOf: pluginDup,
			})
			continue
		}

		seenPluginSigs[sig] = name
		servers[name] = cfg
	}
	return
}

// DedupClaudeAiMcpServers filters claude.ai connectors, dropping any whose
// signature matches an enabled manually-configured server.
// Only enabled manual servers count as dedup targets — a disabled manual server
// mustn't suppress its connector twin, or neither runs.
// Source: config.ts:281-310 dedupClaudeAiMcpServers
func DedupClaudeAiMcpServers(
	claudeAiServers map[string]ScopedMcpServerConfig,
	manualServers map[string]ScopedMcpServerConfig,
	disabledChecker func(name string) bool,
) (servers map[string]ScopedMcpServerConfig, suppressed []SuppressedServer) {
	manualSigs := make(map[string]string)
	for name, cfg := range manualServers {
		if disabledChecker(name) {
			continue
		}
		sig := GetMcpServerSignature(cfg.Config)
		if sig != "" {
			if _, exists := manualSigs[sig]; !exists {
				manualSigs[sig] = name
			}
		}
	}

	servers = make(map[string]ScopedMcpServerConfig)
	for name, cfg := range claudeAiServers {
		sig := GetMcpServerSignature(cfg.Config)
		if manualDup, ok := manualSigs[sig]; ok && sig != "" {
			suppressed = append(suppressed, SuppressedServer{
				Name:        name,
				DuplicateOf: manualDup,
			})
			continue
		}
		servers[name] = cfg
	}
	return
}

// ---------------------------------------------------------------------------
// Env expansion for configs — Source: config.ts:556-616 expandEnvVars
// ---------------------------------------------------------------------------

// ExpandConfigEnv expands environment variables in an MCP server config.
// Source: config.ts:556-616 expandEnvVars
func ExpandConfigEnv(cfg McpServerConfig) (expanded McpServerConfig, missingVars []string) {
	expandString := func(s string) string {
		exp, missing := ExpandEnvVarsInString(s)
		missingVars = append(missingVars, missing...)
		return exp
	}

	expandMap := func(m map[string]string) map[string]string {
		if m == nil {
			return nil
		}
		result := make(map[string]string, len(m))
		for k, v := range m {
			result[k] = expandString(v)
		}
		return result
	}

	switch c := cfg.(type) {
	case *StdioConfig:
		args := make([]string, len(c.Args))
		for i, a := range c.Args {
			args[i] = expandString(a)
		}
		return &StdioConfig{
			Command: expandString(c.Command),
			Args:    args,
			Env:     expandMap(c.Env),
		}, dedupStrings(missingVars)

	case *SSEConfig:
		return &SSEConfig{
			URL:     expandString(c.URL),
			Headers: expandMap(c.Headers),
		}, dedupStrings(missingVars)

	case *HTTPConfig:
		return &HTTPConfig{
			URL:     expandString(c.URL),
			Headers: expandMap(c.Headers),
		}, dedupStrings(missingVars)

	case *WSConfig:
		return &WSConfig{
			URL:     expandString(c.URL),
			Headers: expandMap(c.Headers),
		}, dedupStrings(missingVars)

	case *SSEIDEConfig, *WSIDEConfig, *SDKConfig, *ClaudeAIProxyConfig:
		// These types don't need env expansion.
		return cfg, nil

	default:
		return cfg, nil
	}
}

// dedupStrings removes duplicates while preserving order.
// Source: config.ts:614 [...new Set(missingVars)]
func dedupStrings(ss []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(ss))
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// Policy engine — Source: config.ts:340-551
// ---------------------------------------------------------------------------

// IsMcpServerDenied checks if an MCP server is denied by policy.
// Checks name-based, command-based, and URL-based restrictions.
// Source: config.ts:364-408 isMcpServerDenied
func IsMcpServerDenied(serverName string, cfg McpServerConfig, deniedEntries []McpPolicyEntry) bool {
	if len(deniedEntries) == 0 {
		return false
	}

	// Check name-based denial.
	for _, entry := range deniedEntries {
		if ne, ok := entry.(*McpNameEntry); ok && ne.ServerName == serverName {
			return true
		}
	}

	// Check command-based denial (stdio servers) and URL-based denial (remote servers).
	if cfg != nil {
		serverCmd := GetServerCommandArray(cfg)
		if serverCmd != nil {
			for _, entry := range deniedEntries {
				if ce, ok := entry.(*McpCommandEntry); ok && CommandArraysMatch(ce.ServerCommand, serverCmd) {
					return true
				}
			}
		}

		serverUrl := GetServerUrl(cfg)
		if serverUrl != "" {
			for _, entry := range deniedEntries {
				if ue, ok := entry.(*McpUrlEntry); ok && UrlMatchesPattern(serverUrl, ue.ServerUrl) {
					return true
				}
			}
		}
	}

	return false
}

// IsMcpServerAllowedByPolicy checks if an MCP server is allowed by enterprise policy.
// Denylist takes absolute precedence.
// Source: config.ts:417-508 isMcpServerAllowedByPolicy
func IsMcpServerAllowedByPolicy(
	serverName string,
	cfg McpServerConfig,
	deniedEntries []McpPolicyEntry,
	allowedEntries []McpPolicyEntry,
) bool {
	// Denylist takes absolute precedence.
	if IsMcpServerDenied(serverName, cfg, deniedEntries) {
		return false
	}

	// No allowlist restrictions — all allowed.
	if allowedEntries == nil {
		return true
	}

	// Empty allowlist means block all servers.
	if len(allowedEntries) == 0 {
		return false
	}

	// Check if allowlist contains any command-based or URL-based entries.
	hasCommandEntries := false
	hasUrlEntries := false
	for _, entry := range allowedEntries {
		switch entry.(type) {
		case *McpCommandEntry:
			hasCommandEntries = true
		case *McpUrlEntry:
			hasUrlEntries = true
		}
	}

	if cfg != nil {
		serverCmd := GetServerCommandArray(cfg)
		serverUrl := GetServerUrl(cfg)

		if serverCmd != nil {
			// Stdio server.
			if hasCommandEntries {
				for _, entry := range allowedEntries {
					if ce, ok := entry.(*McpCommandEntry); ok && CommandArraysMatch(ce.ServerCommand, serverCmd) {
						return true
					}
				}
				return false
			}
			// No command entries — check name-based.
			for _, entry := range allowedEntries {
				if ne, ok := entry.(*McpNameEntry); ok && ne.ServerName == serverName {
					return true
				}
			}
			return false
		} else if serverUrl != "" {
			// Remote server.
			if hasUrlEntries {
				for _, entry := range allowedEntries {
					if ue, ok := entry.(*McpUrlEntry); ok && UrlMatchesPattern(serverUrl, ue.ServerUrl) {
						return true
					}
				}
				return false
			}
			// No URL entries — check name-based.
			for _, entry := range allowedEntries {
				if ne, ok := entry.(*McpNameEntry); ok && ne.ServerName == serverName {
					return true
				}
			}
			return false
		} else {
			// Unknown server type — check name-based only.
			for _, entry := range allowedEntries {
				if ne, ok := entry.(*McpNameEntry); ok && ne.ServerName == serverName {
					return true
				}
			}
			return false
		}
	}

	// No config provided — check name-based only.
	for _, entry := range allowedEntries {
		if ne, ok := entry.(*McpNameEntry); ok && ne.ServerName == serverName {
			return true
		}
	}
	return false
}

// FilterMcpServersByPolicy filters MCP servers by policy.
// SDK-type servers are exempt from policy filtering.
// Source: config.ts:536-551 filterMcpServersByPolicy
func FilterMcpServersByPolicy(
	configs map[string]ScopedMcpServerConfig,
	deniedEntries []McpPolicyEntry,
	allowedEntries []McpPolicyEntry,
) (allowed map[string]ScopedMcpServerConfig, blocked []string) {
	allowed = make(map[string]ScopedMcpServerConfig)
	for name, cfg := range configs {
		// SDK servers are exempt.
		if _, ok := cfg.Config.(*SDKConfig); ok {
			allowed[name] = cfg
			continue
		}
		if IsMcpServerAllowedByPolicy(name, cfg.Config, deniedEntries, allowedEntries) {
			allowed[name] = cfg
		} else {
			blocked = append(blocked, name)
		}
	}
	return
}

// ---------------------------------------------------------------------------
// Parse — Source: config.ts:1297-1468
// ---------------------------------------------------------------------------

// ParseMcpConfig validates and processes an MCP configuration object.
// Source: config.ts:1297-1377 parseMcpConfig
func ParseMcpConfig(
	configObject json.RawMessage,
	expandVars bool,
	scope ConfigScope,
	filePath string,
) (config *McpJsonConfig, errors []ValidationError) {
	// Parse top-level structure.
	var raw struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(configObject, &raw); err != nil {
		return nil, []ValidationError{{
			File:    filePath,
			Path:    "",
			Message: "MCP config is not a valid JSON",
		}}
	}

	if raw.McpServers == nil {
		return &McpJsonConfig{McpServers: map[string]json.RawMessage{}}, nil
	}

	var allErrors []ValidationError
	validatedServers := make(map[string]json.RawMessage, len(raw.McpServers))

	for name, serverRaw := range raw.McpServers {
		// Parse into McpServerConfig to validate structure.
		cfg, err := UnmarshalServerConfig(serverRaw)
		if err != nil {
			allErrors = append(allErrors, ValidationError{
				File:    filePath,
				Path:    fmt.Sprintf("mcpServers.%s", name),
				Message: fmt.Sprintf("Does not adhere to MCP server configuration schema: %v", err),
			})
			continue
		}

		if expandVars {
			expanded, missingVars := ExpandConfigEnv(cfg)
			if len(missingVars) > 0 {
				allErrors = append(allErrors, ValidationError{
					File:       filePath,
					Path:       fmt.Sprintf("mcpServers.%s", name),
					Message:    fmt.Sprintf("Missing environment variables: %s", strings.Join(missingVars, ", ")),
					Suggestion: fmt.Sprintf("Set the following environment variables: %s", strings.Join(missingVars, ", ")),
				})
			}
			// Re-marshal the expanded config.
			expandedRaw, marshalErr := json.Marshal(expanded)
			if marshalErr != nil {
				allErrors = append(allErrors, ValidationError{
					File:    filePath,
					Path:    fmt.Sprintf("mcpServers.%s", name),
					Message: fmt.Sprintf("Failed to marshal expanded config: %v", marshalErr),
				})
				continue
			}
			validatedServers[name] = expandedRaw
		} else {
			validatedServers[name] = serverRaw
		}
	}

	return &McpJsonConfig{McpServers: validatedServers}, allErrors
}

// ParseMcpConfigFromFilePath reads and validates an MCP config file.
// Source: config.ts:1384-1468 parseMcpConfigFromFilePath
func ParseMcpConfigFromFilePath(
	filePath string,
	expandVars bool,
	scope ConfigScope,
) (config *McpJsonConfig, errors []ValidationError) {
	configContent, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, []ValidationError{{
				File:    filePath,
				Path:    "",
				Message: fmt.Sprintf("MCP config file not found: %s", filePath),
			}}
		}
		return nil, []ValidationError{{
			File:    filePath,
			Path:    "",
			Message: fmt.Sprintf("Failed to read file: %v", err),
		}}
	}

	return ParseMcpConfig(configContent, expandVars, scope, filePath)
}

// ---------------------------------------------------------------------------
// File I/O — Source: config.ts:88-131 writeMcpjsonFile
// ---------------------------------------------------------------------------

// WriteMcpjsonFile writes an MCP config to a .mcp.json file atomically.
// Writes to a temp file, flushes, then renames. Preserves existing file permissions.
// Source: config.ts:88-131 writeMcpjsonFile
func WriteMcpjsonFile(dir string, config *McpJsonConfig) error {
	mcpJsonPath := filepath.Join(dir, ".mcp.json")

	// Read existing file permissions to preserve them.
	existingMode := os.FileMode(0644)
	if info, err := os.Stat(mcpJsonPath); err == nil {
		existingMode = info.Mode().Perm()
	}

	// Marshal config.
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal MCP config: %w", err)
	}

	// Write to temp file.
	tempPath := fmt.Sprintf("%s.tmp.%d", mcpJsonPath, os.Getpid())
	f, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, existingMode)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	writeErr := func(step string, err error) error {
		_ = f.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("failed to %s temp file: %w", step, err)
	}

	if _, err := f.Write(data); err != nil {
		return writeErr("write", err)
	}

	if err := f.Sync(); err != nil {
		return writeErr("sync", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Restore original file permissions before rename.
	if err := os.Chmod(tempPath, existingMode); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("failed to chmod temp file: %w", err)
	}

	// Atomic rename.
	if err := os.Rename(tempPath, mcpJsonPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Scope loaders — Source: config.ts:843-1026
// ---------------------------------------------------------------------------

// GetProjectMcpConfigsFromCwd reads .mcp.json from the given directory only.
// Used by addMcpConfig and removeMcpConfig to modify the local .mcp.json file.
// Source: config.ts:843-881 getProjectMcpConfigsFromCwd
func GetProjectMcpConfigsFromCwd(cwd string) (servers map[string]ScopedMcpServerConfig, errors []ValidationError) {
	mcpJsonPath := filepath.Join(cwd, ".mcp.json")

	config, errs := ParseMcpConfigFromFilePath(mcpJsonPath, true, ScopeProject)

	if config == nil {
		// Missing .mcp.json is expected — filter out "not found" errors.
		var nonMissing []ValidationError
		for _, e := range errs {
			if !strings.HasPrefix(e.Message, "MCP config file not found") {
				nonMissing = append(nonMissing, e)
			}
		}
		return map[string]ScopedMcpServerConfig{}, nonMissing
	}

	parsed, parseErrs := loadMcpServersFromConfig(config)
	if len(parseErrs) > 0 {
		errors = append(errors, parseErrs...)
	}
	return AddScopeToServers(parsed, ScopeProject), errors
}

// GetMcpConfigsByScope collects MCP configs from a specific scope.
// Source: config.ts:888-1026 getMcpConfigsByScope
func GetMcpConfigsByScope(
	scope ConfigScope,
	cwd string,
	provider McpConfigProvider,
) (servers map[string]ScopedMcpServerConfig, errors []ValidationError) {
	switch scope {
	case ScopeProject:
		return getProjectMcpConfigs(cwd)

	case ScopeUser:
		userServers := provider.UserMcpServers()
		if userServers == nil {
			return map[string]ScopedMcpServerConfig{}, nil
		}
		return AddScopeToServers(userServers, ScopeUser), nil

	case ScopeLocal:
		localServers := provider.LocalMcpServers()
		if localServers == nil {
			return map[string]ScopedMcpServerConfig{}, nil
		}
		return AddScopeToServers(localServers, ScopeLocal), nil

	case ScopeEnterprise:
		enterprisePath := provider.ManagedMcpFilePath()
		config, errs := ParseMcpConfigFromFilePath(enterprisePath, true, ScopeEnterprise)
		if config == nil {
			var nonMissing []ValidationError
			for _, e := range errs {
				if !strings.HasPrefix(e.Message, "MCP config file not found") {
					nonMissing = append(nonMissing, e)
				}
			}
			return map[string]ScopedMcpServerConfig{}, nonMissing
		}
		parsed, parseErrs := loadMcpServersFromConfig(config)
		return AddScopeToServers(parsed, ScopeEnterprise), append(errs, parseErrs...)

	default:
		return map[string]ScopedMcpServerConfig{}, nil
	}
}

// getProjectMcpConfigs walks from root to CWD, merging .mcp.json files
// (closer to CWD has higher priority).
// Source: config.ts:909-961 (project case in getMcpConfigsByScope)
func getProjectMcpConfigs(cwd string) (map[string]ScopedMcpServerConfig, []ValidationError) {
	allServers := make(map[string]ScopedMcpServerConfig)
	var allErrors []ValidationError

	// Build list of directories from root to CWD.
	var dirs []string
	current := cwd
	for {
		dirs = append(dirs, current)
		parent := filepath.Dir(current)
		if parent == current {
			break // reached root
		}
		current = parent
	}

	// Process from root downward to CWD (closer files have higher priority).
	for i := len(dirs) - 1; i >= 0; i-- {
		mcpJsonPath := filepath.Join(dirs[i], ".mcp.json")
		config, errs := ParseMcpConfigFromFilePath(mcpJsonPath, true, ScopeProject)

		if config == nil {
			for _, e := range errs {
				if !strings.HasPrefix(e.Message, "MCP config file not found") {
					allErrors = append(allErrors, e)
				}
			}
			continue
		}

		parsed, parseErrs := loadMcpServersFromConfig(config)
		allErrors = append(allErrors, parseErrs...)
		scoped := AddScopeToServers(parsed, ScopeProject)
		maps.Copy(allServers, scoped) // closer files override parent configs
	}

	return allServers, allErrors
}

// loadMcpServersFromConfig parses the raw messages in McpJsonConfig into McpServerConfig values.
func loadMcpServersFromConfig(config *McpJsonConfig) (map[string]McpServerConfig, []ValidationError) {
	servers := make(map[string]McpServerConfig, len(config.McpServers))
	var errs []ValidationError
	for name, raw := range config.McpServers {
		cfg, err := UnmarshalServerConfig(raw)
		if err != nil {
			errs = append(errs, ValidationError{
				Path:    fmt.Sprintf("mcpServers.%s", name),
				Message: fmt.Sprintf("Invalid config: %v", err),
			})
			continue
		}
		servers[name] = cfg
	}
	return servers, errs
}

// ---------------------------------------------------------------------------
// Config mutations — Source: config.ts:625-834
// ---------------------------------------------------------------------------

// ValidServerName checks if a server name contains only allowed characters.
// Source: config.ts:630 name.match(/[^a-zA-Z0-9_-]/)
var validServerNameRegexp = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// AddMcpConfig adds a new MCP server configuration.
// Source: config.ts:625-761 addMcpConfig
func AddMcpConfig(
	name string,
	config McpServerConfig,
	scope ConfigScope,
	cwd string,
	provider McpConfigProvider,
	deniedEntries []McpPolicyEntry,
	allowedEntries []McpPolicyEntry,
) error {
	if !validServerNameRegexp.MatchString(name) {
		return fmt.Errorf("invalid name %q: names can only contain letters, numbers, hyphens, and underscores", name)
	}

	// Check denylist.
	if IsMcpServerDenied(name, config, deniedEntries) {
		return fmt.Errorf("cannot add MCP server %q: server is explicitly blocked by enterprise policy", name)
	}

	// Check allowlist.
	if !IsMcpServerAllowedByPolicy(name, config, deniedEntries, allowedEntries) {
		return fmt.Errorf("cannot add MCP server %q: not allowed by enterprise policy", name)
	}

	switch scope {
	case ScopeProject:
		existing, _ := GetProjectMcpConfigsFromCwd(cwd)
		if _, ok := existing[name]; ok {
			return fmt.Errorf("MCP server %s already exists in .mcp.json", name)
		}
		// Build new config with all existing servers + new one.
		return writeProjectConfig(cwd, existing, name, config)

	case ScopeUser:
		userServers := provider.UserMcpServers()
		if _, ok := userServers[name]; ok {
			return fmt.Errorf("MCP server %s already exists in user config", name)
		}
		userServers[name] = config
		return provider.SaveUserMcpServers(userServers)

	case ScopeLocal:
		localServers := provider.LocalMcpServers()
		if _, ok := localServers[name]; ok {
			return fmt.Errorf("MCP server %s already exists in local config", name)
		}
		localServers[name] = config
		return provider.SaveLocalMcpServers(localServers)

	default:
		return fmt.Errorf("cannot add MCP server to scope: %s", scope)
	}
}

// RemoveMcpConfig removes an MCP server configuration.
// Source: config.ts:769-834 removeMcpConfig
func RemoveMcpConfig(
	name string,
	scope ConfigScope,
	cwd string,
	provider McpConfigProvider,
) error {
	switch scope {
	case ScopeProject:
		existing, _ := GetProjectMcpConfigsFromCwd(cwd)
		if _, ok := existing[name]; !ok {
			return fmt.Errorf("no MCP server found with name: %s in .mcp.json", name)
		}
		// Remove by name, write back.
		return writeProjectConfigExclude(cwd, existing, name)

	case ScopeUser:
		userServers := provider.UserMcpServers()
		if _, ok := userServers[name]; !ok {
			return fmt.Errorf("no user-scoped MCP server found with name: %s", name)
		}
		delete(userServers, name)
		return provider.SaveUserMcpServers(userServers)

	case ScopeLocal:
		localServers := provider.LocalMcpServers()
		if _, ok := localServers[name]; !ok {
			return fmt.Errorf("no project-local MCP server found with name: %s", name)
		}
		delete(localServers, name)
		return provider.SaveLocalMcpServers(localServers)

	default:
		return fmt.Errorf("cannot remove MCP server from scope: %s", scope)
	}
}

// writeProjectConfig writes all servers (including a new one) to .mcp.json.
func writeProjectConfig(cwd string, existing map[string]ScopedMcpServerConfig, newName string, newCfg McpServerConfig) error {
	mcpServers := make(map[string]json.RawMessage)
	for name, scoped := range existing {
		raw, err := json.Marshal(scoped.Config)
		if err != nil {
			continue
		}
		mcpServers[name] = raw
	}
	raw, err := json.Marshal(newCfg)
	if err != nil {
		return fmt.Errorf("failed to marshal new config: %w", err)
	}
	mcpServers[newName] = raw

	return WriteMcpjsonFile(cwd, &McpJsonConfig{McpServers: mcpServers})
}

// writeProjectConfigExclude writes all servers except one to .mcp.json.
func writeProjectConfigExclude(cwd string, existing map[string]ScopedMcpServerConfig, excludeName string) error {
	mcpServers := make(map[string]json.RawMessage)
	for name, scoped := range existing {
		if name == excludeName {
			continue
		}
		raw, err := json.Marshal(scoped.Config)
		if err != nil {
			continue
		}
		mcpServers[name] = raw
	}

	return WriteMcpjsonFile(cwd, &McpJsonConfig{McpServers: mcpServers})
}

// ---------------------------------------------------------------------------
// Enable/disable — Source: config.ts:1528-1578
// ---------------------------------------------------------------------------

// IsMcpServerDisabled checks if an MCP server is disabled.
// Source: config.ts:1528-1536 isMcpServerDisabled
func IsMcpServerDisabled(name string, provider McpConfigProvider) bool {
	return slices.Contains(provider.ProjectDisabledServers(), name)
}

// toggleMembership adds or removes an item from a string slice.
// Source: config.ts:1538-1546 toggleMembership
func toggleMembership(list []string, name string, shouldContain bool) []string {
	contains := slices.Contains(list, name)
	if contains == shouldContain {
		return list
	}
	if shouldContain {
		return append(list, name)
	}
	result := make([]string, 0, len(list))
	for _, s := range list {
		if s != name {
			result = append(result, s)
		}
	}
	return result
}

	// SetMcpServerEnabled enables or disables an MCP server.
	// Source: config.ts:1553-1578 setMcpServerEnabled
	func SetMcpServerEnabled(name string, enabled bool, provider McpConfigProvider) error {
		prev := provider.ProjectDisabledServers()
		next := toggleMembership(prev, name, !enabled)
		// Source: config.ts:1565 — TS uses \`next === prev\` (reference equality).
		// toggleMembership returns the same slice when no change is needed,
		// but Go cannot use reference equality. Compare contents instead.
		if stringSlicesEqual(next, prev) {
			return nil
		}
		return provider.SaveProjectDisabledServers(next)
	}

	// stringSlicesEqual compares two string slices by value.
	func stringSlicesEqual(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

// ---------------------------------------------------------------------------
// Primary orchestration — Source: config.ts:1071-1290
// ---------------------------------------------------------------------------

// GetClaudeCodeMcpConfigs is the primary config loading function.
// Loads all scopes, deduplicates, and applies policy filtering.
// Source: config.ts:1071-1251 getClaudeCodeMcpConfigs
func GetClaudeCodeMcpConfigs(
	cwd string,
	provider McpConfigProvider,
	dynamicServers map[string]ScopedMcpServerConfig,
	pluginServers map[string]ScopedMcpServerConfig,
) (map[string]ScopedMcpServerConfig, []error) {
	deniedEntries := provider.PolicyDeniedServers()
	allowedEntries := provider.PolicyAllowedServers()

	// Check if enterprise config exists.
	enterpriseServers, _ := GetMcpConfigsByScope(ScopeEnterprise, cwd, provider)
	if len(enterpriseServers) > 0 {
		// Enterprise has exclusive control.
		filtered, _ := FilterMcpServersByPolicy(enterpriseServers, deniedEntries, allowedEntries)
		return filtered, nil
	}

	var mcpLocked bool
	if provider.IsPluginOnly() {
		mcpLocked = true
	}

	noop := map[string]ScopedMcpServerConfig{}

	var userServers map[string]ScopedMcpServerConfig
	if mcpLocked {
		userServers = noop
	} else {
		userServers, _ = GetMcpConfigsByScope(ScopeUser, cwd, provider)
	}

	var projectServers map[string]ScopedMcpServerConfig
	if mcpLocked {
		projectServers = noop
	} else {
		projectServers, _ = GetMcpConfigsByScope(ScopeProject, cwd, provider)
	}

	var localServers map[string]ScopedMcpServerConfig
	if mcpLocked {
		localServers = noop
	} else {
		localServers, _ = GetMcpConfigsByScope(ScopeLocal, cwd, provider)
	}

	// Dedup plugin servers against manually-configured ones.
	// Only enabled servers that pass policy are valid dedup targets.
	enabledManualServers := make(map[string]ScopedMcpServerConfig)
	for name, cfg := range mergeMaps(userServers, projectServers, localServers, dynamicServers) {
		if !IsMcpServerDisabled(name, provider) &&
			IsMcpServerAllowedByPolicy(name, cfg.Config, deniedEntries, allowedEntries) {
			enabledManualServers[name] = cfg
		}
	}

	// Split enabled/disabled plugin servers.
	enabledPluginServers := make(map[string]ScopedMcpServerConfig)
	for name, cfg := range pluginServers {
		if IsMcpServerDisabled(name, provider) ||
			!IsMcpServerAllowedByPolicy(name, cfg.Config, deniedEntries, allowedEntries) {
			continue
		}
		enabledPluginServers[name] = cfg
	}

	dedupedPlugins, _ := DedupPluginMcpServers(enabledPluginServers, enabledManualServers)

	// Merge with precedence: plugin < user < project < local.
	configs := make(map[string]ScopedMcpServerConfig)
	maps.Copy(configs, dedupedPlugins)
	maps.Copy(configs, userServers)
	maps.Copy(configs, projectServers)
	maps.Copy(configs, localServers)

	// Apply policy filtering.
	filtered := make(map[string]ScopedMcpServerConfig)
	for name, cfg := range configs {
		if IsMcpServerAllowedByPolicy(name, cfg.Config, deniedEntries, allowedEntries) {
			filtered[name] = cfg
		}
	}

	return filtered, nil
}

// GetMcpConfigByName looks up a server by name across all scopes.
// Source: config.ts:1033-1060 getMcpConfigByName
func GetMcpConfigByName(
	name string,
	cwd string,
	provider McpConfigProvider,
) *ScopedMcpServerConfig {
	// Enterprise first.
	enterprise, _ := GetMcpConfigsByScope(ScopeEnterprise, cwd, provider)
	if s, ok := enterprise[name]; ok {
		return &s
	}
	// Then local, project, user.
	local, _ := GetMcpConfigsByScope(ScopeLocal, cwd, provider)
	if s, ok := local[name]; ok {
		return &s
	}
	project, _ := GetMcpConfigsByScope(ScopeProject, cwd, provider)
	if s, ok := project[name]; ok {
		return &s
	}
	user, _ := GetMcpConfigsByScope(ScopeUser, cwd, provider)
	if s, ok := user[name]; ok {
		return &s
	}
	return nil
}

// DoesEnterpriseMcpConfigExist checks if an enterprise MCP config file exists and is valid.
// Source: config.ts:1470-1477 doesEnterpriseMcpConfigExist
func DoesEnterpriseMcpConfigExist(cwd string, provider McpConfigProvider) bool {
	path := provider.ManagedMcpFilePath()
	config, _ := ParseMcpConfigFromFilePath(path, true, ScopeEnterprise)
	return config != nil
}

// AreMcpConfigsAllowedWithEnterpriseMcpConfig checks if all configs are allowed alongside
// enterprise MCP config. Only SDK servers with specific names are allowed.
// Source: config.ts:1494-1504 areMcpConfigsAllowedWithEnterpriseMcpConfig
func AreMcpConfigsAllowedWithEnterpriseMcpConfig(configs map[string]ScopedMcpServerConfig) bool {
	for _, cfg := range configs {
		if _, ok := cfg.Config.(*SDKConfig); !ok {
			return false
		}
		// TS checks c.name === 'claude-vscode' but SDKConfig doesn't have a name field in our types.
		// For now, allow all SDK configs. This can be tightened later.
	}
	return true
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mergeMaps merges multiple maps, later maps override earlier ones.
func mergeMaps(ms ...map[string]ScopedMcpServerConfig) map[string]ScopedMcpServerConfig {
	result := make(map[string]ScopedMcpServerConfig)
	for _, m := range ms {
		maps.Copy(result, m)
	}
	return result
}

