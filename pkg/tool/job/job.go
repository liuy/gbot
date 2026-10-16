// Package job provides tools for managing background jobs.
//
// TS divergence: the TypeScript source keeps JobOutput and JobStop as separate tools.
// gbot merges them into a single unified Job tool driven by an action enum
// (list/poll/stop), following the same pattern as browser_tabs-style action tools.
// The earlier three-mutually-exclusive-params form let sub-agents pass values like
// {"poll":"true"} (a boolean literal harvested from prompt text as a job ID), so
// the dispatch now validates a single action plus an explicit job_id. These are
// user-approved changes.
package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ErrNotFound is returned by Registry methods when a job ID does not exist
// in any backing registry. Callers should use errors.Is(err, ErrNotFound)
// to check for this condition rather than string matching.
var ErrNotFound = errors.New("job not found")

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// JobInfo is a snapshot of a background job's state.
type JobInfo struct {
	ID          string `json:"job_id"`
	Type        string `json:"type"`   // "bash" | "agent" | "fork"
	Status      string `json:"status"` // running, completed, failed, killed
	Command     string `json:"command,omitempty"`
	Description string `json:"description,omitempty"`
	Output      string `json:"output,omitempty"`
	ExitCode    int    `json:"exit_code,omitempty"`

	// Agent-specific fields (populated by BackgroundAgentJobAdapter).
	AgentType  string `json:"agent_type,omitempty"`
	Tokens     int    `json:"tokens,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`

	// ElapsedMs is the display elapsed frozen at list time: since start for
	// running jobs, total runtime for terminal ones. Frozen (not recomputed
	// at render time) so the wire tag, the live render, and replay decoding
	// the wire all agree — a still-running job replayed hours later must not
	// show hours of elapsed. Populated only by executeList; the wui panel
	// never sees it.
	ElapsedMs int64 `json:"elapsed_ms,omitempty"`

	// Panel metadata for the wui background-jobs view. Deliberately excluded
	// from wireText: the LLM-visible Job tool output stays unchanged.
	Backgrounded    bool   `json:"backgrounded,omitempty"`
	ParentToolUseID string `json:"parent_tool_use_id,omitempty"`
	StartedMs       int64  `json:"started_ms,omitempty"`
}

// Registry is the interface for querying and managing background jobs.
// bash.BackgroundJobRegistry satisfies this interface via JobInfoAdapter.
type Registry interface {
	// Get returns job info by ID. Returns (nil, false) if not found.
	Get(id string) (*JobInfo, bool)
	// Kill terminates a running job by ID.
	Kill(id string) error
	// List returns all jobs.
	List() []*JobInfo
	// Wait blocks until the job finishes, returning the exit code.
	Wait(id string) (int, error)
}

// Prefixer is an optional interface that Registries can implement to declare
// their ID prefix. MultiRegistry uses this for direct routing without
// cross-registry iteration.
type Prefixer interface {
	Prefix() string
}

// SummaryLister is an optional Registry extension for high-frequency polling:
// identical to List() except Output is always empty. List() implementations
// materialize per-job output buffers under their locks (an 8MB-class copy for
// spilled bash output), which no poller should trigger at 2Hz.
type SummaryLister interface {
	ListSummaries() []*JobInfo
}

// JobInput is the input schema for the Job tool. Exactly one action per call.
type JobInput struct {
	Action  string `json:"action"`            // list, poll, stop
	JobID   string `json:"job_id,omitempty"`  // target job (required for poll and stop)
	Block   *bool  `json:"block,omitempty"`   // default true (only meaningful with poll)
	Timeout int    `json:"timeout,omitempty"` // default 30000ms (only meaningful with poll)
}

// JobOutput is the unified output schema for the Job tool.
// Only the sections that were requested are populated.
type JobOutput struct {
	Poll *PollResult `json:"poll,omitempty"`
	Stop *StopResult `json:"stop,omitempty"`
	List *ListResult `json:"list,omitempty"`
}

// PollResult holds the result of a poll operation.
type PollResult struct {
	RetrievalStatus string   `json:"retrieval_status"` // success, timeout, not_ready
	Task            *JobInfo `json:"task,omitempty"`
}

// StopResult holds the result of a stop operation.
type StopResult struct {
	Status  string `json:"status"` // "killed"
	JobID   string `json:"job_id"`
	Type    string `json:"type"`    // "bash", "agent", "fork"
	Command string `json:"command"` // the command/description of the killed job
}

