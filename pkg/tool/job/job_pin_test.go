package job

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

// Pin tests for the job.go mutation sweep; assertions are exact so a single
// changed byte flips the test. Mutant line:mutator pairs are in the campaign
// ledger.

func TestPinJob_Metadata(t *testing.T) {
	t.Parallel()
	reg := &stubRegistry{}
	tl := NewJob(reg)
	if tl.Name() != "Job" {
		t.Errorf("Name = %q", tl.Name())
	}
	if len(tl.Aliases()) != 1 || tl.Aliases()[0] != "KillShell" {
		t.Errorf("Aliases = %v", tl.Aliases())
	}
	if tl.MaxResultSize() != 100000 {
		t.Errorf("MaxResultSize = %d", tl.MaxResultSize())
	}
	if !strings.Contains(tl.Prompt(), "job") {
		t.Errorf("Prompt = %.40q", tl.Prompt())
	}
	if !tl.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe = false")
	}
	if sh, ok := tl.(tool.ToolWithSearchHint); !ok || sh.SearchHint() != "manage background jobs -- poll output or stop a running job" {
		t.Errorf("SearchHint = %q", sh.SearchHint())
	}
	if d, ok := tl.(interface{ IsDeferred() bool }); ok && d.IsDeferred() {
		t.Error("IsDeferred = true, want false")
	}

	// Description falls back on unparseable input and names the action.
	for _, c := range []struct {
		input string
		want  string
	}{
		{`{"action":"list"}`, "List"},
		{`{"action":"poll","job_id":" j7 "}`, "Poll j7"},
		{`{"action":"stop","job_id":" j7 "}`, "Stop j7"},
		{`{"action":"weird"}`, "Manage job"},
		{`{bad`, "Manage job"},
	} {
		got, err := tl.Description(json.RawMessage(c.input))
		if err != nil || got != c.want {
			t.Errorf("Description(%s) = (%q, %v), want %q", c.input, got, err, c.want)
		}
	}

	// Read-only classification: list/poll yes, stop and unknown no.
	for _, c := range []struct {
		input string
		want  bool
	}{
		{`{"action":"list"}`, true},
		{`{"action":"poll"}`, true},
		{`{"action":"stop"}`, false},
		{`{"action":"weird"}`, false},
		{`{bad`, false},
	} {
		if got := tl.IsReadOnly(json.RawMessage(c.input)); got != c.want {
			t.Errorf("IsReadOnly(%s) = %v, want %v", c.input, got, c.want)
		}
	}
}

func TestPinJob_WireDecode(t *testing.T) {
	t.Parallel()
	// Poll wire text: header line plus indented output, blank-line parts.
	poll := wirePollText(&PollResult{RetrievalStatus: "success", Task: &JobInfo{
		ID: "j1", Type: "shell", Status: "completed", ExitCode: 3, Output: "line one\n\nline two",
	}})
	decoded, err := decodeWireXML(poll)
	if err != nil {
		t.Fatalf("decodeWireXML: %v", err)
	}
	task := decoded.Poll.Task
	if task == nil {
		t.Fatal("decoded poll has no task")
	}
	if task.ID != "j1" || task.Type != "shell" || task.Status != "completed" {
		t.Errorf("decoded task = %+v", *task)
	}
	if task.ExitCode != 3 {
		t.Errorf("decoded exit = %v, want 3", task.ExitCode)
	}
	if task.Output != "line one\n\nline two" {
		t.Errorf("decoded output = %q", task.Output)
	}

	// List wire text: one block per task, every tag consumed in order.
	list := wireListText(&ListResult{Jobs: []*JobInfo{
		{ID: "j1", Type: "shell", Status: "running", Command: "sleep 5"},
		{ID: "j2", Type: "agent", Status: "completed", ExitCode: 0, Output: "done"},
	}})
	decodedList, err := decodeWireXML(list)
	if err != nil {
		t.Fatalf("decodeWireXML(list): %v", err)
	}
	if decodedList.List == nil || len(decodedList.List.Jobs) != 2 {
		t.Fatalf("decoded list = %+v, want two jobs", decodedList)
	}
	j2 := decodedList.List.Jobs[1]
	if j2.ID != "j2" || j2.Type != "agent" || j2.Status != "completed" {
		t.Errorf("decoded j2 = %+v", *j2)
	}
	if j1 := decodedList.List.Jobs[0]; j1.ID != "j1" || j1.Status != "running" || j1.Command != "sleep 5" {
		t.Errorf("decoded j1 = %+v", *j1)
	}
}
