package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/tool"
)

// newSectionsFixture builds an engine whose raw system prompt comes from the
// real Builder + AppendVoiceBlock, so the anchor scan runs against genuine
// production layout. HOME is isolated so {{SYSTEM}}/{{SOUL}} expansion is
// deterministic.
func newSectionsFixture(t *testing.T, home, memDir, skillListing string) *Engine {
	t.Helper()
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "1")
	t.Setenv("HOME", home)
	e := newTestEngineForBreakdown(t)
	b := context.NewBuilder(e.workingDir)
	b.ProjectDir = e.projectDir
	b.MemoryDirOverride = memDir
	b.SkillListing = skillListing
	e.SetSystemPrompt(context.AppendVoiceBlock(b.Build()))
	e.ContextTokens = 50_000
	return e
}

func sectionIDs(secs []PromptSection) []string {
	ids := make([]string, 0, len(secs))
	for _, s := range secs {
		ids = append(ids, s.ID)
	}
	return ids
}

func sectionByID(secs []PromptSection, id string) PromptSection {
	for _, s := range secs {
		if s.ID == id {
			return s
		}
	}
	return PromptSection{ID: id}
}

// Test A — the canonical Build layout yields exactly the anchored sections:
// soul omitted without SOUL.md, no "other" gap.
func TestBuildSystemPromptSections_MainPath(t *testing.T) {
	memDir := t.TempDir()
	idx := "# Memory\n\n"
	for i := range 150 {
		idx += fmt.Sprintf("- [Note %d](mem_%d.md) — hook %d\n", i, i, i)
	}
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte(idx), 0o644); err != nil {
		t.Fatalf("create MEMORY.md: %v", err)
	}
	listing := "- alpha — runs checks\n- beta — files results"
	e := newSectionsFixture(t, t.TempDir(), memDir, listing)

	bd := e.ContextBreakdown()
	if got := sectionIDs(bd.SystemPromptSections); !slices.Equal(got, []string{"base", "runtime", "skills", "voice"}) {
		t.Fatalf("section ids = %v, want [base runtime skills voice]", got)
	}

	base := sectionByID(bd.SystemPromptSections, "base")
	if base.Text != context.DefaultBasePrompt() {
		t.Errorf("base text != DefaultBasePrompt (len %d vs %d)", len(base.Text), len(context.DefaultBasePrompt()))
	}
	if base.Source != "" {
		t.Errorf("base source = %q, want empty (no SYSTEM.md → no file badge)", base.Source)
	}
	if base.Tokens != e.EstimateTokensCalibrated(context.DefaultBasePrompt()) {
		t.Errorf("base tokens = %d, want %d", base.Tokens, e.EstimateTokensCalibrated(context.DefaultBasePrompt()))
	}

	runtime_ := sectionByID(bd.SystemPromptSections, "runtime")
	if want := "# Environment\n\nRuntime: "; !strings.HasPrefix(runtime_.Text, want) {
		t.Errorf("runtime text lacks prefix %q", want)
	}
	if !strings.Contains(runtime_.Text, "model=claude-test") {
		t.Error("runtime text lacks model=claude-test (stub left unexpanded?)")
	}
	if !strings.Contains(runtime_.Text, "modalities=text") {
		t.Error("runtime text lacks modalities=text (nil modalities must default to text)")
	}
	if strings.Contains(runtime_.Text, "{{MODEL}}") {
		t.Error("runtime text still carries the {{MODEL}} stub")
	}
	if runtime_.Source != "" {
		t.Errorf("runtime source = %q, want empty", runtime_.Source)
	}

	skills := sectionByID(bd.SystemPromptSections, "skills")
	if skills.Text != "## Available Skills\n\n"+listing {
		t.Errorf("skills text = %q, want the Build-written header + listing", skills.Text)
	}
	if skills.Source != "" {
		t.Errorf("skills source = %q, want empty (no badge)", skills.Source)
	}

	voice := sectionByID(bd.SystemPromptSections, "voice")
	if voice.Text != context.VoicePromptBlock {
		t.Errorf("voice text != VoicePromptBlock (len %d vs %d)", len(voice.Text), len(context.VoicePromptBlock))
	}
	if voice.Source != "" {
		t.Errorf("voice source = %q, want empty", voice.Source)
	}
}

