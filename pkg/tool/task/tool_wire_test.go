package task

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// taskWireText drives the wire path the engine uses (FormatWireBlocks →
// single text block) and returns the block text.
func taskWireText(t *testing.T, data any) string {
	t.Helper()
	wb, ok := New(NewList(""), nil).(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("Task tool must implement ToolWithWireBlocks")
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

// Source: TaskCreateTool.ts:135, TaskUpdateTool.ts:364-405,
// TaskGetTool.ts:99-127, TaskListTool.ts:91-115 — one segment per TS tool,
// joined with a blank line; entries within a segment join with "\n".
func TestTaskWire_Segments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		out  *TasksOutput
		want string
	}{
		{
			name: "created success",
			out:  &TasksOutput{Created: []CreateResult{{ID: "5", Subject: "Fix bug"}}},
			want: "Task #5 created successfully: Fix bug",
		},
		{
			name: "created failure",
			out:  &TasksOutput{Created: []CreateResult{{Subject: "Fix bug", Error: "storage locked"}}},
			want: `Failed to create task "Fix bug": storage locked`,
		},
		{
			name: "two created entries join with newline",
			out: &TasksOutput{Created: []CreateResult{
				{ID: "1", Subject: "A"},
				{ID: "2", Subject: "B"},
			}},
			want: "Task #1 created successfully: A\nTask #2 created successfully: B",
		},
		{
			name: "updated success with fields",
			out:  &TasksOutput{Updated: []UpdateResult{{Success: true, TaskID: "5", UpdatedFields: []string{"status", "owner"}}}},
			want: "Updated task #5 status, owner",
		},
		{
			name: "updated success without fields keeps trailing space",
			out:  &TasksOutput{Updated: []UpdateResult{{Success: true, TaskID: "5"}}},
			want: "Updated task #5 ",
		},
		{
			name: "updated failure with error",
			out:  &TasksOutput{Updated: []UpdateResult{{Success: false, TaskID: "5", Error: "storage locked"}}},
			want: "storage locked",
		},
		{
			name: "updated failure not found",
			out:  &TasksOutput{Updated: []UpdateResult{{Success: false, TaskID: "5"}}},
			want: "Task #5 not found",
		},
		{
			name: "deleted success",
			out:  &TasksOutput{Deleted: []DeleteResult{{ID: "5", Success: true}}},
			want: "Deleted task #5",
		},
		{
			name: "deleted failure",
			out:  &TasksOutput{Deleted: []DeleteResult{{ID: "5", Success: false, Error: "not found"}}},
			want: "Failed to delete task #5: not found",
		},
		{
			name: "get miss",
			out:  &TasksOutput{Get: &GetResult{Task: nil}},
			want: "Task not found",
		},
		{
			name: "get hit with dependency lines",
			out: &TasksOutput{Get: &GetResult{Task: &GetOutputTask{
				ID: "3", Subject: "Ship it", Description: "Do the thing",
				Status: StatusPending, Blocks: []string{"4", "5"}, BlockedBy: []string{"1", "2"},
			}}},
			want: "Task #3: Ship it\nStatus: pending\nDescription: Do the thing\nBlocked by: #1, #2\nBlocks: #4, #5",
		},
		{
			name: "list empty",
			out:  &TasksOutput{List: &ListResult{Tasks: []ListOutputTask{}}},
			want: "No tasks found",
		},
		{
			name: "list with owner and blocked annotations",
			out: &TasksOutput{List: &ListResult{Tasks: []ListOutputTask{
				{ID: "1", Subject: "First", Status: "completed"},
				{ID: "2", Subject: "Second", Status: "in_progress", Owner: "agent-1", BlockedBy: []string{"1"}},
			}}},
			want: "#1 [completed] First\n#2 [in_progress] Second (agent-1) [blocked by #1]",
		},
		{
			name: "combined segments joined with blank line",
			out: &TasksOutput{
				Created: []CreateResult{{ID: "1", Subject: "A"}},
				Updated: []UpdateResult{{Success: true, TaskID: "1", UpdatedFields: []string{"status"}}},
				List:    &ListResult{Tasks: []ListOutputTask{{ID: "1", Subject: "A", Status: "pending"}}},
			},
			want: "Task #1 created successfully: A\n\nUpdated task #1 status\n\n#1 [pending] A",
		},
		{
			name: "no segments yields empty wire",
			out:  &TasksOutput{},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := taskWireText(t, tc.out); got != tc.want {
				t.Errorf("wire text = %q, want %q", got, tc.want)
			}
		})
	}
}

