package lsptool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
)

func TestHover_ContentsValue(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/hover" {
				return map[string]any{
					"contents": map[string]any{"kind": "markdown", "value": "func foo() bar"},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	res, err := hover(context.Background(), c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{})
	if err != nil {
		t.Fatalf("hover: %v", err)
	}
	if got := fmt.Sprintf("%v", res.Data); got != "func foo() bar" {
		t.Errorf("hover = %q", got)
	}
}

func TestHover_Null(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/hover" {
				return nil, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	res, err := hover(context.Background(), c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{})
	if err != nil {
		t.Fatalf("hover: %v", err)
	}
	if got := fmt.Sprintf("%v", res.Data); got != "No hover information" {
		t.Errorf("hover null = %q", got)
	}
}

func TestHover_Cancelled(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/hover" {
				return holdResponse{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := hover(ctx, c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{})
	if err == nil || !strings.HasPrefix(err.Error(), "hover:") {
		t.Fatalf("error = %v, want prefix hover:", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("no unwrap to context.Canceled: %v", err)
	}
}
