// Package agent provides built-in agent definitions and tool filtering for the Agent tool.
//
// Source reference: tools/AgentTool/loadAgentsDir.ts, utils/markdownConfigLoader.ts
package agent

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"maps"
	"cmp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Loader — source: loadAgentsDir.ts:296-393 — getAgentDefinitionsWithOverrides
// ---------------------------------------------------------------------------

// FailedFile records a file that failed to parse as an agent definition.
// Source: loadAgentsDir.ts:189 — failedFiles in AgentDefinitionsResult
type FailedFile struct {
	Path  string
	Error string
}

// Loader discovers, parses, and resolves agent definitions from built-in,
// user, and project directories.
//
// Source: loadAgentsDir.ts:296-393 — getAgentDefinitionsWithOverrides (memoized)
type Loader struct {
	mu     sync.RWMutex
	once   sync.Once
	cwd    string
	cached []*types.AgentDefinition // merged: built-in + custom + override-resolved
	failed []FailedFile
}

// NewLoader creates a Loader without triggering discovery.
// Call Load() or rely on lazy loading via ensureLoaded().
func NewLoader(cwd string) *Loader {
	return &Loader{cwd: cwd}
}

// ensureLoaded performs lazy loading on first access.
// Source: analogous to TS memoize on getAgentDefinitionsWithOverrides
func (l *Loader) ensureLoaded() {
	l.once.Do(func() {
		l.mu.Lock()
		l.load()
		l.mu.Unlock()
	})
}

// Load discovers all agent files, parses them, and resolves overrides.
// Source: loadAgentsDir.ts:296-393 — getAgentDefinitionsWithOverrides
func (l *Loader) Load() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
}

// load is the internal implementation (caller must hold write lock).
func (l *Loader) load() {
	// Source: loadAgentsDir.ts:308 — loadMarkdownFilesForSubdir('agents', cwd)
	userFiles := loadMarkdownFiles(userAgentDir(), types.AgentSourceUserSettings)
	projectFiles := loadMarkdownFiles(projectAgentDir(l.cwd), types.AgentSourceProjectSettings)

	var allFiles []markdownFileEntry
	allFiles = append(allFiles, userFiles...)
	allFiles = append(allFiles, projectFiles...)

	// Deduplicate by device:inode (symlink hardlink detection)
	// Source: markdownConfigLoader.ts:380-407 — getFileIdentity deduplication
	allFiles = deduplicateFiles(allFiles)

	// Parse each markdown file into an agent definition
	// Source: loadAgentsDir.ts:311-342 — map + filter nulls
	var customAgents []*types.AgentDefinition
	var failed []FailedFile

	for _, f := range allFiles {
		agent := parseAgentFromMarkdown(f.filePath, f.baseDir, f.frontmatter, f.content, f.source)
		if agent == nil {
			// Skip non-agent markdown files silently (reference docs).
			// Only report errors for files that look like agent attempts.
			// Source: loadAgentsDir.ts:320-339
			if _, hasName := f.frontmatter["name"]; hasName {
				failed = append(failed, FailedFile{
					Path:  f.filePath,
					Error: getParseError(f.frontmatter),
				})
			}
			continue
		}
		customAgents = append(customAgents, agent)
	}

	// Get built-in agents
	// Source: loadAgentsDir.ts:357 — getBuiltInAgents()
	var allAgents []*types.AgentDefinition
	for _, def := range builtInAgents {
		allAgents = append(allAgents, def)
	}
	allAgents = append(allAgents, customAgents...)

	// Resolve overrides by priority
	// Source: loadAgentsDir.ts:365 — getActiveAgentsFromList
	activeAgents := getActiveAgentsFromList(allAgents)

	// Sort by AgentType
	// Source: loadAgentsDir.ts:220 — Array.from returns insertion-order
	slices.SortFunc(activeAgents, func(a, b *types.AgentDefinition) int { return cmp.Compare(a.AgentType, b.AgentType) })

	l.cached = activeAgents
	l.failed = failed
}

// Reload clears the cache and reloads.
func (l *Loader) Reload() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cached = nil
	l.failed = nil
	l.once = sync.Once{} // reset so ensureLoaded can re-trigger
	l.load()
}

