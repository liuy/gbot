// Package glob implements the Glob tool for file pattern matching.
//
// Source reference: tools/GlobTool/GlobTool.ts
// 1:1 port from the TypeScript source.
package glob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/utils/proc"
)

// Maximum number of files returned by a single glob call.
// Source: GlobTool.ts — globLimits.maxResults.
const MaxGlobResults = 100

// Source: ripgrep.ts:80 — MAX_BUFFER_SIZE.
const maxBufferSize = 20_000_000 // 20MB

// Source: ripgrep.ts:130 — default timeout (20s; 60s on WSL, omitted for simplicity).
const defaultGlobTimeout = 20 * time.Second

// Input is the glob tool input schema.
// Source: GlobTool.ts — Zod schema for glob input.
type Input struct {
	Pattern string `json:"pattern" validate:"required"`
	Path    string `json:"path,omitempty"`
}

// Output is the glob tool output.
// Source: GlobTool.ts — tool result data.
type Output struct {
	Files      []string `json:"filenames"`
	Count      int      `json:"numFiles"`
	DurationMs int64    `json:"durationMs"`
	Truncated  bool     `json:"truncated"`
}

// New creates the Glob tool.
// Source: tools/GlobTool/GlobTool.ts
func New() tool.Tool {
	schema := json.RawMessage(`{
			"type": "object",
			"required": ["pattern"],
			"properties": {
				"pattern": {
					"type": "string",
					"description": "The glob pattern to match files against (e.g. '**/*.go', 'src/**/*.ts')."
				},
				"path": {
					"type": "string",
					"description": "The directory to search in. Defaults to current working directory."
				}
			}
		}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:        "Glob",
		Aliases_:     []string{"glob"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Find files matching a glob pattern", nil
			}
			return in.Pattern, nil
		},
		Call_: Execute,
		IsReadOnly_: func(json.RawMessage) bool {
			return true // glob is always read-only
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return true // glob is concurrency-safe
		},
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 100000,
		Prompt_:            globPrompt(),
		RenderResult_: func(data any) string {
			out, ok := data.(*Output)
			if !ok {
				b, _ := json.Marshal(data)
				return string(b)
			}
			if len(out.Files) == 0 {
				return ""
			}
			return strings.Join(out.Files, "\n")
		},
		DecodeResult_: func(raw json.RawMessage) (any, error) {
			text, err := tool.UnmarshalSingleBlock(raw)
			if err != nil {
				return nil, err
			}
			var o Output
			if err := json.Unmarshal([]byte(text), &o); err != nil {
				return nil, err
			}
			return &o, nil
		},
		IsSearchOrRead_: func(json.RawMessage) tool.SearchReadKind {
			return tool.SearchReadKind{IsSearch: true}
		},
	})
}

// Execute finds files matching a glob pattern using ripgrep.
// Source: GlobTool.ts:call() + utils/glob.ts:glob() + utils/ripgrep.ts:ripGrepRaw()
func Execute(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	start := time.Now()

	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	// Determine base path
	basePath := in.Path
	if basePath == "" {
		if tctx != nil && tctx.WorkingDir != "" {
			basePath = tctx.WorkingDir
		} else {
			basePath, _ = os.Getwd()
		}
	}

	// Verify base path exists
	info, err := os.Stat(basePath)
	if err != nil {
		return nil, fmt.Errorf("path does not exist: %s", basePath)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", basePath)
	}

	matches, err := rgGlob(ctx, basePath, in.Pattern)
	if err != nil {
		return nil, err
	}

	// TS: GlobTool.ts:166 — relativize paths to save tokens
	for i, p := range matches {
		if rel, relErr := filepath.Rel(basePath, p); relErr == nil {
			matches[i] = rel
		}
	}

	// Apply truncation limit (TS: glob.ts:126-127 — offset + limit)
	truncated := false
	if len(matches) > MaxGlobResults {
		matches = matches[:MaxGlobResults]
		truncated = true
	}

	return &tool.ToolResult{Data: &Output{
		Files:      matches,
		Count:      len(matches),
		DurationMs: time.Since(start).Milliseconds(),
		Truncated:  truncated,
	}}, nil
}

// rgGlob runs ripgrep to list files matching a glob pattern.
// Source: utils/glob.ts:66-130 + utils/ripgrep.ts:108-232
func rgGlob(ctx context.Context, basePath, pattern string) ([]string, error) {
	// TS: glob.ts:100-107 — rg args
	args := []string{
		"--files",
		"--glob", pattern,
		"--sort=modified",
		"--no-ignore",
		"--hidden",
		basePath,
	}

	lines, err := rgRaw(ctx, args)
	if err != nil {
		// TS: ripgrep.ts:394-409 — EAGAIN retry with single-threaded mode
		if isEagainError(err) {
			slog.Info("glob:rg_eagain_retry", "path", basePath)
			argsEagain := []string{
				"-j", "1",
				"--files",
				"--glob", pattern,
				"--sort=modified",
				"--no-ignore",
				"--hidden",
				basePath,
			}
			lines, err = rgRaw(ctx, argsEagain)
			if err != nil {
				return nil, fmt.Errorf("ripgrep error (single-threaded retry): %w", err)
			}
		} else {
			return nil, err
		}
	}

	return lines, nil
}

// rgError wraps ripgrep stderr for EAGAIN detection.
// Source: ripgrep.ts:87-91
type rgError struct {
	stderr  string
	wrapped error
}

func (e *rgError) Error() string { return e.wrapped.Error() }
func (e *rgError) Unwrap() error { return e.wrapped }

// isEagainError checks if the error is EAGAIN (resource temporarily unavailable).
// Source: ripgrep.ts:87-91
func isEagainError(err error) bool {
	rge, ok := errors.AsType[*rgError](err)
	if !ok {
		return false
	}
	return strings.Contains(rge.stderr, "os error 11") ||
		strings.Contains(rge.stderr, "Resource temporarily unavailable")
}

// rgRaw executes ripgrep with the given args and returns parsed output lines.
// Uses pipe-based reading with 20MB buffer limit and returns partial results on timeout.
// Source: ripgrep.ts:108-232
func rgRaw(ctx context.Context, args []string) ([]string, error) {
	// TS: ripgrep.ts:130-133 — default 20s timeout
	rgCtx, cancel := context.WithTimeout(ctx, defaultGlobTimeout)
	defer cancel()

	rgBin := os.Getenv("GBOT_RG_PATH")
	if rgBin == "" {
		rgBin = "rg"
	}
	cmd := exec.CommandContext(rgCtx, rgBin, args...)
	proc.HideWindow(cmd)

	// TS: ripgrep.ts:149-156 — capture stdout with 20MB buffer limit
	var stdout, stderr bytes.Buffer
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ripgrep pipe: %w", err)
	}
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, &rgError{stderr: stderr.String(), wrapped: fmt.Errorf("ripgrep: %w", err)}
	}

	// Read stdout with 20MB limit (TS: ripgrep.ts:149-156)
	limited := io.LimitReader(pipe, maxBufferSize+1)
	if _, copyErr := io.Copy(&stdout, limited); copyErr != nil {
		// If not a timeout, it's a real read error
		if rgCtx.Err() == nil {
			return nil, fmt.Errorf("ripgrep read: %w", copyErr)
		}
		// Timeout during read — fall through to partial result handling
	}

	waitErr := cmd.Wait()

	isTimeout := rgCtx.Err() != nil
	lines := parseOutput(stdout.String())

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) && exitErr.ExitCode() == 1 && !isTimeout {
			return []string{}, nil
		}

		// TS: ripgrep.ts:421-431 — return partial results on timeout
		if isTimeout && len(lines) > 0 {
			// TS: ripgrep.ts:428 — drop last line (may be incomplete)
			lines = lines[:len(lines)-1]
			if len(lines) > 0 {
				slog.Warn("glob:rg_timeout_partial", "results", len(lines))
				return lines, nil
			}
		}

		if isTimeout {
			return nil, &rgError{
				stderr:  stderr.String(),
				wrapped: fmt.Errorf("ripgrep timed out after %s", defaultGlobTimeout),
			}
		}

		return nil, &rgError{stderr: stderr.String(), wrapped: fmt.Errorf("ripgrep error: %w", waitErr)}
	}

	return lines, nil
}

// parseOutput splits rg stdout into lines, filtering empty and trimming \r.
// Source: ripgrep.ts:366-374
func parseOutput(out string) []string {
	if strings.TrimSpace(out) == "" {
		return []string{}
	}
	raw := strings.Split(strings.TrimRight(out, "\n"), "\n")
	filtered := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimRight(line, "\r")
		if line != "" {
			filtered = append(filtered, line)
		}
	}
	return filtered
}
