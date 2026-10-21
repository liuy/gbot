package wui

import (
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/job"
)

// jobReplayRaw marshals the engine's Job wire blocks into the persisted
// tool_result content shape the history path feeds to renderToolOutput.
func jobReplayRaw(t *testing.T, out *job.JobOutput) json.RawMessage {
	t.Helper()
	jt := job.NewJob(nil)
	wb, ok := jt.(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("Job tool must implement ToolWithWireBlocks")
	}
	raw, err := json.Marshal(wb.FormatWireBlocks(out))
	if err != nil {
		t.Fatalf("marshal wire blocks: %v", err)
	}
	return raw
}

// History replay of Job results goes through the tool's DecodeResult +
// RenderResult, so the wui body carries the same string the TUI shows: the
// engine computes DisplayOutput with that very function.
func TestRenderToolOutput_JobListReplay(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{"Job": job.NewJob(nil)}
	raw := jobReplayRaw(t, &job.JobOutput{List: &job.ListResult{Jobs: []*job.JobInfo{
		{ID: "bg-1", Type: "bash", Status: "running", Command: "make check", ElapsedMs: 720000},
		{ID: "agent-2", Type: "agent", Status: "completed", Description: "research"},
	}}})
	got := renderToolOutput("Job", raw, tools)
	want := "bg-1 · bash · running · 12m 0s · make check\n" +
		"agent-2 · agent · completed · research"
	if got != want {
		t.Errorf("renderToolOutput(Job list) = %q, want %q", got, want)
	}
}

func TestRenderToolOutput_JobPollReplay(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{"Job": job.NewJob(nil)}
	raw := jobReplayRaw(t, &job.JobOutput{Poll: &job.PollResult{
		RetrievalStatus: "success",
		Task: &job.JobInfo{
			ID: "bg-1", Type: "bash", Status: "completed", ExitCode: 0,
			Output: "line one\nline two\n",
		},
	}})
	got := renderToolOutput("Job", raw, tools)
	want := "bg-1 · bash · completed · exit 0\n    line one\n    line two"
	if got != want {
		t.Errorf("renderToolOutput(Job poll) = %q, want %q", got, want)
	}
}

// Pre-XML list/stop text from old sessions fails DecodeResult and falls
// through to as-is display.
func TestRenderToolOutput_JobLegacyTextFallsThrough(t *testing.T) {
	t.Parallel()
	tools := map[string]tool.Tool{"Job": job.NewJob(nil)}
	for _, legacy := range []string{
		`job_id=bg-1 status=running command="x"`,
		"Successfully stopped job bg-1 (bash): x",
		"No jobs",
	} {
		textJSON, err := json.Marshal(legacy)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		raw := json.RawMessage(`[{"type":"text","text":` + string(textJSON) + `}]`)
		if got := renderToolOutput("Job", raw, tools); got != legacy {
			t.Errorf("renderToolOutput(%q) = %q, want as-is passthrough", legacy, got)
		}
	}
}