// Get returns the agent definition for the given type.
// Tries exact match first, then case-insensitive fallback.
// Source: builtInAgents lookup pattern + custom agents
func (l *Loader) Get(agentType string) *types.AgentDefinition {
	l.ensureLoaded()
	l.mu.RLock()
	defer l.mu.RUnlock()

	if agentType == "" {
		agentType = "General"
	}

	// Exact match first
	for _, def := range l.cached {
		if def.AgentType == agentType {
			return def
		}
	}

	// Case-insensitive fallback
	// Source: existing GetAgentDefinition pattern
	lower := strings.ToLower(agentType)
	for _, def := range l.cached {
		if strings.ToLower(def.AgentType) == lower {
			return def
		}
	}

	return nil
}

// ListAll returns all active (override-resolved) agent definitions sorted by name.
func (l *Loader) ListAll() []*types.AgentDefinition {
	l.ensureLoaded()
	l.mu.RLock()
	defer l.mu.RUnlock()

	result := make([]*types.AgentDefinition, len(l.cached))
	copy(result, l.cached)
	return result
}

// FailedFiles returns the list of files that failed to parse.
func (l *Loader) FailedFiles() []FailedFile {
	l.ensureLoaded()
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.failed
}

// ---------------------------------------------------------------------------
// Directory helpers
// Source: markdownConfigLoader.ts:234-289 — getProjectDirsUpToHome (simplified)
// ---------------------------------------------------------------------------

// userAgentDir returns the user-level agent directory: ~/.gbot/agents/
// Source: markdownConfigLoader.ts:303 — userDir = join(getClaudeConfigHomeDir(), subdir)
func userAgentDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gbot", "agents")
}

// projectAgentDir returns the project-level agent directory using git root.
// Source: markdownConfigLoader.ts:234-289 — getProjectDirsUpToHome (simplified)
//
// We use `git rev-parse --show-toplevel` to find the git root instead of
// walking up the directory tree (TS walks cwd→git root checking each level).
func projectAgentDir(cwd string) string {
	gitRoot := findGitRoot(cwd)
	if gitRoot == "" {
		// Not in a git repo — check cwd directly
		return filepath.Join(cwd, ".gbot", "agents")
	}
	return filepath.Join(gitRoot, ".gbot", "agents")
}

// findGitRoot returns the git repository root for the given directory.
// Returns empty string if not in a git repo.
// Source: utils/git.ts — findGitRoot
func findGitRoot(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ---------------------------------------------------------------------------
// File discovery
// Source: markdownConfigLoader.ts:546-596 — loadMarkdownFiles
// ---------------------------------------------------------------------------

// markdownFileEntry holds a parsed markdown file with source metadata.
// Source: markdownConfigLoader.ts:40-46 — MarkdownFile
type markdownFileEntry struct {
	filePath    string
	baseDir     string
	frontmatter map[string]any
	content     string
	source      types.AgentSource
}

// loadMarkdownFiles reads all .md files from a directory and parses their frontmatter.
// Source: markdownConfigLoader.ts:546-596 — loadMarkdownFiles
//
// Returns empty slice if directory doesn't exist (fail open).
// Skips files > maxFrontmatterFileSize.
func loadMarkdownFiles(dir string, source types.AgentSource) []markdownFileEntry {
	if dir == "" {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		// Directory doesn't exist or is inaccessible — fail open
		return nil
	}

	var results []markdownFileEntry
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		filePath := filepath.Join(dir, entry.Name())

		// Size check — skip files larger than maxFrontmatterFileSize
		// Source: frontmatterParser.ts — maxFrontmatterFileSize constant
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Size() > maxFrontmatterFileSize {
			slog.Warn("agent: skipping file: too large", "path", filePath, "size", info.Size())
			continue
		}

		rawContent, err := os.ReadFile(filePath)
		if err != nil {
			slog.Warn("agent: failed to read file", "path", filePath, "error", err)
			continue
		}

		parsed := ParseFrontmatter(string(rawContent), filePath)

		results = append(results, markdownFileEntry{
			filePath:    filePath,
			baseDir:     dir,
			frontmatter: parsed.Frontmatter,
			content:     parsed.Content,
			source:      source,
		})
	}

	return results
}

// ---------------------------------------------------------------------------
// Symlink deduplication
// Source: markdownConfigLoader.ts:142-172 — getFileIdentity
// Source: markdownConfigLoader.ts:380-407 — dedup loop
// ---------------------------------------------------------------------------

