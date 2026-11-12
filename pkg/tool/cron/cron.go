// Cron tool: CC's CronCreate/CronList/CronDelete merged into one
// action-enum tool (pkg/tool/job idiom; user-approved shape).
package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// Input is the Cron tool input schema. D5: CC's semanticBoolean becomes plain
// *bool (gbot schema discipline; Job tool precedent).
type Input struct {
	Action    string `json:"action"`
	Cron      string `json:"cron,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
	Recurring *bool  `json:"recurring,omitempty"` // default true
	Durable   *bool  `json:"durable,omitempty"`   // default false
	ID        string `json:"id,omitempty"`
}

// CreateResult is the action=create output. Source: CronCreateTool outputSchema
// (Prompt is a gbot addition: the create wire embeds the full prompt and replay
// decode must recover it to re-render the card).
type CreateResult struct {
	ID            string `json:"id"`
	HumanSchedule string `json:"humanSchedule"`
	Prompt        string `json:"prompt,omitempty"`
	Recurring     bool   `json:"recurring"`
	Durable       bool   `json:"durable"`
}

// ListJob is one list entry. Source: CronListTool outputSchema jobs item —
// durable is emitted only when false (session-only), mirroring CC's
// `...(t.durable === false ? { durable: false } : {})`.
type ListJob struct {
	ID            string `json:"id"`
	Cron          string `json:"cron"`
	HumanSchedule string `json:"humanSchedule"`
	Prompt        string `json:"prompt"`
	Recurring     bool   `json:"recurring,omitempty"`
	Durable       bool   `json:"durable,omitempty"`
}

// ListResult is the action=list output.
type ListResult struct {
	Jobs []ListJob `json:"jobs"`
}

// DeleteResult is the action=delete output.
type DeleteResult struct {
	ID string `json:"id"`
}

// CronOutput is the unified output; only the requested section is populated.
type CronOutput struct {
	Create *CreateResult `json:"create,omitempty"`
	List   *ListResult   `json:"list,omitempty"`
	Delete *DeleteResult `json:"delete,omitempty"`
}

var cronToolSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["create", "list", "delete"],
      "description": "Operation to perform"
    },
    "cron": {
      "type": "string",
      "description": "Standard 5-field cron expression in local time: \"M H DoM Mon DoW\" (e.g. \"*/5 * * * *\" = every 5 minutes, \"30 14 28 2 *\" = Feb 28 at 2:30pm local once)"
    },
    "prompt": {
      "type": "string",
      "description": "The prompt to enqueue at each fire time."
    },
    "recurring": {
      "type": "boolean",
      "default": true,
      "description": "true (default) = fire on every cron match until deleted or auto-expired after 7 days. false = fire once at the next match, then auto-delete. Use false for \"remind me at X\" one-shot requests with pinned minute/hour/dom/month."
    },
    "durable": {
      "type": "boolean",
      "default": false,
      "description": "true = persist to this project's gbot projectspace and survive restarts. false (default) = in-memory only, dies when this gbot session ends. Use true only when the user asks the task to survive across sessions."
    },
    "id": {
      "type": "string",
      "description": "Job ID returned by action:\"create\""
    }
  },
  "required": ["action"]
}`)

