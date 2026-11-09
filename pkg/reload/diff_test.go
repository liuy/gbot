package reload

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/plugins"
	"github.com/liuy/gbot/pkg/types"
)

// Pin tests for diff.go + report.go mutation sweep; assertions are exact.

func TestPinDiffSettingsJSON_Leaves(t *testing.T) {
	cases := []struct {
		name, oldRaw, newRaw string
		want                 []string
	}{
		{"string change", `{"a":"x"}`, `{"a":"y"}`, []string{`a: "x" → "y"`}},
		{"number change", `{"n":1}`, `{"n":2}`, []string{`n: 1 → 2`}},
		{"added key", `{"a":1}`, `{"a":1,"b":2}`, []string{`b: <none> → 2`}},
		{"removed key", `{"a":1,"b":2}`, `{"a":1}`, []string{`b: 2 → <none>`}},
		{"nested path", `{"o":{"k":"x"}}`, `{"o":{"k":"z"}}`, []string{`o.k: "x" → "z"`}},
		{"array element", `{"l":[1,2]}`, `{"l":[1,3]}`, []string{`l[1]: 2 → 3`}},
		{"array shrink", `{"l":[1,2]}`, `{"l":[1]}`, []string{`l[1]: 2 → <none>`}},
		{"map→leaf", `{"o":{"k":1}}`, `{"o":5}`, []string{`o.k: 1 → <none>`}},
		{"sorted output", `{"z":1,"a":1}`, `{"z":2,"a":2}`, []string{`a: 1 → 2`, `z: 1 → 2`}},
		{"empty old", ``, `{"a":1}`, []string{`a: <none> → 1`}},
		{"empty new", `{"a":1}`, ``, []string{`a: 1 → <none>`}},
		{"equal input", `{"a":1}`, `{"a":1}`, nil},
		{"bad old json", `not-json`, `{"a":1}`, []string{`a: <none> → 1`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := diffSettingsJSON([]byte(tc.oldRaw), []byte(tc.newRaw))
			if len(got) != len(tc.want) {
				t.Fatalf("leaves = %#v, want %#v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("leaves[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestPinDiffSettingsJSON_CapFive(t *testing.T) {
	old := `{}`
	var newBuf strings.Builder
	newBuf.WriteString("{")
	for i := range 7 {
		if i > 0 {
			newBuf.WriteString(",")
		}
		newBuf.WriteString(strings.Repeat("a", 1))
	}
	// 7 distinct keys changing
	k := func(i int) string { return string(rune('a' + i)) }
	newBuf.Reset()
	newBuf.WriteString("{")
	for i := range 7 {
		if i > 0 {
			newBuf.WriteString(",")
		}
		newBuf.WriteString(`"` + k(i) + `":` + string(rune('1'+i)))
	}
	newBuf.WriteString("}")
	got := diffSettingsJSON([]byte(old), []byte(newBuf.String()))
	if len(got) != 6 {
		t.Fatalf("len = %d, want 6 (5 leaves + '+2 more')", len(got))
	}
	if got[5] != "+2 more" {
		t.Errorf("marker = %q, want '+2 more'", got[5])
	}
	if got[0] != `a: <none> → 1` {
		t.Errorf("first leaf = %q, want the sorted first", got[0])
	}
}

func TestPinDiffMcpServers(t *testing.T) {
	old := &plugins.PluginPieces{Name: "p", McpServers: map[string]mcp.ScopedMcpServerConfig{
		"keep": {Config: &mcp.StdioConfig{Command: "a"}},
		"drop": {Config: &mcp.StdioConfig{Command: "b"}},
		"chg":  {Config: &mcp.StdioConfig{Command: "c"}},
	}}
	newP := &plugins.PluginPieces{Name: "p", McpServers: map[string]mcp.ScopedMcpServerConfig{
		"keep": {Config: &mcp.StdioConfig{Command: "a"}},
		"new":  {Config: &mcp.StdioConfig{Command: "d"}},
		"chg":  {Config: &mcp.StdioConfig{Command: "e"}},
	}}
	added, removed, changed := diffMcpServers(old, newP)
	if added != 1 || removed != 1 || changed != 1 {
		t.Errorf("diff = +%d -%d ~%d, want +1 -1 ~1", added, removed, changed)
	}
	a2, r2, c2 := diffMcpServers(nil, newP)
	if a2 != 3 || r2 != 0 || c2 != 0 {
		t.Errorf("from nil = +%d -%d ~%d, want +3 -0 ~0", a2, r2, c2)
	}
	a3, r3, c3 := diffMcpServers(old, nil)
	if a3 != 0 || r3 != 3 || c3 != 0 {
		t.Errorf("to nil = +%d -%d ~%d, want +0 -3 ~0", a3, r3, c3)
	}
}

func TestPinFingerprints(t *testing.T) {
	p := &plugins.PluginPieces{
		Name:        "p",
		Skills:      []types.SkillCommand{{Name: "s1", Description: "d", Content: "c1"}},
		Agents:      []types.AgentDefinition{{AgentType: "a1", SystemPrompt: func() string { return "sp1" }, Model: "m"}},
		ReplScripts: []plugins.ReplScript{{Plugin: "p", Name: "r.js", Source: "var x=1"}},
	}
	if skillsFingerprint(p) == "" {
		t.Error("skills fingerprint empty")
	}
	if agentsFingerprint(p) == "" {
		t.Error("agents fingerprint empty")
	}
	if replsFingerprint(p) == "" {
		t.Error("repls fingerprint empty")
	}
	p2 := &plugins.PluginPieces{
		Name:        "p",
		Skills:      []types.SkillCommand{{Name: "s2", Description: "d", Content: "c1"}},
		Agents:      []types.AgentDefinition{{AgentType: "a2", SystemPrompt: func() string { return "sp2" }, Model: "m"}},
		ReplScripts: []plugins.ReplScript{{Plugin: "p", Name: "r.js", Source: "var x=2"}},
	}
	if skillsFingerprint(p) == skillsFingerprint(p2) {
		t.Error("skills fingerprint insensitive to content")
	}
	if agentsFingerprint(p) == agentsFingerprint(p2) {
		t.Error("agents fingerprint insensitive to content")
	}
	if replsFingerprint(p) == replsFingerprint(p2) {
		t.Error("repls fingerprint insensitive to content")
	}
}

func TestPinFingerprintStatus(t *testing.T) {
	old := &plugins.PluginPieces{Name: "p", Skills: []types.SkillCommand{{Name: "s1", Description: "d", Content: "c"}}}
	same := &plugins.PluginPieces{Name: "p", Skills: []types.SkillCommand{{Name: "s1", Description: "d", Content: "c"}}}
	other := &plugins.PluginPieces{Name: "p", Skills: []types.SkillCommand{{Name: "s9", Description: "d", Content: "c"}}}
	if got := fingerprintStatus(skillsFingerprint, old, same); got != StatusUnchanged {
		t.Errorf("same content status = %q, want unchanged", got)
	}
	if got := fingerprintStatus(skillsFingerprint, old, other); got != StatusReloaded {
		t.Errorf("changed content status = %q, want reloaded", got)
	}
	if got := fingerprintStatus(skillsFingerprint, old, nil); got != StatusReloaded {
		t.Errorf("nil new status = %q, want reloaded", got)
	}
}

func TestPinDroppedServerErrors(t *testing.T) {
	envMissing := mcp.ValidationError{Path: "a", Message: "Missing environment variables: FOO"}
	kept := mcp.ValidationError{Path: "b", Message: "server dropped: b"}
	got := droppedServerErrors([]mcp.ValidationError{envMissing, kept})
	if len(got) != 1 || got[0].Path != "b" {
		t.Errorf("droppedServerErrors = %+v, want only b", got)
	}
	if got := droppedServerErrors(nil); got != nil {
		t.Errorf("droppedServerErrors(nil) = %+v, want nil", got)
	}
}

func TestPinReportRender(t *testing.T) {
	r := &Report{
		Applied: false, Phase: 2, Err: "boom",
		Files: []FileEntry{
			{Path: "p1", Status: StatusFailed, ErrLine: "line1"},
			{Path: "p2", Status: StatusReloaded, Note: "n2", NoteKey: "effectiveNextRequest"},
			{Path: "p3", Status: StatusUnchanged},
		},
		PromptChanged:    true,
		SettingsChanged:  true,
		EnginesRefreshed: 2,
	}
	r.Counts.Reloaded, r.Counts.Unchanged, r.Counts.Failed = 1, 1, 1
	want := strings.Join([]string{
		"Reload aborted at phase 3 of 5: boom",
		"1 reloaded · 1 unchanged · 1 failed",
		"- p1 [failed]",
		"  error: line1",
		"- p2 [reloaded] n2 · effective on next request",
		"- p3 [unchanged]",
		"MEMORY.md/CLAUDE.md changed: effective on next request",
		"engine params apply to new sessions only",
		"2 engine(s) context refreshed",
	}, "\n")
	if got := r.Render(); got != want {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}

	ok := &Report{Applied: true}
	ok.Counts.Reloaded = 3
	if got := ok.Render(); got != "Reload applied\n3 reloaded · 0 unchanged · 0 failed" {
		t.Errorf("minimal render = %q", got)
	}
	one := &Report{Applied: true, EnginesRefreshed: 1}
	if got := one.Render(); !strings.Contains(got, "1 engine(s) context refreshed") {
		t.Errorf("single-engine render = %q, want the refreshed line", got)
	}
}

func TestPinNoteKeyText(t *testing.T) {
	if got := noteKeyText("effectiveNextRequest"); got != "effective on next request" {
		t.Errorf("effectiveNextRequest = %q", got)
	}
	if got := noteKeyText("newSessionsOnly"); got != "engine params apply to new sessions only" {
		t.Errorf("newSessionsOnly = %q", got)
	}
	if got := noteKeyText("unknownKey"); got != "unknownKey" {
		t.Errorf("unknown passthrough = %q", got)
	}
}
