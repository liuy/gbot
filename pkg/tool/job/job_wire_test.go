package job

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

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

// Stop segment carries the same tag style as the list lines — the unified
// XML wire for all Job actions (plan: stop XML).
func TestJobWire_Stop(t *testing.T) {
	t.Parallel()
	got := jobWireText(t, &JobOutput{Stop: &StopResult{
		Status:  "killed",
		JobID:   "bg-3",
		Type:    "bash",
		Command: "sleep 100",
	}})
	want := "<job_id>bg-3</job_id><type>bash</type><status>killed</status><command>sleep 100</command>"
	if got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// Stop without a command drops the tag instead of echoing the job ID — the
// id is already its own segment on the render line.
func TestJobWire_StopNoCommandOmitsTag(t *testing.T) {
	t.Parallel()
	got := jobWireText(t, &JobOutput{Stop: &StopResult{
		Status: "killed",
		JobID:  "bg-3",
		Type:   "agent",
	}})
	if want := "<job_id>bg-3</job_id><type>agent</type><status>killed</status>"; got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// Source: List format mirrors renderListResult — one XML line per job with
// the command → description fallback.
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
	want := "<job_id>agent-1</job_id><type>agent</type><status>running</status><command>research</command>\n" +
		"<job_id>fork-2</job_id><type>fork</type><status>running</status><command>audit</command>\n" +
		"<job_id>bg-3</job_id><type>bash</type><status>completed</status><command>make check</command>"
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
	want := "<job_id>bg-1</job_id><status>running</status><command>go test ./...</command>\n" +
		"<job_id>bg-2</job_id><status>completed</status><command>agent: fix bug</command>\n" +
		"<job_id>bg-3</job_id><status>killed</status>"
	if got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
}

// The elapsed tag is humanized by the repo duration formatter and only
// appears when a value exists (frozen by executeList). ElapsedMs=1 pins the
// >0 emit threshold on both the wire and render sides.
func TestJobWire_ListElapsed(t *testing.T) {
	t.Parallel()
	got := jobWireText(t, &JobOutput{List: &ListResult{Jobs: []*JobInfo{
		{ID: "bg-1", Type: "bash", Status: "running", Command: "make check", ElapsedMs: 12 * 60 * 1000},
		{ID: "bg-2", Type: "bash", Status: "completed", Command: "echo hi", ElapsedMs: 193000},
		{ID: "bg-3", Type: "bash", Status: "running", Command: "just started", ElapsedMs: 1},
		{ID: "bg-4", Type: "bash", Status: "killed"},
	}}})
	want := "<job_id>bg-1</job_id><type>bash</type><status>running</status><elapsed>12m 0s</elapsed><command>make check</command>\n" +
		"<job_id>bg-2</job_id><type>bash</type><status>completed</status><elapsed>3m 13s</elapsed><command>echo hi</command>\n" +
		"<job_id>bg-3</job_id><type>bash</type><status>running</status><elapsed>0.0s</elapsed><command>just started</command>\n" +
		"<job_id>bg-4</job_id><type>bash</type><status>killed</status>"
	if got != want {
		t.Errorf("wire text = %q, want %q", got, want)
	}
	rendered := renderListResult(&ListResult{Jobs: []*JobInfo{
		{ID: "bg-3", Type: "bash", Status: "running", Command: "just started", ElapsedMs: 1},
	}})
	if rendered != "bg-3 · bash · running · 0.0s · just started" {
		t.Errorf("render = %q, want the 1ms elapsed segment present", rendered)
	}
}

// executeList freezes the elapsed: running jobs get now-start, terminal jobs
// get their total runtime, and the registry's own snapshot is not mutated.
func TestJobList_ExecuteFreezesElapsed(t *testing.T) {
	// NOT parallel: swaps the package-level timeNow var.
	fakeNow := time.UnixMilli(10_000_000)
	saved := timeNow
	timeNow = func() time.Time { return fakeNow }
	defer func() { timeNow = saved }()

	shared := &JobInfo{ID: "bg-1", Type: "bash", Status: "running", StartedMs: 10_000_000 - 90_000}
	reg := &sharedSnapshotRegistry{jobs: []*JobInfo{
		shared,
		{ID: "bg-2", Type: "bash", Status: "completed", DurationMs: 5000},
		{ID: "bg-3", Type: "agent", Status: "killed"},
		{ID: "bg-4", Type: "bash", Status: "running"}, // no StartedMs
		// StartedMs=1 separates "<= 0" from "<= 1": both guards return 0 for
		// a missing start, only the correct bound computes now−1.
		{ID: "bg-5", Type: "bash", Status: "running", StartedMs: 1},
	}}

	res := executeList(reg)
	if len(res.Jobs) != 5 {
		t.Fatalf("len(Jobs) = %d, want 5", len(res.Jobs))
	}
	if res.Jobs[0].ElapsedMs != 90_000 {
		t.Errorf("running job ElapsedMs = %d, want 90000 (now-started)", res.Jobs[0].ElapsedMs)
	}
	if res.Jobs[1].ElapsedMs != 5000 {
		t.Errorf("completed job ElapsedMs = %d, want 5000 (DurationMs)", res.Jobs[1].ElapsedMs)
	}
	if res.Jobs[2].ElapsedMs != 0 {
		t.Errorf("terminal job without DurationMs ElapsedMs = %d, want 0 (unknown → omitted)", res.Jobs[2].ElapsedMs)
	}
	if res.Jobs[3].ElapsedMs != 0 {
		t.Errorf("running job without StartedMs ElapsedMs = %d, want 0 (not now−0)", res.Jobs[3].ElapsedMs)
	}
	if res.Jobs[4].ElapsedMs != 9_999_999 {
		t.Errorf("StartedMs=1 job ElapsedMs = %d, want 9999999 (now−1, not the missing-start 0)", res.Jobs[4].ElapsedMs)
	}
	if shared.ElapsedMs != 0 {
		t.Errorf("registry snapshot mutated: ElapsedMs = %d, want 0", shared.ElapsedMs)
	}
}

// sharedSnapshotRegistry returns its jobs by reference — the aliasing shape
// executeList must not mutate.
type sharedSnapshotRegistry struct {
	jobs []*JobInfo
	mockRegistry
}

func (s *sharedSnapshotRegistry) List() []*JobInfo { return s.jobs }

// Segments join with a blank line (List, Poll, Stop in renderJobOutput order).
func TestJobWire_CombinedSegments(t *testing.T) {
	t.Parallel()
	got := jobWireText(t, &JobOutput{
		List: &ListResult{Jobs: []*JobInfo{{ID: "bg-1", Status: "running", Command: "x"}}},
		Poll: &PollResult{RetrievalStatus: "timeout"},
		Stop: &StopResult{JobID: "bg-1", Type: "bash", Status: "killed", Command: "x"},
	})
	want := "<job_id>bg-1</job_id><status>running</status><command>x</command>\n\n<retrieval_status>timeout</retrieval_status>\n\n" +
		"<job_id>bg-1</job_id><type>bash</type><status>killed</status><command>x</command>"
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

// ---------------------------------------------------------------------------
// Wire XML → DecodeResult → RenderResult round-trip (history replay path)
// ---------------------------------------------------------------------------

func decodeWireText(t *testing.T, text string) *JobOutput {
	t.Helper()
	tl := NewJob(newMockRegistry())
	v, err := tl.(tool.ToolWithDecodeResult).DecodeResult(tool.WrapSingleBlock(text))
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*JobOutput)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *JobOutput", v)
	}
	return o
}