// Test B — SYSTEM.md/SOUL.md present: base bills the file, soul appears with
// both leading separators stripped (the span's and the expansion value's).
func TestBuildSystemPromptSections_FileSourcesAndSoul(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	gbotDir := filepath.Join(home, ".gbot")
	if err := os.MkdirAll(gbotDir, 0o755); err != nil {
		t.Fatalf("mkdir .gbot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gbotDir, "SYSTEM.md"), []byte("MY SYSTEM"), 0o644); err != nil {
		t.Fatalf("create SYSTEM.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gbotDir, "SOUL.md"), []byte("my soul"), 0o644); err != nil {
		t.Fatalf("create SOUL.md: %v", err)
	}

	e := newSectionsFixture(t, home, t.TempDir(), "")

	bd := e.ContextBreakdown()
	if got := sectionIDs(bd.SystemPromptSections); !slices.Equal(got, []string{"base", "soul", "runtime", "voice"}) {
		t.Fatalf("section ids = %v, want [base soul runtime voice]", got)
	}

	base := sectionByID(bd.SystemPromptSections, "base")
	if base.Text != "MY SYSTEM" {
		t.Errorf("base text = %q, want MY SYSTEM", base.Text)
	}
	if base.Source != "system_md" {
		t.Errorf("base source = %q, want system_md", base.Source)
	}

	soul := sectionByID(bd.SystemPromptSections, "soul")
	want := "---\n\nEmbody the persona and tone defined below. Follow its guidance unless higher-priority instructions override it.\n\nmy soul"
	if soul.Text != want {
		t.Errorf("soul text = %q, want %q", soul.Text, want)
	}
	if soul.Source != "soul_md" {
		t.Errorf("soul source = %q, want soul_md", soul.Source)
	}
}

// Test C — a hand-set prompt carries no anchors: one "other" section holds
// the bytes verbatim.
func TestBuildSystemPromptSections_CustomPromptDegradesToOther(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("custom prompt body")
	e.ContextTokens = 5_000

	bd := e.ContextBreakdown()
	if len(bd.SystemPromptSections) != 1 {
		t.Fatalf("sections = %v, want exactly one", sectionIDs(bd.SystemPromptSections))
	}
	s := bd.SystemPromptSections[0]
	if s.ID != "other" {
		t.Errorf("section id = %q, want other", s.ID)
	}
	if s.Text != "custom prompt body" {
		t.Errorf("section text = %q, want the raw prompt byte for byte", s.Text)
	}
	if s.Source != "" {
		t.Errorf("section source = %q, want empty", s.Source)
	}
	if s.Tokens != e.EstimateTokensCalibrated("custom prompt body") {
		t.Errorf("section tokens = %d, want %d", s.Tokens, e.EstimateTokensCalibrated("custom prompt body"))
	}
}

// Test D — the memory prompt splits at the MEMORY.md marker.
func TestBuildMemorySections_TwoSegments(t *testing.T) {
	memDir := t.TempDir()
	idx := "# Memory\n\n- [Alpha](mem_alpha.md) — hook a\n- [Beta](mem_beta.md) — hook b\n"
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte(idx), 0o644); err != nil {
		t.Fatalf("create MEMORY.md: %v", err)
	}
	e := newSectionsFixture(t, t.TempDir(), memDir, "")

	bd := e.ContextBreakdown()
	if len(bd.MemorySections) != 2 {
		t.Fatalf("memory sections = %v, want two", sectionIDs(bd.MemorySections))
	}
	instr, index := bd.MemorySections[0], bd.MemorySections[1]

	if instr.ID != "instructions" || instr.Source != "" {
		t.Errorf("instructions = %q/%q, want instructions with no source", instr.ID, instr.Source)
	}
	if !strings.HasPrefix(instr.Text, "# auto memory") {
		t.Errorf("instructions text starts with %q, want # auto memory", firstLine(instr.Text))
	}
	if strings.Contains(instr.Text, "## MEMORY.md") {
		t.Error("instructions text contains the index marker")
	}

	if index.ID != "index" || index.Source != "memory_md" {
		t.Errorf("index = %q/%q, want index/memory_md", index.ID, index.Source)
	}
	if !strings.HasPrefix(index.Text, "## MEMORY.md") {
		t.Errorf("index text starts with %q, want ## MEMORY.md", firstLine(index.Text))
	}
	if !strings.Contains(index.Text, "- [Alpha](mem_alpha.md) — hook a") {
		t.Error("index text lacks the first index line")
	}

	if instr.Tokens != e.EstimateTokensCalibrated(instr.Text) {
		t.Errorf("instructions tokens = %d, want %d", instr.Tokens, e.EstimateTokensCalibrated(instr.Text))
	}
	if index.Tokens != e.EstimateTokensCalibrated(index.Text) {
		t.Errorf("index tokens = %d, want %d", index.Tokens, e.EstimateTokensCalibrated(index.Text))
	}
}

func TestBuildMemorySections_EmptyIndexPlaceholder(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "1")
	t.Setenv("HOME", t.TempDir())
	e := newTestEngineForBreakdown(t)
	memDir := t.TempDir()
	memPrompt := context.FormatMemoryPrompt(e.workingDir, memDir)
	if memPrompt == "" {
		t.Fatal("FormatMemoryPrompt returned empty; typed memory must be enabled")
	}
	secs := e.buildMemorySections(memPrompt)
	if len(secs) != 2 {
		t.Fatalf("sections = %v, want two even with an empty index", sectionIDs(secs))
	}
	if !strings.Contains(secs[1].Text, "currently empty") {
		t.Errorf("index text lacks the empty placeholder: %q", firstLine(secs[1].Text))
	}
}

