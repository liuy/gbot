package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// --- djb2Hash tests ---

func TestDjb2Hash_Consistency(t *testing.T) {
	input := "hello world test string"
	h1 := djb2Hash(input)
	h2 := djb2Hash(input)
	if h1 != h2 {
		t.Errorf("djb2Hash should be deterministic: got %d then %d", h1, h2)
	}
}

func TestDjb2Hash_DifferentOutput(t *testing.T) {
	h1 := djb2Hash("hello")
	h2 := djb2Hash("world")
	if h1 == h2 {
		t.Errorf("different inputs should (very likely) produce different hashes: %d == %d", h1, h2)
	}
}

func TestDjb2Hash_EmptyString(t *testing.T) {
	h := djb2Hash("")
	if h != 5381 {
		t.Errorf("empty string should return initial value 5381, got %d", h)
	}
}

// --- computeHash tests ---

func TestComputeHash_NilData(t *testing.T) {
	h := computeHash(nil)
	if h == 0 {
		t.Error("computeHash(nil) should not be 0")
	}
}

func TestComputeHash_StructuredData(t *testing.T) {
	data := []map[string]any{{"type": "text", "text": "hello"}}
	h := computeHash(data)
	if h == 0 {
		t.Error("computeHash should return non-zero for structured data")
	}
}

// --- stripCacheControl tests ---

func TestStripCacheControl_RemovesKey(t *testing.T) {
	blocks := []map[string]any{
		{"type": "text", "text": "hello", "cache_control": map[string]any{"type": "ephemeral"}},
	}
	result := stripCacheControl(blocks)
	if _, ok := result[0]["cache_control"]; ok {
		t.Error("cache_control should be removed")
	}
	if result[0]["type"] != "text" {
		t.Error("other keys should be preserved")
	}
	if result[0]["text"] != "hello" {
		t.Error("text should be preserved")
	}
}

func TestStripCacheControl_NoCacheControl(t *testing.T) {
	blocks := []map[string]any{
		{"type": "text", "text": "hello"},
	}
	result := stripCacheControl(blocks)
	if len(result) != 1 {
		t.Error("should have 1 block")
	}
	if result[0]["text"] != "hello" {
		t.Error("text should be preserved")
	}
}

func TestStripCacheControl_MultipleBlocks(t *testing.T) {
	blocks := []map[string]any{
		{"type": "text", "text": "a", "cache_control": map[string]any{"type": "ephemeral"}},
		{"type": "text", "text": "b"},
		{"type": "text", "text": "c", "cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"}},
	}
	result := stripCacheControl(blocks)
	if len(result) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(result))
	}
	for i, b := range result {
		if _, ok := b["cache_control"]; ok {
			t.Errorf("block %d should not have cache_control", i)
		}
	}
	if result[0]["text"] != "a" || result[1]["text"] != "b" || result[2]["text"] != "c" {
		t.Error("text values should be preserved in order")
	}
}

func TestStripCacheControl_Empty(t *testing.T) {
	result := stripCacheControl(nil)
	if len(result) != 0 {
		t.Error("empty input should return empty output")
	}
}

// --- sanitizeToolName tests ---

func TestSanitizeToolName_MCP(t *testing.T) {
	if got := sanitizeToolName("mcp__filesystem_read"); got != "mcp" {
		t.Errorf("mcp__ tools should collapse to 'mcp', got %q", got)
	}
}

func TestSanitizeToolName_Builtin(t *testing.T) {
	if got := sanitizeToolName("Read"); got != "Read" {
		t.Errorf("non-mcp tools should be unchanged, got %q", got)
	}
}

func TestSanitizeToolName_MCPPrefix(t *testing.T) {
	if got := sanitizeToolName("mcp__server_tool_name"); got != "mcp" {
		t.Errorf("mcp__ prefix should collapse to 'mcp', got %q", got)
	}
}

// --- isExcludedModel tests ---

func TestIsExcludedModel_Haiku(t *testing.T) {
	if !isExcludedModel("claude-haiku-4-5") {
		t.Error("haiku models should be excluded")
	}
	if !isExcludedModel("claude-3-5-haiku-20241022") {
		t.Error("haiku models should be excluded")
	}
}

func TestIsExcludedModel_Sonnet(t *testing.T) {
	if isExcludedModel("claude-sonnet-4-20250514") {
		t.Error("sonnet models should NOT be excluded")
	}
}

// --- getTrackingKey tests ---

func TestGetTrackingKey_CompactShared(t *testing.T) {
	if got := getTrackingKey("compact", ""); got != "repl_main_thread" {
		t.Errorf("compact should share with repl_main_thread, got %q", got)
	}
}

func TestGetTrackingKey_TrackedSource(t *testing.T) {
	if got := getTrackingKey("repl_main_thread", ""); got != "repl_main_thread" {
		t.Errorf("repl_main_thread should be tracked, got %q", got)
	}
}

func TestGetTrackingKey_AgentWithID(t *testing.T) {
	if got := getTrackingKey("agent:custom", "my-agent-123"); got != "my-agent-123" {
		t.Errorf("agent with ID should use agentID as key, got %q", got)
	}
}

func TestGetTrackingKey_AgentNoID(t *testing.T) {
	if got := getTrackingKey("agent:custom", ""); got != "agent:custom" {
		t.Errorf("agent without ID should use querySource as key, got %q", got)
	}
}