// fileIdentity returns a unique identifier for a file based on device ID and inode.
// Returns empty string if the file can't be identified (fail open).
// Source: markdownConfigLoader.ts:159-172 — getFileIdentity
func fileIdentity(path string) string {
	stat, err := os.Stat(path)
	if err != nil {
		return ""
	}
	sys, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	// Skip unreliable identities (NFS, FUSE report dev=0, ino=0)
	// Source: markdownConfigLoader.ts:164-167
	if sys.Dev == 0 && sys.Ino == 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", sys.Dev, sys.Ino)
}

// deduplicateFiles removes files that resolve to the same physical file (same inode).
// Source: markdownConfigLoader.ts:380-407
func deduplicateFiles(files []markdownFileEntry) []markdownFileEntry {
	seen := make(map[string]types.AgentSource)
	var result []markdownFileEntry

	for _, f := range files {
		id := fileIdentity(f.filePath)
		if id == "" {
			// Can't identify — include it (fail open)
			// Source: markdownConfigLoader.ts:393-396
			result = append(result, f)
			continue
		}
		if _, exists := seen[id]; exists {
			// Duplicate — skip
			// Source: markdownConfigLoader.ts:399-403
			continue
		}
		seen[id] = f.source
		result = append(result, f)
	}

	return result
}

// ---------------------------------------------------------------------------
// Override resolution
// Source: loadAgentsDir.ts:193-221 — getActiveAgentsFromList
// ---------------------------------------------------------------------------

// agentSourcePriority defines the order in which sources override each other.
// Later entries override earlier ones.
// Source: loadAgentsDir.ts:196-210 — agentGroups ordering
var agentSourcePriority = []types.AgentSource{
	types.AgentSourceBuiltIn,
	types.AgentSourcePlugin,
	types.AgentSourceUserSettings,
	types.AgentSourceProjectSettings,
	types.AgentSourceFlagSettings,
	types.AgentSourcePolicySettings,
}

// getActiveAgentsFromList resolves agent overrides by priority.
// Agents with the same agentType from a higher-priority source override lower ones.
// Override matching is case-sensitive (exact match on agentType).
// Source: loadAgentsDir.ts:193-221 — getActiveAgentsFromList
func getActiveAgentsFromList(allAgents []*types.AgentDefinition) []*types.AgentDefinition {
	// Group agents by source priority
	groups := make(map[types.AgentSource][]*types.AgentDefinition)
	for _, agent := range allAgents {
		groups[agent.Source] = append(groups[agent.Source], agent)
	}

	// Iterate in priority order — later entries overwrite earlier by agentType
	// Source: loadAgentsDir.ts:212-218 — for agents of agentGroups → agentMap.set
	agentMap := make(map[string]*types.AgentDefinition)
	for _, source := range agentSourcePriority {
		for _, agent := range groups[source] {
	if existing, ok := agentMap[agent.AgentType]; ok {
				slog.Info("agent: overridden by higher priority source", "type", agent.AgentType, "existing", existing.Source, "new", source)
			}
			agentMap[agent.AgentType] = agent
		}
	}

	// Return all resolved agents
	return slices.Collect(maps.Values(agentMap))
}

// ---------------------------------------------------------------------------
// Agent parsing from markdown
// Source: loadAgentsDir.ts:541-755 — parseAgentFromMarkdown
// ---------------------------------------------------------------------------

// parseAgentFromMarkdown parses an agent definition from a markdown file's frontmatter and body.
// Returns nil if the file is not a valid agent definition (silently skips reference docs).
// Source: loadAgentsDir.ts:541-755 — parseAgentFromMarkdown
func parseAgentFromMarkdown(
	filePath string,
	baseDir string,
	frontmatter map[string]any,
	content string,
	source types.AgentSource,
) *types.AgentDefinition {
	// Validate required fields
	// Source: loadAgentsDir.ts:549-562
	agentType, ok := frontmatter["name"].(string)
	if !ok || agentType == "" {
		return nil
	}

	whenToUse, ok := frontmatter["description"].(string)
	if !ok || whenToUse == "" {
		slog.Warn("agent: missing required 'description' in frontmatter", "path", filePath)
		return nil
	}

	// Unescape newlines in whenToUse that were escaped for YAML parsing
	// Source: loadAgentsDir.ts:565
	whenToUse = strings.ReplaceAll(whenToUse, "\\n", "\n")

	// Parse optional fields
	def := &types.AgentDefinition{
		AgentType: agentType,
		WhenToUse: whenToUse,
		Source:    source,
		BaseDir:   baseDir,
	}

	// Filename without .md extension
	// Source: loadAgentsDir.ts:657 — basename(filePath, '.md')
	def.Filename = strings.TrimSuffix(filepath.Base(filePath), ".md")

	// System prompt from body content (trimmed)
	// Source: loadAgentsDir.ts:713 — systemPrompt = content.trim()
	systemPrompt := strings.TrimSpace(content)
	def.SystemPrompt = func() string { return systemPrompt }

	applyOptionalFields(def, frontmatter, filePath)

	return def
}

