package types

import (
	"bytes"
	"encoding/json"
)

// storageContentBlock is a named-type alias of ContentBlock that intentionally
// has NO methods. Because encoding/json invokes MarshalJSON only on the named
// type that defines it, json.Marshal([]storageContentBlock{...}) falls back to
// default struct marshaling — which includes the duration fields via their real
// JSON tags. This is the standard Go idiom for "marshal the same type two ways"
// without duplicating the field list.
type storageContentBlock ContentBlock

// isBlankJSON reports whether raw is empty or whitespace-only. json.Marshal
// cannot compact such a RawMessage and fails the whole call with "unexpected
// end of JSON input" — the shape a stream cut-off leaves behind when tool
// args never finished accumulating.
func isBlankJSON(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0
}

// needsStorageNull reports whether a tool payload must be stored as null:
// blank bytes, or non-blank but invalid JSON — the latter is what a stream
// cut-off leaves when args accumulated partially (2026-09-22 live repro:
// `{"args":"trunc`). Normalizing at the block level keeps the tool_use block
// structurally intact for replay/UI instead of degrading the whole message
// via the EngineMessagesToStore fallback. json.Valid is the same validation
// json.Marshal performs anyway, just moved where we can branch.
func needsStorageNull(raw json.RawMessage) bool {
	return isBlankJSON(raw) || !json.Valid(raw)
}

// isNullJSON reports whether raw is the literal JSON null. Storage writes
// blank tool payloads as null (see MarshalContentBlocksForStorage), so replay
// feeds literal nulls back into the wire path — where providers require
// tool_use.input to be an object, never null.
func isNullJSON(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// MarshalContentBlocksForStorage serializes []ContentBlock for persistence
// (SQLite Content TEXT column), PRESERVING ThinkingDurationNs and
// ToolDurationNs. It is the storage-side counterpart of ContentBlock.MarshalJSON,
// which drops those fields for the LLM wire body.
//
// Returns literal "[]" for an empty/nil slice so callers writing the result
// into a JSON-shaped DB column never see the Go-default "null".
func MarshalContentBlocksForStorage(blocks []ContentBlock) ([]byte, error) {
	if len(blocks) == 0 {
		return []byte("[]"), nil
	}
	s := make([]storageContentBlock, len(blocks))
	for i, b := range blocks {
		// A blank tool payload would abort the entire json.Marshal call, so
		// one poisoned message killed the whole persist batch and wedged the
		// cursor (2026-09-22 incident). Normalize to explicit null — the
		// message, and everything after it, still reaches the DB. Gated by
		// block type so unrelated blocks never grow an input/content key.
		switch b.Type {
		case ContentTypeToolUse:
			if needsStorageNull(b.Input) {
				b.Input = json.RawMessage("null")
			}
		case ContentTypeToolResult:
			if needsStorageNull(b.Content) {
				b.Content = json.RawMessage("null")
			}
		}
		s[i] = storageContentBlock(b)
	}
	return json.Marshal(s)
}