// New creates the Cron tool bound to a Manager.
func New(m *Manager) tool.Tool {
	return tool.BuildTool(tool.ToolDef{
		Name_:        "Cron",
		InputSchema_: func() json.RawMessage { return cronToolSchema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return cronDescription, nil
			}
			switch in.Action {
			case "create":
				return "Schedule " + strings.TrimSpace(in.Cron), nil
			case "list":
				return "List cron jobs", nil
			case "delete":
				return "Cancel " + strings.TrimSpace(in.ID), nil
			default:
				// No/unknown action: the full tool-list description (CC
				// buildCronCreateDescription shape, extended for the merged
				// list/delete actions).
				return cronDescription, nil
			}
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return executeCronCall(m, input)
		},
		// CC marks only CronList read-only + concurrency-safe.
		IsReadOnly_: func(input json.RawMessage) bool {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return false
			}
			return in.Action == "list"
		},
		IsConcurrencySafe_: func(input json.RawMessage) bool {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return false
			}
			return in.Action == "list"
		},
		InterruptBehavior_: tool.InterruptCancel,
		ShouldDefer_:       false,
		SearchHint_:        "schedule a recurring or one-shot prompt",
		MaxResultSizeChars: 100000,
		Prompt_:            cronPrompt(),
		RenderResult_:      renderCronOutput,
		DecodeResult_: func(raw json.RawMessage) (any, error) {
			text, err := tool.UnmarshalSingleBlock(raw)
			if err != nil {
				return nil, err
			}
			out, derr := decodeWire(text)
			if derr != nil {
				return nil, fmt.Errorf("cron: wire text is not a cron result: %w", derr)
			}
			return out, nil
		},
		FormatWireBlocks_: func(data any) []types.ContentBlock { return cronFormatWireBlocks(data) },
	})
}

// executeCronCall dispatches on the action enum. Create validation order
// mirrors CC's validateInput: parse → no-match → MAX_JOBS → durable-reject
// (the reject surfaces verbatim from Manager.Create).
func executeCronCall(m *Manager, input json.RawMessage) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	switch in.Action {
	case "create":
		if in.Cron == "" {
			return nil, fmt.Errorf(`action "create" requires cron`)
		}
		// Divergence from CC (user-adjudicated): an empty prompt is a state
		// that must not exist — firing it would enqueue an empty instruction.
		if strings.TrimSpace(in.Prompt) == "" {
			return nil, fmt.Errorf("cron prompt must not be empty — provide the instruction to run at fire time")
		}
		if ParseCronExpression(in.Cron) == nil {
			return nil, fmt.Errorf("Invalid cron expression '%s'. Expected 5 fields: M H DoM Mon DoW.", in.Cron)
		}
		if _, ok := NextCronRunMs(in.Cron, m.now().UnixMilli()); !ok {
			return nil, fmt.Errorf("Cron expression '%s' does not match any calendar date in the next year.", in.Cron)
		}
		if len(m.List()) >= MaxJobs {
			return nil, fmt.Errorf("Too many scheduled jobs (max %d). Cancel one first.", MaxJobs)
		}
		recurring := in.Recurring == nil || *in.Recurring
		durable := in.Durable != nil && *in.Durable
		id, err := m.Create(in.Cron, in.Prompt, recurring, durable)
		if err != nil {
			return nil, err
		}
		return &tool.ToolResult{Data: &CronOutput{Create: &CreateResult{
			ID:            id,
			HumanSchedule: CronToHuman(in.Cron),
			Prompt:        in.Prompt,
			Recurring:     recurring,
			Durable:       durable,
		}}}, nil

	case "list":
		var jobs []ListJob
		for _, t := range m.List() {
			jobs = append(jobs, ListJob{
				ID:            t.ID,
				Cron:          t.Cron,
				HumanSchedule: listSchedule(CronToHuman(t.Cron)),
				Prompt:        t.Prompt,
				Recurring:     t.Recurring,
				Durable:       t.Durable,
			})
		}
		return &tool.ToolResult{Data: &CronOutput{List: &ListResult{Jobs: jobs}}}, nil

	case "delete":
		if in.ID == "" {
			return nil, fmt.Errorf(`action "delete" requires id`)
		}
		found := false
		for _, t := range m.List() {
			if t.ID == in.ID {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("No scheduled job with id '%s'", in.ID)
		}
		if err := m.Delete(in.ID); err != nil {
			return nil, err
		}
		return &tool.ToolResult{Data: &CronOutput{Delete: &DeleteResult{ID: in.ID}}}, nil

	default:
		if in.Action == "" {
			return nil, fmt.Errorf(`action is required — valid actions are "create", "list", "delete"`)
		}
		return nil, fmt.Errorf(`unknown action %q — valid actions are "create", "list", "delete"`, in.Action)
	}
}

