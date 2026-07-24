package types

import "encoding/json"

// storageContentBlock is a named-type alias of ContentBlock that intentionally
// has NO methods. Because encoding/json invokes MarshalJSON only on the named
// type that defines it, json.Marshal([]storageContentBlock{...}) falls back to
// default struct marshaling — which includes the duration fields via their real
// JSON tags. This is the standard Go idiom for "marshal the same type two ways"
// without duplicating the field list.
type storageContentBlock ContentBlock

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
		s[i] = storageContentBlock(b)
	}
	return json.Marshal(s)
}