// Replay of a poll renders the friendly header + indented body, not raw tags.
func TestJobReplay_PollRendersFriendly(t *testing.T) {
	t.Parallel()
	tl := NewJob(newMockRegistry())
	wire := jobWireText(t, &JobOutput{Poll: &PollResult{
		RetrievalStatus: "success",
		Task: &JobInfo{
			ID: "bg-1", Type: "bash", Status: "completed",
			Command: "go build ./...", Output: "line one\nline two\n", ExitCode: 0,
		},
	}})
	got := tl.RenderResult(decodeWireText(t, wire))
	want := "bg-1 · bash · completed · exit 0\n    line one\n    line two"
	if got != want {
		t.Errorf("replay render = %q, want %q", got, want)
	}
}

// Replay of a list restores every field including the humanized elapsed.
func TestJobReplay_ListRendersFriendly(t *testing.T) {
	t.Parallel()
	tl := NewJob(newMockRegistry())
	wire := jobWireText(t, &JobOutput{List: &ListResult{Jobs: []*JobInfo{
		{ID: "bg-1", Type: "bash", Status: "running", Command: "make check", ElapsedMs: 720000},
		{ID: "agent-2", Type: "agent", Status: "completed", Description: "research"},
	}}})
	got := tl.RenderResult(decodeWireText(t, wire))
	want := "bg-1 · bash · running · 12m 0s · make check\n" +
		"agent-2 · agent · completed · research"
	if got != want {
		t.Errorf("replay render = %q, want %q", got, want)
	}
}