func TestBuildMemorySections_EmptyPromptReturnsNil(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	if got := e.buildMemorySections(""); got != nil {
		t.Errorf("buildMemorySections(\"\") = %v, want nil", sectionIDs(got))
	}
}

func TestBuildMemorySections_MissingMarkerDegenerates(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	secs := e.buildMemorySections("no marker text")
	if len(secs) != 1 {
		t.Fatalf("sections = %v, want a single instructions section", sectionIDs(secs))
	}
	if secs[0].ID != "instructions" || secs[0].Text != "no marker text" || secs[0].Source != "" {
		t.Errorf("section = %+v, want instructions holding the full text", secs[0])
	}
	if secs[0].Tokens != e.EstimateTokensCalibrated("no marker text") {
		t.Errorf("section tokens = %d, want %d", secs[0].Tokens, e.EstimateTokensCalibrated("no marker text"))
	}
}

// The marker may legally sit at index 0 (empty instructions part); that must
// not trip the missing-marker fallback.
func TestBuildMemorySections_MarkerAtStart(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	secs := e.buildMemorySections("\n## MEMORY.md\n\nX")
	if len(secs) != 2 {
		t.Fatalf("sections = %v, want instructions + index even with empty instructions", sectionIDs(secs))
	}
	if secs[0].ID != "instructions" || secs[0].Text != "" {
		t.Errorf("instructions = %q/%q, want empty text", secs[0].ID, secs[0].Text)
	}
	if secs[1].ID != "index" || secs[1].Text != "## MEMORY.md\n\nX" {
		t.Errorf("index = %q/%q, want the marker-led text", secs[1].ID, secs[1].Text)
	}
}

func TestBuildSystemPromptSections_EmptyRawReturnsNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := (&Engine{}).buildSystemPromptSections("", "m", nil, ""); got != nil {
		t.Errorf("sections = %v, want nil for an empty raw prompt", sectionIDs(got))
	}
}

// Direct call: through the engine the modality list is never empty (New
// defaults it), so the ["text"] fallback needs its own input.
func TestBuildSystemPromptSections_NilModalitiesDefaultToText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	secs := (&Engine{}).buildSystemPromptSections("A {{MODALITIES}} B", "m", nil, "")
	if len(secs) != 1 || secs[0].ID != "other" {
		t.Fatalf("sections = %v, want one other section", sectionIDs(secs))
	}
	if secs[0].Text != "A text B" {
		t.Errorf("text = %q, want nil modalities expanded as text", secs[0].Text)
	}
}

