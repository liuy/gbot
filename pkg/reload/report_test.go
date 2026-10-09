package reload

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDiffSettingsJSON(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
		want []string
	}{
		{
			name: "nested key change",
			old:  `{"a":{"b":1}}`,
			new:  `{"a":{"b":2}}`,
			want: []string{`a.b: 1 → 2`},
		},
		{
			name: "added key",
			old:  `{"a":1}`,
			new:  `{"a":1,"c":3}`,
			want: []string{`c: <none> → 3`},
		},
		{
			name: "removed key",
			old:  `{"a":1,"c":3}`,
			new:  `{"a":1}`,
			want: []string{`c: 3 → <none>`},
		},
		{
			name: "array element change",
			old:  `{"l":["x","y"]}`,
			new:  `{"l":["x","z"]}`,
			want: []string{`l[1]: "y" → "z"`},
		},
		{
			name: "unchanged",
			old:  `{"a":{"b":[1,2]}}`,
			new:  `{"a":{"b":[1,2]}}`,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := diffSettingsJSON([]byte(tc.old), []byte(tc.new))
			if len(got) != len(tc.want) {
				t.Fatalf("diffSettingsJSON = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("leaf[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestDiffSettingsJSON_CappedAtFive(t *testing.T) {
	old := `{"a":1}`
	newObj := `{"a":2,"b":1,"c":1,"d":1,"e":1,"f":1,"g":1}`
	got := diffSettingsJSON([]byte(old), []byte(newObj))
	if len(got) != 6 {
		t.Fatalf("got %d leaves, want 5 diffs + 1 more-marker: %v", len(got), got)
	}
	if got[5] != "+2 more" {
		t.Errorf("cap marker = %q, want '+2 more'", got[5])
	}
	for _, leaf := range got[:5] {
		if strings.Contains(leaf, "→ <none>") || !strings.Contains(leaf, "→ ") {
			t.Errorf("unexpected leaf in capped output: %q", leaf)
		}
	}
}

func TestReportRender_Aborted(t *testing.T) {
	rep := &Report{
		Applied: false,
		Phase:   1,
		Err:     "plugins: invalid /x/broken/mcp.json: bad JSON",
		Files: []FileEntry{
			{Path: "~/.gbot/settings.json", Status: StatusUnchanged},
			{Path: "/x/plugins/broken/mcp.json", Status: StatusFailed, ErrLine: "line 3: invalid character '}'"},
		},
	}
	rep.Counts.Reloaded, rep.Counts.Unchanged, rep.Counts.Failed = 0, 1, 1
	rep.LastReloadAt = time.Unix(1700000000, 0).UTC()

	out := rep.Render()
	for _, want := range []string{
		"aborted",
		"phase 2",
		"plugins: invalid /x/broken/mcp.json",
		"~/.gbot/settings.json",
		"/x/plugins/broken/mcp.json",
		"unchanged",
		"failed",
		"line 3: invalid character '}'",
		"0 reloaded",
		"1 unchanged",
		"1 failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Render() missing %q; got:\n%s", want, out)
		}
	}
}

func TestReportRender_Applied(t *testing.T) {
	rep := &Report{
		Applied:         true,
		Phase:           4,
		PromptChanged:   true,
		SettingsChanged: true,
		Files: []FileEntry{
			{Path: "~/.gbot/settings.json", Status: StatusReloaded, Note: "providers[0].models.m.max_tokens: 32000 → 16000"},
			{Path: "MEMORY.md · CLAUDE.md", Status: StatusReloaded, NoteKey: "effectiveNextRequest"},
		},
		EnginesRefreshed: 2,
	}
	rep.Counts.Reloaded = 2
	rep.LastReloadAt = time.Unix(1700000000, 0).UTC()

	out := rep.Render()
	for _, want := range []string{
		"applied",
		"~/.gbot/settings.json",
		"MEMORY.md · CLAUDE.md",
		"reloaded",
		"2 reloaded",
		"0 unchanged",
		"engine params apply to new sessions",
		"2 engine",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Render() missing %q; got:\n%s", want, out)
		}
	}
	if slices.Contains(strings.Split(out, "\n"), "") {
		t.Errorf("Render() produced blank lines:\n%q", out)
	}
}