// Non-*TasksOutput data keeps the JSON fallback so anything DecodeResult
// reconstructs still serializes instead of panicking.
func TestTaskWire_NonOutputFallsBackToJSON(t *testing.T) {
	t.Parallel()
	if got := taskWireText(t, "x"); got != `"x"` {
		t.Errorf("wire text = %q, want %q", got, `"x"`)
	}
}

func TestTaskDecodeResult_LegacyJSONWire(t *testing.T) {
	t.Parallel()
	tt := New(NewList(""), nil)
	raw := tool.WrapSingleBlock(`{"created":[{"id":"5","subject":"Fix bug"}]}`)
	v, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	o, ok := v.(*TasksOutput)
	if !ok {
		t.Fatalf("DecodeResult returned %T, want *TasksOutput", v)
	}
	if len(o.Created) != 1 || o.Created[0].ID != "5" || o.Created[0].Subject != "Fix bug" {
		t.Errorf("Created = %+v, want [{ID:5 Subject:Fix bug}]", o.Created)
	}
}

// Wire text that is a bare JSON object decodes into an all-zero TasksOutput
// (unknown fields ignored), which replay would render as "No changes"
// instead of falling back to the wire text.
func TestTaskDecodeResult_RejectsJSONObjectWire(t *testing.T) {
	t.Parallel()
	tt := New(NewList(""), nil)
	raw := tool.WrapSingleBlock(`{"name":"gbot","version":"1.0"}`)
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject JSON-object wire without identifying fields")
	}
	if !strings.Contains(err.Error(), "identifying fields") {
		t.Errorf("err = %v, want it to mention identifying fields", err)
	}
}

func TestTaskDecodeResult_RejectsPlainTextWire(t *testing.T) {
	t.Parallel()
	tt := New(NewList(""), nil)
	raw := tool.WrapSingleBlock("Task #5 created successfully: Fix bug")
	_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(raw)
	if err == nil {
		t.Fatal("DecodeResult must reject non-JSON wire text")
	}
	if !strings.Contains(err.Error(), "invalid character 'T' looking for beginning of value") {
		t.Errorf("err = %v, want json syntax error", err)
	}
}

// The Get segment's dependency lines are guarded independently: an empty
// slice must render NEITHER line, and each line must appear only for its
// own list.
func TestTaskWire_GetDependencyLinesIndependent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		task *GetOutputTask
		want string
	}{
		{
			name: "no dependencies renders neither line",
			task: &GetOutputTask{ID: "1", Subject: "Solo", Status: StatusPending, Description: "d"},
			want: "Task #1: Solo\nStatus: pending\nDescription: d",
		},
		{
			name: "blockedBy only",
			task: &GetOutputTask{ID: "1", Subject: "Solo", Status: StatusPending, Description: "d", BlockedBy: []string{"9"}},
			want: "Task #1: Solo\nStatus: pending\nDescription: d\nBlocked by: #9",
		},
		{
			name: "blocks only",
			task: &GetOutputTask{ID: "1", Subject: "Solo", Status: StatusPending, Description: "d", Blocks: []string{"9"}},
			want: "Task #1: Solo\nStatus: pending\nDescription: d\nBlocks: #9",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := taskWireText(t, &TasksOutput{Get: &GetResult{Task: tc.task}})
			if got != tc.want {
				t.Errorf("wire text = %q, want %q", got, tc.want)
			}
		})
	}
}

// The Task tool raises the persistence cap to 100000; the value reaches
// the engine through MaxResultSize, so pin it exactly.
func TestTaskTool_MaxResultSize(t *testing.T) {
	t.Parallel()
	if got := New(NewList(""), nil).MaxResultSize(); got != 100000 {
		t.Errorf("MaxResultSize() = %d, want 100000", got)
	}
}

// An empty JSON array leaves its slice field nil, so a wire whose only
// section is an empty array carries no identifying field and must fall back
// rather than replay as "No changes".
func TestTaskDecodeResult_EmptySectionsFallBack(t *testing.T) {
	t.Parallel()
	tt := New(NewList(""), nil)
	for _, wire := range []string{
		`{"created":[]}`,
		`{"updated":[]}`,
		`{"deleted":[]}`,
	} {
		_, err := tt.(tool.ToolWithDecodeResult).DecodeResult(tool.WrapSingleBlock(wire))
		if err == nil {
			t.Errorf("DecodeResult(%s) = nil error, want the identifying-fields rejection", wire)
		}
	}
}
