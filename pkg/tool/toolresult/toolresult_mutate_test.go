package toolresult

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorageConstants(t *testing.T) {
	t.Parallel()
	if MaxPersistSizeBytes != 64*1024*1024 {
		t.Errorf("MaxPersistSizeBytes = %d, want 64 MiB", MaxPersistSizeBytes)
	}
	if PreviewSizeBytes != 2000 {
		t.Errorf("PreviewSizeBytes = %d, want 2000", PreviewSizeBytes)
	}
	if DefaultMaxResultSizeChars != 50000 {
		t.Errorf("DefaultMaxResultSizeChars = %d, want 50000", DefaultMaxResultSizeChars)
	}
	// The wire budget is negotiated with the API side; drifting by one breaks
	// the exact-boundary behavior the selection logic relies on.
	if MaxToolResultsPerMessageChars != 200000 {
		t.Errorf("MaxToolResultsPerMessageChars = %d, want 200000", MaxToolResultsPerMessageChars)
	}
}

// Empty content must not be probed for a JSON marker.
func TestPersistToolResult_IsJSONShape(t *testing.T) {
	for _, c := range []struct {
		name     string
		content  string
		wantJSON bool
		ext      string
	}{
		{"empty", "", false, ".txt"},
		{"array", `[{"type":"text"}]`, true, ".json"},
		{"object", `{"a":1}`, true, ".json"},
		{"text", "plain output", false, ".txt"},
		// A lone '[' byte is array-shaped even though it is not valid JSON:
		// the extension decision must look at the first byte only.
		{"lone bracket", "[", true, ".json"},
	} {
		// Isolated HOME per case: the exclusive-create semantics make a shared
		// session directory leak state across cases and across test runs.
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			ResetDirCache()
			res, err := PersistToolResult("sessshape", "tu-"+c.name, []byte(c.content))
			if err != nil {
				t.Fatalf("%s: PersistToolResult: %v", c.name, err)
			}
			if res.IsJSON != c.wantJSON {
				t.Errorf("%s: IsJSON = %v, want %v", c.name, res.IsJSON, c.wantJSON)
			}
			if ext := filepath.Ext(res.Filepath); ext != c.ext {
				t.Errorf("%s: extension = %q, want %q", c.name, ext, c.ext)
			}
			if res.OriginalSize != len(c.content) {
				t.Errorf("%s: OriginalSize = %d, want %d", c.name, res.OriginalSize, len(c.content))
			}
			if res.Preview != c.content {
				t.Errorf("%s: Preview = %q, want the content back", c.name, res.Preview)
			}
		})
	}
}

// The preview cut must land on a UTF-8 rune start, with the documented
// fallback when the whole window is one rune.
func TestGeneratePreview_UTF8Boundary(t *testing.T) {
	t.Parallel()
	// Cut inside a multi-byte rune: walk back to its start.
	content := "aé" + strings.Repeat("x", 10)
	got, hasMore := GeneratePreview(content, 2)
	if !hasMore {
		t.Fatal("hasMore = false, want true")
	}
	if got != "a" {
		t.Errorf("preview = %q, want a (cut before the rune)", got)
	}
	// Window entirely inside one 4-byte rune: fall back to maxBytes.
	content = "😀" + strings.Repeat("y", 10)
	got, hasMore = GeneratePreview(content, 3)
	if !hasMore {
		t.Fatal("hasMore = false, want true")
	}
	if got != content[:3] {
		t.Errorf("fallback preview = %q, want the first 3 bytes", got)
	}
	// A newline past the halfway mark wins.
	content = strings.Repeat("a", 8) + "\n" + strings.Repeat("b", 20)
	got, hasMore = GeneratePreview(content, 12)
	if !hasMore || got != strings.Repeat("a", 8) {
		t.Errorf("newline cut = %q, want the first 8 a's", got)
	}
	// Under the limit: verbatim, no more.
	got, hasMore = GeneratePreview("short", 10)
	if hasMore || got != "short" {
		t.Errorf("short content = %q, %v, want verbatim and no more", got, hasMore)
	}
}

// The walk-back loop's `cutPoint > 0` bound is load-bearing when the content
// itself is not valid UTF-8: binary tool output (e.g. Bash `cat` of a binary
// file) can start with continuation bytes, so the loop can legitimately walk
// all the way down to cutPoint=0. Only the `cutPoint == 0 → maxBytes`
// fallback then prevents a negative slice — any loosening of the bound
// (>=, operand removal, 0→-1) panics on content[-1] / content[:-1].
func TestGeneratePreview_InvalidUTF8Start(t *testing.T) {
	t.Parallel()
	got, hasMore := GeneratePreview("\x80\x80\x80aaaa", 2)
	if !hasMore {
		t.Fatal("hasMore = false, want true")
	}
	if got != "\x80\x80" {
		t.Errorf("preview = %q, want the first 2 bytes via the maxBytes fallback", got)
	}
}

