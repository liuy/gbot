package short

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

func TestEngineMessagesToStore(t *testing.T) {
	t.Parallel()

	t.Run("empty input returns nil", func(t *testing.T) {
		result, err := EngineMessagesToStore(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != nil {
			t.Errorf("expected nil, got %d items", len(result))
		}
	})

	t.Run("converts messages with UUID", func(t *testing.T) {
		msgs := []types.Message{
			{
				ID:        "uuid-1",
				Role:      types.RoleUser,
				Content:   []types.ContentBlock{types.NewTextBlock("hello")},
				Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			},
		}
		result, err := EngineMessagesToStore(msgs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 message, got %d", len(result))
		}
		if result[0].UUID != "uuid-1" {
			t.Errorf("UUID = %q, want uuid-1", result[0].UUID)
		}
		if result[0].Type != "user" {
			t.Errorf("Type = %q, want user", result[0].Type)
		}
		if !strings.Contains(result[0].Content, "hello") {
			t.Errorf("Content = %q, should contain hello", result[0].Content)
		}
	})

	t.Run("generates UUID when empty", func(t *testing.T) {
		msgs := []types.Message{
			{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		}
		result, err := EngineMessagesToStore(msgs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result[0].UUID == "" {
			t.Error("UUID should be auto-generated when empty")
		}
	})
}

func TestStoreMessageToEngine_ImageSourceRoundTrip(t *testing.T) {
	t.Parallel()
	// Regression: image content block lost its Source field through
	// store round-trip because the persisted block had no Source field.
	// Manifestation: API error "source is required when type=image".
	srcBlock := types.NewImageBlock(types.ImageSource{
		Type:      "base64",
		MediaType: "image/png",
		Data:      "iVBORw0KGgo=",
	})
	engineMsg := types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{srcBlock},
	}

	storeMsgs, err := EngineMessagesToStore([]types.Message{engineMsg})
	if err != nil {
		t.Fatalf("EngineMessagesToStore error: %v", err)
	}
	if len(storeMsgs) != 1 {
		t.Fatalf("expected 1 store message, got %d", len(storeMsgs))
	}

	back := StoreMessageToEngine(storeMsgs[0])
	if len(back.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(back.Content))
	}
	got := back.Content[0]
	if got.Type != types.ContentTypeImage {
		t.Fatalf("Type = %q, want image", got.Type)
	}
	if got.Source == nil {
		t.Fatal("Source = nil, want non-nil (source is required when type=image)")
	}
	if got.Source.Type != "base64" {
		t.Errorf("Source.Type = %q, want base64", got.Source.Type)
	}
	if got.Source.MediaType != "image/png" {
		t.Errorf("Source.MediaType = %q, want image/png", got.Source.MediaType)
	}
	if got.Source.Data != "iVBORw0KGgo=" {
		t.Errorf("Source.Data = %q, want iVBORw0KGgo=", got.Source.Data)
	}
}

func TestStoreMessageToEngine(t *testing.T) {
	t.Parallel()

	t.Run("nil input returns empty", func(t *testing.T) {
		msg := StoreMessageToEngine(nil)
		if msg.Role != "" {
			t.Errorf("expected empty role, got %q", msg.Role)
		}
	})

	t.Run("valid JSON content", func(t *testing.T) {
		blocks := []types.ContentBlock{
			{Type: "text", Text: "response"},
		}
		content, _ := json.Marshal(blocks)
		sm := &TranscriptMessage{
			UUID:    "uuid-2",
			Type:    "assistant",
			Content: string(content),
		}
		msg := StoreMessageToEngine(sm)
		if msg.ID != "uuid-2" {
			t.Errorf("ID = %q, want uuid-2", msg.ID)
		}
		if msg.Role != types.RoleAssistant {
			t.Errorf("Role = %q, want assistant", msg.Role)
		}
		if len(msg.Content) != 1 || msg.Content[0].Text != "response" {
			t.Errorf("Content[0].Text = %q, want response", msg.Content[0].Text)
		}
	})

	t.Run("invalid JSON falls back to text block", func(t *testing.T) {
		sm := &TranscriptMessage{
			UUID:    "uuid-3",
			Type:    "user",
			Content: "plain text, not JSON",
		}
		msg := StoreMessageToEngine(sm)
		if len(msg.Content) != 1 {
			t.Fatalf("expected 1 content block, got %d", len(msg.Content))
		}
		if msg.Content[0].Type != types.ContentTypeText {
			t.Errorf("Type = %q, want text", msg.Content[0].Type)
		}
		if msg.Content[0].Text != "plain text, not JSON" {
			t.Errorf("Text = %q, want fallback text", msg.Content[0].Text)
		}
	})
}

func TestStoreMessagesToEngine(t *testing.T) {
	t.Parallel()

	t.Run("empty input returns nil", func(t *testing.T) {
		result, err := StoreMessagesToEngine(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != nil {
			t.Errorf("expected nil, got %d items", len(result))
		}
	})

	t.Run("valid messages", func(t *testing.T) {
		blocks := []types.ContentBlock{{Type: "text", Text: "hi"}}
		content, _ := json.Marshal(blocks)
		storeMsgs := []*TranscriptMessage{
			{UUID: "u1", Type: "user", Content: string(content)},
			{UUID: "a1", Type: "assistant", Content: string(content)},
		}
		result, err := StoreMessagesToEngine(storeMsgs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(result))
		}
		if result[0].Role != types.RoleUser {
			t.Errorf("Role[0] = %q, want user", result[0].Role)
		}
		if result[1].Role != types.RoleAssistant {
			t.Errorf("Role[1] = %q, want assistant", result[1].Role)
		}
	})

	t.Run("unknown role returns error", func(t *testing.T) {
		storeMsgs := []*TranscriptMessage{
			{UUID: "x1", Type: "unknown_role", Content: "{}"},
		}
		_, err := StoreMessagesToEngine(storeMsgs)
		if err == nil {
			t.Fatal("expected error for unknown role")
		}
		if !strings.Contains(err.Error(), "unknown message role") {
			t.Errorf("error = %q, should mention unknown role", err.Error())
		}
	})

	t.Run("system role accepted", func(t *testing.T) {
		blocks := []types.ContentBlock{{Type: "text", Text: "system prompt"}}
		content, _ := json.Marshal(blocks)
		storeMsgs := []*TranscriptMessage{
			{UUID: "s1", Type: "system", Content: string(content)},
		}
		result, err := StoreMessagesToEngine(storeMsgs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result[0].Role != types.RoleSystem {
			t.Errorf("Role = %q, want system", result[0].Role)
		}
	})
}

// TestEngineMessagesToStore_DegradesUnserializableMessage reproduces the
// 2026-09-22 incident shape at the convert layer: one message whose blocks
// cannot be marshaled (here, a tool_use with truncated args JSON) previously
// failed the whole batch, wedging lastPersistedIdx and dropping every later
// message of the session. The batch must instead degrade the poisoned message
// — and the message holding the paired tool_result, so replayed history keeps
// tool_use/tool_result pairs intact — to text dumps naming the block types
// and the error, while every other message persists untouched.
func TestEngineMessagesToStore_DegradesUnserializableMessage(t *testing.T) {
	t.Parallel()

	// Layer-1 normalization absorbs malformed Input/Content payloads; this
	// poison survives it via cross-corruption — valid Input, invalid bytes in
	// the Content field of a tool_use block (the type-gated switch only
	// normalizes Input there).
	poisoned := types.NewToolUseBlock("tu-bad", "Skill", json.RawMessage(`{"command":"git commit"}`))
	poisoned.Content = json.RawMessage(`[{"type":"te`)

	msgs := []types.Message{
		{ID: "u-ok", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("q")}},
		{
			ID:   "a-poison",
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				types.NewTextBlock("calling skill"),
				poisoned,
			},
		},
		{
			ID:   "u-pair",
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewToolResultBlock("tu-bad", json.RawMessage(`"skill tool: invalid input"`), true),
			},
		},
		{ID: "a-ok", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}},
	}

	result, err := EngineMessagesToStore(msgs)
	if err != nil {
		t.Fatalf("EngineMessagesToStore must never fail the batch: %v", err)
	}
	if len(result) != 4 {
		t.Fatalf("expected 4 store messages, got %d", len(result))
	}

	wantUUIDs := []string{"u-ok", "a-poison", "u-pair", "a-ok"}
	for i, want := range wantUUIDs {
		if result[i].UUID != want {
			t.Errorf("result[%d].UUID = %q, want %q", i, result[i].UUID, want)
		}
	}

	if result[0].Content != `[{"type":"text","text":"q"}]` {
		t.Errorf("result[0].Content = %s, want untouched text message", result[0].Content)
	}
	if result[3].Content != `[{"type":"text","text":"done"}]` {
		t.Errorf("result[3].Content = %s, want untouched text message", result[3].Content)
	}

	// The poisoned assistant message degrades to a text dump naming the
	// original block types and the underlying marshal error.
	poison := result[1].Content
	if !strings.Contains(poison, "tool_use") || !strings.Contains(poison, "tu-bad") {
		t.Errorf("poisoned dump lost block type/id: %s", poison)
	}
	if !strings.Contains(poison, "unexpected end of JSON input") {
		t.Errorf("poisoned dump lost marshal error: %s", poison)
	}
	if strings.Contains(poison, `"command":"git commit`) {
		t.Errorf("poisoned dump leaked the unserializable raw JSON: %s", poison)
	}

	// The paired tool_result message degrades too, also naming the root error.
	pair := result[2].Content
	if !strings.Contains(pair, "tool_result") || !strings.Contains(pair, "tu-bad") {
		t.Errorf("paired dump lost block type/tool_use_id: %s", pair)
	}
	if !strings.Contains(pair, "unexpected end of JSON input") {
		t.Errorf("paired dump lost root marshal error: %s", pair)
	}

	// Degraded rows must themselves parse back as valid block JSON — replay
	// feeds them straight through StoreMessageToEngine.
	for i, tm := range result[1:3] {
		var blocks []types.ContentBlock
		if err := json.Unmarshal([]byte(tm.Content), &blocks); err != nil {
			t.Errorf("result[%d] degraded content is not valid JSON: %v", i+1, err)
		}
	}
}

