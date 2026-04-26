package toolresult

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewContentReplacementState(t *testing.T) {
	state := NewContentReplacementState()
	if state == nil {
		t.Fatal("NewContentReplacementState returned nil")
	}
	if len(state.SeenIDs) != 0 {
		t.Error("SeenIDs should be empty")
	}
	if len(state.Replacements) != 0 {
		t.Error("Replacements should be empty")
	}
}

func TestCloneContentReplacementState(t *testing.T) {
	state := NewContentReplacementState()
	state.SeenIDs["id1"] = true
	state.Replacements["id1"] = "preview1"

	clone := CloneContentReplacementState(state)
	if clone == nil {
		t.Fatal("CloneContentReplacementState returned nil")
	}
	if clone.SeenIDs["id1"] != true {
		t.Error("clone should have same SeenIDs")
	}
	if clone.Replacements["id1"] != "preview1" {
		t.Error("clone should have same Replacements")
	}

	// Mutating clone should not affect original
	clone.SeenIDs["id2"] = true
	if state.SeenIDs["id2"] {
		t.Error("mutating clone affected original")
	}
}

func TestCloneContentReplacementState_Nil(t *testing.T) {
	if CloneContentReplacementState(nil) != nil {
		t.Error("cloning nil should return nil")
	}
}

func TestContentSize(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"empty", "", 0},
		{"string", `"hello world"`, 11},
		{"text blocks", `[{"type":"text","text":"hello"},{"type":"text","text":" world"}]`, 11},
		{"mixed blocks", `[{"type":"text","text":"abc"},{"type":"image","source":{}}]`, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContentSize(json.RawMessage(tt.content))
			if got != tt.want {
				t.Errorf("ContentSize() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestIsContentAlreadyCompacted(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", false},
		{"normal text", `"hello"`, false},
		{"persisted output", `"<persisted-output>some preview</persisted-output>"`, true},
		{"not starting with tag", `"prefix <persisted-output>...</persisted-output>"`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsContentAlreadyCompacted(json.RawMessage(tt.content))
			if got != tt.want {
				t.Errorf("IsContentAlreadyCompacted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnforceToolResultBudget_NilState(t *testing.T) {
	msgs := []BudgetMessage{{Role: "user"}}
	result, records := EnforceToolResultBudget(msgs, nil, "session", nil)
	if len(result) != 1 {
		t.Error("should return messages unchanged")
	}
	if records != nil {
		t.Error("should return nil records")
	}
}

func TestApplyToolResultBudget_NilState(t *testing.T) {
	msg := BudgetMessage{Role: "user"}
	result := ApplyToolResultBudget([]BudgetMessage{msg}, nil, "session", nil)
	if len(result) != 1 {
		t.Error("should return messages unchanged")
	}
}

func TestApplyToolResultBudget_ReturnsMessages(t *testing.T) {
	state := NewContentReplacementState()
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1"},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`"small"`)},
		}},
	}
	result := ApplyToolResultBudget(msgs, state, "test-session", nil)
	if len(result) != 2 {
		t.Errorf("expected 2 messages, got %d", len(result))
	}
}

func TestEnforceToolResultBudget_UnderLimit(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	state := NewContentReplacementState()
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1"},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`"small content"`)},
		}},
	}
	result, records := EnforceToolResultBudget(msgs, state, "test-session", nil)
	if len(records) != 0 {
		t.Errorf("expected no replacements, got %d", len(records))
	}
	// Content should be unchanged
	var s string
	if err := json.Unmarshal(result[1].Content[0].Content, &s); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if s != "small content" {
		t.Errorf("content changed unexpectedly: %q", s)
	}
	// ID should be in SeenIDs now
	if !state.SeenIDs["tr-1"] {
		t.Error("tr-1 should be in SeenIDs")
	}
}

