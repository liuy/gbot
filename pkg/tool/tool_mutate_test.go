package tool

import "testing"

// Any of the four search/read flags makes a call collapsible.
func TestSearchReadKind_IsCollapsible(t *testing.T) {
	t.Parallel()
	flags := []bool{true, false}
	for _, s := range flags {
		for _, r := range flags {
			for _, l := range flags {
				for _, p := range flags {
					k := SearchReadKind{IsSearch: s, IsRead: r, IsList: l, IsLsp: p}
					want := s || r || l || p
					if got := k.IsCollapsible(); got != want {
						t.Errorf("IsCollapsible(%v,%v,%v,%v) = %v, want %v", s, r, l, p, got, want)
					}
				}
			}
		}
	}
}

func TestMaxUncappedOutput(t *testing.T) {
	t.Parallel()
	if MaxUncappedOutput != 64*1024*1024 {
		t.Errorf("MaxUncappedOutput = %d, want 64 MiB", MaxUncappedOutput)
	}
}

// tokenizeLines keeps the newline on every line except a final partial one.
func TestTokenizeLines(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"a\nb", []string{"a\n", "b"}},
		{"a\nb\n", []string{"a\n", "b\n", "\n"}},
		{"single", []string{"single"}},
		{"", []string{""}},
		{"\n", []string{"\n", "\n"}},
	}
	for _, c := range cases {
		got := tokenizeLines(c.in)
		if len(got) != len(c.want) {
			t.Errorf("tokenizeLines(%q) = %q, want %q", c.in, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("tokenizeLines(%q) = %q, want %q", c.in, got, c.want)
				break
			}
		}
	}
}

// The diff summary joins the two halves only when both are present.
func TestFormatDiffSummary_Joining(t *testing.T) {
	t.Parallel()
	cases := []struct {
		added, removed int
		want           string
	}{
		{0, 0, ""},
		{3, 0, "Added 3 lines"},
		{0, 2, "Removed 2 lines"},
		{3, 2, "Added 3 lines, removed 2 lines"},
		{1, 0, "Added 1 line"},
		{0, 1, "Removed 1 line"},
		{1, 1, "Added 1 line, removed 1 line"},
	}
	for _, c := range cases {
		got := stripANSI(FormatDiffSummary(c.added, c.removed))
		if got != c.want {
			t.Errorf("FormatDiffSummary(%d, %d) = %q, want %q", c.added, c.removed, got, c.want)
		}
	}
}

func stripANSI(s string) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

// Only ESC[H and ESC[1;1H act as a carriage return; other CUP targets are
// ignored.
func TestScreen_CursorPosition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		want  []ScreenEvent
	}{
		{
			"bare home", "h1\x1b[Hh2\n",
			[]ScreenEvent{{Kind: ScreenAppend, Content: "h1"}, {Kind: ScreenReplace, Content: "h2"}},
		},
		{
			"explicit 1;1 home", "h1\x1b[1;1Hh2\n",
			[]ScreenEvent{{Kind: ScreenAppend, Content: "h1"}, {Kind: ScreenReplace, Content: "h2"}},
		},
		{
			"row 2 is not home", "i1\x1b[2;5Hi2\n",
			[]ScreenEvent{{Kind: ScreenAppend, Content: "i1i2"}},
		},
		{
			"1;2 is not home", "j1\x1b[1;2Hj2\n",
			[]ScreenEvent{{Kind: ScreenAppend, Content: "j1j2"}},
		},
		{
			"2;1 is not home", "k1\x1b[2;1Hk2\n",
			[]ScreenEvent{{Kind: ScreenAppend, Content: "k1k2"}},
		},
	}
	for _, c := range cases {
		var events []ScreenEvent
		s := NewScreen(func(ev ScreenEvent) { events = append(events, ev) })
		s.Write([]byte(c.input))
		if len(events) != len(c.want) {
			t.Errorf("%s: events = %+v, want %+v", c.name, events, c.want)
			continue
		}
		for i := range c.want {
			if events[i].Kind != c.want[i].Kind || events[i].Content != c.want[i].Content {
				t.Errorf("%s: events = %+v, want %+v", c.name, events, c.want)
				break
			}
		}
	}
}
