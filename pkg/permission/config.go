package permission

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// ---------------------------------------------------------------------------
// LoadConfig — source: permissionsLoader.ts:91-133
//
// Reads permission rules from settings.json files across three scopes:
//  1. User:      ~/.gbot/settings.json
//  2. Project:   .gbot/settings.json
//  3. Local:     .gbot/settings.local.json
//
// Rules are appended in scope order (user → project → local).
// Same merge semantics as hooks (concatenation, not replacement).
// ---------------------------------------------------------------------------

// permissionsJSON matches the "permissions" key in a settings.json file.
// Source: permissionsLoader.ts:91-114 — settingsJsonToRules
type permissionsJSON struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
	Ask   []string `json:"ask,omitempty"`
}

// ruleSource identifies where a permission rule originated.
// Aligned with TS PermissionRuleSource: 'userSettings' | 'projectSettings' | 'localSettings'.
type ruleSource string

const (
	sourceUser    ruleSource = "user"
	sourceProject ruleSource = "project"
	sourceLocal   ruleSource = "local"
)

// LoadConfig reads permission rules from user/project/local settings files.
// Source: permissionsLoader.ts:120-133 — loadAllPermissionRulesFromDisk
//
// Returns a flat list of Rules merged from all scopes.
// Each rule carries its Source and ConfigRoot for root-relative matching.
func LoadConfig(userSettingsDir, projectDir string) []Rule {
	var rules []Rule

	// Scope 1: User settings — ConfigRoot = userSettingsDir
	userPath := filepath.Join(userSettingsDir, "settings.json")
	rules = appendRulesFromFile(rules, userPath, sourceUser, userSettingsDir)

	// Scope 2: Project settings — ConfigRoot = projectDir
	projectPath := filepath.Join(projectDir, ".gbot", "settings.json")
	rules = appendRulesFromFile(rules, projectPath, sourceProject, projectDir)

	// Scope 3: Local project settings — ConfigRoot = projectDir
	localPath := filepath.Join(projectDir, ".gbot", "settings.local.json")
	rules = appendRulesFromFile(rules, localPath, sourceLocal, projectDir)

	return rules
}

// appendRulesFromFile reads permission rules from a single settings file.
// Source: permissionsLoader.ts:91-114 — settingsJsonToRules
//
// Iterates deny → ask → allow arrays. Allow rules are logged and skipped.
// Invalid rules are logged and skipped.
func appendRulesFromFile(rules []Rule, path string, source ruleSource, configRoot string) []Rule {
	var raw struct {
		Permissions permissionsJSON `json:"permissions"`
	}
	if err := readPermJSONFile(path, &raw); err != nil {
		return rules
	}
	perm := raw.Permissions

	// Process deny rules
	for _, ruleStr := range perm.Deny {
		rule, err := parseRule(ruleStr, ActionDeny, string(source), configRoot)
		if err != nil {
			slog.Warn("skipping invalid deny rule", "rule", ruleStr, "source", source, "error", err)
			continue
		}
		rules = append(rules, rule)
	}

	// Process ask rules
	for _, ruleStr := range perm.Ask {
		rule, err := parseRule(ruleStr, ActionAsk, string(source), configRoot)
		if err != nil {
			slog.Warn("skipping invalid ask rule", "rule", ruleStr, "source", source, "error", err)
			continue
		}
		rules = append(rules, rule)
	}

	// Process allow rules — log warning, don't create rules
	for _, ruleStr := range perm.Allow {
		slog.Warn("gbot defaults allow; allow rule ignored", "rule", ruleStr, "source", source)
	}

	return rules
}

// parseRule parses a rule string into a Rule with validation.
// Validates wildcard patterns by pre-compiling regex.
func parseRule(ruleStr string, action RuleAction, source string, configRoot string) (Rule, error) {
	rv := ParseRuleValue(ruleStr)

	// Validate: pre-compile regex for wildcard shell rules
	if rv.RuleContent != nil {
		sr := ParseShellRule(*rv.RuleContent)
		if sr.Type == ShellRuleWildcard && sr.re == nil {
			return Rule{}, fmt.Errorf("invalid wildcard pattern %q", *rv.RuleContent)
		}
		// Validate file patterns for deny/ask rules
		if action == ActionDeny || action == ActionAsk {
			if err := ValidateFilePattern(*rv.RuleContent); err != nil {
				return Rule{}, err
			}
		}
	}

	return Rule{
		Value:      rv,
		Action:     action,
		Source:     source,
		ConfigRoot: configRoot,
	}, nil
}

// readPermJSONFile reads and unmarshals a JSON file.
// Returns error if file doesn't exist or is unreadable (silently skipped, matching TS).
func readPermJSONFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err // includes IsNotExist
	}
	return json.Unmarshal(data, target)
}
