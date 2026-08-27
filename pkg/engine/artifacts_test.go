package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/filewrite"
	"github.com/liuy/gbot/pkg/types"
)

// drainUntilQueryEnd pumps the event channel until EventQueryEnd.
func drainUntilQueryEnd(t *testing.T, eventCh chan types.QueryEvent) {
	t.Helper()
	for {
		select {
		case evt := <-eventCh:
			if evt.Type == types.EventQueryEnd {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for EventQueryEnd")
		}
	}
}

// TestChain_ArtifactAbsolutePathWrite verifies the no-mechanism contract:
// the model writes <projectspace>/artifacts/game.html as a plain absolute
// path and the file lands exactly there. This guards the removal of the
// artifacts/ prefix redirect — absolute paths must keep resolving as-is
// through the engine → ToolUseContext → Write chain.
func TestChain_ArtifactAbsolutePathWrite(t *testing.T) {
	tmp := t.TempDir()
	projectDir := t.TempDir()
	artifactPath := filepath.Join(projectDir, "artifacts", "game.html")

	eventCh := make(chan types.QueryEvent, 50)
	dispatcher := &chanDispatcher{ch: eventCh}

	writeInput := fmt.Sprintf(`{"file_path":%q,"content":"<html>1</html>"}`, artifactPath)
	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test", "tool_1", "Write", writeInput), nil)
	mp.addResponse(textStreamEvents("test", "done"), nil)

	eng := New(&Params{
		Provider: mp,
		ToolsProvider: func() map[string]tool.Tool {
			return map[string]tool.Tool{"Write": filewrite.New()}
		},
		Model:      "test",
		Dispatcher: dispatcher,
		WorkingDir: tmp,
	})
	defer eng.Close()
	eng.SetStore(newTestStore(t), projectDir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	eng.Query(ctx, "create a game", "")
	drainUntilQueryEnd(t, eventCh)

	data, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read artifact %s: %v", artifactPath, err)
	}
	if string(data) != "<html>1</html>" {
		t.Errorf("artifact content = %q, want %q", string(data), "<html>1</html>")
	}
	if _, err := os.Stat(filepath.Join(tmp, "artifacts")); !os.IsNotExist(err) {
		t.Errorf("WorkingDir must stay untouched: %s (stat err=%v)", filepath.Join(tmp, "artifacts"), err)
	}
}
