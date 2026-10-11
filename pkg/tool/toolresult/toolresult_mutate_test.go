package toolresult

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageConstants(t *testing.T) {
	t.Parallel()
	if MaxPersistSizeBytes != 64*1024*1024 {
		t.Errorf("MaxPersistSizeBytes = %d, want 64 MiB", MaxPersistSizeBytes)
	}
	if PreviewSizeBytes != 2000 {
		t.Errorf("PreviewSizeBytes = %d, want 2000", PreviewSizeBytes)
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
	} {
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
	}
}