func TestGetTrackingKey_Untracked(t *testing.T) {
	if got := getTrackingKey("speculation", ""); got != "" {
		t.Errorf("untracked sources should return empty, got %q", got)
	}
}

func TestGetTrackingKey_SDK(t *testing.T) {
	if got := getTrackingKey("sdk", ""); got != "sdk" {
		t.Errorf("sdk should be tracked, got %q", got)
	}
}

// --- slicesEqual / sortCopy tests ---

func TestSlicesEqual_Equal(t *testing.T) {
	if !slicesEqual([]string{"a", "b"}, []string{"a", "b"}) {
		t.Error("identical slices should be equal")
	}
}

func TestSlicesEqual_DifferentLength(t *testing.T) {
	if slicesEqual([]string{"a"}, []string{"a", "b"}) {
		t.Error("different length slices should not be equal")
	}
}

func TestSlicesEqual_DifferentContent(t *testing.T) {
	if slicesEqual([]string{"a", "c"}, []string{"a", "b"}) {
		t.Error("different content should not be equal")
	}
}

func TestSlicesEqual_BothNil(t *testing.T) {
	if !slicesEqual(nil, nil) {
		t.Error("both nil should be equal")
	}
}

func TestSortCopy(t *testing.T) {
	input := []string{"c", "a", "b"}
	result := sortCopy(input)
	if len(result) != 3 || result[0] != "a" || result[1] != "b" || result[2] != "c" {
		t.Errorf("sortCopy should return sorted copy, got %v", result)
	}
	// Original should not be modified
	if input[0] != "c" || input[1] != "a" || input[2] != "b" {
		t.Errorf("sortCopy should not modify original, got %v", input)
	}
}

func TestSortCopy_Empty(t *testing.T) {
	result := sortCopy(nil)
	if result != nil {
		t.Errorf("sortCopy(nil) should return nil, got %v", result)
	}
}

// --- computePerToolHashes tests ---

func TestComputePerToolHashes(t *testing.T) {
	tools := []map[string]any{
		{"name": "Read", "description": "read a file"},
		{"name": "Write", "description": "write a file"},
	}
	names := []string{"Read", "Write"}
	hashes := computePerToolHashes(tools, names)
	if len(hashes) != 2 {
		t.Fatalf("expected 2 hashes, got %d", len(hashes))
	}
	if _, ok := hashes["Read"]; !ok {
		t.Error("missing hash for Read")
	}
	if _, ok := hashes["Write"]; !ok {
		t.Error("missing hash for Write")
	}
	if hashes["Read"] == hashes["Write"] {
		t.Error("different tools should have different hashes")
	}
}

func TestComputePerToolHashes_EmptyName(t *testing.T) {
	tools := []map[string]any{{"description": "no name"}}
	names := []string{""}
	hashes := computePerToolHashes(tools, names)
	if _, ok := hashes["__idx_0"]; !ok {
		t.Error("empty name should use __idx_0 fallback")
	}
}

// --- getSystemCharCount tests ---

func TestGetSystemCharCount(t *testing.T) {
	blocks := []map[string]any{
		{"type": "text", "text": "hello"},
		{"type": "text", "text": "world!"},
	}
	count := getSystemCharCount(blocks)
	if count != 11 {
		t.Errorf("expected 11 chars (5+6), got %d", count)
	}
}

func TestGetSystemCharCount_Empty(t *testing.T) {
	count := getSystemCharCount(nil)
	if count != 0 {
		t.Errorf("expected 0 for nil, got %d", count)
	}
}

// --- buildDiffableContent tests ---

func TestBuildDiffableContent(t *testing.T) {
	system := []map[string]any{
		{"type": "text", "text": "You are helpful."},
	}
	tools := []map[string]any{
		{"name": "Read", "description": "Read file", "input_schema": map[string]any{"type": "object"}},
	}
	content := buildDiffableContent(system, tools, "claude-sonnet-4")
	if !strings.Contains(content, "Model: claude-sonnet-4") {
		t.Error("should contain model name")
	}
	if !strings.Contains(content, "=== System Prompt ===") {
		t.Error("should contain system prompt section")
	}
	if !strings.Contains(content, "You are helpful.") {
		t.Error("should contain system text")
	}
	if !strings.Contains(content, "=== Tools (1) ===") {
		t.Error("should contain tools section with count")
	}
	if !strings.Contains(content, "Read") {
		t.Error("should contain tool name")
	}
}

func TestBuildDiffableContent_ToolsSorted(t *testing.T) {
	tools := []map[string]any{
		{"name": "Zebra", "description": "z tool"},
		{"name": "Alpha", "description": "a tool"},
	}
	content := buildDiffableContent(nil, tools, "model")
	alphaIdx := strings.Index(content, "Alpha")
	zebraIdx := strings.Index(content, "Zebra")
	if alphaIdx >= zebraIdx {
		t.Error("tools should be sorted alphabetically")
	}
}

// --- getCacheBreakDir tests ---