// wireText renders the LLM-facing plain text. Delete keeps CC's
// CronDeleteTool sentence; create/list render the user-ruled card forms
// (2026-11-12), replacing CC's mapToolResultToToolResultBlockParam output.
func wireText(out *CronOutput) string {
	var sections []string
	if out.Create != nil {
		sections = append(sections, wireCreateText(out.Create))
	}
	if out.List != nil {
		sections = append(sections, wireListText(out.List))
	}
	if out.Delete != nil {
		sections = append(sections, "Cancelled job "+out.Delete.ID+".")
	}
	return strings.Join(sections, "\n\n")
}

// wireCreateText renders the user-ruled card form (2026-11-12, five
// iterations): head line, Fires, the FULL prompt (no truncation — create is
// the verification view), then persistence. CC's sentence form and its
// parenthetical explanations were dropped by the same ruling.
func wireCreateText(c *CreateResult) string {
	kind := "task"
	if !c.Recurring {
		kind = "one-shot task"
	}
	persist := "Session-only"
	if c.Durable {
		persist = "Durable"
	}
	return fmt.Sprintf("Scheduled %s %s\n- Fires: %s\n- Prompt: %s\n- %s", kind, c.ID, c.HumanSchedule, c.Prompt, persist)
}

// listSchedule adapts CronToHuman for the list header: the pinned-date arm's
// "12:18, Nov 12" drops its comma — the kind word's comma already separates
// the header fields, so "one-shot, 12:18 Nov 12" reads cleaner (user ruling).
// Every other arm's output contains no ", ", so this is the identity for them.
func listSchedule(human string) string {
	return strings.Replace(human, ", ", " ", 1)
}

// wireListText renders one header + indented prompt line per job; the prompt
// is the truncated browsing form (80 runes, newline-snipped).
func wireListText(l *ListResult) string {
	if len(l.Jobs) == 0 {
		return "No scheduled jobs."
	}
	lines := make([]string, 0, len(l.Jobs)*2)
	for _, j := range l.Jobs {
		kind := "one-shot"
		if j.Recurring {
			kind = "recurring"
		}
		persist := "session-only"
		if j.Durable {
			persist = "durable"
		}
		lines = append(lines,
			fmt.Sprintf("%s — %s, %s [%s]", j.ID, kind, j.HumanSchedule, persist),
			"  "+truncatePrompt(j.Prompt))
	}
	return strings.Join(lines, "\n")
}

// truncatePrompt is CC utils/truncate(str, 80, true) with runes replacing
// display-width columns (approved divergence): snip at the first newline,
// then ≤79 runes → prefix+"…", else first 79 runes + "…".
func truncatePrompt(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
		if r := []rune(s); len(r) >= 80 {
			return string(r[:79]) + "…"
		}
		return s + "…"
	}
	r := []rune(s)
	if len(r) <= 80 {
		return s
	}
	return string(r[:79]) + "…"
}

// decodeWire recovers a CronOutput from wireText for history replay.
func decodeWire(text string) (*CronOutput, error) {
	if rest, ok := strings.CutPrefix(text, "Scheduled one-shot task "); ok {
		return decodeWireCreate(rest, false)
	}
	if rest, ok := strings.CutPrefix(text, "Scheduled task "); ok {
		return decodeWireCreate(rest, true)
	}
	if rest, ok := strings.CutPrefix(text, "Cancelled job "); ok {
		id, ok := strings.CutSuffix(rest, ".")
		if !ok || id == "" {
			return nil, fmt.Errorf("cron: delete wire lacks id")
		}
		return &CronOutput{Delete: &DeleteResult{ID: id}}, nil
	}
	if text == "No scheduled jobs." {
		return &CronOutput{List: &ListResult{}}, nil
	}
	if strings.Contains(text, " — ") {
		return decodeWireList(text)
	}
	return nil, fmt.Errorf("cron: unrecognized wire text")
}