// Exec-time persistence decodes array form and quoted strings before writing.
func TestMaybePersistLargeToolResult_Decode(t *testing.T) {
	text := strings.Repeat("z", 200)
	for _, c := range []struct {
		name  string
		input string
		want  string
	}{
		{"array form", `[{"type":"text","text":"` + text + `"}]`, text},
		{"quoted string", `"` + text + `"`, text},
		{"raw bytes", text, text},
	} {
		// Isolated HOME: without it the persisted file survives across runs and
		// the exclusive-create skip makes the file-content assertion vacuous.
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			ResetDirCache()
			res := MaybePersistLargeToolResult([]byte(c.input), "Bash", 10, "tu-"+c.name, "sessdecode")
			if !res.Persisted {
				t.Fatalf("%s: not persisted: %+v", c.name, res)
			}
			data, err := os.ReadFile(res.FilePath)
			if err != nil {
				t.Fatalf("%s: read persisted file: %v", c.name, err)
			}
			if string(data) != c.want {
				t.Errorf("%s: persisted content = %q…, want the decoded text (%d bytes)", c.name, data[:min(40, len(data))], len(c.want))
			}
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(res.Output, &blocks); err != nil {
				t.Fatalf("%s: preview output is not array form: %v", c.name, err)
			}
			if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "Output too large") {
				t.Errorf("%s: preview = %+v, want the too-large message", c.name, blocks)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// storage.go
// ---------------------------------------------------------------------------

// The dir cache exists so repeated calls skip filesystem work: once a session's
// directory has been created, a later call must not recreate it after external
// removal.
func TestEnsureToolResultsDir_CachedDirNotRecreated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ResetDirCache()

	if err := EnsureToolResultsDir("cachetest"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	dir, err := GetToolResultsDir("cachetest")
	if err != nil {
		t.Fatalf("GetToolResultsDir: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if err := EnsureToolResultsDir("cachetest"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Error("cached session dir was recreated after external removal")
	}
}

func TestEnsureToolResultsDir_DirMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ResetDirCache()

	if err := EnsureToolResultsDir("modetest"); err != nil {
		t.Fatalf("EnsureToolResultsDir: %v", err)
	}
	dir, err := GetToolResultsDir("modetest")
	if err != nil {
		t.Fatalf("GetToolResultsDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// The umask on CI machines is 002/022, under which 0755 passes through
	// unchanged; the mutated permission bits would surface as a different mode.
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("dir mode = %o, want 755", got)
	}
}

// Filesystem failures must survive the wrap so callers can unwrap to *fs.PathError.
func TestEnsureToolResultsDir_MkdirErrorUnwraps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ResetDirCache()

	dir, err := GetToolResultsDir("fileblock")
	if err != nil {
		t.Fatalf("GetToolResultsDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(dir, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write fileblock: %v", err)
	}

	err = EnsureToolResultsDir("fileblock")
	if err == nil {
		t.Fatal("tool-results path is a file: error required")
	}
	if _, ok := errors.AsType[*fs.PathError](err); !ok {
		t.Errorf("error must wrap *fs.PathError, got %v", err)
	}
}

func TestPersistToolResult_FileMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ResetDirCache()

	res, err := PersistToolResult("modetest", "tu-mode", []byte("data"))
	if err != nil {
		t.Fatalf("PersistToolResult: %v", err)
	}
	info, err := os.Stat(res.Filepath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Persisted output can carry secrets; the owner-only mode is a hard contract.
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 600", got)
	}
}

// The write-failure path wraps the underlying *fs.PathError through "ensure dir".
func TestPersistToolResult_WriteErrorUnwraps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ResetDirCache()

	dir, err := GetToolResultsDir("fileblock2")
	if err != nil {
		t.Fatalf("GetToolResultsDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(dir, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write fileblock: %v", err)
	}

	_, err = PersistToolResult("fileblock2", "tu-x", []byte("data"))
	if err == nil {
		t.Fatal("tool-results path is a file: error required")
	}
	if _, ok := errors.AsType[*fs.PathError](err); !ok {
		t.Errorf("error must wrap *fs.PathError, got %v", err)
	}
}

func TestGeneratePreview_ExactLimit(t *testing.T) {
	t.Parallel()
	// Content whose length equals the limit is returned verbatim.
	got, hasMore := GeneratePreview("abcde", 5)
	if hasMore {
		t.Error("hasMore = true at exact limit, want false")
	}
	if got != "abcde" {
		t.Errorf("preview = %q, want verbatim content", got)
	}
}

func TestGeneratePreview_NewlineAtLastByte(t *testing.T) {
	t.Parallel()
	// The newline search must start at the last byte of the window.
	got, hasMore := GeneratePreview("aaaa\nbbbb", 5)
	if !hasMore {
		t.Error("hasMore = false, want true")
	}
	if got != "aaaa" {
		t.Errorf("preview = %q, want aaaa (cut at trailing newline)", got)
	}
}