// The request path replaces each stub exactly once; a second occurrence is
// literal base content.
func TestBuildSystemPromptSections_ExpandsOnlyFirstStubOccurrence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	raw := "A {{MODEL}} B {{MODEL}} C {{MODALITIES}} D {{MODALITIES}}"
	secs := (&Engine{}).buildSystemPromptSections(raw, "m", []string{"text"}, "")
	if len(secs) != 1 || secs[0].ID != "other" {
		t.Fatalf("sections = %v, want one other section", sectionIDs(secs))
	}
	want := "A m B {{MODEL}} C text D {{MODALITIES}}"
	if secs[0].Text != want {
		t.Errorf("text = %q, want %q", secs[0].Text, want)
	}
}

// Junk before the first anchor becomes a trailing "other" section;
// whitespace-only gap text is not a section.
func TestBuildSystemPromptSections_LeadingGap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := &Engine{}
	tail := "\n\n## Available Skills\n\nL\n\n" + context.VoicePromptBlock

	junk := e.buildSystemPromptSections("JUNK"+tail, "m", nil, "")
	if got := sectionIDs(junk); !slices.Equal(got, []string{"skills", "voice", "other"}) {
		t.Fatalf("ids = %v, want [skills voice other]", got)
	}
	if byID := sectionByID(junk, "other"); byID.Text != "JUNK" {
		t.Errorf("other text = %q, want JUNK", byID.Text)
	}

	ws := e.buildSystemPromptSections("  "+tail, "m", nil, "")
	if got := sectionIDs(ws); !slices.Equal(got, []string{"skills", "voice"}) {
		t.Errorf("whitespace-only gap: ids = %v, want no other section", got)
	}
}

// A whitespace-only prompt with no anchors yields no sections at all.
func TestBuildSystemPromptSections_WhitespaceOnlyPromptYieldsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	secs := (&Engine{}).buildSystemPromptSections("   ", "m", nil, "")
	if len(secs) != 0 {
		t.Errorf("sections = %v, want none for whitespace-only input", sectionIDs(secs))
	}
}

// Only the first {{SYSTEM}} in the base span expands — a second stub in a
// hand-set prompt is literal content.
func TestBuildSystemPromptSections_BaseExpandsOnlyFirstStub(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	secs := (&Engine{}).buildSystemPromptSections("{{SYSTEM}} X {{SYSTEM}}", "m", nil, "")
	if len(secs) != 1 || secs[0].ID != "base" {
		t.Fatalf("sections = %v, want one base section", sectionIDs(secs))
	}
	if !strings.HasSuffix(secs[0].Text, " X {{SYSTEM}}") {
		t.Error("base text should keep the second stub literal")
	}
}

// Every anchor must be findable at position 0 (a custom prompt may begin
// with any section); an off-by-one on the anchor scan would drop it.
func TestBuildSystemPromptSections_AnchorAtPositionZero(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	gbotDir := filepath.Join(home, ".gbot")
	if err := os.MkdirAll(gbotDir, 0o755); err != nil {
		t.Fatalf("mkdir .gbot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gbotDir, "SOUL.md"), []byte("soul body"), 0o644); err != nil {
		t.Fatalf("create SOUL.md: %v", err)
	}
	e := &Engine{}
	voiceTail := "\n\n" + context.VoicePromptBlock

	soul := e.buildSystemPromptSections("\n\n{{SOUL}}"+voiceTail, "m", nil, "")
	if got := sectionIDs(soul); !slices.Equal(got, []string{"soul", "voice"}) {
		t.Errorf("soul@0: ids = %v, want [soul voice]", got)
	}
	rt := e.buildSystemPromptSections("\n\n# Environment\n\nRuntime: X"+voiceTail, "m", nil, "")
	if got := sectionIDs(rt); !slices.Equal(got, []string{"runtime", "voice"}) {
		t.Errorf("runtime@0: ids = %v, want [runtime voice]", got)
	}
	sk := e.buildSystemPromptSections("\n\n## Available Skills\n\nL"+voiceTail, "m", nil, "")
	if got := sectionIDs(sk); !slices.Equal(got, []string{"skills", "voice"}) {
		t.Errorf("skills@0: ids = %v, want [skills voice]", got)
	}
}

// Anchors are appended in code order, not layout order — a custom prompt
// with skills before platform info must come out layout-sorted.
func TestBuildSystemPromptSections_OutOfOrderAnchorsSorted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	raw := "\n\n## Available Skills\n\nL\n\n# Environment\n\nRuntime: X" +
		"\n\n" + context.VoicePromptBlock
	secs := (&Engine{}).buildSystemPromptSections(raw, "m", nil, "")
	if got := sectionIDs(secs); !slices.Equal(got, []string{"skills", "runtime", "voice"}) {
		t.Errorf("ids = %v, want layout order [skills runtime voice]", got)
	}
}