// applyOptionalFields parses all optional frontmatter fields into def.
// Source: loadAgentsDir.ts:568-738 — field-by-field parsing
func applyOptionalFields(def *types.AgentDefinition, frontmatter map[string]any, filePath string) {
	// Parse model
	// Source: loadAgentsDir.ts:568-573
	if modelRaw, ok := frontmatter["model"].(string); ok {
		trimmed := strings.TrimSpace(modelRaw)
		if trimmed != "" {
			if strings.ToLower(trimmed) == "inherit" {
				def.Model = "inherit"
			} else {
				def.Model = trimmed
			}
		}
	}

	// Parse tools
	// Source: loadAgentsDir.ts:660 — parseAgentToolsFromFrontmatter(frontmatter['tools'])
	if toolsValue, exists := frontmatter["tools"]; exists {
		def.Tools = parseAgentToolsFromFrontmatter(toolsValue)
	}

	// Parse disallowedTools
	// Source: loadAgentsDir.ts:677-681
	if disallowedValue, exists := frontmatter["disallowedTools"]; exists {
		def.DisallowedTools = parseAgentToolsFromFrontmatter(disallowedValue)
	}

	// Parse skills
	// Source: loadAgentsDir.ts:684 — parseSlashCommandToolsFromFrontmatter
	if skillsValue, exists := frontmatter["skills"]; exists {
		def.Skills = parseSlashCommandToolsFromFrontmatter(skillsValue)
	}

	// Parse color
	// Source: loadAgentsDir.ts:735-737 — AGENT_COLORS validation
	if color, ok := frontmatter["color"].(string); ok && color != "" {
		def.Color = color
	}

	// Parse effort
	// Source: loadAgentsDir.ts:624-632 — parseEffortValue
	if effortRaw, exists := frontmatter["effort"]; exists {
		effort := parseEffortValue(effortRaw)
		if effort != "" {
			def.Effort = effort
		} else {
			slog.Warn("agent: invalid effort value", "path", filePath, "value", effortRaw)
		}
	}

	// Parse permissionMode
	// Source: loadAgentsDir.ts:635-645
	if pmRaw, ok := frontmatter["permissionMode"].(string); ok && pmRaw != "" {
		if isValidPermissionMode(pmRaw) {
			def.PermissionModeField = types.PermissionMode(pmRaw)
		} else {
			slog.Warn("agent: invalid permissionMode", "path", filePath, "value", pmRaw)
		}
	}

	// Parse maxTurns
	// Source: loadAgentsDir.ts:648-654 — parsePositiveIntFromFrontmatter
	if maxTurnsRaw, exists := frontmatter["maxTurns"]; exists {
		maxTurns := parsePositiveIntFromFrontmatter(maxTurnsRaw)
		if maxTurns != nil {
			def.MaxTurns = *maxTurns
		} else {
			slog.Warn("agent: invalid maxTurns", "path", filePath, "value", maxTurnsRaw)
		}
	}

	// Parse background
	// Source: loadAgentsDir.ts:576-591
	if bgRaw, exists := frontmatter["background"]; exists {
		switch v := bgRaw.(type) {
		case bool:
			if v {
				def.Background = true
			}
		case string:
			if v == "true" {
				def.Background = true
			}
		default:
			slog.Warn("agent: invalid background value", "path", filePath, "value", bgRaw)
		}
	}

	// Parse memory
	// Source: loadAgentsDir.ts:593-605
	if memRaw, ok := frontmatter["memory"].(string); ok && memRaw != "" {
		if isValidMemoryScope(memRaw) {
			def.Memory = memRaw
		} else {
			slog.Warn("agent: invalid memory value", "path", filePath, "value", memRaw)
		}
	}

	// Parse isolation
	// Source: loadAgentsDir.ts:607-621 (simplified — no 'remote' mode in external builds)
	if isoRaw, ok := frontmatter["isolation"].(string); ok && isoRaw != "" {
		if isoRaw == "worktree" {
			def.Isolation = isoRaw
		} else {
			slog.Warn("agent: invalid isolation value", "path", filePath, "value", isoRaw)
		}
	}

	// Parse initialPrompt
	// Source: loadAgentsDir.ts:686-689
	if ipRaw, ok := frontmatter["initialPrompt"].(string); ok {
		if trimmed := strings.TrimSpace(ipRaw); trimmed != "" {
			def.InitialPrompt = ipRaw
		}
	}

	// Parse requiredMcpServers
	// Source: loadAgentsDir.ts:693-708 (simplified — no Zod validation, just string array)
	if mcpRaw, ok := frontmatter["requiredMcpServers"].([]any); ok {
		var servers []string
		for _, item := range mcpRaw {
			if s, ok := item.(string); ok {
				servers = append(servers, s)
			}
		}
		if len(servers) > 0 {
			def.RequiredMcpServers = servers
		}
	}

	// criticalSystemReminder_EXPERIMENTAL
	if csr, ok := frontmatter["criticalSystemReminder_EXPERIMENTAL"].(string); ok && csr != "" {
		def.CriticalSystemReminder = csr
	}
}

