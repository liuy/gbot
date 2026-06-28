package computer

import (
	"encoding/json"
	"testing"
)

func TestParseInput_RequiresAction(t *testing.T) {
	t.Parallel()
	_, err := parseInput(json.RawMessage(`{"host":"1.2.3.4"}`))
	if err == nil {
		t.Fatal("parseInput returned nil error for missing action, want error")
	}
}

func TestParseInput_DecodesConnectFields(t *testing.T) {
	t.Parallel()
	in, err := parseInput(json.RawMessage(`{"action":"connect","host":"1.2.3.4","port":9000,"password":"pw"}`))
	if err != nil {
		t.Fatalf("parseInput: %v", err)
	}
	if in.Action != "connect" {
		t.Errorf("Action = %q, want connect", in.Action)
	}
	if in.Host != "1.2.3.4" {
		t.Errorf("Host = %q, want 1.2.3.4", in.Host)
	}
	if in.Port == nil {
		t.Fatal("Port = nil, want non-nil")
	}
	if *in.Port != 9000 {
		t.Errorf("Port = %d, want 9000", *in.Port)
	}
	if in.Password != "pw" {
		t.Errorf("Password = %q, want pw", in.Password)
	}
}

func TestParseInput_PortAbsent(t *testing.T) {
	t.Parallel()
	in, err := parseInput(json.RawMessage(`{"action":"connect","host":"1.2.3.4"}`))
	if err != nil {
		t.Fatalf("parseInput: %v", err)
	}
	if in.Port != nil {
		t.Errorf("Port = %v, want nil when absent (dispatch defaults to 8765)", in.Port)
	}
}

func TestParseInput_DecodesAllFields(t *testing.T) {
	t.Parallel()
	depth := 20
	ref := 3
	scale := 1.5
	in, err := parseInput(json.RawMessage(`{
		"action":"click_element",
		"max_depth":20,
		"ref":3,
		"coordinate":[100,200],
		"direction":"up",
		"text":"hi",
		"key":"back",
		"scale":1.5
	}`))
	if err != nil {
		t.Fatalf("parseInput: %v", err)
	}
	if in.MaxDepth == nil || *in.MaxDepth != depth {
		t.Errorf("MaxDepth = %v, want %d", in.MaxDepth, depth)
	}
	if in.Ref == nil || *in.Ref != ref {
		t.Errorf("Ref = %v, want %d", in.Ref, ref)
	}
	if len(in.Coordinate) == 0 {
		t.Fatal("Coordinate = empty")
	}
	if in.Direction != "up" {
		t.Errorf("Direction = %q, want up", in.Direction)
	}
	if in.Text != "hi" {
		t.Errorf("Text = %q, want hi", in.Text)
	}
	if in.Key != "back" {
		t.Errorf("Key = %q, want back", in.Key)
	}
	if in.Scale == nil || *in.Scale != scale {
		t.Errorf("Scale = %v, want %g", in.Scale, scale)
	}
}

func TestParseInput_InvalidJSON(t *testing.T) {
	t.Parallel()
	_, err := parseInput(json.RawMessage(`{bad json`))
	if err == nil {
		t.Fatal("parseInput returned nil for invalid JSON, want error")
	}
}

func TestParseCoordinate_HappyPath(t *testing.T) {
	t.Parallel()
	x, y, ok := parseCoordinate(json.RawMessage(`[100,200]`))
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if x != 100 {
		t.Errorf("x = %d, want 100", x)
	}
	if y != 200 {
		t.Errorf("y = %d, want 200", y)
	}
}

func TestParseCoordinate_ZeroCoords(t *testing.T) {
	t.Parallel()
	// [0,0] is a valid coordinate (top-left), not absent.
	x, y, ok := parseCoordinate(json.RawMessage(`[0,0]`))
	if !ok {
		t.Fatal("ok = false for [0,0], want true")
	}
	if x != 0 || y != 0 {
		t.Errorf("x=%d y=%d, want 0/0", x, y)
	}
}

func TestParseCoordinate_Absent(t *testing.T) {
	t.Parallel()
	_, _, ok := parseCoordinate(nil)
	if ok {
		t.Error("ok = true for nil, want false")
	}
}

func TestParseCoordinate_WrongLength(t *testing.T) {
	t.Parallel()
	_, _, ok := parseCoordinate(json.RawMessage(`[100]`))
	if ok {
		t.Error("ok = true for single-element array, want false")
	}
	_, _, ok = parseCoordinate(json.RawMessage(`[1,2,3]`))
	if ok {
		t.Error("ok = true for 3-element array, want false")
	}
}

func TestParseCoordinate_Malformed(t *testing.T) {
	t.Parallel()
	_, _, ok := parseCoordinate(json.RawMessage(`"notanarray"`))
	if ok {
		t.Error("ok = true for string, want false")
	}
}

func TestParseCoordinate_NegativeCoords(t *testing.T) {
	t.Parallel()
	x, y, ok := parseCoordinate(json.RawMessage(`[-5,-10]`))
	if !ok {
		t.Fatal("ok = false for negative coords, want true")
	}
	if x != -5 || y != -10 {
		t.Errorf("x=%d y=%d, want -5/-10", x, y)
	}
}

func TestCoerceMaxDepth_BoundaryTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want int
	}{
		{-5, 15},
		{0, 15},
		{1, 1},
		{15, 15},
		{50, 50},
		{1000, 1000},
	}
	for _, c := range cases {
		if got := coerceMaxDepth(c.in); got != c.want {
			t.Errorf("coerceMaxDepth(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
