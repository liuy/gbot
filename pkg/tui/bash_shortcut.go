package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/bash"
)

// isBashShortcut returns true when input starts with '!' AND has non-empty
// content after it. A bare "!" or "!  " with only whitespace after is NOT a
// shortcut — those fall through to the normal path.
func isBashShortcut(text string) bool {
	if !strings.HasPrefix(text, "!") {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(text, "!")) != ""
}

// stripBangPrefix removes the leading '!' and trims surrounding whitespace.
func stripBangPrefix(text string) string {
	return strings.TrimSpace(strings.TrimPrefix(text, "!"))
}

// runBashShortcut creates a virtual Bash tool call and runs the command with
// streaming output. The tool call block is created synchronously so the user
// sees it immediately; bash output streams in via appCh messages (same path
// as model-initiated Bash calls).
func (a *App) runBashShortcut(command string) tea.Cmd {
	toolID := "bash-shortcut-" + uuid.New().String()[:8]

	// Add user message and create the tool block.
	a.repl.AddUserMessage("!" + command)
	a.repl.StartQuery()
	a.repl.PendingToolStarted(toolID, "Bash", command, command, tool.SearchReadKind{})
	a.status.SetStreaming(true)
	a.spinner.Start()
	a.markViewportDirty()

	workingDir := a.projectDir
	return func() tea.Msg {
		inputJSON, err := json.Marshal(bash.Input{Command: command})
		if err != nil {
			return toolEndMsg{
				ToolUseID: toolID,
				Output:    fmt.Sprintf("marshal bash input: %v", err),
				IsError:   true,
			}
		}

		// Accumulate output in the goroutine; send accumulated text each
		// callback so PendingToolOutput (which replaces, not appends)
		// shows the full output at each step.
		var accumulated string
		tctx := &tool.ToolUseContext{
			WorkingDir: workingDir,
			OnProgress: func(u tool.ProgressUpdate) {
				accumulated += strings.Join(u.Lines, "\n")
				select {
				case a.tuiHandler.appCh <- toolOutputDeltaMsg{
					ToolUseID:     toolID,
					DisplayOutput: accumulated,
				}:
				default:
				}
			},
		}

		result, execErr := bash.Execute(context.Background(), inputJSON, tctx)

		var output string
		isError := false
		if execErr != nil {
			output = execErr.Error()
			isError = true
		} else if result != nil {
			if out, ok := result.Data.(*bash.Output); ok {
				if accumulated == "" && out.Stdout != "" {
					accumulated = out.Stdout
				}
				output = accumulated
				if out.ExitCode != 0 {
					isError = true
					if output != "" {
						output += "\n"
					}
					output += fmt.Sprintf("(exit code %d)", out.ExitCode)
				}
				if out.TimedOut {
					if output != "" {
						output += "\n"
					}
					output += "Command timed out"
				}
			} else {
				output = accumulated
			}
		} else {
			output = accumulated
		}
		if output == "" {
			output = "(finished)"
		}

		// toolEndMsg marks the tool done. FinishStream is called
		// inline in the toolEndMsg handler (detected by "bash-shortcut-"
		// prefix) so the spinner stops and streaming state resets.
		return toolEndMsg{
			ToolUseID: toolID,
			Output:    output,
			IsError:   isError,
		}
	}
}
