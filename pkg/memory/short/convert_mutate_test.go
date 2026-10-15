package short

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// marshalPoisonReason is how the storage marshaler reports the invalid
// RawMessage below; pinned so the degradation text is asserted exactly.
const marshalPoisonReason = "json: error calling MarshalJSON for type *jsontext.Value: unexpected end of JSON input"

// degradedDumpText unwraps a degraded store row back to its single text block.
func degradedDumpText(t *testing.T, content string) string {
	t.Helper()
	var blocks []types.ContentBlock
	if err := json.Unmarshal([]byte(content), &blocks); err != nil {
		t.Fatalf("degraded content is not valid block JSON: %v (%s)", err, content)
	}
	if len(blocks) != 1 || blocks[0].Type != types.ContentTypeText {
		t.Fatalf("degraded content = %+v, want one text block", blocks)
	}
	return blocks[0].Text
}

// textBlockPoison is a text block whose Content RawMessage is invalid JSON:
// the type-gated normalization in MarshalContentBlocksForStorage only repairs
// tool payloads, so this survives to make the whole marshal fail while
// toolCallIDs stays empty (no pair degradation).
func textBlockPoison() types.ContentBlock {
	return types.ContentBlock{
		Type:    types.ContentTypeText,
		Text:    "half a thought",
		Content: json.RawMessage(`[{"type":"te`),
	}
}

// convert.go:38,139 — a message with no tool-call ids degrades through the
// marshal-error branch alone, and the dump lists every original block type.
func TestEngineMessagesToStore_TextOnlyPoisonDegrades(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{{
		ID:      "a-text-poison",
		Role:    types.RoleAssistant,
		Content: []types.ContentBlock{textBlockPoison()},
	}}

	result, err := EngineMessagesToStore(msgs)
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d store messages, want 1", len(result))
	}
	want := "[storage fallback] marshal content blocks for storage: " + marshalPoisonReason + " [block type=text]"
	if got := degradedDumpText(t, result[0].Content); got != want {
		t.Errorf("degraded text = %q, want %q", got, want)
	}
}

// convert.go:60 — the pair scan checks every tool-call id of a message, not
// just the first one.
func TestEngineMessagesToStore_PairDegradeScansAllCallIDs(t *testing.T) {
	t.Parallel()

	poisoned := types.NewToolUseBlock("tu-bad", "Skill", json.RawMessage(`{"command":"go"}`))
	poisoned.Content = json.RawMessage(`[{"type":"te`)

	msgs := []types.Message{
		{ID: "a-poison", Role: types.RoleAssistant, Content: []types.ContentBlock{poisoned}},
		{ID: "u-pair", Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("tu-unrelated", json.RawMessage(`"ok"`), false),
			types.NewToolResultBlock("tu-bad", json.RawMessage(`"failed"`), true),
		}},
	}

	result, err := EngineMessagesToStore(msgs)
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	text := degradedDumpText(t, result[1].Content)
	if !strings.HasPrefix(text, "[storage fallback] ") {
		t.Errorf("paired message not degraded: %q", text)
	}
	if !strings.Contains(text, `paired tool call "tu-bad" failed to serialize`) {
		t.Errorf("degradation reason missing the second call id: %q", text)
	}
}