// ---------------------------------------------------------------------------
// Tool list parsing
// Source: utils/markdownConfigLoader.ts:77-106 — parseToolListString
// Source: utils/markdownConfigLoader.ts:113-126 — parseAgentToolsFromFrontmatter
// Source: utils/permissions/permissionSetup.ts:813-862 — parseToolListFromCLI
// ---------------------------------------------------------------------------

// parseToolListFromCLI splits tool name strings by comma and space,
// respecting parentheses (e.g. "Bash(command)" stays together).
// Source: permissionSetup.ts:813-862 — parseToolListFromCLI
func parseToolListFromCLI(tools []string) []string {
	if len(tools) == 0 {
		return nil
	}

	var result []string
	for _, toolString := range tools {
		if toolString == "" {
			continue
		}

		var current strings.Builder
		isInParens := false

		for _, char := range toolString {
			switch char {
			case '(':
				isInParens = true
				current.WriteRune(char)
			case ')':
				isInParens = false
				current.WriteRune(char)
			case ',':
				if isInParens {
					current.WriteRune(char)
				} else {
					if trimmed := strings.TrimSpace(current.String()); trimmed != "" {
						result = append(result, trimmed)
					}
					current.Reset()
				}
			case ' ':
				if isInParens {
					current.WriteRune(char)
				} else if strings.TrimSpace(current.String()) != "" {
					result = append(result, strings.TrimSpace(current.String()))
					current.Reset()
				}
			default:
				current.WriteRune(char)
			}
		}

		// Flush remaining
		if trimmed := strings.TrimSpace(current.String()); trimmed != "" {
			result = append(result, trimmed)
		}
	}

	return result
}

// parseToolListString parses tools from frontmatter, supporting both string and array formats.
// Returns nil for missing/null (caller decides default), empty slice for falsy/empty.
// Source: markdownConfigLoader.ts:77-106 — parseToolListString
func parseToolListString(toolsValue any) []string {
	// Return nil for missing/null — let caller decide the default
	// Source: markdownConfigLoader.ts:79-81
	if toolsValue == nil {
		return nil
	}

	// Empty string or other falsy values mean no tools
	// Source: markdownConfigLoader.ts:84-86
	switch v := toolsValue.(type) {
	case string:
		if v == "" {
			return []string{}
		}
		// Single string — wrap in array for parseToolListFromCLI
		// Source: markdownConfigLoader.ts:89-90
		parsed := parseToolListFromCLI([]string{v})
		if slices.Contains(parsed, "*") {
			return []string{"*"}
		}
		return parsed
	case []any:
		// Filter to strings only
		// Source: markdownConfigLoader.ts:91-95
		var strTools []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				strTools = append(strTools, s)
			}
		}
		if len(strTools) == 0 {
			return []string{}
		}
		parsed := parseToolListFromCLI(strTools)
		if slices.Contains(parsed, "*") {
			return []string{"*"}
		}
		return parsed
	default:
		return []string{}
	}
}

