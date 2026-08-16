package job

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// jobWireText drives the wire path the engine uses (FormatWireBlocks →
// single text block) and returns the block text.
func jobWireText(t *testing.T, data any) string {
	t.Helper()
	wb, ok := NewJob(newMockRegistry()).(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("Job tool must implement ToolWithWireBlocks")
	}
	blocks := wb.FormatWireBlocks(data)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText {
		t.Fatalf("blocks[0].Type = %q, want %q", blocks[0].Type, types.ContentTypeText)
	}
	return blocks[0].Text
}

// Source: TaskOutputTool.tsx:283-308 — XML-tag parts joined with a blank
// line. No <error>: JobInfo has no Error field. <output> content is not
// tail-truncated here — engine-level persistence keeps the full log on disk
// (plan D9).
func TestJobWire_PollWithTerminalTask(t *testing.T) {
	t.Parallel()
	got := jobWireText(t, &JobOutput{Poll: &PollResult{
		RetrievalStatus: "success",
		Task: &JobInfo{
			ID:       "bg-1",
			Type:     "local_bash",
			Status:   "completed",
			Command:  "go build ./...",
			Output:   "hello world\n\n",
			ExitCode: 0,
		},
	}})
	want := "<retrieval_status>success</retrieval_status>\n\n" +
		"<task_id>bg-1</task_id>\n\n" +
		"<task_type>local_bash</task_type>\n\n" +
		"<status>completed</status>\n\n" +
		"<exit_code>0</exit_code>\n\n" +
		"<output>\nhello world\n</output>"
	if got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// A running task has no exit code to report (gbot cannot distinguish
// unset from zero, so the tag is terminal-only) and whitespace-only
// output omits the <output> block (TS `output?.trim()`).
func TestJobWire_PollRunningTaskNoExitCodeNoOutput(t *testing.T) {
	t.Parallel()
	got := jobWireText(t, &JobOutput{Poll: &PollResult{
		RetrievalStatus: "not_ready",
		Task: &JobInfo{
			ID:     "bg-2",
			Type:   "local_bash",
			Status: "running",
			Output: "  \n",
		},
	}})
	want := "<retrieval_status>not_ready</retrieval_status>\n\n" +
		"<task_id>bg-2</task_id>\n\n" +
		"<task_type>local_bash</task_type>\n\n" +
		"<status>running</status>"
	if got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// Poll timeout with the task snapshot missing → status tag only.
func TestJobWire_PollWithoutTask(t *testing.T) {
	t.Parallel()
	got := jobWireText(t, &JobOutput{Poll: &PollResult{RetrievalStatus: "timeout"}})
	if want := "<retrieval_status>timeout</retrieval_status>"; got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// Source: Stop segment is gbot's own plain-text sentence (plan D4) — TS
// TaskStop sends JSON on the wire.
func TestJobWire_Stop(t *testing.T) {
	t.Parallel()
	got := jobWireText(t, &JobOutput{Stop: &StopResult{
		Status:  "killed",
		JobID:   "bg-3",
		JobType: "local_bash",
		Command: "sleep 100",
	}})
	if want := "Successfully stopped job bg-3 (local_bash): sleep 100"; got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// Source: List format mirrors renderListResult (plan D8) — one line per
// job, command → description → ID fallback.
func TestJobWire_ListEmpty(t *testing.T) {
	t.Parallel()
	got := jobWireText(t, &JobOutput{List: &ListResult{Jobs: nil}})
	if want := "No jobs"; got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

func TestJobWire_ListMultipleWithFallbacks(t *testing.T) {
	t.Parallel()
	got := jobWireText(t, &JobOutput{List: &ListResult{Jobs: []*JobInfo{
		{ID: "bg-1", Status: "running", Command: "go test ./..."},
		{ID: "bg-2", Status: "completed", Description: "agent: fix bug"},
		{ID: "bg-3", Status: "killed"},
	}}})
	want := "bg-1 [running] go test ./...\nbg-2 [completed] agent: fix bug\nbg-3 [killed] bg-3"
	if got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// Segments join with a blank line (List, Poll, Stop in renderJobOutput order).
func TestJobWire_CombinedSegments(t *testing.T) {
	t.Parallel()
	got := jobWireText(t, &JobOutput{
		List: &ListResult{Jobs: []*JobInfo{{ID: "bg-1", Status: "running", Command: "x"}}},
		Poll: &PollResult{RetrievalStatus: "timeout"},
		Stop: &StopResult{JobID: "bg-1", JobType: "local_bash", Command: "x"},
	})
	want := "bg-1 [running] x\n\n<retrieval_status>timeout</retrieval_status>\n\n" +
		"Successfully stopped job bg-1 (local_bash): x"
	if got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// Non-*JobOutput data keeps the JSON fallback so anything DecodeResult
// reconstructs still serializes instead of panicking.
func TestJobWire_NonOutputFallsBackToJSON(t *testing.T) {
	t.Parallel()
	if got := jobWireText(t, "x"); got != `"x"` {
		t.Errorf("wire text = %q, want %q", got, `"x"`)
	}
}

func TestJobDecodeResult_LegacyJSONWire(t *testing.T) {
	t.Parallel()
	tl := NewJob(newMockRegistry())
	raw := tool.WrapSingleBlock(`{"poll":{"retrieval_status":"success","task":{"job_id":"bg-1","status":"completed","exit_code":0}}}`)
	v, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*JobOutput)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *JobOutput", v)
	}
	if o.Poll == nil || o.Poll.Task == nil || o.Poll.Task.ID != "bg-1" || o.Poll.RetrievalStatus != "success" {
		t.Errorf("decoded = %+v, want poll.task.job_id=bg-1 retrieval_status=success", o)
	}
}

// Wire text that is a bare JSON object decodes into an all-zero JobOutput
// (unknown fields ignored), which replay would render as an empty result
// instead of falling back to the wire text.
func TestJobDecodeResult_RejectsJSONObjectWire(t *testing.T) {
	t.Parallel()
	tl := NewJob(newMockRegistry())
	raw := tool.WrapSingleBlock(`{"name":"gbot","version":"1.0"}`)
	_, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject JSON-object wire without identifying fields")
	}
	if !strings.Contains(err.Error(), "identifying fields") {
		t.Errorf("err = %v, want it to mention identifying fields", err)
	}
}

func TestJobDecodeResult_RejectsPlainTextWire(t *testing.T) {
	t.Parallel()
	tl := NewJob(newMockRegistry())
	raw := tool.WrapSingleBlock("Successfully stopped job bg-1 (local_bash): x")
	_, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject non-JSON wire text")
	}
	if !strings.Contains(err.Error(), "invalid character 'S' looking for beginning of value") {
		t.Errorf("err = %v, want json syntax error", err)
	}
}
