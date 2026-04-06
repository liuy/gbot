package glob

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/user/gbot/pkg/types"
)

func TestExecute_GetwdFallback(t *testing.T) {
	t.Parallel()

	// No tctx, no path -> falls back to os.Getwd()
	input := json.RawMessage(`{"pattern":"*.go"}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		// May error if cwd has no .go files, but that's fine — just no panic
		t.Logf("Execute() returned error (expected in some cwd): %v", err)
		return
	}

	output := result.Data.(*Output)
	if output == nil {
		t.Error("Output is nil")
	}
}

func TestExecute_InvalidGlobPattern(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tctx := &types.ToolUseContext{WorkingDir: dir}

	// Use an invalid glob pattern that doublestar rejects
	input := json.RawMessage(`{"pattern":"[invalid"}`)
	_, err := Execute(context.Background(), input, tctx)
	if err == nil {
		t.Error("Execute() error = nil, want error for invalid glob pattern")
	}
}