// parseAgentToolsFromFrontmatter parses tools from agent frontmatter.
// Missing field = nil (all tools), Empty field = [] (no tools), "*" = nil (all tools).
// Source: markdownConfigLoader.ts:113-126 — parseAgentToolsFromFrontmatter
func parseAgentToolsFromFrontmatter(toolsValue any) []string {
	// For agents: undefined/nil → nil (all tools)
	// Source: markdownConfigLoader.ts:117-119
	if toolsValue == nil {
		return nil
	}
	parsed := parseToolListString(toolsValue)
	if len(parsed) == 0 {
		return []string{} // no tools
	}
	// If parsed contains '*', return nil (all tools)
	// Source: markdownConfigLoader.ts:121-124
	if slices.Contains(parsed, "*") {
		return nil
	}
	return parsed
}

// parseSlashCommandToolsFromFrontmatter parses skills/tools from frontmatter.
// Missing or empty field = [] (no tools).
// Source: markdownConfigLoader.ts:132-140 — parseSlashCommandToolsFromFrontmatter
func parseSlashCommandToolsFromFrontmatter(toolsValue any) []string {
	parsed := parseToolListString(toolsValue)
	if parsed == nil {
		return []string{}
	}
	return parsed
}

// ---------------------------------------------------------------------------
// Helper parsing functions
// ---------------------------------------------------------------------------

// parsePositiveIntFromFrontmatter parses a positive integer from frontmatter.
// Handles both number and string representations.
// Returns nil if invalid or not provided.
// Source: frontmatterParser.ts:275-289 — parsePositiveIntFromFrontmatter
func parsePositiveIntFromFrontmatter(value any) *int {
	if value == nil {
		return nil
	}

	var parsed int
	switch v := value.(type) {
	case int:
		parsed = v
	case float64:
		parsed = int(v)
	case string:
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil
		}
		parsed = n
	default:
		return nil
	}

	if parsed > 0 {
		return &parsed
	}
	return nil
}

// parseEffortValue parses an effort value from frontmatter.
// Supports string levels ("low","medium","high","max") and integers.
// Returns empty string if invalid.
// Source: loadAgentsDir.ts:624-632 — parseEffortValue
func parseEffortValue(value any) string {
	switch v := value.(type) {
	case string:
		lower := strings.ToLower(v)
		switch lower {
		case "low", "medium", "high", "max":
			return lower
		}
		// Try as integer string
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return v
		}
		return ""
	case int:
		if v > 0 {
			return strconv.Itoa(v)
		}
		return ""
	case float64:
		if v > 0 && v == float64(int(v)) {
			return strconv.Itoa(int(v))
		}
		return ""
	default:
		return ""
	}
}

// isValidPermissionMode checks if the string is a valid PermissionMode constant.
// Source: loadAgentsDir.ts:638-645 — isValidPermissionMode check
func isValidPermissionMode(mode string) bool {
	switch types.PermissionMode(mode) {
	case types.PermissionModeAcceptEdits,
		types.PermissionModeBypass,
		types.PermissionModeDefault,
		types.PermissionModeDontAsk,
		types.PermissionModePlan,
		types.PermissionModeAuto:
		return true
	}
	return false
}

// isValidMemoryScope checks if the string is a valid memory scope.
// Source: loadAgentsDir.ts:594 — VALID_MEMORY_SCOPES
func isValidMemoryScope(scope string) bool {
	return scope == "user" || scope == "project" || scope == "local"
}

// getParseError returns a human-readable error for a failed parse attempt.
// Source: loadAgentsDir.ts:403-416 — getParseError
func getParseError(frontmatter map[string]any) string {
	name, _ := frontmatter["name"].(string)
	desc, _ := frontmatter["description"].(string)

	if name == "" {
		return `Missing required "name" field in frontmatter`
	}
	if desc == "" {
		return fmt.Sprintf(`Missing required "description" field in frontmatter for agent %q`, name)
	}
	return "Unknown parsing error"
}

// ---------------------------------------------------------------------------
// Global loader
// ---------------------------------------------------------------------------

// globalLoader is the singleton Loader instance.
// Set by InitLoader; used by GetAgentDefinition and ListAgentDefinitions.
var globalLoader *Loader

// InitLoader initializes or reloads the global agent loader.
// Safe to call multiple times (subsequent calls trigger Reload).
func InitLoader(cwd string) {
	if globalLoader != nil {
		globalLoader.Reload()
		return
	}
	globalLoader = NewLoader(cwd)
	// Lazy loading — don't call Load() here
}
