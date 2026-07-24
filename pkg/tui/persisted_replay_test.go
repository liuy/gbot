package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/fileedit"
	"github.com/liuy/gbot/pkg/types"
)

// TestRenderToolOutput_PersistedOutputReplayRenderOutput verifies that a tool
// result persisted to disk (via MaybePersistLargeToolResult) replays correctly
// through renderToolOutput → renderViaTool → DecodeResult + RenderResult.
// The persisted file contains raw JSON (no duration prefix since we removed it).
func TestRenderToolOutput_PersistedOutputReplayRenderOutput(t *testing.T) {
	tmpDir := t.TempDir()

	editData := &fileedit.Output{
		FilePath:  "/tmp/test.go",
		OldString: "old",
		NewString: "new",
	}
	rawJSON, _ := json.Marshal(editData)
	persistedContent := string(rawJSON)

	persistedPath := filepath.Join(tmpDir, "tool_result.json")
	if err := os.WriteFile(persistedPath, []byte(persistedContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	preview := persistedContent
	if len(preview) > 200 {
		preview = preview[:200]
	}
	persistedTag := "<persisted-output>\nOutput too large. Full output saved to: " + persistedPath + "\nPreview (first 2.0KB):\n" + preview

	toolResultContent, _ := json.Marshal([]types.ContentBlock{
		{Type: types.ContentTypeText, Text: persistedTag},
	})

	editTool := fileedit.New()
	tools := map[string]tool.Tool{"Edit": editTool}

	output := renderToolOutput("Edit", toolResultContent, tools)

	if strings.Contains(output, `"filePath"`) {
		t.Errorf("output contains raw JSON keys — renderViaTool failed:\n%s", output)
	}
	if !strings.Contains(output, "old") || !strings.Contains(output, "new") {
		t.Errorf("output missing diff content:\n%s", output)
	}
}