// ListResult holds the result of a list operation.
type ListResult struct {
	Jobs []*JobInfo `json:"jobs"`
}

// ---------------------------------------------------------------------------
// Tool construction
// ---------------------------------------------------------------------------

var jobToolSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["list", "poll", "stop"],
      "description": "Operation to perform"
    },
    "job_id": {
      "type": "string",
      "description": "Job ID — the job_id value from Job list output, e.g. \"bg-1\" (required for poll and stop)"
    },
    "block": {
      "type": "boolean",
      "default": true,
      "description": "Whether to wait for completion (only used with poll)"
    },
    "timeout": {
      "type": "number",
      "default": 30000,
      "description": "Max wait time in ms (only used with poll)"
    }
  },
  "required": ["action"]
}`)

// NewJob creates the unified Job tool.
func NewJob(reg Registry) tool.Tool {
	return tool.BuildTool(tool.ToolDef{
		Name_:        "Job",
		Aliases_:     []string{"KillShell"},
		InputSchema_: func() json.RawMessage { return jobToolSchema },
		Description_: func(input json.RawMessage) (string, error) {
			var in JobInput
			if err := json.Unmarshal(input, &in); err != nil {
				return "Manage job", nil
			}
			switch in.Action {
			case "list":
				return "List", nil
			case "poll":
				return "Poll " + strings.TrimSpace(in.JobID), nil
			case "stop":
				return "Stop " + strings.TrimSpace(in.JobID), nil
			default:
				return "Manage job", nil
			}
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return executeJobCall(ctx, reg, input)
		},
		IsReadOnly_: func(input json.RawMessage) bool {
			var in JobInput
			if err := json.Unmarshal(input, &in); err != nil {
				return false
			}
			// Unknown actions default to not read-only: they will fail at
			// dispatch anyway, and treating them as mutating is the safe side.
			return in.Action == "list" || in.Action == "poll"
		},
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		InterruptBehavior_: tool.InterruptCancel,
		ShouldDefer_:       false,
		SearchHint_:        "manage background jobs -- poll output or stop a running job",
		MaxResultSizeChars: 100000,
		Prompt_:            jobPrompt(),
		RenderResult_:      jobRenderResult,
		DecodeResult_: func(raw json.RawMessage) (any, error) {
			text, err := tool.UnmarshalSingleBlock(raw)
			if err != nil {
				return nil, err
			}
			var o JobOutput
			jsonErr := json.Unmarshal([]byte(text), &o)
			if jsonErr == nil {
				// Wire text that happens to be a JSON object decodes into an
				// all-zero JobOutput (unknown fields ignored), which replay
				// would render as an empty result instead of falling back to
				// the wire text. Uniform rule across wire-plaintext tools.
				if o.Poll == nil && o.Stop == nil && o.List == nil {
					return nil, fmt.Errorf("job: decoded output lacks identifying fields (not a legacy JSON result)")
				}
				return &o, nil
			}
			// The current wire is XML: recovering the structured result lets
			// replay render the friendly line format instead of raw tags.
			if out, xerr := decodeWireXML(text); xerr == nil {
				return out, nil
			}
			return nil, jsonErr
		},
		FormatWireBlocks_: func(data any) []types.ContentBlock {
			out, ok := data.(*JobOutput)
			if !ok {
				raw, _ := json.Marshal(data)
				return []types.ContentBlock{types.NewTextBlock(string(raw))}
			}
			return []types.ContentBlock{types.NewTextBlock(wireText(out))}
		},
	})
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

// executeJobCall dispatches on the action enum. Exactly one operation runs
// per call; poll and stop require a job_id, which must come from list output
// (the error text points there because sub-agents previously invented IDs
// like "true" from prompt prose and retried in a loop).
func executeJobCall(ctx context.Context, reg Registry, input json.RawMessage) (*tool.ToolResult, error) {
	var in JobInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	switch in.Action {
	case "list":
		return &tool.ToolResult{Data: &JobOutput{List: executeList(reg)}}, nil

	case "poll":
		if in.JobID == "" {
			return nil, fmt.Errorf(`action "poll" requires job_id — run {"action":"list"} to see job IDs`)
		}
		result, err := executePoll(ctx, reg, in.JobID, in.Block, in.Timeout)
		if err != nil {
			return nil, err
		}
		return &tool.ToolResult{Data: &JobOutput{Poll: result}}, nil

	case "stop":
		if in.JobID == "" {
			return nil, fmt.Errorf(`action "stop" requires job_id — run {"action":"list"} to see job IDs`)
		}
		result, err := executeStop(reg, in.JobID)
		if err != nil {
			return nil, err
		}
		return &tool.ToolResult{Data: &JobOutput{Stop: result}}, nil

	default:
		if in.Action == "" {
			// json.Unmarshal silently drops legacy keys like {"stop":"bg-1"} —
			// point the caller at the enum instead of echoing an empty action.
			return nil, fmt.Errorf(`action is required — valid actions are "list", "poll", "stop" (run {"action":"list"} to see jobs)`)
		}
		return nil, fmt.Errorf(`unknown action %q — valid actions are "list", "poll", "stop"`, in.Action)
	}
}

// executePoll polls a job for output.
func executePoll(ctx context.Context, reg Registry, jobID string, block *bool, timeout int) (*PollResult, error) {
	if jobID == "" {
		return nil, fmt.Errorf("job_id is required")
	}

	doBlock := true
	if block != nil {
		doBlock = *block
	}

	if timeout <= 0 {
		timeout = 30000
	}

	info, found := reg.Get(jobID)
	if !found {
		return nil, fmt.Errorf("no job found with ID: %s", jobID)
	}

	if isTerminal(info.Status) {
		return &PollResult{RetrievalStatus: "success", Task: info}, nil
	}

	if !doBlock {
		return &PollResult{RetrievalStatus: "not_ready", Task: info}, nil
	}

	// block=true: poll until terminal or timeout
	deadline := timeAfter(timeout)
	ticker := timeNewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			info, _ = reg.Get(jobID)
			return &PollResult{RetrievalStatus: "timeout", Task: info}, nil
		case <-ticker.C:
			info, _ = reg.Get(jobID)
			if info != nil && isTerminal(info.Status) {
				return &PollResult{RetrievalStatus: "success", Task: info}, nil
			}
		}
	}
}

// executeStop stops a running job.
func executeStop(reg Registry, jobID string) (*StopResult, error) {
	if jobID == "" {
		return nil, fmt.Errorf("job_id is required")
	}

	info, found := reg.Get(jobID)
	if !found {
		return nil, fmt.Errorf("no job found with ID: %s", jobID)
	}

	if isTerminal(info.Status) {
		return nil, fmt.Errorf("job %s is not running (status: %s)", jobID, info.Status)
	}

	if err := reg.Kill(jobID); err != nil {
		return nil, fmt.Errorf("failed to stop job %s: %w", jobID, err)
	}

	cmd := info.Command
	if cmd == "" {
		cmd = info.Description
	}

	return &StopResult{
		Status:  "killed",
		JobID:   jobID,
		Type:    info.Type,
		Command: cmd,
	}, nil
}

// executeList returns all registered jobs with the display elapsed frozen
// per job on shallow copies — registries may hand out shared snapshots.
func executeList(reg Registry) *ListResult {
	jobs := reg.List()
	for i, j := range jobs {
		cp := *j
		cp.ElapsedMs = jobElapsedMs(&cp, timeNow())
		jobs[i] = &cp
	}
	return &ListResult{Jobs: jobs}
}

// jobElapsedMs: running jobs report time since start; terminal jobs report
// total runtime from DurationMs (0 = unknown — the segment is then omitted).
func jobElapsedMs(j *JobInfo, now time.Time) int64 {
	if isTerminal(j.Status) {
		return j.DurationMs
	}
	if j.StartedMs <= 0 {
		return 0
	}
	return now.UnixMilli() - j.StartedMs
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// isTerminal returns true for terminal job statuses.
func isTerminal(status string) bool {
	return status == "completed" || status == "failed" || status == "killed"
}

// timeAfter, timeNewTicker and timeNow are vars for testability.
var (
	timeAfter     = func(ms int) <-chan time.Time { return time.After(time.Duration(ms) * time.Millisecond) }
	timeNewTicker = time.NewTicker
	timeNow       = time.Now
)

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// wireText renders the LLM-facing plain-text form. Segments (List, Poll,
// Stop) join with a blank line in renderJobOutput order.
func wireText(out *JobOutput) string {
	var sections []string
	if out.List != nil {
		sections = append(sections, wireListText(out.List))
	}
	if out.Poll != nil {
		sections = append(sections, wirePollText(out.Poll))
	}
	if out.Stop != nil {
		sections = append(sections, wireStopText(out.Stop))
	}
	return strings.Join(sections, "\n\n")
}

// wirePollText mirrors TaskOutputTool.tsx:283-308 — XML-tag parts joined
// with a blank line. Divergences, all deliberate:
//   - no <error>: JobInfo has no Error field.
//   - <task_type> carries gbot's job kinds (bash/agent/fork); TS emits its own
//     kinds (local_bash/local_agent) and has no fork concept.
//   - <output> content is not tail-truncated. TS truncates in the tool
//     because it has no engine-level persistence; gbot's
//     MaybePersistLargeToolResult runs after the wire is built and keeps
//     the full log on disk, so tool-level pre-truncation would permanently
//     discard the head of the log (plan D9).
func wirePollText(p *PollResult) string {
	parts := []string{fmt.Sprintf("<retrieval_status>%s</retrieval_status>", p.RetrievalStatus)}
	if p.Task != nil {
		j := p.Task
		parts = append(parts, fmt.Sprintf("<task_id>%s</task_id>", j.ID))
		parts = append(parts, fmt.Sprintf("<task_type>%s</task_type>", j.Type))
		parts = append(parts, fmt.Sprintf("<status>%s</status>", j.Status))
		// Terminal-only: gbot cannot distinguish an unset exit code from a
		// real 0, and TS only emits the tag when exitCode is defined.
		if isTerminal(j.Status) {
			parts = append(parts, fmt.Sprintf("<exit_code>%d</exit_code>", j.ExitCode))
		}
		// TS gates on `output?.trim()` — whitespace-only output omits the
		// block entirely.
		if strings.TrimSpace(j.Output) != "" {
			content := strings.TrimRightFunc(j.Output, unicode.IsSpace)
			parts = append(parts, "<output>\n"+content+"\n</output>")
		}
	}
	return strings.Join(parts, "\n\n")
}

// wireListText emits one XML tag line per job (plan: unified XML wire for
// all Job actions). Agent jobs carry their description in the <command> tag —
// the same command → description fallback the pre-XML line format used.
func wireListText(l *ListResult) string {
	if len(l.Jobs) == 0 {
		return "No jobs"
	}
	lines := make([]string, 0, len(l.Jobs))
	for _, j := range l.Jobs {
		lines = append(lines, wireJobLine(j))
	}
	return strings.Join(lines, "\n")
}

// wireJobLine lays out tags in a fixed order (job_id, type, status, elapsed,
// command); decodeWireJobLine relies on it. The type tag appears only when
// Type is set, so callers that never populate it keep a slimmer line.
func wireJobLine(j *JobInfo) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<job_id>%s</job_id>", j.ID)
	if j.Type != "" {
		fmt.Fprintf(&sb, "<type>%s</type>", j.Type)
	}
	fmt.Fprintf(&sb, "<status>%s</status>", j.Status)
	if j.ElapsedMs > 0 {
		fmt.Fprintf(&sb, "<elapsed>%s</elapsed>", formatElapsedMs(j.ElapsedMs))
	}
	if cmd := listCommand(j); cmd != "" {
		fmt.Fprintf(&sb, "<command>%s</command>", cmd)
	}
	return sb.String()
}

// wireStopText emits the killed-job record as a single XML tag line — same
// tag style as the list lines (minus elapsed, which stop does not carry).
func wireStopText(s *StopResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<job_id>%s</job_id>", s.JobID)
	if s.Type != "" {
		fmt.Fprintf(&sb, "<type>%s</type>", s.Type)
	}
	fmt.Fprintf(&sb, "<status>%s</status>", s.Status)
	if s.Command != "" {
		fmt.Fprintf(&sb, "<command>%s</command>", s.Command)
	}
	return sb.String()
}

// jobRenderResult renders the unified JobOutput for TUI display.
func jobRenderResult(data any) string {
	switch v := data.(type) {
	case *JobOutput:
		return renderJobOutput(v)
	default:
		return fmt.Sprintf("%v", data)
	}
}

// jobLineSep joins the display-line segments. TUI and wui both render the
// engine's RenderResult output verbatim, so this function is the single
// human-facing line format on both ends.
const jobLineSep = " · "

func renderJobOutput(out *JobOutput) string {
	var sections []string

	if out.List != nil {
		sections = append(sections, renderListResult(out.List))
	}
	if out.Poll != nil {
		sections = append(sections, renderPollResult(out.Poll))
	}
	if out.Stop != nil {
		sections = append(sections, renderStopResult(out.Stop))
	}

	return strings.Join(sections, "\n\n")
}

// formatJobLine builds "id · type · status · elapsed · exit N · command",
// omitting any segment with no value. Status words come straight from the
// wire field values — no icons, no translation.
func formatJobLine(id, typ, status string, elapsedMs int64, exitCode *int, cmd string) string {
	segments := []string{id}
	if typ != "" {
		segments = append(segments, typ)
	}
	if status != "" {
		segments = append(segments, status)
	}
	if elapsedMs > 0 {
		segments = append(segments, formatElapsedMs(elapsedMs))
	}
	if exitCode != nil {
		segments = append(segments, fmt.Sprintf("exit %d", *exitCode))
	}
	if cmd != "" {
		segments = append(segments, cmd)
	}
	return strings.Join(segments, jobLineSep)
}

// formatElapsedMs humanizes the elapsed segment. It mirrors
// utils.FormatDuration's output byte-for-byte (the repo-wide convention) but
// lives here: pkg/utils tests import pkg/tool/bash, which imports this
// package, so importing utils back would be an import cycle.
func formatElapsedMs(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	s := int(d.Seconds())
	switch {
	case s < 1:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh %dm %ds", s/3600, (s%3600)/60, s%60)
	}
}

// listCommand prefers the command, falling back to the description agent
// jobs carry instead.
func listCommand(j *JobInfo) string {
	if j.Command != "" {
		return j.Command
	}
	return j.Description
}

// renderPollResult: completed/failed polls show the header plus the output
// body indented 4 spaces; still-running polls (not_ready/timeout) show the
// header only — partial output belongs to the wire, not the line format.
func renderPollResult(p *PollResult) string {
	if p.Task == nil {
		return p.RetrievalStatus
	}
	t := p.Task
	header := formatJobLine(t.ID, t.Type, t.Status, 0, pollExitSegment(t), "")
	if t.Status != "completed" && t.Status != "failed" {
		return header
	}
	if strings.TrimSpace(t.Output) == "" {
		return header
	}
	var sb strings.Builder
	sb.WriteString(header)
	for line := range strings.SplitSeq(strings.TrimRight(t.Output, "\n"), "\n") {
		// Blank lines take no indent — "    " would be pure trailing
		// whitespace on the rendered line.
		if line == "" {
			sb.WriteString("\n")
			continue
		}
		sb.WriteString("\n    ")
		sb.WriteString(line)
	}
	return sb.String()
}

// pollExitSegment surfaces the exit code only for completed/failed polls:
// killed jobs carry a signal artifact (137 and friends), not a real exit.
func pollExitSegment(t *JobInfo) *int {
	if t.Status != "completed" && t.Status != "failed" {
		return nil
	}
	code := t.ExitCode
	return &code
}

func renderStopResult(s *StopResult) string {
	return formatJobLine(s.JobID, s.Type, s.Status, 0, nil, s.Command)
}

func renderListResult(l *ListResult) string {
	if len(l.Jobs) == 0 {
		return "No jobs"
	}
	var sb strings.Builder
	for i, j := range l.Jobs {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(formatJobLine(j.ID, j.Type, j.Status, j.ElapsedMs, nil, listCommand(j)))
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// Wire XML parsing (replay)
// ---------------------------------------------------------------------------

// decodeWireXML recovers a JobOutput from wireText's XML form so history
// replay renders the friendly lines instead of raw tags. It accepts only
// what wireText itself emits; pre-XML text formats fail here and replay
// falls through to as-is display (no legacy parsing).
func decodeWireXML(text string) (*JobOutput, error) {
	switch {
	case strings.HasPrefix(text, "<retrieval_status>"):
		return decodeWirePoll(text)
	case strings.HasPrefix(text, "<job_id>"):
		return decodeWireJobLines(text)
	default:
		return nil, fmt.Errorf("job: wire text is not job XML")
	}
}

// decodeWirePoll walks the blank-line-joined parts wirePollText emits.
func decodeWirePoll(text string) (*JobOutput, error) {
	status, rest, ok := cutTag(text, "retrieval_status")
	if !ok {
		return nil, fmt.Errorf("job: poll wire missing retrieval_status")
	}
	res := &PollResult{RetrievalStatus: status}
	var task *JobInfo
	for rest != "" {
		if !strings.HasPrefix(rest, "\n\n") {
			return nil, fmt.Errorf("job: poll wire parts not blank-line separated at %q", wirePreview(rest))
		}
		part := rest[2:]
		// Output is always the last part, so its value is everything between
		// the fixed prefix and the fixed suffix — embedded angle brackets and
		// look-alike closing tags stay part of the content.
		const open, close = "<output>\n", "\n</output>"
		if strings.HasPrefix(part, open) && strings.HasSuffix(part, close) {
			if task == nil {
				task = &JobInfo{}
			}
			task.Output = part[len(open) : len(part)-len(close)]
			rest = ""
			continue
		}
		if v, next, ok := cutTag(part, "task_id"); ok {
			if task == nil {
				task = &JobInfo{}
			}
			task.ID = v
			rest = next
			continue
		}
		if v, next, ok := cutTag(part, "task_type"); ok {
			if task == nil {
				task = &JobInfo{}
			}
			task.Type = v
			rest = next
			continue
		}
		if v, next, ok := cutTag(part, "status"); ok {
			if task == nil {
				task = &JobInfo{}
			}
			task.Status = v
			rest = next
			continue
		}
		if v, next, ok := cutTag(part, "exit_code"); ok {
			code, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("job: exit_code %q: %w", v, err)
			}
			if task == nil {
				task = &JobInfo{}
			}
			task.ExitCode = code
			rest = next
			continue
		}
		return nil, fmt.Errorf("job: unrecognized poll part %q", wirePreview(part))
	}
	res.Task = task
	return &JobOutput{Poll: res}, nil
}

// decodeWireJobLines parses the per-job XML lines wireListText (and
// wireStopText, whose single line has the same shape) emit. Commands are
// embedded verbatim, so a multi-line command makes one wire job line span
// several "\n"-split fragments: a fragment starting with <job_id> opens a
// new job, any other fragment continues the previous line (re-joined with
// "\n" before parsing, so it lands inside the open <command> tag). A command
// embedding a literal "\n<job_id>…" sequence would mis-split — accepted
// ambiguity, since escaping newlines on the wire would change the LLM-visible
// format.
func decodeWireJobLines(text string) (*JobOutput, error) {
	var lines []string
	for frag := range strings.SplitSeq(text, "\n") {
		if !strings.HasPrefix(frag, "<job_id>") {
			if len(lines) == 0 {
				return nil, fmt.Errorf("job: job line missing job_id")
			}
			lines[len(lines)-1] += "\n" + frag
			continue
		}
		lines = append(lines, frag)
	}
	var jobs []*JobInfo
	for _, line := range lines {
		j, err := decodeWireJobLine(line)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return &JobOutput{List: &ListResult{Jobs: jobs}}, nil
}

func decodeWireJobLine(line string) (*JobInfo, error) {
	id, rest, ok := cutTag(line, "job_id")
	if !ok || id == "" {
		return nil, fmt.Errorf("job: job line missing job_id")
	}
	j := &JobInfo{ID: id}
	if v, next, ok := cutTag(rest, "type"); ok {
		j.Type = v
		rest = next
	}
	if v, next, ok := cutTag(rest, "status"); ok {
		j.Status = v
		rest = next
	}
	if v, next, ok := cutTag(rest, "elapsed"); ok {
		ms, err := parseElapsedMs(v)
		if err != nil {
			return nil, err
		}
		j.ElapsedMs = ms
		rest = next
	}
	// Command is free text and always the last tag, so the value is
	// everything between the fixed prefix and the fixed suffix.
	const open, close = "<command>", "</command>"
	if rest != "" {
		if !strings.HasPrefix(rest, open) || !strings.HasSuffix(rest, close) {
			return nil, fmt.Errorf("job: job line has trailing non-command content %q", wirePreview(rest))
		}
		j.Command = rest[len(open) : len(rest)-len(close)]
	}
	return j, nil
}

// cutTag removes a leading "<name>value</name>" unit, taking the value up to
// the first closing tag — safe for scalar tags whose values come from fixed
// vocabularies (IDs, types, statuses, numbers).
func cutTag(s, name string) (value, rest string, ok bool) {
	open := "<" + name + ">"
	if !strings.HasPrefix(s, open) {
		return "", "", false
	}
	after := s[len(open):]
	closing := "</" + name + ">"
	before, after0, ok := strings.Cut(after, closing)
	if !ok {
		return "", "", false
	}
	return before, after0, true
}

// parseElapsedMs inverts utils.FormatDuration ("1h 2m 3s" style): the
// decorative spaces are the only difference from time.ParseDuration syntax.
func parseElapsedMs(s string) (int64, error) {
	d, err := time.ParseDuration(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		return 0, fmt.Errorf("job: elapsed %q: %w", s, err)
	}
	return d.Milliseconds(), nil
}

// wirePreview keeps error messages short — a poll <output> part can be huge.
func wirePreview(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}
