package computer

import (
	"encoding/json"
	"testing"
)

// TestCoerceMaxElements exercises every branch of coerceMaxElements
// (tool.py:484-505): nil/0/-1 → 100, 1 → 1, 1000 → 1000, 1001 → 1000,
// non-numeric → 100.
func TestCoerceMaxElements(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int
	}{
		{"nil", nil, 100},
		{"zero", float64(0), 100},
		{"negative", float64(-1), 100},
		{"one", float64(1), 1},
		{"hundred", float64(100), 100},
		{"thousand", float64(1000), 1000},
		{"over max", float64(1001), 1000},
		{"huge", float64(99999), 1000},
		{"string non-numeric", "abc", 100},
		{"empty string", "", 100},
		{"bool true", true, 100},
		{"array", []any{1, 2}, 100},
		{"object", map[string]any{"a": 1}, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := coerceMaxElements(tc.in)
			if got != tc.want {
				t.Errorf("coerceMaxElements(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestCoerceMaxElementsRawJSON exercises the json.RawMessage bridge path
// used by Input.MaxElements — the value arrives as a raw JSON token, decoded
// by rawAny into a generic value before coerceMaxElements sees it.
func TestCoerceMaxElementsRawJSON(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{`null`, 100},
		{`0`, 100},
		{`50`, 50},
		{`1000`, 1000},
		{`1001`, 1000},
		{`"abc"`, 100},
		{`""`, 100},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got := coerceMaxElements(rawAny(json.RawMessage(tc.raw)))
			if got != tc.want {
				t.Errorf("coerceMaxElements(rawAny(%s)) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

// TestParseCoordinate verifies the coordinate array parser handles 2-int
// arrays, rejects malformed input, and returns ok=false on absent.
func TestParseCoordinate(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		x, y int
		ok   bool
	}{
		{"two ints", `[100,200]`, 100, 200, true},
		{"absent", ``, 0, 0, false},
		{"three ints", `[1,2,3]`, 0, 0, false},
		{"one int", `[1]`, 0, 0, false},
		{"string elements", `["a","b"]`, 0, 0, false},
		{"not an array", `"abc"`, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x, y, ok := parseCoordinate(json.RawMessage(tc.raw))
			if ok != tc.ok {
				t.Errorf("parseCoordinate(%s) ok = %v, want %v", tc.raw, ok, tc.ok)
			}
			if ok && (x != tc.x || y != tc.y) {
				t.Errorf("parseCoordinate(%s) = (%d,%d), want (%d,%d)", tc.raw, x, y, tc.x, tc.y)
			}
		})
	}
}
