package config

import (
	"encoding/json"
	"testing"
)

func TestParseIntOrHuman(t *testing.T) {
	tests := []struct {
		input string
		want  int
		err   bool
	}{
		{"32k", 32 * 1024, false},
		{"32K", 32 * 1024, false},
		{"200k", 200 * 1024, false},
		{"1M", 1024 * 1024, false},
		{"1m", 1024 * 1024, false},
		{"2M", 2 * 1024 * 1024, false},
		{"32768", 32768, false},
		{"0", 0, false},
		{"", 0, true},
		{"abc", 0, true},
		{"12x", 0, true},
		{"k", 0, true},
		{"M", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseIntOrHuman(tt.input)
			if tt.err {
				if err == nil {
					t.Fatalf("expected error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("ParseIntOrHuman(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestIntOrHuman_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
		err   bool
	}{
		{"json number", `32768`, 32768, false},
		{"json string k", `"32k"`, 32 * 1024, false},
		{"json string K", `"32K"`, 32 * 1024, false},
		{"json string M", `"1M"`, 1024 * 1024, false},
		{"json string plain", `"32768"`, 32768, false},
		{"json zero", `0`, 0, false},
		{"invalid", `true`, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h IntOrHuman
			err := json.Unmarshal([]byte(tt.input), &h)
			if tt.err {
				if err == nil {
					t.Fatalf("expected error, got %d", h.Int())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if h.Int() != tt.want {
				t.Errorf("unmarshal %s = %d, want %d", tt.input, h.Int(), tt.want)
			}
		})
	}
}

func TestIntOrHuman_MarshalJSON(t *testing.T) {
	// Exact k/M multiples marshal back in the human form — a hand-written
	// "1M" in settings.json must survive a save round-trip instead of
	// being rewritten to 1048576 (user-facing: the settings UI re-reads
	// the file after save). Non-multiples stay plain numbers.
	tests := []struct {
		in   IntOrHuman
		want string
	}{
		{0, "0"},
		{1536, "1536"}, // 1.5k — not an exact multiple
		{100000, "100000"},
		{32768, `"32k"`},
		{200 * 1024, `"200k"`},
		{1024 * 1024, `"1M"`},
		{3 * 1024 * 1024, `"3M"`},
	}
	for _, tt := range tests {
		got, err := json.Marshal(tt.in)
		if err != nil {
			t.Fatalf("marshal %d: %v", int(tt.in), err)
		}
		if string(got) != tt.want {
			t.Errorf("marshal %d = %s, want %s", int(tt.in), got, tt.want)
		}
	}
}

func TestIntOrHuman_RoundTrip(t *testing.T) {
	tests := []int{
		0,
		1024,
		32 * 1024,
		200 * 1024,
		1024 * 1024,
	}
	for _, want := range tests {
		t.Run("", func(t *testing.T) {
			h := IntOrHuman(want)
			data, err := json.Marshal(h)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var h2 IntOrHuman
			if err := json.Unmarshal(data, &h2); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if h2.Int() != want {
				t.Errorf("roundtrip: got %d, want %d", h2.Int(), want)
			}
		})
	}
}

func TestIntOrHuman_InStruct(t *testing.T) {
	type cfg struct {
		Context   IntOrHuman `json:"context"`
		MaxTokens IntOrHuman `json:"max_tokens"`
	}

	raw := `{"context":"200k","max_tokens":"16k"}`
	var c cfg
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Context.Int() != 200*1024 {
		t.Errorf("context = %d, want %d", c.Context.Int(), 200*1024)
	}
	if c.MaxTokens.Int() != 16*1024 {
		t.Errorf("max_tokens = %d, want %d", c.MaxTokens.Int(), 16*1024)
	}
}

func TestIntOrHuman_IsSet(t *testing.T) {
	if IntOrHuman(0).IsSet() {
		t.Error("zero IntOrHuman should not be set")
	}
	if !IntOrHuman(42).IsSet() {
		t.Error("non-zero IntOrHuman should be set")
	}
}

func TestIntOrHuman_OmitEmpty(t *testing.T) {
	type cfg struct {
		Context   IntOrHuman `json:"context,omitempty"`
		MaxTokens IntOrHuman `json:"max_tokens,omitempty"`
	}

	// Zero values should be omitted.
	c := cfg{}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != `{}` {
		t.Errorf("empty struct = %s, want {}", data)
	}

	// Non-zero exact multiples appear in human form (save round-trip).
	c = cfg{Context: IntOrHuman(32 * 1024), MaxTokens: IntOrHuman(16 * 1024)}
	data, err = json.Marshal(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"context":"32k","max_tokens":"16k"}`
	if string(data) != want {
		t.Errorf("got %s, want %s", data, want)
	}
}