// A stop wire line has the same shape as a list line; replay renders the
// same string renderStopResult produces.
func TestJobReplay_StopRendersFriendly(t *testing.T) {
	t.Parallel()
	tl := NewJob(newMockRegistry())
	wire := jobWireText(t, &JobOutput{Stop: &StopResult{JobID: "bg-3", Type: "bash", Status: "killed", Command: "sleep 100"}})
	v := decodeWireText(t, wire)
	if v.List == nil || v.Stop != nil {
		t.Fatalf("decoded = %+v, want stop wire decoded as a one-job list (identical render)", v)
	}
	got := tl.RenderResult(v)
	if want := "bg-3 · bash · killed · sleep 100"; got != want {
		t.Errorf("replay render = %q, want %q", got, want)
	}
}

// Poll output containing look-alike tags survives the round-trip — the
// parser takes everything between the fixed output prefix and suffix.
func TestJobReplay_PollOutputWithEmbeddedTags(t *testing.T) {
	t.Parallel()
	wire := jobWireText(t, &JobOutput{Poll: &PollResult{
		RetrievalStatus: "success",
		Task: &JobInfo{
			ID: "bg-1", Type: "bash", Status: "completed", ExitCode: 0,
			Output: "echo '<status>running</status>'\n</output> done",
		},
	}})
	o := decodeWireText(t, wire)
	want := "echo '<status>running</status>'\n</output> done"
	if o.Poll.Task.Output != want {
		t.Errorf("round-trip Output = %q, want %q", o.Poll.Task.Output, want)
	}
}

// Command containing closing-tag look-alikes survives the round-trip.
func TestJobReplay_ListCommandWithEmbeddedTags(t *testing.T) {
	t.Parallel()
	wire := jobWireText(t, &JobOutput{List: &ListResult{Jobs: []*JobInfo{
		{ID: "bg-1", Type: "bash", Status: "running", Command: "echo a</command>b && make <all>"},
	}}})
	o := decodeWireText(t, wire)
	want := "echo a</command>b && make <all>"
	if o.List.Jobs[0].Command != want {
		t.Errorf("round-trip Command = %q, want %q", o.List.Jobs[0].Command, want)
	}
}

// The Bash tool passes the LLM's command through verbatim, so a command can
// span several lines and one wire job line spans several "\n"-split
// fragments. Replay must re-join the fragments: only a fragment starting
// with <job_id> opens a new job, everything else continues the previous
// command.
func TestJobReplay_ListMultiLineCommandRoundTrip(t *testing.T) {
	t.Parallel()
	tl := NewJob(newMockRegistry())
	wire := jobWireText(t, &JobOutput{List: &ListResult{Jobs: []*JobInfo{
		{ID: "bg-1", Type: "bash", Status: "running", Command: "go test ./..."},
		{ID: "bg-2", Type: "bash", Status: "running", ElapsedMs: 5000, Command: "for i in 1 2 3\ndo\necho $i\ndone"},
	}}})
	o := decodeWireText(t, wire)
	if len(o.List.Jobs) != 2 {
		t.Fatalf("len(Jobs) = %d, want 2 (continuations must not become jobs)", len(o.List.Jobs))
	}
	wantCmd := "for i in 1 2 3\ndo\necho $i\ndone"
	if o.List.Jobs[1].Command != wantCmd {
		t.Errorf("round-trip Command = %q, want %q", o.List.Jobs[1].Command, wantCmd)
	}
	if o.List.Jobs[1].ElapsedMs != 5000 {
		t.Errorf("round-trip ElapsedMs = %d, want 5000", o.List.Jobs[1].ElapsedMs)
	}
	got := tl.RenderResult(o)
	want := "bg-1 · bash · running · go test ./...\n" +
		"bg-2 · bash · running · 5s · for i in 1 2 3\ndo\necho $i\ndone"
	if got != want {
		t.Errorf("replay render = %q, want %q", got, want)
	}
}

