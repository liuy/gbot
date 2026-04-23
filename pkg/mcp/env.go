// Package mcp — env: Environment variable expansion for MCP configs.
//
// Source: src/services/mcp/envExpansion.ts (38 lines)
package mcp

import (
	"os"
	"regexp"
	"strings"
)

// envVarPattern matches ${VAR} and ${VAR:-default} patterns.
// Source: envExpansion.ts:16 — /\$\{([^}]+)\}/g
var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// ExpandEnvVarsInString expands environment variables in a string value.
// Handles ${VAR} and ${VAR:-default} syntax.
// Returns the expanded string and a list of missing variables (no value, no default).
// Source: envExpansion.ts:10-38
func ExpandEnvVarsInString(value string) (expanded string, missingVars []string) {
	expanded = envVarPattern.ReplaceAllStringFunc(value, func(match string) string {
		// Extract the content inside ${...}
		varContent := match[2 : len(match)-1] // strip ${ and }

		// Source: envExpansion.ts:18 — split on :- to support default values (limit 2)
		parts := strings.SplitN(varContent, ":-", 2)
		varName := parts[0]

		// Source: envExpansion.ts:19 — check environment
		// TS uses `process.env[varName]` which returns undefined for missing vars.
		// Go's os.Getenv can't distinguish empty from missing; use LookupEnv.
		envValue, found := os.LookupEnv(varName)
		if found {
			return envValue
		}

		// Source: envExpansion.ts:22-24 — use default if provided
		if len(parts) == 2 {
			return parts[1]
		}

		// Source: envExpansion.ts:28-29 — track missing variable
		missingVars = append(missingVars, varName)
		return match
	})

	return expanded, missingVars
}