// TestEngineMessagesToStore_PairDegradationPropagatesThroughBatchedToolResults
// covers the transitive half of pair degradation: gbot batches several
// tool_results into one user message, so degrading that message (because one
// of its results pairs with a poisoned tool_use) also orphans the healthy
// calls whose results shared the batch. Their assistant tool_use messages
// must degrade too, or replayed history ships tool_use blocks without
// results.
func TestEngineMessagesToStore_PairDegradationPropagatesThroughBatchedToolResults(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{
		{
			ID:   "a-poison",
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				// Cross-corrupted block: valid Input, invalid Content bytes —
				// the marshal-fatal shape that survives layer-1 normalization.
				func() types.ContentBlock {
					b := types.NewToolUseBlock("tu-bad", "Skill", json.RawMessage(`{"command":"oops"}`))
					b.Content = json.RawMessage(`[{"type":"te`)
					return b
				}(),
			},
		},
		{
			ID:   "a-healthy",
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				types.NewToolUseBlock("tu-good", "Bash", json.RawMessage(`{"command":"ls"}`)),
			},
		},
		{
			ID:   "u-batch",
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewToolResultBlock("tu-bad", json.RawMessage(`"skill tool: invalid input"`), true),
				types.NewToolResultBlock("tu-good", json.RawMessage(`"file.txt"`), false),
			},
		},
	}

	result, err := EngineMessagesToStore(msgs)
	if err != nil {
		t.Fatalf("EngineMessagesToStore must never fail the batch: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 store messages, got %d", len(result))
	}
	for i, id := range []string{"a-poison", "a-healthy", "u-batch"} {
		if result[i].UUID != id {
			t.Errorf("result[%d].UUID = %q, want %q", i, result[i].UUID, id)
		}
		if !strings.Contains(result[i].Content, "[storage fallback]") {
			t.Errorf("result[%d] (%s) should be degraded: %s", i, id, result[i].Content)
		}
	}
	// The healthy assistant call degrades because its result shared the
	// batched user message, and the reason must trace back to the root error.
	if !strings.Contains(result[1].Content, "tu-good") || !strings.Contains(result[1].Content, "unexpected end of JSON input") {
		t.Errorf("healthy call dump lost its id or root error: %s", result[1].Content)
	}
}