// decodeWireCreate parses the card form the prefix stripper left behind:
//
//	id\n- Fires: human\n- Prompt: text[\ntext…]\n- Session-only|- Durable
//
// The prompt is free text and may span lines (and may itself contain lines
// that look like the markers); the persistence line is always LAST, which is
// what bounds the prompt.
func decodeWireCreate(rest string, recurring bool) (*CronOutput, error) {
	id, body, ok := strings.Cut(rest, "\n")
	if !ok || id == "" {
		return nil, fmt.Errorf("cron: create wire lacks id head")
	}
	lines := strings.Split(body, "\n")
	if len(lines) < 3 {
		return nil, fmt.Errorf("cron: create wire lacks Fires/Prompt/persistence body")
	}
	human, ok := strings.CutPrefix(lines[0], "- Fires: ")
	if !ok || human == "" {
		return nil, fmt.Errorf("cron: create wire lacks Fires line")
	}
	promptBlock := strings.Join(lines[1:len(lines)-1], "\n")
	prompt, ok := strings.CutPrefix(promptBlock, "- Prompt: ")
	if !ok {
		return nil, fmt.Errorf("cron: create wire lacks Prompt line")
	}
	persist := lines[len(lines)-1]
	durable := persist == "- Durable"
	if !durable && persist != "- Session-only" {
		return nil, fmt.Errorf("cron: create wire lacks persistence line")
	}
	return &CronOutput{Create: &CreateResult{
		ID:            id,
		HumanSchedule: human,
		Prompt:        prompt,
		Recurring:     recurring,
		Durable:       durable,
	}}, nil
}

// decodeWireList parses the "id — kind, human [tag]" header plus the
// two-space-indented prompt line per job. Prompts were newline-snipped at
// emit time, so the line pairing is unambiguous.
func decodeWireList(text string) (*CronOutput, error) {
	lines := strings.Split(text, "\n")
	if len(lines)%2 != 0 {
		return nil, fmt.Errorf("cron: list wire is not header+prompt pairs")
	}
	var jobs []ListJob
	for i := 0; i < len(lines); i += 2 {
		prompt, ok := strings.CutPrefix(lines[i+1], "  ")
		if !ok {
			return nil, fmt.Errorf("cron: list job lacks indented prompt line: %q", lines[i+1])
		}
		id, rest, ok := strings.Cut(lines[i], " — ")
		if !ok || id == "" {
			return nil, fmt.Errorf("cron: list header lacks id: %q", lines[i])
		}
		kind, meta, ok := strings.Cut(rest, ", ")
		if !ok {
			return nil, fmt.Errorf("cron: list header lacks kind: %q", lines[i])
		}
		job := ListJob{ID: id, Prompt: prompt}
		switch kind {
		case "recurring":
			job.Recurring = true
		case "one-shot":
		default:
			return nil, fmt.Errorf("cron: list header has unknown kind %q: %q", kind, lines[i])
		}
		if before, ok := strings.CutSuffix(meta, " [durable]"); ok {
			job.Durable = true
			job.HumanSchedule = before
		} else if before, ok := strings.CutSuffix(meta, " [session-only]"); ok {
			job.HumanSchedule = before
		} else {
			return nil, fmt.Errorf("cron: list header lacks persistence tag: %q", lines[i])
		}
		jobs = append(jobs, job)
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("cron: empty list wire")
	}
	return &CronOutput{List: &ListResult{Jobs: jobs}}, nil
}

// cronFormatWireBlocks renders Data to the wire block (JSON fallback for
// unexpected types).
func cronFormatWireBlocks(data any) []types.ContentBlock {
	out, ok := data.(*CronOutput)
	if !ok {
		raw, _ := json.Marshal(data)
		return []types.ContentBlock{types.NewTextBlock(string(raw))}
	}
	return []types.ContentBlock{types.NewTextBlock(wireText(out))}
}

// renderCronOutput renders the TUI/wui display form — the same friendly
// sentences as the wire (CC's renderers show exactly the wire content).
func renderCronOutput(data any) string {
	if out, ok := data.(*CronOutput); ok {
		return wireText(out)
	}
	return fmt.Sprintf("%v", data)
}
