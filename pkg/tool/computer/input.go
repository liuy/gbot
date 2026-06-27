package computer

import (
	"encoding/json"
	"fmt"

	"github.com/liuy/gbot/pkg/tool"
)

// Input is the parsed tool input. Go fields mirror the JSON schema 1:1
// (schema.py:29-211). Coordinate arrays use json.RawMessage so callers can
// distinguish absent from `[0,0]`; optional element indices use *int so
// absent is distinguishable from the valid value 0.
type Input struct {
	Action         string          `json:"action"`
	Mode           string          `json:"mode,omitempty"`
	App            string          `json:"app,omitempty"`
	MaxElements    json.RawMessage `json:"max_elements,omitempty"` // raw — coerceMaxElements parses nil/0/-1/"abc"
	Element        *int            `json:"element,omitempty"`
	Coordinate     json.RawMessage `json:"coordinate,omitempty"`
	Button         string          `json:"button,omitempty"`
	Modifiers      []string        `json:"modifiers,omitempty"`
	FromElement    *int            `json:"from_element,omitempty"`
	ToElement      *int            `json:"to_element,omitempty"`
	FromCoordinate json.RawMessage `json:"from_coordinate,omitempty"`
	ToCoordinate   json.RawMessage `json:"to_coordinate,omitempty"`
	Direction      string          `json:"direction,omitempty"`
	Amount         *int            `json:"amount,omitempty"`
	Value          string          `json:"value,omitempty"`
	Text           string          `json:"text,omitempty"`
	Keys           string          `json:"keys,omitempty"`
	Seconds        *float64        `json:"seconds,omitempty"`
	RaiseWindow    bool            `json:"raise_window,omitempty"`
	CaptureAfter   bool            `json:"capture_after,omitempty"`
}

// parseInput unmarshals the raw tool input and validates it against the
// schema's required-field list (currently just `action`).
// Translate of handle_computer_use's first lines (tool.py:228-235):
// action missing => error.
func parseInput(raw json.RawMessage) (Input, error) {
	if err := tool.ValidateInput(inputSchema(), raw); err != nil {
		return Input{}, fmt.Errorf("computer: %w", err)
	}
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return Input{}, fmt.Errorf("computer: parse input: %w", err)
	}
	return in, nil
}

// defaultMaxElements is the AX elements cap when none/invalid is supplied.
// Translate of tool.py:462 _DEFAULT_MAX_ELEMENTS.
const defaultMaxElements = 100

// maxAllowedMaxElements is the hard ceiling on caller-supplied max_elements.
// Translate of tool.py:466 _MAX_ALLOWED_MAX_ELEMENTS.
const maxAllowedMaxElements = 1000

// coerceMaxElements validates the caller-supplied max_elements value.
// Falls back to defaultMaxElements for missing / non-integer / sub-1 inputs
// so the cap can never be silently disabled by a malformed argument. Clamps
// oversized values to maxAllowedMaxElements so a caller cannot bypass the
// safeguard by passing a very large integer.
//
// Translate of tool.py:484-505 `_coerce_max_elements`. The Go version takes
// `any` to mirror the Python `Any` parameter (the value arrives from a
// json.RawMessage field, so its concrete type may be nil, float64, string, …).
func coerceMaxElements(v any) int {
	if v == nil {
		return defaultMaxElements
	}
	switch n := v.(type) {
	case float64:
		return coerceMaxElementsInt(int(n))
	case int:
		return coerceMaxElementsInt(n)
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return defaultMaxElements
		}
		return coerceMaxElementsInt(int(i))
	default:
		// Non-numeric JSON value (string, bool, array, object) — fall back
		// rather than fail, mirroring Python's `int(value)` exception path.
		return defaultMaxElements
	}
}

// coerceMaxElementsInt applies the <1 → default, >max → max, else passthrough
// rule shared by all coercion entry points.
func coerceMaxElementsInt(n int) int {
	if n < 1 {
		return defaultMaxElements
	}
	if n > maxAllowedMaxElements {
		return maxAllowedMaxElements
	}
	return n
}

// parseCoordinate extracts [x, y] from a raw JSON coordinate array. Returns
// ok=false when the input is absent or malformed. Used by click/drag/scroll
// dispatch where the schema allows but does not require coordinates.
func parseCoordinate(raw json.RawMessage) (x, y int, ok bool) {
	if len(raw) == 0 {
		return 0, 0, false
	}
	var pair []int
	if err := json.Unmarshal(raw, &pair); err != nil {
		return 0, 0, false
	}
	if len(pair) != 2 {
		return 0, 0, false
	}
	return pair[0], pair[1], true
}