// The wire needs name-ascending order regardless of map iteration order; a
// single pass can be lucky, so pin it across many iterations.
func TestBuildDetails_SystemToolsSortedAcrossIterations(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	tools := map[string]tool.Tool{
		"Bash":  &testTool{name: "Bash"},
		"Agent": &testTool{name: "Agent"},
		"Read":  &testTool{name: "Read"},
	}
	for range 12 {
		ds := e.buildDetails(nil, tools, nil, "", nil, nil, nil)
		if len(ds.systemTools) != 3 {
			t.Fatalf("systemTools = %v, want three tools", toolDetailSig(ds.systemTools))
		}
		for i := 1; i < len(ds.systemTools); i++ {
			if ds.systemTools[i-1].Name > ds.systemTools[i].Name {
				t.Fatalf("systemTools not sorted: %v", toolDetailSig(ds.systemTools))
			}
		}
	}
}

// Test F — the zero-token early return carries no sections.
func TestContextBreakdown_ZeroTokenEarlyReturnHasNoSections(t *testing.T) {
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("x")
	bd := e.ContextBreakdown()
	if bd.SystemPromptSections != nil {
		t.Errorf("SystemPromptSections = %v, want nil before the first API response", sectionIDs(bd.SystemPromptSections))
	}
	if bd.MemorySections != nil {
		t.Errorf("MemorySections = %v, want nil before the first API response", sectionIDs(bd.MemorySections))
	}
}

// Test E — system tool details carry the full request entry as display text
// (name + Prompt + schema), sorted by name for wire determinism; deferred
// entries stay textless (their prompt never enters the context).
func TestContextBreakdown_ToolSectionTextAndOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := newTestEngineForBreakdown(t)
	e.SetSystemPrompt("x")
	e.tools["Bash"] = &testTool{name: "Bash"}
	e.tools["Agent"] = &testTool{name: "Agent"}
	e.tools["ToolSearch"] = deferredStubTool("ToolSearch")
	e.ContextTokens = 5_000

	bd := e.ContextBreakdown()
	if len(bd.SystemTools) != 2 {
		t.Fatalf("SystemTools = %v, want Bash + Agent", toolDetailSig(bd.SystemTools))
	}
	if bd.SystemTools[0].Name != "Agent" || bd.SystemTools[1].Name != "Bash" {
		t.Errorf("SystemTools order = [%s %s], want [Agent Bash] (name ascending)",
			bd.SystemTools[0].Name, bd.SystemTools[1].Name)
	}
	for _, d := range bd.SystemTools {
		tt := e.tools[d.Name]
		want := tt.Name() + "\n\n" + tt.Prompt() + "\n\n" + string(tt.InputSchema())
		if d.Text != want {
			t.Errorf("%s text = %q, want %q", d.Name, d.Text, want)
		}
	}
	if len(bd.DeferredBuiltinTools) != 1 || bd.DeferredBuiltinTools[0].Name != "ToolSearch" {
		t.Fatalf("DeferredBuiltinTools = %v, want exactly ToolSearch", toolDetailSig(bd.DeferredBuiltinTools))
	}
	if bd.DeferredBuiltinTools[0].Text != "" {
		t.Errorf("deferred ToolSearch text = %q, want empty", bd.DeferredBuiltinTools[0].Text)
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