func TestGeneratePreview_HalfwayBoundary(t *testing.T) {
	t.Parallel()
	// A newline exactly at maxBytes/2 does NOT win; the full window is kept.
	got, hasMore := GeneratePreview("aaaaa\nbbbbbc", 10)
	if !hasMore {
		t.Error("hasMore = false, want true")
	}
	if got != "aaaaa\nbbbb" {
		t.Errorf("preview = %q, want the full 10-byte window", got)
	}
	// A newline at 5 with limit 12 is below the halfway mark (12/2=6) and must
	// not move the cut point either.
	got, hasMore = GeneratePreview("aaaaa\nbbbbbbcc", 12)
	if !hasMore {
		t.Error("hasMore = false, want true")
	}
	if got != "aaaaa\nbbbbbb" {
		t.Errorf("preview = %q, want the full 12-byte window", got)
	}
}

// ---------------------------------------------------------------------------
// persist.go
// ---------------------------------------------------------------------------

// Output that starts with '[' but is not valid JSON is persisted as raw bytes.
func TestMaybePersistLargeToolResult_InvalidArrayRaw(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ResetDirCache()

	raw := "[not json at all " + strings.Repeat("y", 300)
	result := MaybePersistLargeToolResult([]byte(raw), "Bash", 10, "tu-badarr", "sessraw")
	if !result.Persisted {
		t.Fatalf("should persist raw oversized output: %+v", result)
	}
	data, err := os.ReadFile(result.FilePath)
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	if string(data) != raw {
		t.Errorf("persisted content = %q (%d bytes), want raw input (%d bytes)", string(data[:min(30, len(data))]), len(data), len(raw))
	}
}