// TestEngineMessagesToStore_EmptyInputToolUsePersists is the end-to-end
// persistence semantics of the 2026-09-22 incident, at the PersistNewMessages
// boundary: an assistant message finalized with an empty tool_use Input (GLM
// cut the Skill args mid-stream) plus its error tool_result must convert
// successfully — as real blocks, not a text dump — so the session keeps
// landing in the DB and restart replay works.
func TestEngineMessagesToStore_EmptyInputToolUsePersists(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{
		{
			ID:   "a-glitch",
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				types.NewToolUseBlock("tu-glitch", "Skill", json.RawMessage(" ")),
			},
		},
		{
			ID:   "u-glitch",
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewToolResultBlock("tu-glitch", json.RawMessage(`"skill tool: invalid input"`), true),
			},
		},
	}

	result, err := EngineMessagesToStore(msgs)
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 store messages, got %d", len(result))
	}
	if !strings.Contains(result[0].Content, `"input":null`) {
		t.Errorf("result[0].Content = %s, want input serialized as null", result[0].Content)
	}
	if !strings.Contains(result[1].Content, `"tool_use_id":"tu-glitch"`) {
		t.Errorf("result[1].Content = %s, want paired tool_result", result[1].Content)
	}
	if strings.Contains(result[0].Content, "[storage fallback]") {
		t.Errorf("result[0] should persist as real blocks, not a dump: %s", result[0].Content)
	}

	// Replay: the stored rows convert back, and the blank-input tool_use
	// marshals onto the wire as a valid object.
	back := StoreMessageToEngine(result[0])
	if len(back.Content) != 1 || back.Content[0].Type != types.ContentTypeToolUse {
		t.Fatalf("round-trip lost the tool_use block: %+v", back.Content)
	}
	wire, err := json.Marshal(back.Content[0])
	if err != nil {
		t.Fatalf("replayed tool_use wire marshal: %v", err)
	}
	if !strings.Contains(string(wire), `"input":{}`) {
		t.Errorf("replayed tool_use wire form = %s, want \"input\":{}", wire)
	}
}