func TestGetCacheBreakDir(t *testing.T) {
	dir, err := getCacheBreakDir()
	if err != nil {
		t.Fatalf("getCacheBreakDir failed: %v", err)
	}
	if !strings.HasSuffix(dir, filepath.Join("gbot-cache-break")) {
		t.Errorf("unexpected dir: %s", dir)
	}
	// Verify directory exists
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("directory should exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("should be a directory")
	}
}

// --- writeCacheBreakDiff tests ---

func TestWriteCacheBreakDiff(t *testing.T) {
	prev := "Model: v1\n\nHello world"
	curr := "Model: v2\n\nHello world 2"
	diffPath := writeCacheBreakDiff(prev, curr)
	if diffPath == "" {
		t.Fatal("writeCacheBreakDiff should return a path")
	}
	if !strings.HasPrefix(diffPath, filepath.Join(os.TempDir(), "gbot-cache-break")) {
		t.Errorf("diff path should be in cache-break dir, got %s", diffPath)
	}
	// Verify file exists
	if _, err := os.Stat(diffPath); os.IsNotExist(err) {
		t.Errorf("diff file should exist at %s", diffPath)
	}
	// Clean up
	_ = os.Remove(diffPath)
}

// --- extractCacheControlHash tests ---

func TestExtractCacheControlHash(t *testing.T) {
	blocks := []map[string]any{
		{"type": "text", "text": "hello", "cache_control": map[string]any{"type": "ephemeral"}},
		{"type": "text", "text": "world"},
	}
	h := extractCacheControlHash(blocks)
	if h == 0 {
		t.Error("hash should be non-zero")
	}
}

func TestExtractCacheControlHash_NoCacheControl(t *testing.T) {
	blocks := []map[string]any{
		{"type": "text", "text": "hello"},
		{"type": "text", "text": "world"},
	}
	h := extractCacheControlHash(blocks)
	if h == 0 {
		t.Error("hash of all-nil cache_control should still be non-zero")
	}
}

// --- RecordPromptState tests ---

func resetGlobalState() {
	ResetPromptCacheBreakDetection()
}

func makeSystemBlocks(text string) []map[string]any {
	return []map[string]any{
		{"type": "text", "text": text},
	}
}

func makeTools(names ...string) []map[string]any {
	var tools []map[string]any
	for _, n := range names {
		tools = append(tools, map[string]any{
			"name": n, "description": n + " tool",
		})
	}
	return tools
}

