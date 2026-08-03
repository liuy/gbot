package types_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// uuidV4Regex matches a RFC 4122 v4 UUID string.
var uuidV4Regex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// ---------------------------------------------------------------------------
// Role constants
// ---------------------------------------------------------------------------

func TestRoleConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		role     types.Role
		expected string
	}{
		{"user", types.RoleUser, "user"},
		{"assistant", types.RoleAssistant, "assistant"},
		{"system", types.RoleSystem, "system"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.role) != tc.expected {
				t.Errorf("Role %s = %q, want %q", tc.name, tc.role, tc.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ContentType constants
// ---------------------------------------------------------------------------

func TestContentTypeConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ct       types.ContentType
		expected string
	}{
		{"text", types.ContentTypeText, "text"},
		{"tool_use", types.ContentTypeToolUse, "tool_use"},
		{"tool_result", types.ContentTypeToolResult, "tool_result"},
		{"thinking", types.ContentTypeThinking, "thinking"},
		{"image", types.ContentTypeImage, "image"},
		{"video", types.ContentTypeVideo, "video"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.ct) != tc.expected {
				t.Errorf("ContentType %s = %q, want %q", tc.name, tc.ct, tc.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ContentBlock constructors
// ---------------------------------------------------------------------------

func TestNewTextBlock(t *testing.T) {
	t.Parallel()

	block := types.NewTextBlock("hello world")
	if block.Type != types.ContentTypeText {
		t.Errorf("Type = %q, want %q", block.Type, types.ContentTypeText)
	}
	if block.Text != "hello world" {
		t.Errorf("Text = %q, want %q", block.Text, "hello world")
	}
}

func TestNewToolUseBlock(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"cmd":"ls"}`)
	block := types.NewToolUseBlock("id-1", "Bash", input)
	if block.Type != types.ContentTypeToolUse {
		t.Errorf("Type = %q, want %q", block.Type, types.ContentTypeToolUse)
	}
	if block.ID != "id-1" {
		t.Errorf("ID = %q, want %q", block.ID, "id-1")
	}
	if block.Name != "Bash" {
		t.Errorf("Name = %q, want %q", block.Name, "Bash")
	}
	if string(block.Input) != `{"cmd":"ls"}` {
		t.Errorf("Input = %s, want %s", block.Input, `{"cmd":"ls"}`)
	}
}

func TestNewToolResultBlock(t *testing.T) {
	t.Parallel()

	content := json.RawMessage(`"done"`)
	block := types.NewToolResultBlock("use-1", content, false)
	if block.Type != types.ContentTypeToolResult {
		t.Errorf("Type = %q, want %q", block.Type, types.ContentTypeToolResult)
	}
	if block.ToolUseID != "use-1" {
		t.Errorf("ToolUseID = %q, want %q", block.ToolUseID, "use-1")
	}
	if block.IsError {
		t.Error("IsError = true, want false")
	}

	errBlock := types.NewToolResultBlock("use-2", content, true)
	if !errBlock.IsError {
		t.Error("IsError = false, want true")
	}
}

func TestNewImageBlock(t *testing.T) {
	t.Parallel()

	src := types.ImageSource{Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="}
	block := types.NewImageBlock(src)
	if block.Type != types.ContentTypeImage {
		t.Fatalf("Type = %q, want %q", block.Type, types.ContentTypeImage)
	}
	if block.Source == nil {
		t.Fatal("Source = nil, want non-nil")
	}
	if block.Source.Type != "base64" {
		t.Errorf("Source.Type = %q, want %q", block.Source.Type, "base64")
	}
	if block.Source.MediaType != "image/png" {
		t.Errorf("Source.MediaType = %q, want %q", block.Source.MediaType, "image/png")
	}
	if block.Source.Data != "iVBORw0KGgo=" {
		t.Errorf("Source.Data = %q, want %q", block.Source.Data, "iVBORw0KGgo=")
	}
}

func TestImageSource_IsFileSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  *types.ImageSource
		want bool
	}{
		{"file source", &types.ImageSource{Type: "file", Path: "/x.png"}, true},
		{"base64 source", &types.ImageSource{Type: "base64", Data: "abc"}, false},
		{"empty type", &types.ImageSource{Type: ""}, false},
		{"nil source", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.src.IsFileSource(); got != tc.want {
				t.Errorf("IsFileSource() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestImageBlockJSONRoundTrip(t *testing.T) {
	t.Parallel()

	src := types.ImageSource{Type: "base64", MediaType: "image/jpeg", Data: "/9j/4AAQ"}
	block := types.NewImageBlock(src)

	data, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	// Anthropic API shape: {"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"..."}}
	want := `{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"/9j/4AAQ"}}`
	if got != want {
		t.Fatalf("JSON = %s\nwant  %s", got, want)
	}

	var back types.ContentBlock
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Type != types.ContentTypeImage {
		t.Fatalf("round-trip Type = %q, want %q", back.Type, types.ContentTypeImage)
	}
	if back.Source == nil || back.Source.MediaType != "image/jpeg" || back.Source.Data != "/9j/4AAQ" {
		t.Fatalf("round-trip Source = %+v, want media_type=image/jpeg data=/9j/4AAQ", back.Source)
	}
}

// ---------------------------------------------------------------------------
// ContentBlock JSON round-trip
// ---------------------------------------------------------------------------

func TestContentBlockJSONRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		block types.ContentBlock
	}{
		{
			"text block",
			types.NewTextBlock("some text"),
		},
		{
			"tool use block",
			types.NewToolUseBlock("id-2", "Grep", json.RawMessage(`{"pattern":"foo"}`)),
		},
		{
			"tool result block",
			types.NewToolResultBlock("use-3", json.RawMessage(`"output"`), true),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(tc.block)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			var got types.ContentBlock
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			if got.Type != tc.block.Type {
				t.Errorf("Type = %q, want %q", got.Type, tc.block.Type)
			}
			if got.Text != tc.block.Text {
				t.Errorf("Text = %q, want %q", got.Text, tc.block.Text)
			}
			if got.ID != tc.block.ID {
				t.Errorf("ID = %q, want %q", got.ID, tc.block.ID)
			}
			if got.Name != tc.block.Name {
				t.Errorf("Name = %q, want %q", got.Name, tc.block.Name)
			}
			if got.ToolUseID != tc.block.ToolUseID {
				t.Errorf("ToolUseID = %q, want %q", got.ToolUseID, tc.block.ToolUseID)
			}
			if got.IsError != tc.block.IsError {
				t.Errorf("IsError = %v, want %v", got.IsError, tc.block.IsError)
			}
		})
	}
}

// TestContentBlockMarshalJSON_DropsDuration verifies the custom MarshalJSON
// removes ThinkingDurationNs and ToolDurationNs from the wire form for every
// block type. The LLM API has no schema slot for these fields, so leaking
// them is a wire violation. Round-trip proves the value never returns.
func TestContentBlockMarshalJSON_DropsDuration(t *testing.T) {
	t.Parallel()

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeThinking, Thinking: "ponder", ThinkingDurationNs: 5_000_000_000},
		{Type: types.ContentTypeToolResult, ToolUseID: "tu1", Content: json.RawMessage(`"ok"`), ToolDurationNs: 3_000_000_000},
		types.NewTextBlock("plain text"),
	}
	for i, b := range blocks {
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("block[%d] marshal: %v", i, err)
		}
		if strings.Contains(string(data), "duration_ns") {
			t.Errorf("block[%d] wire form contains duration_ns: %s", i, string(data))
		}
		var got types.ContentBlock
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("block[%d] unmarshal: %v", i, err)
		}
		if got.ThinkingDurationNs != 0 {
			t.Errorf("block[%d] round-trip ThinkingDurationNs = %d, want 0 (duration must not survive)", i, got.ThinkingDurationNs)
		}
		if got.ToolDurationNs != 0 {
			t.Errorf("block[%d] round-trip ToolDurationNs = %d, want 0 (duration must not survive)", i, got.ToolDurationNs)
		}
	}
}

// TestMarshalContentBlocksForStorage_PreservesDuration verifies the storage
// helper produces JSON containing thinking_duration_ns and tool_duration_ns
// with the exact integer values from the input blocks, and that the result
// round-trips back into blocks with the same durations. This is the
// persistence contract: the DB string is the source of truth for replay.
func TestMarshalContentBlocksForStorage_PreservesDuration(t *testing.T) {
	t.Parallel()

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeThinking, Thinking: "ponder", ThinkingDurationNs: 1_500_000_000},
		{Type: types.ContentTypeToolResult, ToolUseID: "tu1", Content: json.RawMessage(`"ok"`), ToolDurationNs: 7_200_000_000},
	}
	data, err := types.MarshalContentBlocksForStorage(blocks)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `"thinking_duration_ns":1500000000`) {
		t.Errorf("storage form missing thinking_duration_ns=1500000000: %s", got)
	}
	if !strings.Contains(got, `"tool_duration_ns":7200000000`) {
		t.Errorf("storage form missing tool_duration_ns=7200000000: %s", got)
	}
	var back []types.ContentBlock
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back) != 2 {
		t.Fatalf("round-trip len = %d, want 2", len(back))
	}
	if back[0].ThinkingDurationNs != 1_500_000_000 {
		t.Errorf("back[0].ThinkingDurationNs = %d, want 1500000000", back[0].ThinkingDurationNs)
	}
	if back[1].ToolDurationNs != 7_200_000_000 {
		t.Errorf("back[1].ToolDurationNs = %d, want 7200000000", back[1].ToolDurationNs)
	}

	// Empty input must produce literal "[]" so DB columns never see Go's
	// default "null".
	empty, err := types.MarshalContentBlocksForStorage(nil)
	if err != nil {
		t.Fatalf("nil marshal: %v", err)
	}
	if string(empty) != "[]" {
		t.Errorf("nil marshal = %s, want []", string(empty))
	}
}

// TestContentBlockMarshalJSON_WireStructMirror is a maintenance guard: it
// verifies that every non-duration field of ContentBlock is present in the
// wire projection produced by MarshalJSON. If a future field is added to
// ContentBlock and intended for the wire but forgotten in the wire struct,
// its substring assertion here must be added too — its absence during code
// review is the red flag.
func TestContentBlockMarshalJSON_WireStructMirror(t *testing.T) {
	t.Parallel()

	block := types.ContentBlock{
		Type:               types.ContentTypeToolResult,
		Text:               "T",
		Thinking:           "Th",
		Signature:          "S",
		ID:                 "I",
		Name:               "N",
		Input:              json.RawMessage(`{}`),
		ToolUseID:          "TUI",
		Content:            json.RawMessage(`[]`),
		IsError:            true,
		Data:               "D",
		Source:             &types.ImageSource{Type: "base64", MediaType: "image/jpeg", Data: "x"},
		CacheControl:       &types.CacheControlConfig{Type: "ephemeral"},
		ThinkingDurationNs: 999,
		ToolDurationNs:     999,
	}
	data, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	wantSubstrings := []string{
		`"type":`,
		`"text":"T"`,
		`"thinking":"Th"`,
		`"signature":"S"`,
		`"id":"I"`,
		`"name":"N"`,
		`"input":`,
		`"tool_use_id":"TUI"`,
		`"content":`,
		`"is_error":true`,
		`"data":"D"`,
		`"source":`,
		`"cache_control":`,
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(got, s) {
			t.Errorf("wire form missing substring %q: %s", s, got)
		}
	}
	if strings.Contains(got, "duration_ns") {
		t.Errorf("wire form must NOT contain duration_ns: %s", got)
	}
}

// ---------------------------------------------------------------------------
// Message JSON
// ---------------------------------------------------------------------------

func TestMessageJSON(t *testing.T) {
	t.Parallel()

	msg := types.Message{
		ID:         "msg-1",
		Role:       types.RoleUser,
		Content:    []types.ContentBlock{types.NewTextBlock("hello")},
		Model:      "claude-4-sonnet",
		StopReason: "end_turn",
		Usage: &types.Usage{
			InputTokens:  10,
			OutputTokens: 5,
		},
		Timestamp: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got types.Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.ID != "msg-1" {
		t.Errorf("ID = %q, want %q", got.ID, "msg-1")
	}
	if got.Role != types.RoleUser {
		t.Errorf("Role = %q, want %q", got.Role, types.RoleUser)
	}
	if len(got.Content) != 1 {
		t.Fatalf("Content length = %d, want 1", len(got.Content))
	}
	if got.Content[0].Text != "hello" {
		t.Errorf("Content[0].Text = %q, want %q", got.Content[0].Text, "hello")
	}
	if got.Model != "claude-4-sonnet" {
		t.Errorf("Model = %q, want %q", got.Model, "claude-4-sonnet")
	}
	if got.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", got.StopReason, "end_turn")
	}
	if got.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if got.Usage.InputTokens != 10 {
		t.Errorf("Usage.InputTokens = %d, want 10", got.Usage.InputTokens)
	}
	if got.Usage.OutputTokens != 5 {
		t.Errorf("Usage.OutputTokens = %d, want 5", got.Usage.OutputTokens)
	}
}

func TestMessageOmitEmpty(t *testing.T) {
	t.Parallel()

	// Minimal message — optional fields should be omitted
	msg := types.Message{
		Role:    types.RoleAssistant,
		Content: []types.ContentBlock{},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Ensure optional fields are not present in JSON output
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal to map: %v", err)
	}

	if _, ok := raw["id"]; ok {
		t.Error("id should be omitted")
	}
	if _, ok := raw["model"]; ok {
		t.Error("model should be omitted")
	}
	if _, ok := raw["stop_reason"]; ok {
		t.Error("stop_reason should be omitted")
	}
	if _, ok := raw["usage"]; ok {
		t.Error("usage should be omitted")
	}
}

// ---------------------------------------------------------------------------
// Usage JSON
// ---------------------------------------------------------------------------

func TestUsageJSON(t *testing.T) {
	t.Parallel()

	u := types.Usage{
		InputTokens:              100,
		OutputTokens:             50,
		CacheCreationInputTokens: 20,
		CacheReadInputTokens:     10,
	}

	data, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got types.Usage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", got.InputTokens)
	}
	if got.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", got.OutputTokens)
	}
	if got.CacheCreationInputTokens != 20 {
		t.Errorf("CacheCreationInputTokens = %d, want 20", got.CacheCreationInputTokens)
	}
	if got.CacheReadInputTokens != 10 {
		t.Errorf("CacheReadInputTokens = %d, want 10", got.CacheReadInputTokens)
	}
}

// ---------------------------------------------------------------------------
// Metadata JSON serialization (SetMetadataFromJSON / MetadataToJSON)
// ---------------------------------------------------------------------------

func TestSetMetadataFromJSON_Empty(t *testing.T) {
	var msg types.Message
	msg.SetMetadataFromJSON("")
	if msg.Usage != nil {
		t.Error("expected nil Usage for empty metadata")
	}
	if msg.Model != "" {
		t.Error("expected empty Model for empty metadata")
	}
	if msg.StopReason != "" {
		t.Error("expected empty StopReason for empty metadata")
	}
}

func TestSetMetadataFromJSON_FullMetadata(t *testing.T) {
	var msg types.Message
	msg.SetMetadataFromJSON(`{"usage":{"input_tokens":50000,"output_tokens":100,"cache_read_input_tokens":30000},"model":"sonnet","stop_reason":"end_turn"}`)
	if msg.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	if msg.Usage.InputTokens != 50000 {
		t.Errorf("InputTokens = %d, want 50000", msg.Usage.InputTokens)
	}
	if msg.Usage.OutputTokens != 100 {
		t.Errorf("OutputTokens = %d, want 100", msg.Usage.OutputTokens)
	}
	if msg.Usage.CacheReadInputTokens != 30000 {
		t.Errorf("CacheReadInputTokens = %d, want 30000", msg.Usage.CacheReadInputTokens)
	}
	if msg.Model != "sonnet" {
		t.Errorf("Model = %q, want %q", msg.Model, "sonnet")
	}
	if msg.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", msg.StopReason, "end_turn")
	}
}

func TestSetMetadataFromJSON_PartialMetadata(t *testing.T) {
	var msg types.Message
	msg.SetMetadataFromJSON(`{"model":"opus"}`)
	if msg.Usage != nil {
		t.Error("expected nil Usage for partial metadata")
	}
	if msg.Model != "opus" {
		t.Errorf("Model = %q, want %q", msg.Model, "opus")
	}
	if msg.StopReason != "" {
		t.Error("expected empty StopReason for partial metadata")
	}
}

func TestSetMetadataFromJSON_InvalidJSON(t *testing.T) {
	var msg types.Message
	msg.SetMetadataFromJSON(`{invalid json`)
	if msg.Usage != nil {
		t.Error("expected nil Usage for invalid JSON")
	}
	if msg.Model != "" {
		t.Error("expected empty Model for invalid JSON")
	}
}

func TestMetadataToJSON_NoFields(t *testing.T) {
	msg := types.Message{}
	got := msg.MetadataToJSON()
	if got != "" {
		t.Errorf("expected empty string for no fields, got %q", got)
	}
}

func TestMetadataToJSON_AllFields(t *testing.T) {
	msg := types.Message{
		Usage:      &types.Usage{InputTokens: 50000, OutputTokens: 100, CacheReadInputTokens: 30000},
		Model:      "sonnet",
		StopReason: "end_turn",
	}
	got := msg.MetadataToJSON()
	if !strings.Contains(got, `"usage"`) {
		t.Error("expected usage field in JSON")
	}
	if !strings.Contains(got, `"model":"sonnet"`) {
		t.Error("expected model field in JSON")
	}
	if !strings.Contains(got, `"stop_reason":"end_turn"`) {
		t.Error("expected stop_reason field in JSON")
	}
}

func TestMetadataToJSON_OnlyUsage(t *testing.T) {
	msg := types.Message{
		Usage: &types.Usage{InputTokens: 1000},
	}
	got := msg.MetadataToJSON()
	if !strings.Contains(got, `"usage"`) {
		t.Error("expected usage field in JSON")
	}
	if strings.Contains(got, `"model"`) {
		t.Error("should not contain model field when empty")
	}
}

func TestMetadataRoundTrip(t *testing.T) {
	original := types.Message{
		Usage:      &types.Usage{InputTokens: 80000, OutputTokens: 200, CacheReadInputTokens: 50000, CacheCreationInputTokens: 1000},
		Model:      "minimax-2.7",
		StopReason: "tool_use",
	}
	metadata := original.MetadataToJSON()

	var restored types.Message
	restored.SetMetadataFromJSON(metadata)

	if restored.Usage.InputTokens != 80000 {
		t.Errorf("InputTokens = %d, want 80000", restored.Usage.InputTokens)
	}
	if restored.Usage.OutputTokens != 200 {
		t.Errorf("OutputTokens = %d, want 200", restored.Usage.OutputTokens)
	}
	if restored.Usage.CacheReadInputTokens != 50000 {
		t.Errorf("CacheReadInputTokens = %d, want 50000", restored.Usage.CacheReadInputTokens)
	}
	if restored.Usage.CacheCreationInputTokens != 1000 {
		t.Errorf("CacheCreationInputTokens = %d, want 1000", restored.Usage.CacheCreationInputTokens)
	}
	if restored.Model != "minimax-2.7" {
		t.Errorf("Model = %q, want %q", restored.Model, "minimax-2.7")
	}
	if restored.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want %q", restored.StopReason, "tool_use")
	}
}

func TestMetadataRoundTrip_IsCompactSummary(t *testing.T) {
	// Only IsCompactSummary, no other metadata — tests the nil guard fix.
	original := types.Message{Flags: types.FlagCompactSummary}
	metadata := original.MetadataToJSON()
	if metadata == "" {
		t.Fatal("MetadataToJSON should not return empty for IsCompactSummary=true")
	}

	var restored types.Message
	restored.SetMetadataFromJSON(metadata)
	if !restored.HasFlag(types.FlagCompactSummary) {
		t.Error("FlagCompactSummary should be set after round trip")
	}

	// False case: default zero value should not produce metadata.
	original2 := types.Message{}
	if got := original2.MetadataToJSON(); got != "" {
		t.Errorf("expected empty for zero-value message, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// PermissionMode constants
// ---------------------------------------------------------------------------

func TestPermissionModeConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode types.PermissionMode
		want string
	}{
		{"acceptEdits", types.PermissionModeAcceptEdits, "acceptEdits"},
		{"bypass", types.PermissionModeBypass, "bypassPermissions"},
		{"default", types.PermissionModeDefault, "default"},
		{"dontAsk", types.PermissionModeDontAsk, "dontAsk"},
		{"plan", types.PermissionModePlan, "plan"},
		{"auto", types.PermissionModeAuto, "auto"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.mode) != tc.want {
				t.Errorf("PermissionMode %s = %q, want %q", tc.name, tc.mode, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PermissionBehavior constants
// ---------------------------------------------------------------------------

func TestPermissionBehaviorConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		b    types.PermissionBehavior
		want string
	}{
		{"allow", types.BehaviorAllow, "allow"},
		{"deny", types.BehaviorDeny, "deny"},
		{"ask", types.BehaviorAsk, "ask"},
		{"passthrough", types.BehaviorPassthrough, "passthrough"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.b) != tc.want {
				t.Errorf("PermissionBehavior %s = %q, want %q", tc.name, tc.b, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PermissionResult implementations
// ---------------------------------------------------------------------------

func TestPermissionAllowDecision(t *testing.T) {
	t.Parallel()

	var d types.PermissionResult = types.PermissionAllowDecision{}
	if d.Behavior() != types.BehaviorAllow {
		t.Errorf("Behavior() = %q, want %q", d.Behavior(), types.BehaviorAllow)
	}
}

func TestPermissionAskDecision(t *testing.T) {
	t.Parallel()

	var d types.PermissionResult = types.PermissionAskDecision{Message: "confirm?"}
	if d.Behavior() != types.BehaviorAsk {
		t.Errorf("Behavior() = %q, want %q", d.Behavior(), types.BehaviorAsk)
	}
}

func TestPermissionDenyDecision(t *testing.T) {
	t.Parallel()

	var d types.PermissionResult = types.PermissionDenyDecision{Message: "forbidden"}
	if d.Behavior() != types.BehaviorDeny {
		t.Errorf("Behavior() = %q, want %q", d.Behavior(), types.BehaviorDeny)
	}
}

// ---------------------------------------------------------------------------
// QueryEventType constants
// ---------------------------------------------------------------------------

func TestQueryEventTypeConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		et   types.QueryEventType
		want string
	}{
		{"query_start", types.EventQueryStart, "query_start"},
		{"query_end", types.EventQueryEnd, "query_end"},
		{"turn_start", types.EventTurnStart, "turn_start"},
		{"turn_end", types.EventTurnEnd, "turn_end"},
		{"text_delta", types.EventTextDelta, "text_delta"},
		{"tool_start", types.EventToolStart, "tool_start"},
		{"tool_param_delta", types.EventToolParamDelta, "tool_param_delta"},
		{"tool_output_delta", types.EventToolOutputDelta, "tool_output_delta"},
		{"tool_end", types.EventToolEnd, "tool_end"},
		{"usage", types.EventUsage, "usage"},
		{"ask", types.EventAsk, "ask"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.et) != tc.want {
				t.Errorf("QueryEventType %s = %q, want %q", tc.name, tc.et, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// QueryEvent JSON
// ---------------------------------------------------------------------------

func TestQueryEventJSON(t *testing.T) {
	t.Parallel()

	evt := types.QueryEvent{
		Type: types.EventTextDelta,
		Text: "hello",
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got types.QueryEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Type != types.EventTextDelta {
		t.Errorf("Type = %q, want %q", got.Type, types.EventTextDelta)
	}
	if got.Text != "hello" {
		t.Errorf("Text = %q, want %q", got.Text, "hello")
	}
}

// ---------------------------------------------------------------------------
// ContinueReason constants
// ---------------------------------------------------------------------------

func TestContinueReasonConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason types.ContinueReason
		want   string
	}{
		{"next_turn", types.ContinueNextTurn, "next_turn"},
		{"max_tokens_retry", types.ContinueMaxTokensRetry, "max_tokens_retry"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.reason) != tc.want {
				t.Errorf("ContinueReason %s = %q, want %q", tc.name, tc.reason, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LoopAction
// ---------------------------------------------------------------------------

func TestLoopAction(t *testing.T) {
	t.Parallel()

	action := types.LoopAction{
		Continue: true,
		Reason:   types.ContinueNextTurn,
	}

	if !action.Continue {
		t.Error("Continue = false, want true")
	}
	if action.Reason != types.ContinueNextTurn {
		t.Errorf("Reason = %q, want %q", action.Reason, types.ContinueNextTurn)
	}

	terminal := types.LoopAction{
		Continue: false,
	}

	if terminal.Continue {
		t.Error("Continue = true, want false")
	}
}

// ---------------------------------------------------------------------------
// ToolUseEvent / ToolResultEvent JSON
// ---------------------------------------------------------------------------

func TestToolUseEventJSON(t *testing.T) {
	t.Parallel()

	evt := types.ToolUseEvent{
		ID:    "tu-1",
		Name:  "Bash",
		Input: json.RawMessage(`{"command":"ls"}`),
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got types.ToolUseEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.ID != "tu-1" {
		t.Errorf("ID = %q, want %q", got.ID, "tu-1")
	}
	if got.Name != "Bash" {
		t.Errorf("Name = %q, want %q", got.Name, "Bash")
	}
}

func TestToolResultEventJSON(t *testing.T) {
	t.Parallel()

	evt := types.ToolResultEvent{
		ToolUseID: "tu-1",
		Output:    json.RawMessage(`"ok"`),
		IsError:   false,
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got types.ToolResultEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.ToolUseID != "tu-1" {
		t.Errorf("ToolUseID = %q, want %q", got.ToolUseID, "tu-1")
	}
	if got.IsError {
		t.Error("IsError = true, want false")
	}
}

// ---------------------------------------------------------------------------
// ToolUseContext
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Usage.TotalInputTokens
// ---------------------------------------------------------------------------

func TestTotalInputTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		u    types.Usage
		want int
	}{
		{"zero", types.Usage{}, 0},
		{"input only", types.Usage{InputTokens: 100}, 100},
		{"all fields", types.Usage{InputTokens: 100, CacheReadInputTokens: 30, CacheCreationInputTokens: 20}, 150},
		{"cache only", types.Usage{CacheReadInputTokens: 50, CacheCreationInputTokens: 50}, 100},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.u.TotalInputTokens()
			if got != tc.want {
				t.Errorf("TotalInputTokens() = %d, want %d", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// EventDispatcher (merged from events_test.go)
// ---------------------------------------------------------------------------

// mockDispatcher satisfies EventDispatcher for testing.
type mockDispatcher struct {
	events []types.QueryEvent
}

func (d *mockDispatcher) Dispatch(event types.QueryEvent) {
	d.events = append(d.events, event)
}

func TestEventDispatcher_Interface(t *testing.T) {
	var d types.EventDispatcher = &mockDispatcher{}

	d.Dispatch(types.QueryEvent{
		Type: types.EventQueryStart,
		Text: "test",
	})

	md := d.(*mockDispatcher)
	if len(md.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(md.events))
	}
	if md.events[0].Type != types.EventQueryStart {
		t.Errorf("expected EventQueryStart, got %s", md.events[0].Type)
	}
	if md.events[0].Text != "test" {
		t.Errorf("expected text 'test', got %q", md.events[0].Text)
	}
}

func TestEventDispatcher_NilCheck(t *testing.T) {
	var d types.EventDispatcher
	if d != nil {
		t.Error("expected nil EventDispatcher")
	}
}

// ---------------------------------------------------------------------------
// Skill types (merged from skills_test.go)
// ---------------------------------------------------------------------------

func TestSkillCommand_IsHidden(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{IsUserInvocable: true}
	if cmd.IsHidden() {
		t.Error("user-invocable skill should not be hidden")
	}

	cmd2 := &types.SkillCommand{IsUserInvocable: false}
	if !cmd2.IsHidden() {
		t.Error("agent-only skill should be hidden")
	}
}

func TestSkillCommand_UserFacingName(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{Name: "commit", DisplayName: "Git Commit"}
	if got := cmd.UserFacingName(); got != "Git Commit" {
		t.Errorf("UserFacingName() = %q, want %q", got, "Git Commit")
	}

	cmd2 := &types.SkillCommand{Name: "commit"}
	if got := cmd2.UserFacingName(); got != "commit" {
		t.Errorf("UserFacingName() = %q, want %q", got, "commit")
	}
}

func TestSkillCommand_MeetsAvailabilityRequirement(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{}
	if !cmd.MeetsAvailabilityRequirement() {
		t.Error("skill with no availability should meet requirement")
	}

	cmd2 := &types.SkillCommand{Availability: []string{"claude-ai"}}
	if !cmd2.MeetsAvailabilityRequirement() {
		t.Error("gbot has no auth tiers, should always pass")
	}
}

func TestSkillSource_Constants(t *testing.T) {
	t.Parallel()

	sources := map[types.SkillSource]string{
		types.SkillSourceBundled: "bundled",
		types.SkillSourceUser:    "user",
		types.SkillSourceProject: "project",
		types.SkillSourceManaged: "managed",
		types.SkillSourceMCP:     "mcp",
		types.SkillSourcePlugin:  "plugin",
	}
	for src, want := range sources {
		if string(src) != want {
			t.Errorf("SkillSource %q = %q, want %q", src, string(src), want)
		}
	}
}

func TestSkillCommand_Defaults(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{}
	if !cmd.IsHidden() {
		t.Error("zero-value SkillCommand should be hidden (IsUserInvocable defaults to false)")
	}
}

func TestSkillCommand_Context(t *testing.T) {
	t.Parallel()

	cmd := &types.SkillCommand{Context: ""}
	if cmd.Context != "" {
		t.Error("empty Context should mean inline")
	}

	cmd2 := &types.SkillCommand{Context: "fork"}
	if cmd2.Context != "fork" {
		t.Error("fork Context should be 'fork'")
	}
}

func TestCommandPermissionsAttachment(t *testing.T) {
	t.Parallel()

	att := types.CommandPermissionsAttachment{
		AllowedTools: []string{"Bash", "Read", "Write"},
		Model:        "haiku",
	}
	if len(att.AllowedTools) != 3 {
		t.Errorf("expected 3 allowed tools, got %d", len(att.AllowedTools))
	}
	if att.Model != "haiku" {
		t.Errorf("Model = %q, want %q", att.Model, "haiku")
	}
}

func TestInvokedSkillInfo(t *testing.T) {
	t.Parallel()

	info := types.InvokedSkillInfo{
		SkillName: "commit",
		SkillPath: "project:commit",
		Content:   "skill content here",
		AgentID:   "agent-1",
	}
	if info.SkillName != "commit" {
		t.Errorf("SkillName = %q, want %q", info.SkillName, "commit")
	}
	if info.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want %q", info.AgentID, "agent-1")
	}
}

// ---------------------------------------------------------------------------
// Permission ask dialog types
// ---------------------------------------------------------------------------

func TestUserDecisionConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		d    types.UserDecision
		want string
	}{
		{"allow", types.DecisionAllow, "allow"},
		{"deny", types.DecisionDeny, "deny"},
		{"allow_always", types.DecisionAllowAlways, "allow_always"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.d) != tc.want {
				t.Errorf("UserDecision %s = %q, want %q", tc.name, tc.d, tc.want)
			}
		})
	}
}

func TestAskEventFields(t *testing.T) {
	t.Parallel()

	ch := make(chan types.AskResponse, 1)
	evt := types.AskEvent{
		ToolName:   "Bash",
		Input:      json.RawMessage(`{"command":"rm -rf /tmp"}`),
		Message:    "permission required",
		RuleDetail: "Bash(rm -rf *) from project",
		AgentType:  "",
		ResponseCh: ch,
	}

	if evt.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want %q", evt.ToolName, "Bash")
	}
	if evt.Message != "permission required" {
		t.Errorf("Message = %q, want %q", evt.Message, "permission required")
	}
	if evt.RuleDetail != "Bash(rm -rf *) from project" {
		t.Errorf("RuleDetail = %q, want %q", evt.RuleDetail, "Bash(rm -rf *) from project")
	}
	if evt.AgentType != "" {
		t.Errorf("AgentType = %q, want empty", evt.AgentType)
	}
	if evt.ResponseCh == nil {
		t.Error("ResponseCh should not be nil")
	}
}

func TestAskEventResponseChRoundtrip(t *testing.T) {
	t.Parallel()

	ch := make(chan types.AskResponse, 1)
	evt := types.AskEvent{
		ToolName:   "Bash",
		Input:      json.RawMessage(`{"command":"ls"}`),
		Message:    "test",
		ResponseCh: ch,
	}

	// Simulate TUI writing a decision
	evt.ResponseCh <- types.AskResponse{Decision: types.DecisionAllow}

	// Engine reads the decision
	select {
	case d := <-evt.ResponseCh:
		if d.Decision != types.DecisionAllow {
			t.Errorf("got %v, want %v", d.Decision, types.DecisionAllow)
		}
	default:
		t.Fatal("expected decision on ResponseCh but got nothing")
	}
}

func TestAskEventWithAgentType(t *testing.T) {
	t.Parallel()

	ch := make(chan types.AskResponse, 1)
	evt := types.AskEvent{
		ToolName:   "Bash",
		Input:      json.RawMessage(`{"command":"ls"}`),
		Message:    "agent permission",
		AgentType:  "Explore",
		ResponseCh: ch,
	}

	if evt.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want %q", evt.AgentType, "Explore")
	}
}

func TestEventAskConstant(t *testing.T) {
	t.Parallel()

	if string(types.EventAsk) != "ask" {
		t.Errorf("EventAsk = %q, want %q", types.EventAsk, "ask")
	}
}

func TestQueryEventAskField(t *testing.T) {
	t.Parallel()

	ch := make(chan types.AskResponse, 1)
	evt := types.QueryEvent{
		Type: types.EventAsk,
		Ask: &types.AskEvent{
			ToolName:   "Write",
			Input:      json.RawMessage(`{"file_path":"test.go"}`),
			Message:    "write permission required",
			RuleDetail: "Write(*.go) from user settings",
			ResponseCh: ch,
		},
	}

	if evt.Type != types.EventAsk {
		t.Errorf("Type = %q, want %q", evt.Type, types.EventAsk)
	}
	if evt.Ask == nil {
		t.Fatal("PermissionAsk should not be nil")
	}
	if evt.Ask.ToolName != "Write" {
		t.Errorf("ToolName = %q, want %q", evt.Ask.ToolName, "Write")
	}
	if evt.Ask.RuleDetail != "Write(*.go) from user settings" {
		t.Errorf("RuleDetail = %q, want %q", evt.Ask.RuleDetail, "Write(*.go) from user settings")
	}
}

// ---------------------------------------------------------------------------
// Factory functions — NewUserMessage / NewAssistantMessage / NewSystemMessage
// ---------------------------------------------------------------------------

func TestNewUserMessage_AssignsUUID(t *testing.T) {
	t.Parallel()

	msg := types.NewUserMessage([]types.ContentBlock{types.NewTextBlock("test")})
	if !uuidV4Regex.MatchString(msg.ID) {
		t.Errorf("ID = %q, want a valid UUID v4", msg.ID)
	}
	if msg.Role != types.RoleUser {
		t.Errorf("Role = %q, want %q", msg.Role, types.RoleUser)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("len(Content) = %d, want 1", len(msg.Content))
	}
	if msg.Content[0].Text != "test" {
		t.Errorf("Content[0].Text = %q, want %q", msg.Content[0].Text, "test")
	}
	if msg.Timestamp.IsZero() {
		t.Error("Timestamp is zero, want non-zero")
	}
}

func TestNewUserMessage_UniqueIDs(t *testing.T) {
	t.Parallel()

	a := types.NewUserMessage(nil)
	b := types.NewUserMessage(nil)
	if a.ID == b.ID {
		t.Errorf("two calls produced same ID: %q — UUIDs must be unique", a.ID)
	}
}

func TestNewAssistantMessage_AssignsUUID(t *testing.T) {
	t.Parallel()

	msg := types.NewAssistantMessage([]types.ContentBlock{types.NewTextBlock("test")})
	if !uuidV4Regex.MatchString(msg.ID) {
		t.Errorf("ID = %q, want a valid UUID v4", msg.ID)
	}
	if msg.Role != types.RoleAssistant {
		t.Errorf("Role = %q, want %q", msg.Role, types.RoleAssistant)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("len(Content) = %d, want 1", len(msg.Content))
	}
	if msg.Timestamp.IsZero() {
		t.Error("Timestamp is zero, want non-zero")
	}
}

func TestNewSystemMessage_AssignsUUID(t *testing.T) {
	t.Parallel()

	msg := types.NewSystemMessage([]types.ContentBlock{types.NewTextBlock("test")})
	if !uuidV4Regex.MatchString(msg.ID) {
		t.Errorf("ID = %q, want a valid UUID v4", msg.ID)
	}
	if msg.Role != types.RoleSystem {
		t.Errorf("Role = %q, want %q", msg.Role, types.RoleSystem)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("len(Content) = %d, want 1", len(msg.Content))
	}
	if msg.Timestamp.IsZero() {
		t.Error("Timestamp is zero, want non-zero")
	}
}