// Only text blocks contribute to the joined content, and empty text blocks
// contribute nothing (no stray separators).
func TestMaybePersistLargeToolResult_ArrayPartsFilter(t *testing.T) {
	big := strings.Repeat("z", 300)
	bigJSON, _ := json.Marshal(big)
	for _, c := range []struct {
		name  string
		input string
	}{
		// A non-text block carrying a text field must not contribute. Image
		// blocks cannot be used here: they short-circuit persistence entirely.
		{"non-text block text ignored", `[{"type":"thinking","text":"caption"},{"type":"text","text":` + string(bigJSON) + `}]`},
		{"empty text no separator", `[{"type":"text","text":""},{"type":"text","text":` + string(bigJSON) + `}]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			ResetDirCache()
			result := MaybePersistLargeToolResult([]byte(c.input), "Bash", 10, "tu-parts", "sessparts")
			if !result.Persisted {
				t.Fatalf("should persist: %+v", result)
			}
			data, err := os.ReadFile(result.FilePath)
			if err != nil {
				t.Fatalf("read persisted file: %v", err)
			}
			if string(data) != big {
				t.Errorf("persisted content = %d bytes, want exactly the %d-byte text block", len(data), len(big))
			}
		})
	}
}

// A quoted string is decoded even when it is only one byte longer than a tiny
// threshold: the decode branch must not demand extra headroom.
func TestMaybePersistLargeToolResult_QuotedDecodeShort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ResetDirCache()

	result := MaybePersistLargeToolResult([]byte(`"x"`), "Bash", 2, "tu-short", "sessshort")
	if !result.Persisted {
		t.Fatalf("should persist 3-byte output over threshold 2: %+v", result)
	}
	data, err := os.ReadFile(result.FilePath)
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	if string(data) != "x" {
		t.Errorf("persisted content = %q, want decoded x", string(data))
	}
}

// A leading space before the '[' defeats the first-byte decode gate even
// though encoding/json tolerates leading whitespace: tool output carrying
// incidental leading whitespace must be persisted raw, byte for byte.
// Without the gate the same input would unmarshal and persist decoded.
func TestMaybePersistLargeToolResult_LeadingSpaceArrayRaw(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ResetDirCache()

	raw := ` [{"type":"text","text":"` + strings.Repeat("z", 300) + `"}]`
	result := MaybePersistLargeToolResult([]byte(raw), "Bash", 10, "tu-leadarr", "sessleadarr")
	if !result.Persisted {
		t.Fatalf("should persist raw oversized output: %+v", result)
	}
	data, err := os.ReadFile(result.FilePath)
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	if string(data) != raw {
		t.Errorf("persisted content = %d bytes, want the raw %d-byte input undecoded", len(data), len(raw))
	}
}

// Same leading-whitespace contract for the quoted-string gate: ` "…"` stays
// raw even though Unmarshal would accept it. The first-byte check is the
// actual contract, not a redundant guard over an always-failing decode —
// weakening it (gate removal, || instead of &&) turns the persist decoded.
func TestMaybePersistLargeToolResult_LeadingSpaceQuoteRaw(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ResetDirCache()

	raw := ` "` + strings.Repeat("z", 300) + `"`
	result := MaybePersistLargeToolResult([]byte(raw), "Bash", 10, "tu-leadquote", "sessleadquote")
	if !result.Persisted {
		t.Fatalf("should persist raw oversized output: %+v", result)
	}
	data, err := os.ReadFile(result.FilePath)
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	if string(data) != raw {
		t.Errorf("persisted content = %d bytes, want the raw %d-byte input undecoded", len(data), len(raw))
	}
}

func TestIsOnlyWhitespace(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"", true},
		{" ", true},
		{"\t", true},
		{"\n", true},
		{"\r", true},
		{" \t\r\n", true},
		{"x", false},
		{"\vx", false},
	} {
		if got := isOnlyWhitespace(c.in); got != c.want {
			t.Errorf("isOnlyWhitespace(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// budget.go
// ---------------------------------------------------------------------------

func TestContentSize_Boundaries(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		content string
		want    int
	}{
		// Non-JSON single byte falls through to the byte length.
		{"one raw byte", `x`, 1},
		// An empty block array is not candidate content: byte length applies.
		{"empty array", `[]`, 2},
		// A single text block counts only its text, not the JSON envelope.
		{"single block", `[{"type":"text","text":"hello"}]`, 5},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := ContentSize(json.RawMessage(c.content)); got != c.want {
				t.Errorf("ContentSize(%s) = %d, want %d", c.name, got, c.want)
			}
		})
	}
}

func TestIsContentAlreadyCompacted_TagBoundaries(t *testing.T) {
	t.Parallel()
	arrayForm := func(text string) json.RawMessage {
		b, _ := json.Marshal([]struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{{Type: "text", Text: text}})
		return b
	}
	// The first text block decides even when a non-text block precedes it.
	mixedBlocks, err := json.Marshal([]struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{{Type: "image"}, {Type: "text", Text: "<persisted-output>x</persisted-output>"}})
	if err != nil {
		t.Fatalf("marshal mixed blocks: %v", err)
	}
	if !IsContentAlreadyCompacted(json.RawMessage(mixedBlocks)) {
		t.Error("array with leading image block and compacted text should be detected")
	}
	// Text exactly as long as the tag (the tag itself) is compacted content.
	if !IsContentAlreadyCompacted(arrayForm(PersistedOutputTag)) {
		t.Error("array text exactly the tag should be detected as compacted")
	}
	if !IsContentAlreadyCompacted(json.RawMessage(`"` + PersistedOutputTag + `"`)) {
		t.Error("string exactly the tag should be detected as compacted")
	}
	// Longer text that merely reaches tag length must not match.
	if IsContentAlreadyCompacted(arrayForm(strings.Repeat("A", len(PersistedOutputTag)))) {
		t.Error("array text of tag length without the tag must not be detected")
	}
}

// encoding/json partially fills the destination on a mid-array element error:
// elements before the failing one stay decoded while Unmarshal still returns
// an error. The early return on that error is therefore load-bearing —
// without it the block loop below would read the partially decoded blocks
// and classify on them.
func TestIsContentAlreadyCompacted_UnmarshalErrorPartialFill(t *testing.T) {
	t.Parallel()
	if IsContentAlreadyCompacted(json.RawMessage(`[{"type":"text","text":"<persisted-output>x"},{"type":123}]`)) {
		t.Error("array with a mid-element type error must not be treated as compacted")
	}
}

// A skip set entry for a different tool must not suppress replacement.
func TestEnforceToolResultBudget_SkipSetNonMatching(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ResetDirCache()

	state := NewContentReplacementState()
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{
			{Type: "tool_use", ID: "tu-1", Name: "Bash"},
		}},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tu-1", Content: json.RawMessage(mustMarshalStr(strings.Repeat("x", MaxToolResultsPerMessageChars+1000)))},
		}},
	}
	_, records := EnforceToolResultBudget(msgs, state, "sess-skip2", map[string]bool{"Read": true})
	if len(records) != 1 || records[0].ToolUseID != "tu-1" {
		t.Fatalf("records = %+v, want exactly one replacement for tu-1", records)
	}
}

// Replacement records carry the kind and the exact preview string so a resumed
// session can rebuild byte-identical state.
func TestEnforceToolResultBudget_RecordFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ResetDirCache()

	state := NewContentReplacementState()
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{
			{Type: "tool_use", ID: "tu-1", Name: "Bash"},
		}},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tu-1", Content: json.RawMessage(mustMarshalStr(strings.Repeat("x", MaxToolResultsPerMessageChars+1000)))},
		}},
	}
	_, records := EnforceToolResultBudget(msgs, state, "sess-rec", nil)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Kind != "tool-result" {
		t.Errorf("Kind = %q, want tool-result", records[0].Kind)
	}
	if !strings.Contains(records[0].Replacement, PersistedOutputTag) {
		t.Errorf("Replacement = %q, want preview containing the persisted tag", records[0].Replacement)
	}
	if state.Replacements["tu-1"] != records[0].Replacement {
		t.Errorf("state replacement %q differs from record %q", state.Replacements["tu-1"], records[0].Replacement)
	}
}

// The second turn over the same messages must re-apply the recorded preview
// byte-identically instead of leaving the oversized content in place.
func TestEnforceToolResultBudget_SecondTurnReapply(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ResetDirCache()

	state := NewContentReplacementState()
	big := json.RawMessage(mustMarshalStr(strings.Repeat("x", MaxToolResultsPerMessageChars+1000)))
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{
			{Type: "tool_use", ID: "tu-1", Name: "Bash"},
		}},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tu-1", Content: big},
		}},
	}
	first, records := EnforceToolResultBudget(msgs, state, "sess-twice", nil)
	if len(records) != 1 {
		t.Fatalf("first turn: records = %d, want 1", len(records))
	}
	second, records2 := EnforceToolResultBudget(msgs, state, "sess-twice", nil)
	if len(records2) != 0 {
		t.Errorf("second turn: records = %d, want 0 (pure re-apply)", len(records2))
	}
	if string(second[1].Content[0].Content) != string(first[1].Content[0].Content) {
		t.Error("second turn must re-apply the byte-identical preview")
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(second[1].Content[0].Content, &blocks); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, PersistedOutputTag) {
		t.Errorf("second turn content lost the preview, got %+v", blocks)
	}
}

// Candidates already marked in SeenIDs (with no replacement) are frozen: their
// fate is sealed and the state must record that.
func TestEnforceToolResultBudget_ReapplyMarksSeen(t *testing.T) {
	state := NewContentReplacementState()
	state.Replacements["tu-1"] = "cached preview"
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1"},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tu-1", Content: json.RawMessage(`"original"`)},
		}},
	}
	_, records := EnforceToolResultBudget(msgs, state, "sess", nil)
	if len(records) != 0 {
		t.Fatalf("records = %d, want 0 for pure re-apply", len(records))
	}
	if !state.SeenIDs["tu-1"] {
		t.Error("re-applied candidate must be marked seen (frozen across turns)")
	}
}

// A group that needs no work must not stop later groups from being processed.
func TestEnforceToolResultBudget_LaterGroupsProcessed(t *testing.T) {
	big := json.RawMessage(mustMarshalStr(strings.Repeat("x", MaxToolResultsPerMessageChars+1000)))
	small := json.RawMessage(`"tiny"`)

	t.Run("reapply group first", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		ResetDirCache()
		state := NewContentReplacementState()
		state.Replacements["tu-1"] = "cached"
		msgs := []BudgetMessage{
			{Role: "assistant", ID: "asst-1"},
			{Role: "user", Content: []BudgetBlock{{Type: "tool_result", ToolUseID: "tu-1", Content: small}}},
			{Role: "assistant", ID: "asst-2", Content: []BudgetBlock{{Type: "tool_use", ID: "tu-2", Name: "Bash"}}},
			{Role: "user", Content: []BudgetBlock{{Type: "tool_result", ToolUseID: "tu-2", Content: big}}},
		}
		_, records := EnforceToolResultBudget(msgs, state, "sess-g1", nil)
		if len(records) != 1 || records[0].ToolUseID != "tu-2" {
			t.Fatalf("records = %+v, want one replacement for tu-2 after the re-apply group", records)
		}
	})
	t.Run("under limit group first", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		ResetDirCache()
		state := NewContentReplacementState()
		msgs := []BudgetMessage{
			{Role: "assistant", ID: "asst-1"},
			{Role: "user", Content: []BudgetBlock{{Type: "tool_result", ToolUseID: "tu-1", Content: small}}},
			{Role: "assistant", ID: "asst-2", Content: []BudgetBlock{{Type: "tool_use", ID: "tu-2", Name: "Bash"}}},
			{Role: "user", Content: []BudgetBlock{{Type: "tool_result", ToolUseID: "tu-2", Content: big}}},
		}
		_, records := EnforceToolResultBudget(msgs, state, "sess-g2", nil)
		if len(records) != 1 || records[0].ToolUseID != "tu-2" {
			t.Fatalf("records = %+v, want one replacement for tu-2 after the under-limit group", records)
		}
	})
}

// A group totaling exactly the limit stays untouched; the budget only reacts
// to strictly-over totals.
func TestEnforceToolResultBudget_ExactLimitNoReplace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ResetDirCache()

	half := json.RawMessage(mustMarshalStr(strings.Repeat("x", MaxToolResultsPerMessageChars/2)))
	state := NewContentReplacementState()
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{{Type: "tool_use", ID: "tu-1", Name: "Bash"}}},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tu-1", Content: half},
			{Type: "tool_result", ToolUseID: "tu-2", Content: half},
		}},
	}
	result, records := EnforceToolResultBudget(msgs, state, "sess-exact", nil)
	if len(records) != 0 {
		t.Fatalf("records = %d, want 0 at exactly the limit", len(records))
	}
	if string(result[1].Content[0].Content) != string(half) {
		t.Error("content at exact limit must stay unchanged")
	}
}

// Blocks that individually fit can still overflow as a group; selection then
// picks largest-first until the remaining total fits.
func TestEnforceToolResultBudget_SumOverflow(t *testing.T) {
	third := json.RawMessage(mustMarshalStr(strings.Repeat("x", 70000)))

	t.Run("three fresh blocks", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		ResetDirCache()
		state := NewContentReplacementState()
		msgs := []BudgetMessage{
			{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{{Type: "tool_use", ID: "tu-1", Name: "Bash"}}},
			{Role: "user", Content: []BudgetBlock{
				{Type: "tool_result", ToolUseID: "tu-1", Content: third},
				{Type: "tool_result", ToolUseID: "tu-2", Content: third},
				{Type: "tool_result", ToolUseID: "tu-3", Content: third},
			}},
		}
		_, records := EnforceToolResultBudget(msgs, state, "sess-sum1", nil)
		if len(records) != 1 {
			t.Fatalf("records = %d, want 1 (one 70k block brings 210k under the limit)", len(records))
		}
	})
	t.Run("frozen blocks counted", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		ResetDirCache()
		state := NewContentReplacementState()
		// The three big blocks were seen on a prior turn without replacement:
		// they are frozen, and only the fresh tail block can be selected to
		// shrink the total.
		state.SeenIDs["tu-1"] = true
		state.SeenIDs["tu-2"] = true
		state.SeenIDs["tu-3"] = true
		msgs := []BudgetMessage{
			{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{{Type: "tool_use", ID: "tu-4", Name: "Bash"}}},
			{Role: "user", Content: []BudgetBlock{
				{Type: "tool_result", ToolUseID: "tu-1", Content: third},
				{Type: "tool_result", ToolUseID: "tu-2", Content: third},
				{Type: "tool_result", ToolUseID: "tu-3", Content: third},
				{Type: "tool_result", ToolUseID: "tu-4", Content: json.RawMessage(`"tail"`)},
			}},
		}
		_, records := EnforceToolResultBudget(msgs, state, "sess-sum2", nil)
		if len(records) != 1 || records[0].ToolUseID != "tu-4" {
			t.Fatalf("records = %+v, want exactly the fresh tail block selected", records)
		}
	})
}

// One candidate whose persist fails must not block the rest of the batch.
func TestEnforceToolResultBudget_PersistFailureContinues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ResetDirCache()

	// A tool_use_id longer than NAME_MAX makes the first candidate's file
	// creation fail with a non-IsExist error, while the second persists fine.
	longID := strings.Repeat("a", 300)
	state := NewContentReplacementState()
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{
			{Type: "tool_use", ID: longID, Name: "Bash"},
			{Type: "tool_use", ID: "good-id", Name: "Bash"},
		}},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: longID, Content: json.RawMessage(mustMarshalStr(strings.Repeat("x", 250000)))},
			{Type: "tool_result", ToolUseID: "good-id", Content: json.RawMessage(mustMarshalStr(strings.Repeat("y", 210000)))},
		}},
	}

	_, records := EnforceToolResultBudget(msgs, state, "sessfail", nil)
	if len(records) != 1 || records[0].ToolUseID != "good-id" {
		t.Fatalf("records = %+v, want exactly good-id persisted despite the failed sibling", records)
	}
	if !state.SeenIDs[longID] {
		t.Error("failed candidate must still be marked seen (treated as frozen)")
	}
}

// Selection stops as soon as the remaining total reaches the limit, and the
// decrement is what tracks progress.
func TestSelectFreshToReplace_Boundaries(t *testing.T) {
	t.Parallel()
	// 3x50 over limit 100: selecting one 50 leaves exactly 100 → stop.
	fresh := []toolResultCandidate{
		{toolUseID: "a", size: 50},
		{toolUseID: "b", size: 50},
		{toolUseID: "c", size: 50},
	}
	selected := selectFreshToReplace(fresh, 0, 100)
	if len(selected) != 1 {
		t.Errorf("selected %d candidates, want 1 (remaining hits limit exactly)", len(selected))
	}
	// A single huge block dominates: selecting it must finish the job even
	// though a small block follows.
	fresh = []toolResultCandidate{
		{toolUseID: "big", size: 250},
		{toolUseID: "small", size: 50},
	}
	selected = selectFreshToReplace(fresh, 0, 100)
	if len(selected) != 1 || selected[0].toolUseID != "big" {
		t.Errorf("selected = %+v, want exactly [big]", selected)
	}
}

// Only NEW assistant message IDs split candidate groups; fragments with no ID
// belong to the current group.
func TestCollectCandidatesByMessage_EmptyAssistantID(t *testing.T) {
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1"},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`"c1"`)},
		}},
		{Role: "assistant"}, // streaming fragment without ID — no boundary
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-2", Content: json.RawMessage(`"c2"`)},
		}},
	}
	groups := collectCandidatesByMessage(msgs)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1 (empty assistant ID must not split)", len(groups))
	}
	if len(groups[0]) != 2 {
		t.Errorf("group has %d candidates, want 2", len(groups[0]))
	}
}

func TestCollectCandidatesFromMessage_TypeGate(t *testing.T) {
	t.Parallel()
	// Non-tool_result blocks are never candidates even when they carry content.
	msg := BudgetMessage{Role: "user", Content: []BudgetBlock{
		{Type: "text", Content: json.RawMessage(`"not a result"`)},
	}}
	if got := collectCandidatesFromMessage(msg); len(got) != 0 {
		t.Errorf("candidates = %d, want 0 for text blocks", len(got))
	}
	// The scan must continue past non-candidate blocks.
	msg = BudgetMessage{Role: "user", Content: []BudgetBlock{
		{Type: "tool_use", ID: "tu-1", Name: "Bash"},
		{Type: "tool_result", ToolUseID: "tu-1", Content: json.RawMessage(`"result"`)},
	}}
	if got := collectCandidatesFromMessage(msg); len(got) != 1 || got[0].toolUseID != "tu-1" {
		t.Errorf("candidates = %+v, want [tu-1] after the tool_use block", got)
	}
}

// Replacement must find targets regardless of block order and must preserve
// everything else in the message slice.
func TestReplaceToolResultContents_MixedBlocks(t *testing.T) {
	t.Parallel()
	replacementMap := map[string]string{"tu-1": "PREVIEW"}

	// Target not in the first block of the message.
	msgs := []BudgetMessage{
		{Role: "user", Content: []BudgetBlock{
			{Type: "text", Content: json.RawMessage(`"keep me"`)},
			{Type: "tool_result", ToolUseID: "tu-1", Content: json.RawMessage(`"original"`)},
		}},
	}
	out := replaceToolResultContents(msgs, replacementMap)
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(out[0].Content[1].Content, &blocks); err != nil {
		t.Fatalf("unmarshal replaced block: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Text != "PREVIEW" {
		t.Errorf("target block = %s, want replaced preview", string(out[0].Content[1].Content))
	}
	if string(out[0].Content[0].Content) != `"keep me"` {
		t.Errorf("non-target block changed: %s", string(out[0].Content[0].Content))
	}

	// Untouched messages pass through intact.
	msgs = []BudgetMessage{
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tu-1", Content: json.RawMessage(`"original"`)},
		}},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tu-9", Content: json.RawMessage(`"untouched"`)},
		}},
	}
	out = replaceToolResultContents(msgs, replacementMap)
	if string(out[1].Content[0].Content) != `"untouched"` {
		t.Errorf("untouched message changed: %+v", out[1])
	}
}

// Records are scanned in order; a foreign-kind record must not stop the scan
// for the valid ones behind it.
func TestReconstructContentReplacementState_RecordOrder(t *testing.T) {
	msgs := []BudgetMessage{
		{Role: "assistant", ID: "asst-1"},
		{Role: "user", Content: []BudgetBlock{
			{Type: "tool_result", ToolUseID: "tr-1", Content: json.RawMessage(`"content"`)},
		}},
	}
	records := []ContentReplacementRecord{
		{Kind: "other", ToolUseID: "tr-1", Replacement: "bogus"},
		{Kind: "tool-result", ToolUseID: "tr-1", Replacement: "preview"},
	}
	state := ReconstructContentReplacementState(msgs, records, nil)
	if state.Replacements["tr-1"] != "preview" {
		t.Errorf("replacement = %q, want preview from the record after the foreign-kind one", state.Replacements["tr-1"])
	}
}

// Only tool_use blocks map ids to tool names; other block shapes must not
// grant or withhold skip exemptions.
func TestBuildToolNameMap_TypeGate(t *testing.T) {
	big := json.RawMessage(mustMarshalStr(strings.Repeat("x", MaxToolResultsPerMessageChars+1000)))

	t.Run("text block with id is not a tool_use", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		ResetDirCache()
		msgs := []BudgetMessage{
			{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{
				{Type: "text", ID: "tu-1", Name: "Read"},
			}},
			{Role: "user", Content: []BudgetBlock{
				{Type: "tool_result", ToolUseID: "tu-1", Content: big},
			}},
		}
		_, records := EnforceToolResultBudget(msgs, NewContentReplacementState(), "sess-gate1", map[string]bool{"Read": true})
		if len(records) != 1 {
			t.Fatalf("records = %d, want 1: a text block must not map tu-1 to Read", len(records))
		}
	})
	t.Run("tool use with empty id is not mapped", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		ResetDirCache()
		msgs := []BudgetMessage{
			{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{
				{Type: "tool_use", ID: "", Name: "Read"},
				{Type: "tool_use", ID: "tu-1", Name: "Bash"},
			}},
			{Role: "user", Content: []BudgetBlock{
				{Type: "tool_result", ToolUseID: "tu-1", Content: big},
			}},
		}
		_, records := EnforceToolResultBudget(msgs, NewContentReplacementState(), "sess-gate2", map[string]bool{"Read": true})
		if len(records) != 1 || records[0].ToolUseID != "tu-1" {
			t.Fatalf("records = %+v, want tu-1 replaced: empty-id tool_use must not match", records)
		}
	})
	// A tool_result with an empty id looks up the empty-id tool_use: an id-less
	// tool_use block must not map "" to its name.
	t.Run("empty id result not exempted by empty id tool use", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		ResetDirCache()
		msgs := []BudgetMessage{
			{Role: "assistant", ID: "asst-1", Content: []BudgetBlock{
				{Type: "tool_use", ID: "", Name: "Read"},
			}},
			{Role: "user", Content: []BudgetBlock{
				{Type: "tool_result", ToolUseID: "", Content: big},
			}},
		}
		_, records := EnforceToolResultBudget(msgs, NewContentReplacementState(), "sess-gate3", map[string]bool{"Read": true})
		if len(records) != 1 || records[0].ToolUseID != "" {
			t.Fatalf("records = %+v, want the empty-id result replaced despite the Read name", records)
		}
	})
}

// ---------------------------------------------------------------------------
// cleanup.go
// ---------------------------------------------------------------------------

// The scan must survive entries it cannot clean and keep going.
func TestCleanupOldSessions_ScanContinuesPast(t *testing.T) {
	t.Run("file entry before cleanable dir", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		ResetDirCache()
		sessionsDir := filepath.Join(mustHome(t), ".gbot", "sessions")
		if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
			t.Fatalf("mkdir sessions: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sessionsDir, "not-a-dir.txt"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write file entry: %v", err)
		}
		oldDir := filepath.Join(sessionsDir, "old-session", ToolResultsSubdir)
		if err := os.MkdirAll(oldDir, 0o755); err != nil {
			t.Fatalf("mkdir old session: %v", err)
		}
		oldTime := time.Now().Add(-48 * time.Hour) // REAL-TIME: filesystem mtime comparison
		if err := os.Chtimes(oldDir, oldTime, oldTime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		cleaned, err := CleanupOldSessions(time.Now().Add(-24 * time.Hour)) // REAL-TIME: filesystem mtime comparison
		if err != nil {
			t.Fatalf("CleanupOldSessions: %v", err)
		}
		if cleaned != 1 {
			t.Errorf("cleaned = %d, want 1 (scan must continue past the file entry)", cleaned)
		}
	})
	t.Run("session without tool-results before cleanable dir", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		ResetDirCache()
		sessionsDir := filepath.Join(mustHome(t), ".gbot", "sessions")
		if err := os.MkdirAll(filepath.Join(sessionsDir, "bare-session"), 0o755); err != nil {
			t.Fatalf("mkdir bare session: %v", err)
		}
		oldDir := filepath.Join(sessionsDir, "old-session2", ToolResultsSubdir)
		if err := os.MkdirAll(oldDir, 0o755); err != nil {
			t.Fatalf("mkdir old session: %v", err)
		}
		oldTime := time.Now().Add(-48 * time.Hour) // REAL-TIME: filesystem mtime comparison
		if err := os.Chtimes(oldDir, oldTime, oldTime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		cleaned, err := CleanupOldSessions(time.Now().Add(-24 * time.Hour)) // REAL-TIME: filesystem mtime comparison
		if err != nil {
			t.Fatalf("CleanupOldSessions: %v", err)
		}
		if cleaned != 1 {
			t.Errorf("cleaned = %d, want 1 (scan must continue past the bare session)", cleaned)
		}
	})
}

// A tool-results directory we cannot empty must surface as an error.
func TestCleanupSession_RemoveFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure does not trigger for root")
	}
	t.Setenv("HOME", t.TempDir())
	ResetDirCache()

	dir, err := GetToolResultsDir("rmfail")
	if err != nil {
		t.Fatalf("GetToolResultsDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o755) }()

	if err := CleanupSession("rmfail"); err == nil {
		t.Fatal("expected error when tool-results cannot be emptied")
	} else if !strings.Contains(err.Error(), ToolResultsSubdir) {
		t.Errorf("error should point at the tool-results path, got: %v", err)
	}
}

func mustHome(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	return home
}