// convert.go:69 — when several of a message's call ids are already poisoned,
// the first one in block order names the degradation reason.
func TestEngineMessagesToStore_PairDegradeReasonIsFirstMatch(t *testing.T) {
	t.Parallel()

	poisonA := types.NewToolUseBlock("tu-bad1", "Skill", json.RawMessage(`{"command":"a"}`))
	poisonA.Content = json.RawMessage(`[{"type":"te`)
	poisonB := types.NewToolUseBlock("tu-bad2", "Skill", json.RawMessage(`{"command":"b"}`))
	poisonB.Content = json.RawMessage(`{"a":}`)

	msgs := []types.Message{
		{ID: "a-poison1", Role: types.RoleAssistant, Content: []types.ContentBlock{poisonA}},
		{ID: "a-poison2", Role: types.RoleAssistant, Content: []types.ContentBlock{poisonB}},
		{ID: "u-pair", Role: types.RoleUser, Content: []types.ContentBlock{
			types.NewToolResultBlock("tu-bad1", json.RawMessage(`"failed"`), true),
			types.NewToolResultBlock("tu-bad2", json.RawMessage(`"failed"`), true),
		}},
	}

	result, err := EngineMessagesToStore(msgs)
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	text := degradedDumpText(t, result[2].Content)
	if !strings.Contains(text, `paired tool call "tu-bad1" failed to serialize`) {
		t.Errorf("reason does not name the first poisoned call: %q", text)
	}
	if strings.Contains(text, `paired tool call "tu-bad2" failed to serialize`) {
		t.Errorf("reason was overwritten by a later call id: %q", text)
	}
}

// convert.go:85 — every degraded message is logged with its identity.
func TestEngineMessagesToStore_LogsDegradation(t *testing.T) {
	logs := captureSlogDefault(t)

	msgs := []types.Message{{
		ID:      "a-logged",
		Role:    types.RoleAssistant,
		Content: []types.ContentBlock{textBlockPoison()},
	}}
	result, err := EngineMessagesToStore(msgs)
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}

	want := fmt.Sprintf("msg=\"EngineMessagesToStore: degraded unserializable message to text dump\" message_uuid=%s role=assistant reason=\"marshal content blocks for storage: %s\"", result[0].UUID, marshalPoisonReason)
	if !strings.Contains(logs.String(), want) {
		t.Errorf("log output = %q, want a record containing %q", logs.String(), want)
	}
}

// convert.go:101 — the store row carries the engine message timestamp.
func TestEngineMessagesToStore_PreservesTimestamp(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	msgs := []types.Message{{
		ID:        "u-ts",
		Role:      types.RoleUser,
		Content:   []types.ContentBlock{types.NewTextBlock("hi")},
		Timestamp: ts,
	}}

	result, err := EngineMessagesToStore(msgs)
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	if !result[0].CreatedAt.Equal(ts) {
		t.Errorf("CreatedAt = %v, want %v", result[0].CreatedAt, ts)
	}
}

// convert.go:155-160 — the non-JSON fallback keeps id, role, timestamp and
// metadata, and ships the raw content as one text block.
func TestStoreMessageToEngine_NonJSONFallbackFields(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	sm := &TranscriptMessage{
		UUID:      "uuid-fallback",
		Type:      "assistant",
		Content:   "raw transcript line",
		Metadata:  `{"model":"opus-4"}`,
		CreatedAt: ts,
	}

	msg := StoreMessageToEngine(sm)
	if msg.ID != "uuid-fallback" {
		t.Errorf("ID = %q, want uuid-fallback", msg.ID)
	}
	if msg.Role != types.RoleAssistant {
		t.Errorf("Role = %q, want assistant", msg.Role)
	}
	if !msg.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", msg.Timestamp, ts)
	}
	if len(msg.Content) != 1 || msg.Content[0].Text != "raw transcript line" {
		t.Errorf("Content = %+v, want one text block with the raw content", msg.Content)
	}
	if msg.Model != "opus-4" {
		t.Errorf("Model = %q, want opus-4 (metadata must survive the fallback)", msg.Model)
	}
}

// convert.go:168 — the JSON path carries the timestamp too.
func TestStoreMessageToEngine_JSONTimestamp(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC)
	sm := &TranscriptMessage{
		UUID:      "uuid-json",
		Type:      "user",
		Content:   `[{"type":"text","text":"hi"}]`,
		CreatedAt: ts,
	}

	msg := StoreMessageToEngine(sm)
	if !msg.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", msg.Timestamp, ts)
	}
	if len(msg.Content) != 1 || msg.Content[0].Text != "hi" {
		t.Errorf("Content = %+v, want one text block", msg.Content)
	}
}