// A multi-line command whose <command> tag never closes is still malformed —
// re-joined fragments must land inside a closed tag or the line is rejected.
func TestJobReplay_UnterminatedMultiLineCommandRejected(t *testing.T) {
	t.Parallel()
	if _, err := decodeWireXML("<job_id>a</job_id><command>x\nstill open"); err == nil {
		t.Error("decodeWireXML accepted an unterminated multi-line command, want rejection")
	}
}

// A continuation fragment with no preceding job line has no command to
// continue — decodeWireXML's <job_id> prefix gate keeps this off the public
// path, so the guard is pinned by calling decodeWireJobLines directly.
func TestJobReplay_ContinuationWithoutJobRejected(t *testing.T) {
	t.Parallel()
	_, err := decodeWireJobLines("junk\nx")
	if err == nil || err.Error() != "job: job line missing job_id" {
		t.Errorf("err = %v, want %q", err, "job: job line missing job_id")
	}
}

// Timeout poll with no task tags decodes with a nil task.
func TestJobReplay_PollTimeoutWithoutTask(t *testing.T) {
	t.Parallel()
	o := decodeWireText(t, "<retrieval_status>timeout</retrieval_status>")
	if o.Poll == nil || o.Poll.RetrievalStatus != "timeout" || o.Poll.Task != nil {
		t.Errorf("decoded = %+v, want poll timeout with nil task", o)
	}
}

// An output part with no preceding task tags still lands on a task — the
// parser tolerates tag order for this never-generated-but-parseable shape.
func TestJobReplay_PollOutputWithoutTaskTags(t *testing.T) {
	t.Parallel()
	o := decodeWireText(t, "<retrieval_status>success</retrieval_status>\n\n<output>\nx\n</output>")
	if o.Poll == nil || o.Poll.Task == nil || o.Poll.Task.Output != "x" {
		t.Errorf("decoded = %+v, want poll with output-only task", o)
	}
}

// Scalar task tags without a preceding task_id still build the snapshot.
func TestJobReplay_PollTagOrderTolerance(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		wire string
		want func(*JobInfo) bool
		desc string
	}{
		{"<retrieval_status>success</retrieval_status>\n\n<task_type>bash</task_type>",
			func(t *JobInfo) bool { return t.Type == "bash" }, "task_type"},
		{"<retrieval_status>success</retrieval_status>\n\n<status>running</status>",
			func(t *JobInfo) bool { return t.Status == "running" }, "status"},
		{"<retrieval_status>success</retrieval_status>\n\n<exit_code>3</exit_code>",
			func(t *JobInfo) bool { return t.ExitCode == 3 }, "exit_code"},
	} {
		o := decodeWireText(t, tc.wire)
		if o.Poll == nil || o.Poll.Task == nil {
			t.Errorf("DecodeResult(%s): decoded = %+v, want a task snapshot", tc.desc, o)
			continue
		}
		if !tc.want(o.Poll.Task) {
			t.Errorf("DecodeResult(%s): task = %+v, field not recovered", tc.desc, o.Poll.Task)
		}
	}
}

// Pre-XML text formats, arbitrary prose, and malformed XML fail the parse,
// so replay falls through to as-is display.
func TestJobDecodeResult_RejectsLegacyTextFormats(t *testing.T) {
	t.Parallel()
	for _, legacy := range []string{
		"job_id=bg-1 status=running command=\"x\"",
		"No jobs",
		"<job_id>bg-1</job_id><status>running", // truncated tag
		"<job_id>bg-1</job_id><status>running</status> trailing",
		// Empty id and non-tag second line.
		"<job_id></job_id>",
		"<job_id>a</job_id><status>running</status>\njunk",
	} {
		if _, err := decodeWireXML(legacy); err == nil {
			t.Errorf("decodeWireXML(%q) accepted, want rejection", legacy)
		}
	}
}