func TestEnforceToolResultBudget_OverLimit(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	state := NewContentReplacementState()
	bigContent := strings.Repeat("x", MaxToolResultsPerMessageChars+50000)
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{
			{Type: "tool_use", ID: "tu-1", Name: "Bash"},
		}},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(mustMarshalStr(bigContent))},
		}},
	}
	result, records := EnforceToolResultBudget(msgs, state, "test-session", nil)
	if len(records) == 0 {
		t.Fatal("expected at least one replacement")
	}
	if records[0].ToolUseID != "tr-1" {
		t.Errorf("replacement ToolUseID = %q, want %q", records[0].ToolUseID, "tr-1")
	}
	// Output should contain persisted-output tag
	var s string
	if err := json.Unmarshal(result[1].Content[0].Content, &s); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if !strings.Contains(s, PersistedOutputTag) {
		t.Error("output should contain persisted-output tag")
	}
}

func TestEnforceToolResultBudget_MustReapply(t *testing.T) {
	state := NewContentReplacementState()
	state.SeenIDs["tr-1"] = true
	state.Replacements["tr-1"] = "cached preview"

	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1"},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`"original large content"`)},
		}},
	}
	result, records := EnforceToolResultBudget(msgs, state, "test-session", nil)
	if len(records) != 0 {
		t.Errorf("no new replacements expected, got %d", len(records))
	}
	// Content should be the cached replacement
	var s string
	if err := json.Unmarshal(result[1].Content[0].Content, &s); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if s != "cached preview" {
		t.Errorf("content = %q, want cached preview", s)
	}
}

func TestEnforceToolResultBudget_Frozen(t *testing.T) {
	state := NewContentReplacementState()
	state.SeenIDs["tr-1"] = true
	// No replacement — tr-1 is frozen

	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1"},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`"frozen content"`)},
		}},
	}
	result, records := EnforceToolResultBudget(msgs, state, "test-session", nil)
	if len(records) != 0 {
		t.Errorf("frozen content should not be replaced, got %d records", len(records))
	}
	var s string
	if err := json.Unmarshal(result[1].Content[0].Content, &s); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if s != "frozen content" {
		t.Errorf("frozen content changed: %q", s)
	}
}

func TestEnforceToolResultBudget_SkipReadTool(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	state := NewContentReplacementState()
	bigContent := strings.Repeat("x", MaxToolResultsPerMessageChars+50000)
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{
			{Type: "tool_use", ID: "tr-1", Name: "Read"},
		}},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(mustMarshalStr(bigContent))},
		}},
	}

	skipNames := map[string]bool{"Read": true}
	result, records := EnforceToolResultBudget(msgs, state, "test-session", skipNames)
	if len(records) != 0 {
		t.Errorf("Read tool should be skipped, got %d replacements", len(records))
	}
	// Content unchanged
	var s string
	if err := json.Unmarshal(result[1].Content[0].Content, &s); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if s != bigContent {
		t.Error("Read tool content was changed despite skip")
	}
}

func TestSelectFreshToReplace(t *testing.T) {
	fresh := []toolResultCandidate{
		{toolUseID: "a", size: 50000},
		{toolUseID: "b", size: 100000},
		{toolUseID: "c", size: 30000},
	}
	// frozenSize=0, limit=100000 → total=180000, need to shed 80000
	selected := selectFreshToReplace(fresh, 0, 100000)
	if len(selected) == 0 {
		t.Fatal("expected at least one selection")
	}
	// Largest (b:100000) should be selected first
	if selected[0].toolUseID != "b" {
		t.Errorf("expected b first (largest), got %q", selected[0].toolUseID)
	}
}

func TestPartitionByPriorDecision(t *testing.T) {
	state := NewContentReplacementState()
	state.SeenIDs["frozen-id"] = true
	state.Replacements["reapply-id"] = "cached"

	candidates := []toolResultCandidate{
		{toolUseID: "fresh-id", size: 10},
		{toolUseID: "frozen-id", size: 20},
		{toolUseID: "reapply-id", size: 30},
	}

	p := partitionByPriorDecision(candidates, state)
	if len(p.fresh) != 1 || p.fresh[0].toolUseID != "fresh-id" {
		t.Errorf("fresh = %v, want [fresh-id]", p.fresh)
	}
	if len(p.frozen) != 1 || p.frozen[0].toolUseID != "frozen-id" {
		t.Errorf("frozen = %v, want [frozen-id]", p.frozen)
	}
	if len(p.mustReapply) != 1 || p.mustReapply[0].toolUseID != "reapply-id" {
		t.Errorf("mustReapply = %v, want [reapply-id]", p.mustReapply)
	}
}