// TestEngineMessagesToStore_PreservesDuration verifies the persistence path
// (EngineMessagesToStore → DB string → StoreMessageToEngine) round-trips both
// ThinkingDurationNs and ToolDurationNs with their exact integer values.
// Guards against a regression where someone reverts Step 4 to plain json.Marshal,
// which would silently drop the duration fields via the custom MarshalJSON.
// TestEngineMessagesToStore_TruncatedInputToolUsePersists is the 2026-09-22
// live-repro shape: the argument stream was cut mid-JSON, so Input held
// non-blank truncated bytes ({"args":"cut). Layer-1 must absorb it at the
// block level — real tool_use block with input:null, no fallback dump — so
// history still renders the Skill call.
func TestEngineMessagesToStore_TruncatedInputToolUsePersists(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{
		{
			ID:   "a-cut",
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeThinking, Thinking: "plan"},
				{Type: types.ContentTypeText, Text: "invoking"},
				types.NewToolUseBlock("tu-cut", "Skill", json.RawMessage(`{"args":"实现 gbot`)),
			},
		},
		{
			ID:   "u-cut",
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewToolResultBlock("tu-cut", json.RawMessage(`"skill tool: invalid input: unexpected end of JSON input"`), true),
			},
		},
	}

	result, err := EngineMessagesToStore(msgs)
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 store messages, got %d", len(result))
	}
	got := result[0].Content
	for _, want := range []string{`"type":"thinking"`, `"type":"text"`, `"type":"tool_use"`, `"name":"Skill"`, `"input":null`} {
		if !strings.Contains(got, want) {
			t.Errorf("structure lost: %s missing from %s", want, got)
		}
	}
	if strings.Contains(got, "[storage fallback]") {
		t.Errorf("truncated input must not reach the fallback: %s", got)
	}
	if !strings.Contains(result[1].Content, `"tool_use_id":"tu-cut"`) {
		t.Errorf("result[1].Content = %s, want paired tool_result intact", result[1].Content)
	}
}

