package types

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMarshalContentBlocksForStorage_BlankRawMessageAsNull reproduces the
// 2026-09-22 incident class: a stream cut-off finalized a tool_use whose Input
// RawMessage never got valid JSON, json.Marshal aborted with "unexpected end
// of JSON input", and the whole persist batch died — wedging lastPersistedIdx
// and silently dropping the rest of the session. Blank (empty or
// whitespace-only) tool payloads must serialize as explicit null instead.
// TestMarshalContentBlocksForStorage_BlankRawMessageAsNull guards the
// 2026-09-22 incident shape where a stream cut-off finalized a tool_use with
// blank input: storage must record null instead of failing the batch.
func TestMarshalContentBlocksForStorage_BlankRawMessageAsNull(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		block ContentBlock
		want  string
	}{
		{"whitespace tool_use input", NewToolUseBlock("tu1", "Skill", json.RawMessage("  ")), `"input":null`},
		{"empty tool_use input", NewToolUseBlock("tu2", "Skill", nil), `"input":null`},
		{"whitespace tool_result content", NewToolResultBlock("tu1", json.RawMessage(" \t\n "), false), `"content":null`},
		{"empty tool_result content", NewToolResultBlock("tu2", nil, true), `"content":null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := MarshalContentBlocksForStorage([]ContentBlock{tc.block})
			if err != nil {
				t.Fatalf("MarshalContentBlocksForStorage: %v", err)
			}
			if !strings.Contains(string(b), tc.want) {
				t.Errorf("storage JSON = %s, want substring %s", b, tc.want)
			}
		})
	}
}

// TestMarshalContentBlocksForStorage_InvalidJSONStoredAsNull covers the
// second incident shape (2026-09-22 live repro): the argument stream was cut
// mid-JSON, so Input held truncated NON-blank bytes. That must normalize to
// null at the block level too — so the tool_use block (and its thinking/text
// siblings) survive structurally and history still renders a Skill call,
// instead of the whole message degrading via the storage fallback.
func TestMarshalContentBlocksForStorage_InvalidJSONStoredAsNull(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		block ContentBlock
		want  string
	}{
		{"truncated tool_use input", NewToolUseBlock("tu1", "Skill", json.RawMessage(`{"args":"实现 gbot`)), `"input":null`},
		{"garbage tool_use input", NewToolUseBlock("tu2", "Bash", json.RawMessage(`}{not json`)), `"input":null`},
		{"truncated tool_result content", NewToolResultBlock("tu1", json.RawMessage(`[{"type":"te`), false), `"content":null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := MarshalContentBlocksForStorage([]ContentBlock{tc.block})
			if err != nil {
				t.Fatalf("MarshalContentBlocksForStorage: %v", err)
			}
			if !strings.Contains(string(b), tc.want) {
				t.Errorf("storage JSON = %s, want substring %s", b, tc.want)
			}
		})
	}
}

// TestMarshalContentBlocksForStorage_InvalidJSONKeepsBlockStructure pins the
// reason this normalization lives at layer 1 rather than the fallback: the
// surrounding blocks stay intact and the tool_use keeps its identity.
func TestMarshalContentBlocksForStorage_InvalidJSONKeepsBlockStructure(t *testing.T) {
	t.Parallel()

	blocks := []ContentBlock{
		{Type: ContentTypeThinking, Thinking: "mid-strategy"},
		{Type: ContentTypeText, Text: "invoking"},
		NewToolUseBlock("call_poison", "Skill", json.RawMessage(`{"args":"cut`)),
	}
	b, err := MarshalContentBlocksForStorage(blocks)
	if err != nil {
		t.Fatalf("MarshalContentBlocksForStorage: %v", err)
	}
	got := string(b)
	for _, want := range []string{`"type":"thinking"`, `"type":"text"`, `"type":"tool_use"`, `"name":"Skill"`, `"id":"call_poison"`, `"input":null`} {
		if !strings.Contains(got, want) {
			t.Errorf("structure lost: %s missing from %s", want, got)
		}
	}
}

// TestContentBlockMarshalJSON_InvalidToolUseInputAsEmptyObject keeps replay
// legal: providers reject a tool_use whose input is not a JSON object, so
// truncated bytes map to {} on the wire just like blank/null do.
func TestContentBlockMarshalJSON_InvalidToolUseInputAsEmptyObject(t *testing.T) {
	t.Parallel()

	b := NewToolUseBlock("tu1", "Skill", json.RawMessage(`{"args":"trunc`))
	wire, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("json.Marshal wire form: %v", err)
	}
	if !strings.Contains(string(wire), `"input":{}`) {
		t.Errorf("wire form = %s, want input:{} for truncated bytes", wire)
	}
}

// TestMarshalContentBlocksForStorage_ValidPayloadUntouched guards against
// over-normalization: only blank payloads become null; valid JSON — including
// a literal null payload and a non-tool block carrying no Input — passes
// through exactly as before.
func TestMarshalContentBlocksForStorage_ValidPayloadUntouched(t *testing.T) {
	t.Parallel()

	blocks := []ContentBlock{
		NewToolUseBlock("tu1", "Skill", json.RawMessage(`{"command":"x"}`)),
		NewToolUseBlock("tu2", "Skill", json.RawMessage(`null`)),
		NewToolResultBlock("tu1", json.RawMessage(`"ok"`), false),
		NewTextBlock("hi"),
	}
	b, err := MarshalContentBlocksForStorage(blocks)
	if err != nil {
		t.Fatalf("MarshalContentBlocksForStorage: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, `"input":{"command":"x"}`) {
		t.Errorf("valid input rewritten: %s", got)
	}
	if !strings.Contains(got, `"input":null`) || strings.Count(got, `"input":null`) != 1 {
		t.Errorf("literal null input not preserved verbatim: %s", got)
	}
	if !strings.Contains(got, `"content":"ok"`) {
		t.Errorf("valid tool_result content rewritten: %s", got)
	}
	if strings.Contains(got, `"text":"hi","input"`) {
		t.Errorf("text block gained an input key: %s", got)
	}
}

// TestContentBlockMarshalJSON_BlankToolUseInputAsEmptyObject pins the wire
// self-protection: tool_use.input must reach the provider as a JSON object.
// The main replay path has no orphan/malformed filter (FilterIncompleteToolCalls
// only guards the fork path), so a blank Input — finalized empty by a stream
// cut-off — and a literal null Input (what storage normalization writes and
// replay reads back) must both marshal as {} rather than being omitted or
// failing the request.
func TestContentBlockMarshalJSON_BlankToolUseInputAsEmptyObject(t *testing.T) {
	t.Parallel()

	inputs := []json.RawMessage{
		nil,
		json.RawMessage("  "),
		json.RawMessage("null"),
	}
	for i, in := range inputs {
		b, err := json.Marshal(NewToolUseBlock("tu1", "Skill", in))
		if err != nil {
			t.Fatalf("input[%d] wire marshal: %v", i, err)
		}
		if !strings.Contains(string(b), `"input":{}`) {
			t.Errorf("input[%d] wire form = %s, want \"input\":{}", i, b)
		}
	}

	// A real payload passes through byte-identically.
	b, err := json.Marshal(NewToolUseBlock("tu2", "Skill", json.RawMessage(`{"command":"x"}`)))
	if err != nil {
		t.Fatalf("valid input wire marshal: %v", err)
	}
	if !strings.Contains(string(b), `"input":{"command":"x"}`) {
		t.Errorf("valid input wire form = %s, input rewritten", b)
	}
}