func TestCollectCandidatesByMessage(t *testing.T) {
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1"},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`"content1"`)},
		}},
		{Role: "assistant", ID: "asst-2"},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-2", Content: json.RawMessage(`"content2"`)},
		}},
	}
	groups := collectCandidatesByMessage(msgs)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	if groups[0][0].toolUseID != "tr-1" {
		t.Errorf("group 0 = %q, want tr-1", groups[0][0].toolUseID)
	}
	if groups[1][0].toolUseID != "tr-2" {
		t.Errorf("group 1 = %q, want tr-2", groups[1][0].toolUseID)
	}
}

func TestCollectCandidatesByMessage_SameAssistantID(t *testing.T) {
	// Same assistant ID should NOT create a boundary (streaming fragments)
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1"},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`"c1"`)},
		}},
		{Role: "assistant", ID: "asst-1"}, // same ID — no boundary
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-2", Content: json.RawMessage(`"c2"`)},
		}},
	}
	groups := collectCandidatesByMessage(msgs)
	if len(groups) != 1 {
		t.Fatalf("same assistant ID should not split, got %d groups", len(groups))
	}
	if len(groups[0]) != 2 {
		t.Errorf("group should have 2 candidates, got %d", len(groups[0]))
	}
}

func TestReconstructContentReplacementState(t *testing.T) {
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{
			{Type: "tool_use", ID: "tu-1", Name: "Bash"},
		}},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`"big content"`)},
			{Type: "tool_result", ToolUseID: "tr-2", Content: json.RawMessage(`"small"`)},
		}},
	}
	records := []ContentReplacementRecord{
		{Kind: "tool-result", ToolUseID: "tr-1", Replacement: "preview"},
	}

	state := ReconstructContentReplacementState(msgs, records, nil)
	if !state.SeenIDs["tr-1"] {
		t.Error("tr-1 should be in SeenIDs")
	}
	if !state.SeenIDs["tr-2"] {
		t.Error("tr-2 should be in SeenIDs")
	}
	if state.Replacements["tr-1"] != "preview" {
		t.Error("tr-1 replacement should be 'preview'")
	}
	if _, ok := state.Replacements["tr-2"]; ok {
		t.Error("tr-2 should not have a replacement")
	}
}

func TestReconstructForSubagentResume_NilParent(t *testing.T) {
	result := ReconstructForSubagentResume(nil, nil, nil)
	if result != nil {
		t.Error("nil parent should return nil")
	}
}

func TestReconstructContentReplacementState_InheritedGapFill(t *testing.T) {
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{
			{Type: "tool_use", ID: "tu-1", Name: "Bash"},
		}},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`"content"`)},
			{Type: "tool_result", ToolUseID: "tr-2", Content: json.RawMessage(`"content2"`)},
		}},
	}

	// Only tr-1 has a record. tr-2 has no record but parent has a replacement.
	records := []ContentReplacementRecord{
		{Kind: "tool-result", ToolUseID: "tr-1", Replacement: "preview1"},
	}
	inherited := map[string]string{"tr-2": "parent-preview2"}

	state := ReconstructContentReplacementState(msgs, records, inherited)
	if state.Replacements["tr-1"] != "preview1" {
		t.Error("tr-1 replacement should come from records")
	}
	if state.Replacements["tr-2"] != "parent-preview2" {
		t.Error("tr-2 replacement should be gap-filled from inherited")
	}
}

func TestReconstructContentReplacementState_InheritedNoOverwrite(t *testing.T) {
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1"},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`"content"`)},
		}},
	}

	// Record takes precedence over inherited.
	records := []ContentReplacementRecord{
		{Kind: "tool-result", ToolUseID: "tr-1", Replacement: "from-record"},
	}
	inherited := map[string]string{"tr-1": "from-parent"}

	state := ReconstructContentReplacementState(msgs, records, inherited)
	if state.Replacements["tr-1"] != "from-record" {
		t.Errorf("record should take precedence over inherited, got %q", state.Replacements["tr-1"])
	}
}