// TestEngineMessagesToStore_APIErrorMessageRoundTrip locks both halves of
// the API-error persistence contract: FlagAPIError round-trips through the
// metadata JSON (the structured signal replay styling consumes), and the
// "API Error" text prefix survives verbatim (the model-facing marker for
// the next turn, TS API_ERROR_MESSAGE_PREFIX). Losing either half breaks
// restart parity differently.
func TestEngineMessagesToStore_APIErrorMessageRoundTrip(t *testing.T) {
	t.Parallel()

	msgs := []types.Message{
		{ID: "u1", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		func() types.Message {
			m := types.NewAssistantMessage([]types.ContentBlock{
				types.NewTextBlock("API Error 429: rate limited"),
			})
			m.ID = "a1"
			m.Flags |= types.FlagAPIError
			return m
		}(),
	}

	result, err := EngineMessagesToStore(msgs)
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 store messages, got %d", len(result))
	}

	back := StoreMessageToEngine(result[1])
	if back.Role != types.RoleAssistant {
		t.Fatalf("restored role = %s, want assistant", back.Role)
	}
	if !back.HasFlag(types.FlagAPIError) {
		t.Error("FlagAPIError must round-trip via metadata so replay styling and filtering survive restarts")
	}
	if len(back.Content) != 1 || back.Content[0].Type != types.ContentTypeText {
		t.Fatalf("restored content shape wrong: %+v", back.Content)
	}
	if !strings.HasPrefix(back.Content[0].Text, "API Error") {
		t.Errorf("restored text lost the durable prefix: %q", back.Content[0].Text)
	}
	if !strings.Contains(back.Content[0].Text, "rate limited") {
		t.Errorf("restored text lost the failure detail: %q", back.Content[0].Text)
	}
}

func TestEngineMessagesToStore_PreservesDuration(t *testing.T) {
	t.Parallel()

	em := types.Message{
		ID:   "asst-dur",
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking, Thinking: "ponder", ThinkingDurationNs: 2_000_000_000},
			{Type: types.ContentTypeToolResult, ToolUseID: "tu1", Content: json.RawMessage(`"ok"`), ToolDurationNs: 4_000_000_000},
		},
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	storeMsgs, err := EngineMessagesToStore([]types.Message{em})
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	if len(storeMsgs) != 1 {
		t.Fatalf("expected 1 store message, got %d", len(storeMsgs))
	}
	if !strings.Contains(storeMsgs[0].Content, `"thinking_duration_ns":2000000000`) {
		t.Errorf("store Content missing thinking_duration_ns=2000000000: %s", storeMsgs[0].Content)
	}
	if !strings.Contains(storeMsgs[0].Content, `"tool_duration_ns":4000000000`) {
		t.Errorf("store Content missing tool_duration_ns=4000000000: %s", storeMsgs[0].Content)
	}

	back := StoreMessageToEngine(storeMsgs[0])
	if len(back.Content) != 2 {
		t.Fatalf("round-trip content len = %d, want 2", len(back.Content))
	}
	if back.Content[0].ThinkingDurationNs != 2_000_000_000 {
		t.Errorf("back.Content[0].ThinkingDurationNs = %d, want 2000000000", back.Content[0].ThinkingDurationNs)
	}
	if back.Content[1].ToolDurationNs != 4_000_000_000 {
		t.Errorf("back.Content[1].ToolDurationNs = %d, want 4000000000", back.Content[1].ToolDurationNs)
	}
}
