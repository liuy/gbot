package computer

import (
	"encoding/json"
	"fmt"

	"github.com/liuy/gbot/pkg/tool"
)

// Input is the parsed tool input. Coordinate uses json.RawMessage so callers
// can distinguish absent from [0,0]; scalar pointer fields use the same
// absent-vs-zero distinction.
type Input struct {
	Action     string          `json:"action"`
	MaxDepth   *int            `json:"max_depth,omitempty"`  // [screen]
	Ref        *int            `json:"ref,omitempty"`        // [click_element, open_menu_element]
	Coordinate json.RawMessage `json:"coordinate,omitempty"` // [click, open_menu, zoom] [x,y]
	Direction  string          `json:"direction,omitempty"`  // [scroll] up|down|left|right
	Text       string          `json:"text,omitempty"`       // [type]
	Mode       string          `json:"mode,omitempty"`       // [type] "replace" (default) or "append"
	Key        string          `json:"key,omitempty"`        // [send_key]
	Scale      *float64        `json:"scale,omitempty"`      // [zoom]
	Package    string          `json:"package,omitempty"`    // [open_app]
	Path       string          `json:"path,omitempty"`       // [send_file]
}

// parseInput unmarshals the raw tool input and validates it against the
// schema's required-field list (currently just `action`).
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

// parseCoordinate extracts [x, y] from a raw JSON coordinate array. Returns
// ok=false when the input is absent or malformed. Used by click/open_menu/zoom
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

// coerceMaxDepth returns maxDepth as-is when >0, else 15. There is no upper
// ceiling — the GBot app applies none, and adding one would diverge from the
// device (a caller asking for depth 100 gets depth 100, matching Kotlin).
//
// This intentionally diverges from the GBot app's Kotlin `params?.get("maxDepth")
// ?.asInt ?: 15`, which only fires when the key is absent (a caller-supplied 0
// or negative passes straight through and collapses the tree to root-only via
// buildUITree's `depth < maxDepth` guard). A model emitting max_depth:0 means
// "use the default", not "give me one node", so ≤0 normalizes to 15.
func coerceMaxDepth(maxDepth int) int {
	if maxDepth <= 0 {
		return 15
	}
	return maxDepth
}