func TestReconstructForSubagentResume_MergesParent(t *testing.T) {
	parent := NewContentReplacementState()
	parent.SeenIDs["tr-1"] = true
	parent.Replacements["tr-1"] = "parent-preview"

	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1"},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`"content"`)},
		}},
	}

	state := ReconstructForSubagentResume(parent, msgs, nil)
	if state == nil {
		t.Fatal("expected non-nil state")
	}
	if state.Replacements["tr-1"] != "parent-preview" {
		t.Errorf("should gap-fill from parent replacements, got %q", state.Replacements["tr-1"])
	}
}


func TestEnforceToolResultBudget_FrozenAndFresh(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	state := NewContentReplacementState()
	// tr-1 is frozen (seen but no replacement)
	state.SeenIDs["tr-1"] = true

	bigContent := strings.Repeat("x", MaxToolResultsPerMessageChars-1000)
	msg := []BudgetMessage{
		{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{
			{Type: "tool_use", ID: "tu-1", Name: "Bash"},
		}},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(mustMarshalStr(bigContent))},
			{Type: "tool_result", ToolUseID: "tr-2", Content: json.RawMessage(mustMarshalStr(bigContent))},
		}},
	}
	result, records := EnforceToolResultBudget(msg, state, "test-session", nil)
	// tr-1 is frozen, tr-2 is fresh and should be persisted to bring total under limit
	if len(records) == 0 {
		t.Fatal("expected at least one replacement (tr-2 is fresh and exceeds budget with frozen)")
	}
	if records[0].ToolUseID != "tr-2" {
		t.Errorf("expected tr-2 to be replaced, got %q", records[0].ToolUseID)
	}
	var s string
	if err := json.Unmarshal(result[1].Content[1].Content, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(s, PersistedOutputTag) {
		t.Error("tr-2 output should contain persisted-output tag")
	}
	if !state.SeenIDs["tr-2"] {
		t.Error("tr-2 should be in SeenIDs")
	}
}

func TestBuildBudgetReplacement_EmptySessionID(t *testing.T) {
	c := toolResultCandidate{toolUseID: "tr-1", content: "data", size: 100}
	result := buildBudgetReplacement(c, "")
	if result != "" {
		t.Errorf("expected empty string for empty sessionID, got %q", result)
	}
}

func TestCollectCandidatesFromMessage_SkipsImage(t *testing.T) {
	msg := BudgetMessage{Role: "user", Content: []BudgetBlock{
		{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`[{"type":"image","source":{}}]`)},
		{Type: "tool_result", ToolUseID: "tr-2", Content: json.RawMessage(`"text"`)},
	}}
	candidates := collectCandidatesFromMessage(msg)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (skip image), got %d", len(candidates))
	}
	if candidates[0].toolUseID != "tr-2" {
		t.Errorf("expected tr-2, got %q", candidates[0].toolUseID)
	}
}

func TestCollectCandidatesFromMessage_NonUser(t *testing.T) {
	msg := BudgetMessage{Role: "assistant"}
	candidates := collectCandidatesFromMessage(msg)
	if len(candidates) != 0 {
		t.Errorf("expected 0 candidates for non-user message, got %d", len(candidates))
	}
}

func TestEnforceToolResultBudget_EmptySessionBuildFails(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	state := NewContentReplacementState()
	bigContent := strings.Repeat("x", MaxToolResultsPerMessageChars+50000)
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{
			{Type: "tool_use", ID: "tu-1", Name: "Bash"},
		}},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(mustMarshalStr(bigContent))},
		}},
	}
	// Empty sessionID → buildBudgetReplacement returns empty → no replacement
	result, records := EnforceToolResultBudget(msgs, state, "", nil)
	if len(records) != 0 {
		t.Errorf("expected no records with empty sessionID, got %d", len(records))
	}
	var s string
	if err := json.Unmarshal(result[1].Content[0].Content, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s != bigContent {
		t.Error("content should be unchanged when persist fails")
	}
}

func TestContentSize_BlockArrayWithNonText(t *testing.T) {
	content := json.RawMessage(`[{"type":"image","source":{}},{"type":"text","text":"hello"}]`)
	got := ContentSize(content)
	if got != 5 {
		t.Errorf("ContentSize = %d, want 5 (only text blocks counted)", got)
	}
}