func TestRecordPromptState_FirstCall(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("hello"), makeTools("Read", "Write"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	val, ok := stateStore["repl_main_thread"]
	if !ok {
		t.Fatal("state should be stored after first call")
	}
	state := val
	if state.CallCount != 1 {
		t.Errorf("callCount should be 1, got %d", state.CallCount)
	}
	if state.PrevCacheRead != 0 {
		t.Errorf("prevCacheRead should be 0 (null) on first call, got %d", state.PrevCacheRead)
	}
	if state.Model != "sonnet" {
		t.Errorf("model should be 'sonnet', got %q", state.Model)
	}
	if state.PendingChanges != nil {
		t.Error("pendingChanges should be nil on first call")
	}
}

func TestRecordPromptState_SystemChanged(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	RecordPromptState(makeSystemBlocks("v1"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	RecordPromptState(makeSystemBlocks("v22"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	state := stateStore["repl_main_thread"]
	if state.PendingChanges == nil {
		t.Fatal("pendingChanges should be set when system changes")
	}
	if !state.PendingChanges.SystemPromptChanged {
		t.Error("SystemPromptChanged should be true")
	}
	if state.PendingChanges.SystemCharDelta != 1 {
		t.Errorf("SystemCharDelta should be 1 (3-2=1 for 'v22'-'v1'), got %d", state.PendingChanges.SystemCharDelta)
	}
}

func TestRecordPromptState_ToolsChanged(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read", "Write"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	state := stateStore["repl_main_thread"]
	if state.PendingChanges == nil {
		t.Fatal("pendingChanges should be set when tools change")
	}
	if !state.PendingChanges.ToolSchemasChanged {
		t.Error("ToolSchemasChanged should be true")
	}
	if state.PendingChanges.AddedToolCount != 1 {
		t.Errorf("AddedToolCount should be 1, got %d", state.PendingChanges.AddedToolCount)
	}
	if len(state.PendingChanges.AddedTools) != 1 || state.PendingChanges.AddedTools[0] != "Write" {
		t.Errorf("AddedTools should be [Write], got %v", state.PendingChanges.AddedTools)
	}
}

func TestRecordPromptState_ModelChanged(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "opus", nil, "", false, false, false, false, "", 0)

	state := stateStore["repl_main_thread"]
	if state.PendingChanges == nil {
		t.Fatal("pendingChanges should be set when model changes")
	}
	if !state.PendingChanges.ModelChanged {
		t.Error("ModelChanged should be true")
	}
	if state.PendingChanges.PreviousModel != "sonnet" {
		t.Errorf("PreviousModel should be 'sonnet', got %q", state.PendingChanges.PreviousModel)
	}
	if state.PendingChanges.NewModel != "opus" {
		t.Errorf("NewModel should be 'opus', got %q", state.PendingChanges.NewModel)
	}
}

func TestRecordPromptState_CacheControlChanged(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	sys1 := []map[string]any{
		{"type": "text", "text": "sys", "cache_control": map[string]any{"type": "ephemeral", "ttl": "5m"}},
	}
	sys2 := []map[string]any{
		{"type": "text", "text": "sys", "cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"}},
	}

	RecordPromptState(sys1, makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	RecordPromptState(sys2, makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	state := stateStore["repl_main_thread"]
	if state.PendingChanges == nil {
		t.Fatal("pendingChanges should be set when cache_control changes")
	}
	if !state.PendingChanges.CacheControlChanged {
		t.Error("CacheControlChanged should be true")
	}
}

func TestRecordPromptState_BetasChanged(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", []string{"beta1"}, "", false, false, false, false, "", 0)
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", []string{"beta1", "beta2"}, "", false, false, false, false, "", 0)

	state := stateStore["repl_main_thread"]
	if state.PendingChanges == nil {
		t.Fatal("pendingChanges should be set when betas change")
	}
	if !state.PendingChanges.BetasChanged {
		t.Error("BetasChanged should be true")
	}
	if len(state.PendingChanges.AddedBetas) != 1 || state.PendingChanges.AddedBetas[0] != "beta2" {
		t.Errorf("AddedBetas should be [beta2], got %v", state.PendingChanges.AddedBetas)
	}
}

func TestRecordPromptState_NoChange(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	state := stateStore["repl_main_thread"]
	if state.PendingChanges != nil {
		t.Error("pendingChanges should be nil when nothing changes")
	}
	if state.CallCount != 2 {
		t.Errorf("callCount should be 2, got %d", state.CallCount)
	}
}

func TestRecordPromptState_MaxSourcesEviction(t *testing.T) {
	resetGlobalState()

	for i := range 12 {
		key := PromptStateKey{QuerySource: "repl_main_thread", AgentID: fmt.Sprintf("agent-%d", i)}
		RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	}

	muState.Lock()
	count := len(stateStore)
	muState.Unlock()
	if count > maxTrackedSources {
		t.Errorf("stateStore length should be <= %d, got %d", maxTrackedSources, count)
	}
}

func TestRecordPromptState_CompactSharesReplThread(t *testing.T) {
	resetGlobalState()
	keyRepl := PromptStateKey{QuerySource: "repl_main_thread"}
	keyCompact := PromptStateKey{QuerySource: "compact"}

	RecordPromptState(makeSystemBlocks("v1"), makeTools("Read"), keyRepl, "sonnet", nil, "", false, false, false, false, "", 0)
	RecordPromptState(makeSystemBlocks("v2"), makeTools("Read"), keyCompact, "sonnet", nil, "", false, false, false, false, "", 0)

	state := stateStore["repl_main_thread"]
	if state.CallCount != 2 {
		t.Errorf("compact should share state with repl_main_thread, callCount should be 2, got %d", state.CallCount)
	}
	if state.PendingChanges == nil || !state.PendingChanges.SystemPromptChanged {
		t.Error("compact's change should be detected against repl_main_thread's state")
	}
}

func TestRecordPromptState_PerToolHash(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	tools1 := []map[string]any{
		{"name": "Read", "description": "read file"},
		{"name": "Write", "description": "write file"},
	}
	tools2 := []map[string]any{
		{"name": "Read", "description": "read file"},           // same
		{"name": "Write", "description": "write file CHANGED"}, // changed description
	}

	RecordPromptState(makeSystemBlocks("sys"), tools1, key, "sonnet", nil, "", false, false, false, false, "", 0)
	RecordPromptState(makeSystemBlocks("sys"), tools2, key, "sonnet", nil, "", false, false, false, false, "", 0)

	state := stateStore["repl_main_thread"]
	if state.PendingChanges == nil {
		t.Fatal("pendingChanges should be set when tool schema changes")
	}
	if !state.PendingChanges.ToolSchemasChanged {
		t.Error("ToolSchemasChanged should be true")
	}
	if len(state.PendingChanges.ChangedToolSchemas) != 1 || state.PendingChanges.ChangedToolSchemas[0] != "Write" {
		t.Errorf("ChangedToolSchemas should be [Write], got %v", state.PendingChanges.ChangedToolSchemas)
	}
}

func TestRecordPromptState_UntrackedSource(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "speculation"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	if _, ok := stateStore["speculation"]; ok {
		t.Error("untracked sources should not be stored")
	}
}

// --- CheckResponseForCacheBreak tests ---

func setupStateForCheck() PromptStateKey {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	// Simulate prev cache read
	val := stateStore["repl_main_thread"]
	val.PrevCacheRead = 10000
	return key
}

func TestCheckResponse_Normal(t *testing.T) {
	key := setupStateForCheck()
	// 5% drop tolerance: 10000 * 0.95 = 9500
	CheckResponseForCacheBreak(key, 9600, 0, nil)
	// Should not log a break — verify state is clean
	state := stateStore["repl_main_thread"]
	if state.PendingChanges != nil {
		t.Error("no break should mean pendingChanges is nil")
	}
}

func TestCheckResponse_Drop5Percent(t *testing.T) {
	key := setupStateForCheck()
	// Simulate a change that will trigger pending changes
	RecordPromptState(makeSystemBlocks("sys CHANGED"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	// Drop from 10000 to 5000 = 50% drop, well above 5% and 2000
	CheckResponseForCacheBreak(key, 5000, 100, nil)

	state := stateStore["repl_main_thread"]
	// After check, pendingChanges should be cleared
	if state.PendingChanges != nil {
		t.Error("pendingChanges should be cleared after check")
	}
	// prevCacheRead should be updated
	if state.PrevCacheRead != 5000 {
		t.Errorf("prevCacheRead should be updated to 5000, got %d", state.PrevCacheRead)
	}
}

func TestCheckResponse_DropBelow2000(t *testing.T) {
	key := setupStateForCheck()
	// Drop from 10000 to 9000 = 1000 drop (below 2000 threshold)
	// Even though 10% drop, absolute < 2000
	CheckResponseForCacheBreak(key, 9000, 0, nil)

	state := stateStore["repl_main_thread"]
	if state.PendingChanges != nil {
		t.Error("drop < 2000 tokens should not trigger break")
	}
}

func TestCheckResponse_HaikuExcluded(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "claude-haiku-4-5", nil, "", false, false, false, false, "", 0)
	val := stateStore["repl_main_thread"]
	val.PrevCacheRead = 10000

	// Haiku should be excluded — no break detection
	CheckResponseForCacheBreak(key, 1000, 0, nil)
	// TS returns before updating prevCacheRead for excluded models
	val2 := stateStore["repl_main_thread"]
	if val2.PrevCacheRead != 10000 {
		t.Error("prevCacheRead should NOT be updated for excluded models (TS returns early)")
	}
}

func TestCheckResponse_FirstCall(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	// prevCacheRead = 0 (first call)

	CheckResponseForCacheBreak(key, 5000, 100, nil)
	// Should skip — first call has no previous to compare
	state := stateStore["repl_main_thread"]
	if state.PrevCacheRead != 5000 {
		t.Errorf("prevCacheRead should be updated to 5000, got %d", state.PrevCacheRead)
	}
}

func TestCheckResponse_CacheDeletionsPending(t *testing.T) {
	key := setupStateForCheck()
	NotifyCacheDeletion(key)

	// Large drop but should be tolerated
	CheckResponseForCacheBreak(key, 1000, 0, nil)

	state := stateStore["repl_main_thread"]
	if state.CacheDeletionsPending {
		t.Error("cacheDeletionsPending should be reset after check")
	}
}

func TestCheckResponse_NoPrevState(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	// No state stored — should not panic
	CheckResponseForCacheBreak(key, 5000, 100, nil)
}

// --- Notify tests ---

func TestNotifyCacheDeletion(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	NotifyCacheDeletion(key)

	state := stateStore["repl_main_thread"]
	if !state.CacheDeletionsPending {
		t.Error("cacheDeletionsPending should be true after NotifyCacheDeletion")
	}
}

func TestNotifyCompaction(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	val := stateStore["repl_main_thread"]
	val.PrevCacheRead = 10000

	NotifyCompaction(key)

	val2 := stateStore["repl_main_thread"]
	state := val2
	if state.PrevCacheRead != 0 {
		t.Errorf("prevCacheRead should be reset to 0, got %d", state.PrevCacheRead)
	}
}

func TestCleanupAgentTracking(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "agent:custom", AgentID: "test-agent-1"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	CleanupAgentTracking("test-agent-1")

	if _, ok := stateStore["test-agent-1"]; ok {
		t.Error("agent state should be deleted after cleanup")
	}
}

func TestResetPromptCacheBreakDetection(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	ResetPromptCacheBreakDetection()

	if _, ok := stateStore["repl_main_thread"]; ok {
		t.Error("state should be cleared after reset")
	}
	muState.Lock()
	count := len(stateStore)
	muState.Unlock()
	if count != 0 {
		t.Errorf("stateStore length should be 0 after reset, got %d", count)
	}
}

func TestResetMainThreadCacheBreakDetection_PreservesSubAgents(t *testing.T) {
	resetGlobalState()

	// Record state for main thread and a sub-agent
	mainKey := PromptStateKey{QuerySource: "repl_main_thread"}
	agentKey := PromptStateKey{QuerySource: "agent:builtin:Explore", AgentID: "Explore"}
	RecordPromptState(makeSystemBlocks("main sys"), makeTools("Read"), mainKey, "sonnet", nil, "", false, false, false, false, "", 0)
	RecordPromptState(makeSystemBlocks("agent sys"), makeTools("Read", "Bash"), agentKey, "sonnet", nil, "", false, false, false, false, "", 0)

	// Verify both entries exist
	muState.Lock()
	if len(stateStore) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(stateStore))
	}
	muState.Unlock()

	// Reset main thread only
	ResetMainThreadCacheBreakDetection()

	// Main thread should be gone
	if _, ok := stateStore["repl_main_thread"]; ok {
		t.Error("main thread state should be cleared")
	}
	// Sub-agent should still exist (tracking key is the agentID "Explore", not the QuerySource)
	if _, ok := stateStore["Explore"]; !ok {
		t.Error("sub-agent state should be preserved")
	}
	muState.Lock()
	count := len(stateStore)
	muState.Unlock()
	if count != 1 {
		t.Errorf("stateStore length should be 1 after reset, got %d", count)
	}
}

// --- buildDiffableContent edge cases ---

func TestBuildDiffableContent_NoTools(t *testing.T) {
	content := buildDiffableContent(nil, nil, "model")
	if !strings.Contains(content, "=== Tools (0) ===") {
		t.Error("should show 0 tools")
	}
}

func TestBuildDiffableContent_ToolWithoutName(t *testing.T) {
	tools := []map[string]any{
		{"description": "unnamed tool"},
	}
	content := buildDiffableContent(nil, tools, "model")
	if !strings.Contains(content, "unknown") {
		t.Error("tool without name should show 'unknown'")
	}
}

// --- Integration: RecordPromptState + CheckResponseForCacheBreak ---

func TestIntegration_SystemChangeTriggersBreak(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	// First call
	RecordPromptState(makeSystemBlocks("v1"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	val := stateStore["repl_main_thread"]
	val.PrevCacheRead = 10000

	// Second call with changed system
	RecordPromptState(makeSystemBlocks("v2 - much longer system prompt"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	// Check response: large drop
	CheckResponseForCacheBreak(key, 5000, 200, nil)

	// Verify state is cleaned up
	val2 := stateStore["repl_main_thread"]
	state := val2
	if state.PendingChanges != nil {
		t.Error("pendingChanges should be cleared after break check")
	}
	if state.PrevCacheRead != 5000 {
		t.Errorf("prevCacheRead should be 5000, got %d", state.PrevCacheRead)
	}
}

// --- JSON marshal/unmarshal for new types ---

func TestUsageDelta_CacheTokenJSON(t *testing.T) {
	delta := UsageDelta{
		InputTokens:              100,
		OutputTokens:             50,
		CacheCreationInputTokens: 200,
		CacheReadInputTokens:     300,
	}
	b, err := json.Marshal(delta)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var got UsageDelta
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if got.CacheCreationInputTokens != 200 {
		t.Errorf("CacheCreationInputTokens should be 200, got %d", got.CacheCreationInputTokens)
	}
	if got.CacheReadInputTokens != 300 {
		t.Errorf("CacheReadInputTokens should be 300, got %d", got.CacheReadInputTokens)
	}
}

func TestUsageDelta_OmitEmpty(t *testing.T) {
	delta := UsageDelta{InputTokens: 100, OutputTokens: 50}
	b, err := json.Marshal(delta)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	s := string(b)
	if strings.Contains(s, "cache_creation") {
		t.Error("cache fields should be omitted when zero")
	}
	if strings.Contains(s, "cache_read") {
		t.Error("cache fields should be omitted when zero")
	}
}

func TestCacheControlConfig(t *testing.T) {
	cc := types.CacheControlConfig{Type: "ephemeral", TTL: "1h", Scope: "global"}
	// Just verify the struct is usable
	if cc.Type != "ephemeral" {
		t.Error("Type should be ephemeral")
	}
}

func TestPromptStateKey_String(t *testing.T) {
	k1 := PromptStateKey{QuerySource: "repl_main_thread"}
	if got := k1.String(); got != "repl_main_thread" {
		t.Errorf("expected 'repl_main_thread', got %q", got)
	}
	k2 := PromptStateKey{QuerySource: "agent:custom", AgentID: "test-123"}
	if got := k2.String(); got != "agent:custom:test-123" {
		t.Errorf("expected 'agent:custom:test-123', got %q", got)
	}
}

func TestSystemBlockParam(t *testing.T) {
	cc := &types.CacheControlConfig{Type: "ephemeral", TTL: "1h"}
	block := SystemBlockParam{Type: "text", Text: "hello", CacheControl: cc}
	b, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(b), "cache_control") {
		t.Error("cache_control should be in JSON when set")
	}
}

// --- TTL label test ---

func TestTTLExpiryLabel(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{-1, ""},
		{1000, "server-side"},
		{6 * 60 * 1000, "5min"},
		{61 * 60 * 1000, "1h"},
	}
	for _, tt := range tests {
		got := ttlExpiryLabel(tt.ms)
		if got != tt.want {
			t.Errorf("ttlExpiryLabel(%d) = %q, want %q", tt.ms, got, tt.want)
		}
	}
}

// --- Sort stability test ---

func TestRecordPromptState_BetasSortedStorage(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", []string{"c", "a", "b"}, "", false, false, false, false, "", 0)
	state := stateStore["repl_main_thread"]

	want := []string{"a", "b", "c"}
	if !slicesEqual(state.Betas, want) {
		t.Errorf("Betas should be sorted: got %v, want %v", state.Betas, want)
	}
}

// --- TTL detection tests ---

func TestCheckResponse_TTLExpiry1Hour(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	val := stateStore["repl_main_thread"]
	val.PrevCacheRead = 10000

	// No change — pending changes will be nil, so reason should be TTL-based
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	// Message from 2 hours ago
	msgs := []messageWithTimestamp{
		{role: "assistant", timestamp: time.Now().Add(-2 * time.Hour)},
	}
	CheckResponseForCacheBreak(key, 5000, 100, msgs)

	val2 := stateStore["repl_main_thread"]
	if val2.PrevCacheRead != 5000 {
		t.Error("prevCacheRead should be updated")
	}
}

func TestCheckResponse_TTLExpiry5Min(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	val := stateStore["repl_main_thread"]
	val.PrevCacheRead = 10000

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	// Message from 10 minutes ago (>5min, <1h)
	msgs := []messageWithTimestamp{
		{role: "assistant", timestamp: time.Now().Add(-10 * time.Minute)},
	}
	CheckResponseForCacheBreak(key, 5000, 100, msgs)
}

func TestCheckResponse_ServerSide(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	val := stateStore["repl_main_thread"]
	val.PrevCacheRead = 10000

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	// Message from 1 minute ago (<5min)
	msgs := []messageWithTimestamp{
		{role: "assistant", timestamp: time.Now().Add(-1 * time.Minute)},
	}
	CheckResponseForCacheBreak(key, 5000, 100, msgs)
}

func TestCheckResponse_UnknownCause(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	val := stateStore["repl_main_thread"]
	val.PrevCacheRead = 10000

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	// No messages — unknown cause
	CheckResponseForCacheBreak(key, 5000, 100, nil)
}

func TestCheckResponse_WithDiffGeneration(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("v1"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	val := stateStore["repl_main_thread"]
	val.PrevCacheRead = 10000

	// Change system → triggers pendingChanges with buildPrevDiffableContent
	RecordPromptState(makeSystemBlocks("v2 - changed system"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	CheckResponseForCacheBreak(key, 5000, 100, nil)
	// Diff file should have been created (checked by slog output in test run)
}

func TestCheckResponse_BetasChangedReason(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", []string{"beta1"}, "", false, false, false, false, "", 0)
	val := stateStore["repl_main_thread"]
	val.PrevCacheRead = 10000

	// Add a new beta
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", []string{"beta1", "beta2"}, "", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(key, 5000, 100, nil)
}

func TestCheckResponse_ToolsChangedReason(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	val := stateStore["repl_main_thread"]
	val.PrevCacheRead = 10000

	// Add a tool
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read", "Write"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(key, 5000, 100, nil)
}

func TestCheckResponse_ToolSchemaChangedNoAddRemove(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	tools1 := []map[string]any{{"name": "Read", "description": "v1"}}
	tools2 := []map[string]any{{"name": "Read", "description": "v2"}}

	RecordPromptState(makeSystemBlocks("sys"), tools1, key, "sonnet", nil, "", false, false, false, false, "", 0)
	val := stateStore["repl_main_thread"]
	val.PrevCacheRead = 10000

	RecordPromptState(makeSystemBlocks("sys"), tools2, key, "sonnet", nil, "", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(key, 5000, 100, nil)
}

func TestCheckResponse_GlobalCacheStrategyChanged(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "tool_based", false, false, false, false, "", 0)
	val := stateStore["repl_main_thread"]
	val.PrevCacheRead = 10000

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "system_prompt", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(key, 5000, 100, nil)
}

func TestCheckResponse_CacheControlStandalone(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	sys1 := []map[string]any{
		{"type": "text", "text": "sys", "cache_control": map[string]any{"type": "ephemeral", "ttl": "5m"}},
	}
	sys2 := []map[string]any{
		{"type": "text", "text": "sys", "cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"}},
	}

	RecordPromptState(sys1, makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	val := stateStore["repl_main_thread"]
	val.PrevCacheRead = 10000

	RecordPromptState(sys2, makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(key, 5000, 100, nil)
}

func TestCheckResponse_ModelChangedReason(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	val := stateStore["repl_main_thread"]
	val.PrevCacheRead = 10000

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "opus", nil, "", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(key, 5000, 100, nil)
}

// --- computeHash error path ---

func TestComputeHash_UnmarshallableData(t *testing.T) {
	// Channels can't be marshaled to JSON
	h := computeHash(make(chan int))
	if h != 0 {
		t.Error("computeHash should return 0 for unmarshallable data")
	}
}

// --- Notify with untracked key ---

func TestNotifyCacheDeletion_Untracked(t *testing.T) {
	resetGlobalState()
	// Should not panic with untracked source
	key := PromptStateKey{QuerySource: "speculation"}
	NotifyCacheDeletion(key)
}

func TestNotifyCompaction_Untracked(t *testing.T) {
	resetGlobalState()
	key := PromptStateKey{QuerySource: "speculation"}
	NotifyCompaction(key)
}

// --- Eviction ordering test ---

func TestRecordPromptState_EvictionOrder(t *testing.T) {
	resetGlobalState()

	// Fill up to max
	for i := range maxTrackedSources {
		key := PromptStateKey{QuerySource: "repl_main_thread", AgentID: fmt.Sprintf("agent-%02d", i)}
		RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	}

	// Add one more — should evict agent-00
	key := PromptStateKey{QuerySource: "repl_main_thread", AgentID: "agent-99"}
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)

	// agent-00 should be evicted
	if _, ok := stateStore["agent-00"]; ok {
		t.Error("agent-00 should have been evicted (oldest)")
	}
	// agent-01 should still exist
	if _, ok := stateStore["agent-01"]; !ok {
		t.Error("agent-01 should still exist")
	}
	// agent-99 should exist
	if _, ok := stateStore["agent-99"]; !ok {
		t.Error("agent-99 should exist (newly added)")
	}
}

// --- Tests for 6 new change detection flags (fastMode, autoMode, overage, cachedMC, effort, extraBody) ---

func TestRecordPromptState_FastModeChange(t *testing.T) {

	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	prev := stateStore[getTrackingKey("repl_main_thread", "")]
	if prev == nil {
		t.Fatal("expected state to be stored")
	}
	if prev.FastMode != false {
		t.Errorf("expected FastMode=false, got %v", prev.FastMode)
	}

	// Second call with fastMode=true — should set PendingChanges
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", true, false, false, false, "", 0)
	if prev.PendingChanges == nil {
		t.Fatal("expected PendingChanges for fastMode change")
	}
	if !prev.PendingChanges.FastModeChanged {
		t.Error("expected FastModeChanged=true")
	}
	if prev.FastMode != true {
		t.Errorf("expected FastMode updated to true, got %v", prev.FastMode)
	}
}

func TestRecordPromptState_AutoModeChange(t *testing.T) {

	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	prev := stateStore[getTrackingKey("repl_main_thread", "")]

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, true, false, false, "", 0)
	if prev.PendingChanges == nil {
		t.Fatal("expected PendingChanges for autoMode change")
	}
	if !prev.PendingChanges.AutoModeActiveChanged {
		t.Error("expected AutoModeActiveChanged=true")
	}
}

func TestRecordPromptState_OverageChange(t *testing.T) {

	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	prev := stateStore[getTrackingKey("repl_main_thread", "")]

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, true, false, "", 0)
	if prev.PendingChanges == nil {
		t.Fatal("expected PendingChanges for overage change")
	}
	if !prev.PendingChanges.OverageChanged {
		t.Error("expected OverageChanged=true")
	}
}

func TestRecordPromptState_CachedMCChange(t *testing.T) {

	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	prev := stateStore[getTrackingKey("repl_main_thread", "")]

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, true, "", 0)
	if prev.PendingChanges == nil {
		t.Fatal("expected PendingChanges for cachedMC change")
	}
	if !prev.PendingChanges.CachedMCEnabledChanged {
		t.Error("expected CachedMCEnabledChanged=true")
	}
}

func TestRecordPromptState_EffortChange(t *testing.T) {

	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "low", 0)
	prev := stateStore[getTrackingKey("repl_main_thread", "")]
	if prev.EffortValue != "low" {
		t.Errorf("expected EffortValue=low, got %q", prev.EffortValue)
	}

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "high", 0)
	if prev.PendingChanges == nil {
		t.Fatal("expected PendingChanges for effort change")
	}
	if !prev.PendingChanges.EffortChanged {
		t.Error("expected EffortChanged=true")
	}
	if prev.PendingChanges.PrevEffortValue != "low" {
		t.Errorf("expected PrevEffortValue=low, got %q", prev.PendingChanges.PrevEffortValue)
	}
	if prev.PendingChanges.NewEffortValue != "high" {
		t.Errorf("expected NewEffortValue=high, got %q", prev.PendingChanges.NewEffortValue)
	}
}

func TestRecordPromptState_ExtraBodyChange(t *testing.T) {

	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 12345)
	prev := stateStore[getTrackingKey("repl_main_thread", "")]
	if prev.ExtraBodyHash != 12345 {
		t.Errorf("expected ExtraBodyHash=12345, got %d", prev.ExtraBodyHash)
	}

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 99999)
	if prev.PendingChanges == nil {
		t.Fatal("expected PendingChanges for extraBody change")
	}
	if !prev.PendingChanges.ExtraBodyChanged {
		t.Error("expected ExtraBodyChanged=true")
	}
}

func TestRecordPromptState_NoChangeWithSameNewParams(t *testing.T) {

	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", true, true, true, true, "high", 42)
	prev := stateStore[getTrackingKey("repl_main_thread", "")]
	if prev.PendingChanges != nil {
		t.Fatal("first call should not have PendingChanges")
	}

	// Same params — no change
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", true, true, true, true, "high", 42)
	if prev.PendingChanges != nil {
		t.Errorf("expected no PendingChanges when all params same, got %+v", prev.PendingChanges)
	}
}

func TestCheckResponseForCacheBreak_FastModeExplanation(t *testing.T) {

	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	var logBuf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(old)

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "", 0)
	// Set PrevCacheRead via first response check
	CheckResponseForCacheBreak(key, 10000, 100, nil)

	// Change fastMode
	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", true, false, false, false, "", 0)

	CheckResponseForCacheBreak(key, 5000, 100, nil)
	output := logBuf.String()
	if !strings.Contains(output, "fast mode toggled") {
		t.Errorf("expected 'fast mode toggled' in log, got: %s", output)
	}
	if !strings.Contains(output, "fastModeChanged") {
		t.Errorf("expected 'fastModeChanged' in log, got: %s", output)
	}
}

func TestCheckResponseForCacheBreak_EffortExplanation(t *testing.T) {

	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	var logBuf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(old)

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "low", 0)
	// Set PrevCacheRead via first response check
	CheckResponseForCacheBreak(key, 10000, 100, nil)

	RecordPromptState(makeSystemBlocks("sys"), makeTools("Read"), key, "sonnet", nil, "", false, false, false, false, "high", 0)

	CheckResponseForCacheBreak(key, 5000, 100, nil)
	output := logBuf.String()
	if !strings.Contains(output, "effort changed") {
		t.Errorf("expected 'effort changed' in log, got: %s", output)
	}
}