// Exact rejection messages pin the diagnostics replay relies on and the
// preview truncation that keeps them bounded.
func TestJobDecodeResult_RejectionMessages(t *testing.T) {
	t.Parallel()
	z60 := strings.Repeat("z", 60)
	z61 := strings.Repeat("z", 61)
	for _, tc := range []struct {
		wire    string
		wantErr string
	}{
		{
			"<retrieval_status>ok</retrieval_status>\n<task_id>bg-1</task_id>",
			`job: poll wire parts not blank-line separated at "\n<task_id>bg-1</task_id>"`,
		},
		{
			"<retrieval_status>ok</retrieval_status>\n\n<exit_code>x</exit_code>",
			`job: exit_code "x": strconv.Atoi: parsing "x": invalid syntax`,
		},
		{
			"<retrieval_status>ok</retrieval_status>\n\n<status>running</status>\n\n<output>x</output>",
			`job: unrecognized poll part "<output>x</output>"`,
		},
		{
			// Output prefix without the closing form must not slice garbage.
			"<retrieval_status>ok</retrieval_status>\n\n<output>\nabc",
			`job: unrecognized poll part "<output>\nabc"`,
		},
		{
			// Closing form without the opening prefix is plain junk.
			"<retrieval_status>ok</retrieval_status>\n\nx\n</output>",
			`job: unrecognized poll part "x\n</output>"`,
		},
		{
			"<job_id>bg-1</job_id><elapsed>bogus</elapsed>",
			`job: elapsed "bogus": time: invalid duration "bogus"`,
		},
		{
			// Command open tag without its close is trailing content.
			"<job_id>a</job_id><command>x",
			`job: job line has trailing non-command content "<command>x"`,
		},
		{
			// Close tag without its open is trailing content too.
			"<job_id>a</job_id>x</command>",
			`job: job line has trailing non-command content "x</command>"`,
		},
		{
			// Preview threshold: 60 chars stay verbatim.
			"<job_id>bg-1</job_id>" + z60,
			`job: job line has trailing non-command content "` + z60 + `"`,
		},
		{
			// 61 chars truncate to 60 plus ellipsis.
			"<job_id>bg-1</job_id>" + z61,
			"job: job line has trailing non-command content \"" + z60 + "…\"",
		},
		{
			// A malformed line must still error when a later line follows:
			// the line boundary cannot mask the parse error.
			"<job_id>a</job_id><elapsed>bogus</elapsed>\n<job_id>b</job_id><status>running</status>",
			`job: elapsed "bogus": time: invalid duration "bogus"`,
		},
		{
			"<retrieval_status>ok",
			`job: poll wire missing retrieval_status`,
		},
	} {
		_, err := decodeWireXML(tc.wire)
		if err == nil {
			t.Errorf("decodeWireXML(%q) accepted, want error %q", tc.wire, tc.wantErr)
			continue
		}
		if err.Error() != tc.wantErr {
			t.Errorf("decodeWireXML(%q) err = %q, want %q", tc.wire, err.Error(), tc.wantErr)
		}
	}

	// The two wrapped causes must stay reachable through errors.As/Is — a
	// message-identical %v rewrite of either Errorf would break the chain.
	_, exitErr := decodeWireXML("<retrieval_status>ok</retrieval_status>\n\n<exit_code>x</exit_code>")
	if _, ok := errors.AsType[*strconv.NumError](exitErr); !ok {
		t.Errorf("exit_code error %v lost the strconv cause via errors.As", exitErr)
	}
	// ParseDuration's error type is unexported in current Go and not
	// identity-stable across calls, so pin the chain via Unwrap + message.
	_, durErr := decodeWireXML("<job_id>bg-1</job_id><elapsed>bogus</elapsed>")
	_, wantDurErr := time.ParseDuration("bogus")
	cause := errors.Unwrap(durErr)
	if cause == nil || cause.Error() != wantDurErr.Error() {
		t.Errorf("elapsed error %v lost the ParseDuration cause (unwrap = %v)", durErr, cause)
	}
}

// parseElapsedMs inverts formatElapsedMs exactly; failures return the zero
// duration alongside the error.
func TestParseElapsedMs(t *testing.T) {
	t.Parallel()
	for s, want := range map[string]int64{
		"0.1s":     100,
		"5s":       5000,
		"12m 0s":   720000,
		"3m 13s":   193000,
		"1h 2m 3s": 3723000,
	} {
		got, err := parseElapsedMs(s)
		if err != nil || got != want {
			t.Errorf("parseElapsedMs(%q) = %d, %v; want %d, nil", s, got, err, want)
		}
	}
	ms, err := parseElapsedMs("bogus")
	if err == nil {
		t.Fatal("parseElapsedMs(bogus) err = nil, want error")
	}
	if ms != 0 {
		t.Errorf("parseElapsedMs(bogus) ms = %d, want 0 alongside the error", ms)
	}
}