func TestIsContentAlreadyCompacted_NotString(t *testing.T) {
	content := json.RawMessage(`[{"type":"text","text":"hello"}]`)
	if IsContentAlreadyCompacted(content) {
		t.Error("block array should not be detected as compacted")
	}
}

func TestReconstructContentReplacementState_EmptyMessages(t *testing.T) {
	state := ReconstructContentReplacementState(nil, nil, nil)
	if state == nil {
		t.Fatal("expected non-nil state")
	}
	if len(state.SeenIDs) != 0 {
		t.Error("SeenIDs should be empty")
	}
}

func TestReconstructContentReplacementState_UnknownRecordIgnored(t *testing.T) {
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1"},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`"content"`)},
		}},
	}
	records := []ContentReplacementRecord{
		{Kind: "other", ToolUseID: "tr-1", Replacement: "preview"}, // wrong kind
	}
	state := ReconstructContentReplacementState(msgs, records, nil)
	if _, ok := state.Replacements["tr-1"]; ok {
		t.Error("wrong kind record should not create replacement")
	}
}

func TestReconstructContentReplacementState_RecordNotInMessages(t *testing.T) {
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1"},
	}
	records := []ContentReplacementRecord{
		{Kind: "tool-result", ToolUseID: "tr-999", Replacement: "orphan"},
	}
	state := ReconstructContentReplacementState(msgs, records, nil)
	if _, ok := state.Replacements["tr-999"]; ok {
		t.Error("record for ID not in messages should be ignored")
	}
}


func TestContentSize_Fallback(t *testing.T) {
	// Non-string, non-array content falls through to len(content)
	got := ContentSize(json.RawMessage(`42`))
	if got != 2 {
		t.Errorf("ContentSize(42) = %d, want 2", got)
	}
}

func TestCollectCandidatesFromMessage_AlreadyCompacted(t *testing.T) {
	msg := BudgetMessage{Role: "user", Content: []BudgetBlock{
		{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`"<persisted-output>preview</persisted-output>"`)},
		{Type: "tool_result", ToolUseID: "tr-2", Content: json.RawMessage(`"normal"`)},
	}}
	candidates := collectCandidatesFromMessage(msg)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (skip compacted), got %d", len(candidates))
	}
	if candidates[0].toolUseID != "tr-2" {
		t.Errorf("expected tr-2, got %q", candidates[0].toolUseID)
	}
}

func TestCollectCandidatesFromMessage_NonStringContent(t *testing.T) {
	msg := BudgetMessage{Role: "user", Content: []BudgetBlock{
		{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`12345`)},
	}}
	candidates := collectCandidatesFromMessage(msg)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].content != "12345" {
		t.Errorf("content = %q, want 12345", candidates[0].content)
	}
}

func TestCollectCandidatesFromMessage_EmptyContent(t *testing.T) {
	msg := BudgetMessage{Role: "user", Content: []BudgetBlock{
		{Type: "tool_result", ToolUseID: "tr-1", Content: nil},
		{Type: "text"},
	}}
	candidates := collectCandidatesFromMessage(msg)
	if len(candidates) != 0 {
		t.Errorf("expected 0 candidates, got %d", len(candidates))
	}
}

func TestReplaceToolResultContents_NoMatches(t *testing.T) {
	msgs := []BudgetMessage{
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`"original"`)},
		}},
	}
	replacementMap := map[string]string{"tr-999": "replacement"}
	result := replaceToolResultContents(msgs, replacementMap)
	if result[0].Content[0].Content == nil {
		t.Error("content should be unchanged")
	}
}

func TestBuildBudgetReplacement_PersistError(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	// Create a read-only sessions dir to force PersistToolResult failure
	sessionDir := filepath.Join(tmpDir, ".gbot", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(sessionDir, 0o444); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(sessionDir, 0o755) }() // restore for cleanup

	c := toolResultCandidate{toolUseID: "tr-1", content: "data", size: 100}
	result := buildBudgetReplacement(c, "fail-session")
	if result != "" {
		t.Errorf("expected empty string on persist failure, got %q", result)
	}
}

func mustMarshalStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
