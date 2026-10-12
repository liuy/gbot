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
			Type:     "bash",
			Status:   "completed",
			Command:  "go build ./...",
			Output:   "hello world\n\n",
			ExitCode: 0,
		},
	}})
	want := "<retrieval_status>success</retrieval_status>\n\n" +
		"<task_id>bg-1</task_id>\n\n" +
		"<task_type>bash</task_type>\n\n" +
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
			Type:   "bash",
			Status: "running",
			Output: "  \n",
		},
	}})
	want := "<retrieval_status>not_ready</retrieval_status>\n\n" +
		"<task_id>bg-2</task_id>\n\n" +
		"<task_type>bash</task_type>\n\n" +
		"<status>running</status>"
	if got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// The task_type token carries gbot's job kinds (bash/agent/fork), not TS's
// local_bash/local_agent — for an agent job the token is also the signal that
// the id is a message address.
func TestJobWire_PollAgentTaskType(t *testing.T) {
	t.Parallel()
	got := jobWireText(t, &JobOutput{Poll: &PollResult{
		RetrievalStatus: "not_ready",
		Task: &JobInfo{
			ID:     "agent-1",
			Type:   "agent",
			Status: "running",
			Output: "still working",
		},
	}})
	want := "<retrieval_status>not_ready</retrieval_status>\n\n" +
		"<task_id>agent-1</task_id>\n\n" +
		"<task_type>agent</task_type>\n\n" +
		"<status>running</status>\n\n" +
		"<output>\nstill working\n</output>"
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
		Type:    "bash",
		Command: "sleep 100",
	}})
	if want := "Successfully stopped job bg-3 (bash): sleep 100"; got != want {
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

// List lines carry the job kind (agent/fork/bash) so the LLM can tell
// message-addressable background agents apart from shells.
func TestJobListWire_IncludesType(t *testing.T) {
	t.Parallel()
	got := jobWireText(t, &JobOutput{List: &ListResult{Jobs: []*JobInfo{
		{ID: "agent-1", Type: "agent", Status: "running", Command: "research"},
		{ID: "fork-2", Type: "fork", Status: "running", Command: "audit"},
		{ID: "bg-3", Type: "bash", Status: "completed", Command: "make check"},
	}}})
	want := "job_id=agent-1 type=agent status=running command=\"research\"\n" +
		"job_id=fork-2 type=fork status=running command=\"audit\"\n" +
		"job_id=bg-3 type=bash status=completed command=\"make check\""
	if got != want {
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
	want := "job_id=bg-1 status=running command=\"go test ./...\"\njob_id=bg-2 status=completed command=\"agent: fix bug\"\njob_id=bg-3 status=killed command=\"bg-3\""
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
		Stop: &StopResult{JobID: "bg-1", Type: "bash", Command: "x"},
	})
	want := "job_id=bg-1 status=running command=\"x\"\n\n<retrieval_status>timeout</retrieval_status>\n\n" +
		"Successfully stopped job bg-1 (bash): x"
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
	want := "job: decoded output lacks identifying fields (not a legacy JSON result)"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// Each segment alone must survive the "all three nil" guard: the guard is a
// conjunction, so any single populated field means the wire is a real result.
func TestJobDecodeResult_SingleSegmentWireAccepted(t *testing.T) {
	t.Parallel()
	tl := NewJob(newMockRegistry())
	for _, tc := range []struct {
		raw  string
		want func(*JobOutput) bool
		desc string
	}{
		{`{"stop":{"status":"killed","job_id":"bg-1","type":"bash","command":"sleep 5"}}`,
			func(o *JobOutput) bool {
				return o.Stop != nil && o.Stop.JobID == "bg-1" && o.Stop.Type == "bash" && o.Poll == nil && o.List == nil
			},
			"stop-only"},
		{`{"list":{"jobs":[{"job_id":"bg-2","status":"running"}]}}`,
			func(o *JobOutput) bool {
				return o.List != nil && len(o.List.Jobs) == 1 && o.List.Jobs[0].ID == "bg-2" && o.Poll == nil && o.Stop == nil
			},
			"list-only"},
	} {
		v, err := tl.(tool.ToolWithDecodeResult).DecodeResult(tool.WrapSingleBlock(tc.raw))
		if err != nil {
			t.Errorf("DecodeResult(%s): %v", tc.desc, err)
			continue
		}
		o, ok := v.(*JobOutput)
		if !ok {
			t.Errorf("DecodeResult(%s) returned %T, want *JobOutput", tc.desc, v)
			continue
		}
		if !tc.want(o) {
			t.Errorf("DecodeResult(%s) = %+v, shape mismatch", tc.desc, o)
		}
	}
}

func TestJobDecodeResult_RejectsPlainTextWire(t *testing.T) {
	t.Parallel()
	tl := NewJob(newMockRegistry())
	raw := tool.WrapSingleBlock("Successfully stopped job bg-1 (bash): x")
	_, err := tl.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject non-JSON wire text")
	}
	if !strings.Contains(err.Error(), "invalid character 'S' looking for beginning of value") {
		t.Errorf("err = %v, want json syntax error", err)
	}
}
