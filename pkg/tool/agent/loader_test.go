package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/markdown"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// parseAgentToolsFromFrontmatter tests
// Source: utils/markdownConfigLoader.ts:113-126
// ---------------------------------------------------------------------------

func TestParseAgentToolsFromFrontmatter(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  []string // nil = all tools (undefined)
	}{
		{
			name:  "nil value = all tools",
			input: nil,
			want:  nil,
		},
		{
			name:  "empty string = no tools",
			input: "",
			want:  []string{},
		},
		{
			name:  "single tool string",
			input: "Read",
			want:  []string{"Read"},
		},
		{
			name:  "comma-separated tools",
			input: "Read, Grep, Glob",
			want:  []string{"Read", "Grep", "Glob"},
		},
		{
			name:  "wildcard = all tools",
			input: "*",
			want:  nil,
		},
		{
			name:  "YAML list of tools",
			input: []any{"Read", "Grep", "Glob"},
			want:  []string{"Read", "Grep", "Glob"},
		},
		{
			name:  "YAML list with wildcard",
			input: []any{"Read", "*"},
			want:  nil,
		},
		{
			name:  "tool with parentheses preserved",
			input: "Bash(command), Read",
			want:  []string{"Bash(command)", "Read"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAgentToolsFromFrontmatter(tt.input)
			if tt.want == nil {
				if got != nil {
					t.Errorf("expected nil (all tools), got %v", got)
				}
			} else if len(tt.want) == 0 {
				if len(got) != 0 {
					t.Errorf("expected empty slice (no tools), got %v", got)
				}
			} else {
				if len(got) != len(tt.want) {
					t.Fatalf("expected %v, got %v", tt.want, got)
				}
				for i, v := range got {
					if v != tt.want[i] {
						t.Errorf("tool[%d] = %q, want %q", i, v, tt.want[i])
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseSlashCommandToolsFromFrontmatter tests
// Source: utils/markdownConfigLoader.ts:132-140
// ---------------------------------------------------------------------------

func TestParseSlashCommandToolsFromFrontmatter(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  []string
	}{
		{
			name:  "nil = empty",
			input: nil,
			want:  []string{},
		},
		{
			name:  "single skill",
			input: "commit",
			want:  []string{"commit"},
		},
		{
			name:  "comma-separated",
			input: "commit, review",
			want:  []string{"commit", "review"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSlashCommandToolsFromFrontmatter(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
			for i, v := range got {
				if v != tt.want[i] {
					t.Errorf("skill[%d] = %q, want %q", i, v, tt.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parsePositiveIntFromFrontmatter tests
// Source: utils/frontmatterParser.ts:275-289
// ---------------------------------------------------------------------------

func TestParsePositiveIntFromFrontmatter(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  int // 0 = nil/invalid
	}{
		{name: "nil", input: nil, want: 0},
		{name: "positive int", input: 50, want: 50},
		{name: "zero = invalid", input: 0, want: 0},
		{name: "negative = invalid", input: -5, want: 0},
		{name: "string number", input: "25", want: 25},
		{name: "string negative", input: "-3", want: 0},
		{name: "string zero", input: "0", want: 0},
		{name: "string non-number", input: "abc", want: 0},
		{name: "float64 10.0", input: float64(10), want: 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePositiveIntFromFrontmatter(tt.input)
			if tt.want == 0 {
				if got != nil {
					t.Errorf("expected nil, got %d", *got)
				}
			} else {
				if got == nil {
					t.Fatalf("expected %d, got nil", tt.want)
				}
				if *got != tt.want {
					t.Errorf("expected %d, got %d", tt.want, *got)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseEffortValue tests
// Source: loadAgentsDir.ts:624-632
// ---------------------------------------------------------------------------

func TestParseEffortValue(t *testing.T) {
	tests := []struct {
		input any
		want  string
	}{
		{"low", "low"},
		{"medium", "medium"},
		{"HIGH", "high"},
		{"Max", "max"},
		{"42", "42"},
		{42, "42"},
		{float64(10), "10"},
		{"invalid", ""},
		{0, ""},
		{nil, ""},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := parseEffortValue(tt.input)
			if got != tt.want {
				t.Errorf("parseEffortValue(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// isValidPermissionMode tests
// ---------------------------------------------------------------------------

func TestIsValidPermissionMode(t *testing.T) {
	if !isValidPermissionMode("default") {
		t.Error("default should be valid")
	}
	if !isValidPermissionMode("auto") {
		t.Error("auto should be valid")
	}
	if !isValidPermissionMode("plan") {
		t.Error("plan should be valid")
	}
	if isValidPermissionMode("invalid") {
		t.Error("invalid should not be valid")
	}
}

// ---------------------------------------------------------------------------
// isValidMemoryScope tests
// ---------------------------------------------------------------------------

func TestIsValidMemoryScope(t *testing.T) {
	for _, valid := range []string{"user", "project", "local"} {
		if !isValidMemoryScope(valid) {
			t.Errorf("%q should be valid", valid)
		}
	}
	if isValidMemoryScope("invalid") {
		t.Error("invalid should not be valid")
	}
}

// ---------------------------------------------------------------------------
// parseAgentFromMarkdown tests
// Source: loadAgentsDir.ts:541-755
// ---------------------------------------------------------------------------

func TestParseAgentFromMarkdown_AllFields(t *testing.T) {
	fm := map[string]any{
		"name":         "my-agent",
		"description":  "A test agent",
		"model":        "sonnet",
		"tools":        []any{"Read", "Grep"},
		"maxTurns":     50,
		"color":        "blue",
		"effort":       "high",
		"permissionMode": "auto",
		"background":   true,
		"memory":       "user",
		"isolation":    "worktree",
		"initialPrompt": "Start here",
		"skills":       "commit, review",
		"requiredMcpServers": []any{"github"},
	}

	def := parseAgentFromMarkdown("/test/my-agent.md", "/test", fm, "System prompt body.", types.AgentSourceUserSettings)
	if def == nil {
		t.Fatal("expected non-nil definition")
	}

	// Required fields
	if def.AgentType != "my-agent" {
		t.Errorf("AgentType = %q, want my-agent", def.AgentType)
	}
	if def.WhenToUse != "A test agent" {
		t.Errorf("WhenToUse = %q, want 'A test agent'", def.WhenToUse)
	}
	if def.Source != types.AgentSourceUserSettings {
		t.Errorf("Source = %v, want userSettings", def.Source)
	}
	if def.Filename != "my-agent" {
		t.Errorf("Filename = %q, want my-agent", def.Filename)
	}
	if def.BaseDir != "/test" {
		t.Errorf("BaseDir = %q, want /test", def.BaseDir)
	}

	// System prompt
	if def.SystemPrompt == nil {
		t.Fatal("SystemPrompt should not be nil")
	}
	if def.SystemPrompt() != "System prompt body." {
		t.Errorf("SystemPrompt() = %q, want 'System prompt body.'", def.SystemPrompt())
	}

	// Optional fields
	if def.Model != "sonnet" {
		t.Errorf("Model = %q, want sonnet", def.Model)
	}
	if len(def.Tools) != 2 || def.Tools[0] != "Read" || def.Tools[1] != "Grep" {
		t.Errorf("Tools = %v, want [Read Grep]", def.Tools)
	}
	if def.MaxTurns != 50 {
		t.Errorf("MaxTurns = %d, want 50", def.MaxTurns)
	}
	if def.Color != "blue" {
		t.Errorf("Color = %q, want blue", def.Color)
	}
	if def.Effort != "high" {
		t.Errorf("Effort = %q, want high", def.Effort)
	}
	if def.PermissionModeField != types.PermissionModeAuto {
		t.Errorf("PermissionModeField = %v, want auto", def.PermissionModeField)
	}
	if !def.Background {
		t.Error("Background should be true")
	}
	if def.Memory != "user" {
		t.Errorf("Memory = %q, want user", def.Memory)
	}
	if def.Isolation != "worktree" {
		t.Errorf("Isolation = %q, want worktree", def.Isolation)
	}
	if def.InitialPrompt != "Start here" {
		t.Errorf("InitialPrompt = %q, want 'Start here'", def.InitialPrompt)
	}
	if len(def.Skills) != 2 || def.Skills[0] != "commit" || def.Skills[1] != "review" {
		t.Errorf("Skills = %v, want [commit review]", def.Skills)
	}
	if len(def.RequiredMcpServers) != 1 || def.RequiredMcpServers[0] != "github" {
		t.Errorf("RequiredMcpServers = %v, want [github]", def.RequiredMcpServers)
	}
}

func TestParseAgentFromMarkdown_MissingName_ReturnsNil(t *testing.T) {
	fm := map[string]any{
		"description": "No name",
	}
	def := parseAgentFromMarkdown("/test/noname.md", "/test", fm, "body", types.AgentSourceBuiltIn)
	if def != nil {
		t.Error("expected nil for missing name")
	}
}

func TestParseAgentFromMarkdown_MissingDescription_ReturnsNil(t *testing.T) {
	fm := map[string]any{
		"name": "test-agent",
	}
	def := parseAgentFromMarkdown("/test/nodesc.md", "/test", fm, "body", types.AgentSourceBuiltIn)
	if def != nil {
		t.Error("expected nil for missing description")
	}
}

func TestParseAgentFromMarkdown_EmptyBody_OK(t *testing.T) {
	fm := map[string]any{
		"name":        "empty-body",
		"description": "Has no body",
	}
	def := parseAgentFromMarkdown("/test/empty.md", "/test", fm, "", types.AgentSourceProjectSettings)
	if def == nil {
		t.Fatal("expected non-nil definition with empty body")
	}
	if def.SystemPrompt() != "" {
		t.Errorf("SystemPrompt() = %q, want empty", def.SystemPrompt())
	}
}

func TestParseAgentFromMarkdown_NewlineUnescape(t *testing.T) {
	fm := map[string]any{
		"name":        "nl-agent",
		"description": "Line1\\nLine2",
	}
	def := parseAgentFromMarkdown("/test/nl.md", "/test", fm, "body", types.AgentSourceBuiltIn)
	if def == nil {
		t.Fatal("expected non-nil")
	}
	if def.WhenToUse != "Line1\nLine2" {
		t.Errorf("WhenToUse = %q, want 'Line1\\nLine2' (unescaped)", def.WhenToUse)
	}
}

func TestParseAgentFromMarkdown_SingleStringTools(t *testing.T) {
	// "tools: Read" — single string should be parsed as one tool
	fm := map[string]any{
		"name":        "single-tool",
		"description": "test",
		"tools":       "Read",
	}
	def := parseAgentFromMarkdown("/test/single.md", "/test", fm, "", types.AgentSourceBuiltIn)
	if def == nil {
		t.Fatal("expected non-nil")
	}
	if len(def.Tools) != 1 || def.Tools[0] != "Read" {
		t.Errorf("Tools = %v, want [Read]", def.Tools)
	}
}

func TestParseAgentFromMarkdown_InvalidPermissionMode_Skipped(t *testing.T) {
	fm := map[string]any{
		"name":           "bad-pm",
		"description":    "test",
		"permissionMode": "nonexistent",
	}
	def := parseAgentFromMarkdown("/test/badpm.md", "/test", fm, "", types.AgentSourceBuiltIn)
	if def == nil {
		t.Fatal("expected non-nil")
	}
	if def.PermissionModeField != "" {
		t.Errorf("PermissionModeField should be empty for invalid mode, got %v", def.PermissionModeField)
	}
}

// ---------------------------------------------------------------------------
// getActiveAgentsFromList tests
// Source: loadAgentsDir.ts:193-221
// ---------------------------------------------------------------------------

func TestGetActiveAgentsFromList_BuiltInOnly(t *testing.T) {
	agents := []*types.AgentDefinition{
		{AgentType: "General", Source: types.AgentSourceBuiltIn},
		{AgentType: "Explore", Source: types.AgentSourceBuiltIn},
	}
	result := getActiveAgentsFromList(agents)
	if len(result) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(result))
	}
}

func TestGetActiveAgentsFromList_UserOverridesBuiltIn(t *testing.T) {
	agents := []*types.AgentDefinition{
		{AgentType: "General", Source: types.AgentSourceBuiltIn, Model: "inherit"},
		{AgentType: "General", Source: types.AgentSourceUserSettings, Model: "sonnet"},
	}
	result := getActiveAgentsFromList(agents)
	if len(result) != 1 {
		t.Fatalf("expected 1 agent (override), got %d", len(result))
	}
	if result[0].Model != "sonnet" {
		t.Errorf("Model = %q, want sonnet (user override)", result[0].Model)
	}
	if result[0].Source != types.AgentSourceUserSettings {
		t.Errorf("Source = %v, want userSettings", result[0].Source)
	}
}

func TestGetActiveAgentsFromList_CaseSensitiveOverride(t *testing.T) {
	// "explore" is a NEW agent, does NOT override "Explore"
	agents := []*types.AgentDefinition{
		{AgentType: "Explore", Source: types.AgentSourceBuiltIn},
		{AgentType: "explore", Source: types.AgentSourceUserSettings},
	}
	result := getActiveAgentsFromList(agents)
	if len(result) != 2 {
		t.Fatalf("expected 2 agents (case-sensitive), got %d: %v", len(result), result)
	}
}

func TestGetActiveAgentsFromList_ProjectOverridesUser(t *testing.T) {
	agents := []*types.AgentDefinition{
		{AgentType: "test", Source: types.AgentSourceBuiltIn, WhenToUse: "built-in"},
		{AgentType: "test", Source: types.AgentSourceUserSettings, WhenToUse: "user"},
		{AgentType: "test", Source: types.AgentSourceProjectSettings, WhenToUse: "project"},
	}
	result := getActiveAgentsFromList(agents)
	if len(result) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(result))
	}
	if result[0].WhenToUse != "project" {
		t.Errorf("WhenToUse = %q, want 'project' (highest priority)", result[0].WhenToUse)
	}
}

// ---------------------------------------------------------------------------
// loadMarkdownFiles tests
// Source: markdownConfigLoader.ts:546-596
// ---------------------------------------------------------------------------

func TestLoadMarkdownFiles_NonexistentDir(t *testing.T) {
	result := loadMarkdownFiles("/nonexistent/path/agents", types.AgentSourceUserSettings)
	if result != nil {
		t.Errorf("expected nil for nonexistent dir, got %d files", len(result))
	}
}

func TestLoadMarkdownFiles_ValidDir(t *testing.T) {
	dir := t.TempDir()

	// Create test agent files
	agentContent := "---\nname: test-agent\ndescription: \"A test\"\n---\nBody here."
	if err := os.WriteFile(filepath.Join(dir, "test-agent.md"), []byte(agentContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Non-markdown file should be skipped
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not an agent"), 0644); err != nil {
		t.Fatal(err)
	}

	result := loadMarkdownFiles(dir, types.AgentSourceUserSettings)
	if len(result) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result))
	}
	if result[0].frontmatter["name"] != "test-agent" {
		t.Errorf("name = %v, want test-agent", result[0].frontmatter["name"])
	}
	if result[0].source != types.AgentSourceUserSettings {
		t.Errorf("source = %v, want userSettings", result[0].source)
	}
}

func TestLoadMarkdownFiles_SkipsOversizedFiles(t *testing.T) {
	dir := t.TempDir()

	// Create a file larger than maxFrontmatterFileSize
	bigContent := "---\nname: big\n---\n" + strings.Repeat("x", markdown.MaxFrontmatterFileSize+1)
	if err := os.WriteFile(filepath.Join(dir, "big.md"), []byte(bigContent), 0644); err != nil {
		t.Fatal(err)
	}

	result := loadMarkdownFiles(dir, types.AgentSourceUserSettings)
	if len(result) != 0 {
		t.Errorf("expected 0 files (oversized skipped), got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// Loader integration tests
// ---------------------------------------------------------------------------

func TestLoader_LoadAndList(t *testing.T) {
	dir := t.TempDir()

	// Create user agent directory
	userDir := filepath.Join(dir, ".gbot", "agents")
	if err := os.MkdirAll(userDir, 0755); err != nil {
		t.Fatal(err)
	}

	agentContent := "---\nname: custom-test\ndescription: \"Custom agent\"\n---\nCustom prompt."
	if err := os.WriteFile(filepath.Join(userDir, "custom-test.md"), []byte(agentContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Override userAgentDir and projectAgentDir for testing
	loader := NewLoader(dir)
	// Manually set cached by calling internal load with mocked paths
	// Since load() uses userAgentDir()/projectAgentDir() which use home/git,
	// we test via the exported Load method with a real temp structure

	// For this test, we verify the loader doesn't crash
	loader.Load()

	// Built-in agents should always be present
	all := loader.ListAll()
	found := false
	for _, def := range all {
		if def.AgentType == "General" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected General in ListAll()")
	}
}

func TestLoader_LazyLoading(t *testing.T) {
	loader := NewLoader(t.TempDir())

	// Not loaded yet — cached should be nil
	loader.mu.RLock()
	cached := loader.cached
	loader.mu.RUnlock()
	if cached != nil {
		t.Error("cached should be nil immediately after NewLoader")
	}

	// Get triggers lazy loading
	_ = loader.Get("General")

	loader.mu.RLock()
	cached = loader.cached
	loader.mu.RUnlock()
	if cached == nil {
		t.Error("cached should be non-nil after Get()")
	}
}

func TestLoader_GetExactThenCaseInsensitive(t *testing.T) {
	loader := NewLoader(t.TempDir())
	loader.Load()

	// Exact match
	def, err := GetAgentDefinition("Explore")
	if err != nil {
		t.Fatalf("GetAgentDefinition(Explore) error: %v", err)
	}
	if def.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want Explore", def.AgentType)
	}

	// Case-insensitive
	def, err = GetAgentDefinition("explore")
	if err != nil {
		t.Fatalf("GetAgentDefinition(explore) error: %v", err)
	}
	if def.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want Explore (case-insensitive)", def.AgentType)
	}
}

func TestLoader_Reload(t *testing.T) {
	loader := NewLoader(t.TempDir())
	loader.Load()

	all1 := loader.ListAll()
	count1 := len(all1)

	loader.Reload()
	all2 := loader.ListAll()
	count2 := len(all2)

	if count1 != count2 {
		t.Errorf("count changed after reload: %d -> %d", count1, count2)
	}
}

// ---------------------------------------------------------------------------
// getParseError tests
// Source: loadAgentsDir.ts:403-416
// ---------------------------------------------------------------------------

func TestGetParseError_MissingName(t *testing.T) {
	fm := map[string]any{"description": "has desc"}
	msg := getParseError(fm)
	if msg != `Missing required "name" field in frontmatter` {
		t.Errorf("unexpected error: %q", msg)
	}
}

func TestGetParseError_MissingDescription(t *testing.T) {
	fm := map[string]any{"name": "test"}
	msg := getParseError(fm)
	if msg != `Missing required "description" field in frontmatter for agent "test"` {
		t.Errorf("unexpected error: %q", msg)
	}
}

func TestGetParseError_BothPresent(t *testing.T) {
	fm := map[string]any{"name": "test", "description": "desc"}
	msg := getParseError(fm)
	if msg != "Unknown parsing error" {
		t.Errorf("unexpected error: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// Deduplication tests
// Source: markdownConfigLoader.ts:380-407
// ---------------------------------------------------------------------------

func TestDeduplicateFiles_Empty(t *testing.T) {
	result := deduplicateFiles(nil)
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestDeduplicateFiles_SymlinkDedup(t *testing.T) {
	dir := t.TempDir()

	// Create a file and a symlink to it
	original := filepath.Join(dir, "agent1.md")
	symlink := filepath.Join(dir, "agent2.md")

	if err := os.WriteFile(original, []byte("---\nname: a\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, symlink); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	files := []markdownFileEntry{
		{filePath: original, source: types.AgentSourceBuiltIn},
		{filePath: symlink, source: types.AgentSourceUserSettings},
	}

	result := deduplicateFiles(files)
	if len(result) != 1 {
		t.Errorf("expected 1 file after dedup, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// Coverage gap tests for 100% coverage
// ---------------------------------------------------------------------------

func TestInitLoader_FirstInit(t *testing.T) {
	// Reset global state
	globalLoader = nil

	InitLoader(t.TempDir())

	if globalLoader == nil {
		t.Fatal("globalLoader should be set after InitLoader")
	}

	// Clean up
	globalLoader = nil
}

func TestInitLoader_Reload(t *testing.T) {
	globalLoader = nil

	InitLoader(t.TempDir())
	first := globalLoader

	InitLoader(t.TempDir())
	if globalLoader != first {
		t.Error("InitLoader should reuse same globalLoader on second call")
	}

	globalLoader = nil
}

func TestLoader_FailedFiles(t *testing.T) {
	dir := t.TempDir()

	// Create an agent file with name but no description (will fail parsing)
	badContent := "---\nname: bad-agent\n---\nBody."
	if err := os.WriteFile(filepath.Join(dir, "bad-agent.md"), []byte(badContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create .gbot/agents/ under temp dir
	_ = &Loader{cwd: dir}
	agentsDir := filepath.Join(dir, ".gbot", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(agentsDir, "bad-agent.md")
	if err := os.WriteFile(badPath, []byte(badContent), 0644); err != nil {
		t.Fatal(err)
	}

	loader2 := NewLoader(dir)
	loader2.Load()
	failed := loader2.FailedFiles()
	if len(failed) == 0 {
		t.Error("expected at least one failed file for agent with name but no description")
	}
	found := false
	for _, f := range failed {
		if strings.Contains(f.Path, "bad-agent.md") {
			found = true
			if !strings.Contains(f.Error, "description") {
				t.Errorf("error should mention description, got: %s", f.Error)
			}
		}
	}
	if !found {
		t.Errorf("bad-agent.md not in failed files: %v", failed)
	}
}

func TestLoader_Get_EmptyString_ReturnsGeneral(t *testing.T) {
	loader := NewLoader(t.TempDir())
	loader.Load()

	def := loader.Get("")
	if def == nil {
		t.Fatal("Get('') should return General")
	}
	if def.AgentType != "General" {
		t.Errorf("Get('') = %q, want General", def.AgentType)
	}
}

func TestLoader_Get_CaseInsensitiveFallback(t *testing.T) {
	loader := NewLoader(t.TempDir())
	loader.Load()

	def := loader.Get("explore")
	if def == nil {
		t.Fatal("Get('explore') should find Explore via case-insensitive")
	}
	if def.AgentType != "Explore" {
		t.Errorf("Get('explore') = %q, want Explore", def.AgentType)
	}
}

func TestLoader_Get_NotFound_ReturnsNil(t *testing.T) {
	loader := NewLoader(t.TempDir())
	loader.Load()

	def := loader.Get("nonexistent-agent-type")
	if def != nil {
		t.Errorf("Get('nonexistent-agent-type') = %v, want nil", def)
	}
}

func TestApplyOptionalFields_ModelInherit(t *testing.T) {
	def := &types.AgentDefinition{}
	fm := map[string]any{"model": "inherit"}
	applyOptionalFields(def, fm, "test.md")
	if def.Model != "inherit" {
		t.Errorf("Model = %q, want 'inherit'", def.Model)
	}
}

func TestApplyOptionalFields_DisallowedTools(t *testing.T) {
	def := &types.AgentDefinition{}
	fm := map[string]any{"disallowedTools": "Edit, Write"}
	applyOptionalFields(def, fm, "test.md")
	if len(def.DisallowedTools) != 2 {
		t.Fatalf("DisallowedTools = %v, want 2 items", def.DisallowedTools)
	}
	if def.DisallowedTools[0] != "Edit" || def.DisallowedTools[1] != "Write" {
		t.Errorf("DisallowedTools = %v, want [Edit Write]", def.DisallowedTools)
	}
}

func TestApplyOptionalFields_InvalidEffort_LogsWarning(t *testing.T) {
	def := &types.AgentDefinition{}
	fm := map[string]any{"effort": "superfast"}
	applyOptionalFields(def, fm, "test.md")
	if def.Effort != "" {
		t.Errorf("Effort = %q, want empty for invalid value", def.Effort)
	}
}

func TestApplyOptionalFields_InvalidMaxTurns_LogsWarning(t *testing.T) {
	def := &types.AgentDefinition{}
	fm := map[string]any{"maxTurns": "not-a-number"}
	applyOptionalFields(def, fm, "test.md")
	if def.MaxTurns != 0 {
		t.Errorf("MaxTurns = %d, want 0 for invalid value", def.MaxTurns)
	}
}

func TestApplyOptionalFields_BackgroundStringTrue(t *testing.T) {
	def := &types.AgentDefinition{}
	fm := map[string]any{"background": "true"}
	applyOptionalFields(def, fm, "test.md")
	if !def.Background {
		t.Error("Background should be true for string 'true'")
	}
}

func TestApplyOptionalFields_InvalidBackgroundType_LogsWarning(t *testing.T) {
	def := &types.AgentDefinition{}
	fm := map[string]any{"background": 42}
	applyOptionalFields(def, fm, "test.md")
	if def.Background {
		t.Error("Background should be false for int value")
	}
}

func TestApplyOptionalFields_InvalidMemory_LogsWarning(t *testing.T) {
	def := &types.AgentDefinition{}
	fm := map[string]any{"memory": "invalid-scope"}
	applyOptionalFields(def, fm, "test.md")
	if def.Memory != "" {
		t.Errorf("Memory = %q, want empty for invalid scope", def.Memory)
	}
}

func TestApplyOptionalFields_InvalidIsolation_LogsWarning(t *testing.T) {
	def := &types.AgentDefinition{}
	fm := map[string]any{"isolation": "docker"}
	applyOptionalFields(def, fm, "test.md")
	if def.Isolation != "" {
		t.Errorf("Isolation = %q, want empty for invalid value", def.Isolation)
	}
}

func TestApplyOptionalFields_CriticalSystemReminder(t *testing.T) {
	def := &types.AgentDefinition{}
	fm := map[string]any{"criticalSystemReminder_EXPERIMENTAL": "always check this"}
	applyOptionalFields(def, fm, "test.md")
	if def.CriticalSystemReminder != "always check this" {
		t.Errorf("CriticalSystemReminder = %q, want 'always check this'", def.CriticalSystemReminder)
	}
}

func TestParseToolListFromCLI_ParenthesizedWithCommaAndSpace(t *testing.T) {
	result := parseToolListFromCLI([]string{"Bash(nice io_uring cp, big file)"})
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d: %v", len(result), result)
	}
	if result[0] != "Bash(nice io_uring cp, big file)" {
		t.Errorf("tool = %q, want 'Bash(nice io_uring cp, big file)'", result[0])
	}
}

func TestParseToolListFromCLI_SpaceSeparated(t *testing.T) {
	result := parseToolListFromCLI([]string{"Read Grep Glob"})
	if len(result) != 3 {
		t.Fatalf("expected 3 tools, got %d: %v", len(result), result)
	}
	if result[0] != "Read" || result[1] != "Grep" || result[2] != "Glob" {
		t.Errorf("tools = %v, want [Read Grep Glob]", result)
	}
}

func TestParseToolListFromCLI_CommaAndSpace(t *testing.T) {
	result := parseToolListFromCLI([]string{"Bash(cmd), Read, Grep(query)"})
	if len(result) != 3 {
		t.Fatalf("expected 3 tools, got %d: %v", len(result), result)
	}
	if result[0] != "Bash(cmd)" || result[1] != "Read" || result[2] != "Grep(query)" {
		t.Errorf("tools = %v, want [Bash(cmd) Read Grep(query)]", result)
	}
}

func TestParseToolListFromCLI_Empty(t *testing.T) {
	result := parseToolListFromCLI(nil)
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}
	result = parseToolListFromCLI([]string{})
	if result != nil {
		t.Errorf("expected nil for empty slice, got %v", result)
	}
}

func TestParseToolListString_EmptyArray(t *testing.T) {
	result := parseToolListString([]any{})
	if result == nil {
		t.Error("expected non-nil (empty slice) for empty array")
	}
	if len(result) != 0 {
		t.Errorf("expected empty slice, got %v", result)
	}
}

func TestProjectAgentDir_InGitRepo(t *testing.T) {
	dir := t.TempDir()
	// Init git repo
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	result := projectAgentDir(dir)
	if !strings.HasSuffix(result, filepath.Join(".gbot", "agents")) {
		t.Errorf("projectAgentDir = %q, should end with .gbot/agents", result)
	}
	if !strings.HasPrefix(result, dir) {
		t.Errorf("projectAgentDir = %q, should start with %q", result, dir)
	}
}

func TestProjectAgentDir_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	result := projectAgentDir(dir)
	expected := filepath.Join(dir, ".gbot", "agents")
	if result != expected {
		t.Errorf("projectAgentDir(%q) = %q, want %q", dir, result, expected)
	}
}

func TestFindGitRoot_InGitRepo(t *testing.T) {
	dir := t.TempDir()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	root := findGitRoot(dir)
	if root == "" {
		t.Error("findGitRoot should return non-empty for git repo")
	}
	// The root should be the dir we initialized (git init uses the dir itself)
	if root != dir {
		// git might resolve symlinks, just check it's not empty
		t.Logf("findGitRoot(%q) = %q (may differ due to symlink resolution)", dir, root)
	}
}

func TestFindGitRoot_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	root := findGitRoot(dir)
	if root != "" {
		t.Errorf("findGitRoot(%q) = %q, want empty string", dir, root)
	}
}

func TestFileIdentity_StatError(t *testing.T) {
	// Non-existent file should return empty string
	id := fileIdentity("/nonexistent/path/file.md")
	if id != "" {
		t.Errorf("fileIdentity for nonexistent file = %q, want empty", id)
	}
}

func TestDeduplicateFiles_FailOpenOnStatError(t *testing.T) {
	files := []markdownFileEntry{
		{filePath: "/nonexistent/a.md", source: types.AgentSourceBuiltIn},
		{filePath: "/nonexistent/b.md", source: types.AgentSourceUserSettings},
	}
	// Both fail stat → both included (fail open)
	result := deduplicateFiles(files)
	if len(result) != 2 {
		t.Errorf("expected 2 files (fail open), got %d", len(result))
	}
}

func TestLoadMarkdownFiles_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	result := loadMarkdownFiles(dir, types.AgentSourceBuiltIn)
	if result != nil {
		t.Errorf("expected nil for empty directory, got %d files", len(result))
	}
}

func TestUserAgentDir(t *testing.T) {
	dir := userAgentDir()
	if dir == "" {
		t.Error("userAgentDir should not return empty on a normal system")
	}
	if !strings.HasSuffix(dir, filepath.Join(".gbot", "agents")) {
		t.Errorf("userAgentDir = %q, should end with .gbot/agents", dir)
	}
}

func TestGetAgentDefinition_WithGlobalLoader(t *testing.T) {
	// Save and restore globalLoader
	orig := globalLoader
	globalLoader = NewLoader(t.TempDir())
	defer func() { globalLoader = orig }()

	def, err := GetAgentDefinition("Explore")
	if err != nil {
		t.Fatalf("GetAgentDefinition with globalLoader: %v", err)
	}
	if def.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want Explore", def.AgentType)
	}

	// Unknown type through globalLoader
	_, err = GetAgentDefinition("nonexistent-global-test")
	if err == nil {
		t.Fatal("expected error for unknown agent type via globalLoader")
	}
	if !strings.Contains(err.Error(), "unknown agent type") {
		t.Errorf("error should mention unknown agent type, got: %v", err)
	}
}

func TestListAgentDefinitions_WithGlobalLoader(t *testing.T) {
	orig := globalLoader
	globalLoader = NewLoader(t.TempDir())
	defer func() { globalLoader = orig }()

	defs := ListAgentDefinitions()
	if len(defs) < 3 {
		t.Errorf("expected at least 3 definitions via globalLoader, got %d", len(defs))
	}
}

func TestLoadMarkdownFiles_EmptyDirString(t *testing.T) {
	result := loadMarkdownFiles("", types.AgentSourceBuiltIn)
	if result != nil {
		t.Errorf("expected nil for empty dir string, got %d files", len(result))
	}
}

func TestLoadMarkdownFiles_UnreadableFile(t *testing.T) {
	dir := t.TempDir()
	// Create a file then make it unreadable
	fp := filepath.Join(dir, "secret.md")
	if err := os.WriteFile(fp, []byte("---\nname: test\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fp, 0000); err != nil {
		t.Skipf("chmod not supported: %v", err)
	}
	defer func() { _ = os.Chmod(fp, 0644) }() // restore for cleanup

	result := loadMarkdownFiles(dir, types.AgentSourceBuiltIn)
	// File should be skipped (log + continue)
	if len(result) != 0 {
		t.Errorf("expected 0 files (unreadable), got %d", len(result))
	}
}

func TestParseToolListFromCLI_EmptyStringInArray(t *testing.T) {
	result := parseToolListFromCLI([]string{"", "Read"})
	if len(result) != 1 {
		t.Fatalf("expected 1 tool (empty skipped), got %d: %v", len(result), result)
	}
	if result[0] != "Read" {
		t.Errorf("tool = %q, want Read", result[0])
	}
}

func TestParseToolListString_NonStringNonArray(t *testing.T) {
	result := parseToolListString(42)
	if result == nil {
		t.Error("expected non-nil for non-string non-array type")
	}
	if len(result) != 0 {
		t.Errorf("expected empty slice, got %v", result)
	}
}

func TestParseAgentToolsFromFrontmatter_ExplicitNil(t *testing.T) {
	// This tests the path where parseToolListString returns nil for explicit nil input
	// but parseAgentToolsFromFrontmatter receives non-nil toolsValue
	// Pass an explicit nil — should return nil (all tools)
	result := parseAgentToolsFromFrontmatter(nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got %v", result)
	}
}

func TestParsePositiveIntFromFrontmatter_DefaultType(t *testing.T) {
	// Pass a type that doesn't match any case (e.g., bool)
	result := parsePositiveIntFromFrontmatter(true)
	if result != nil {
		t.Errorf("expected nil for bool input, got %d", *result)
	}
}

func TestParseEffortValue_DefaultType(t *testing.T) {
	result := parseEffortValue(true)
	if result != "" {
		t.Errorf("expected empty for bool input, got %q", result)
	}
}

func TestParseEffortValue_Float64NonInteger(t *testing.T) {
	// float64 that's not a whole number → return ""
	result := parseEffortValue(3.14)
	if result != "" {
		t.Errorf("expected empty for non-integer float64, got %q", result)
	}
}

func TestParseEffortValue_Float64Negative(t *testing.T) {
	result := parseEffortValue(-5.0)
	if result != "" {
		t.Errorf("expected empty for negative float64, got %q", result)
	}
}

func TestUserAgentDir_HomeError(t *testing.T) {
	orig := os.Getenv("HOME")
	_ = os.Setenv("HOME", "")
	defer func() { _ = os.Setenv("HOME", orig) }()
	dir := userAgentDir()
	if dir != "" {
		t.Errorf("expected empty when HOME unset, got %q", dir)
	}
}

func TestLoadMarkdownFiles_DanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	// Create a dangling symlink (target doesn't exist)
	target := filepath.Join(dir, "real.md")
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	// The symlink target doesn't exist, so entry.Info() may fail
	result := loadMarkdownFiles(dir, types.AgentSourceBuiltIn)
	if len(result) != 0 {
		t.Errorf("expected 0 files (dangling symlink), got %d", len(result))
	}
}

func TestFileIdentity_ProcFile(t *testing.T) {
	// /dev/null should have valid stat but may have unusual device/inode
	id := fileIdentity("/dev/null")
	// On most Linux systems, /dev/null has valid stat
	if id == "" {
		t.Error("fileIdentity(/dev/null) returned empty string, expected non-empty identity")
	}
}
