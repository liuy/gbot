package short

import (
	"strings"
	"testing"
)

// restore.go:351,353 — the mismatch warning reports the signed delta and the
// distance from the end of the chain, not some other arithmetic.
func TestCheckResumeConsistency_WarnsExactDeltaAndAge(t *testing.T) {
	logs := captureSlogDefault(t)

	chain := []*TranscriptMessage{
		{Type: "user", Content: "{}"},
		{
			Type:    "system",
			Subtype: "turn_duration",
			Content: mustMarshal(map[string]any{
				"messageCount": 5, // checkpoint sits at position 1
			}),
		},
		{Type: "user", Content: "{}"},
	}

	CheckResumeConsistency(chain)

	want := `msg="resume consistency check failed" expected=5 actual=1 delta=-4 chain_length=3 checkpoint_age_entries=1`
	if !strings.Contains(logs.String(), want) {
		t.Errorf("log output = %q, want a record containing %q", logs.String(), want)
	}
}

// --- RestoreSkillStateFromMessages (restore.go:20-68) ---

func skillAttachmentBody(skills ...any) string {
	return mustMarshal(map[string]any{"type": "invoked_skills", "skills": skills})
}

// restore.go:21-22 — the skill payload is only read from attachment messages;
// the same JSON body on another message type must stay unread.
func TestRestoreSkillStateFromMessages_NonAttachmentTypeIsIgnored(t *testing.T) {
	state := RestoreSkillStateFromMessages([]*TranscriptMessage{
		{UUID: "u1", Type: "user", Content: skillAttachmentBody(
			map[string]any{"name": "pdf", "path": "/skills/pdf", "content": "body"})},
	})
	if state != nil {
		t.Errorf("state = %+v, want nil: a user message is not an attachment", state)
	}
}

// restore.go:22,27,38 — a skipped entry must not end the scan: the state lives
// in a later attachment, so every guard has to continue rather than break.
func TestRestoreSkillStateFromMessages_SkippedEntriesDoNotEndTheScan(t *testing.T) {
	valid := skillAttachmentBody(map[string]any{"name": "pdf", "path": "/skills/pdf", "content": "body"})
	cases := []struct {
		name     string
		messages []*TranscriptMessage
	}{
		{"non-attachment first", []*TranscriptMessage{
			{UUID: "u1", Type: "user", Content: `[{"type":"text","text":"hi"}]`},
			{UUID: "att1", Type: "attachment", Content: valid},
		}},
		{"unreadable attachment first", []*TranscriptMessage{
			{UUID: "att0", Type: "attachment", Content: `{"type":"invoked_skills"`},
			{UUID: "att1", Type: "attachment", Content: valid},
		}},
		{"non-map skill first", []*TranscriptMessage{
			{UUID: "att1", Type: "attachment", Content: skillAttachmentBody("not-a-map",
				map[string]any{"name": "pdf", "path": "/skills/pdf", "content": "body"})},
		}},
	}

	for _, c := range cases {
		state := RestoreSkillStateFromMessages(c.messages)
		if state == nil {
			t.Fatalf("%s: got nil state, want the skill from the entry after the skipped one", c.name)
		}
		if len(state.InvokedSkills) != 1 || !state.InvokedSkills["pdf"] {
			t.Errorf("%s: InvokedSkills = %v, want exactly one entry pdf=true", c.name, state.InvokedSkills)
		}
	}
}

// restore.go:44 — a skill counts as invoked only when name, path and content
// are all present; every partial combination has to be rejected on its own.
func TestRestoreSkillStateFromMessages_SkillNeedsNamePathAndContent(t *testing.T) {
	cases := []struct {
		name  string
		skill map[string]any
		want  bool
	}{
		{"all fields", map[string]any{"name": "pdf", "path": "/s/pdf", "content": "body"}, true},
		{"name only", map[string]any{"name": "pdf"}, false},
		{"path only", map[string]any{"path": "/s/pdf"}, false},
		{"content only", map[string]any{"content": "body"}, false},
		{"name and path", map[string]any{"name": "pdf", "path": "/s/pdf"}, false},
		{"name and content", map[string]any{"name": "pdf", "content": "body"}, false},
		{"path and content", map[string]any{"path": "/s/pdf", "content": "body"}, false},
	}

	for _, c := range cases {
		state := RestoreSkillStateFromMessages([]*TranscriptMessage{
			{UUID: "att1", Type: "attachment", Content: skillAttachmentBody(c.skill)},
		})
		if !c.want {
			if state != nil {
				t.Errorf("%s: state = %+v, want nil: an incomplete skill is not invoked", c.name, state)
			}
			continue
		}
		if state == nil {
			t.Fatalf("%s: got nil state, want the complete skill restored", c.name)
		}
		if len(state.InvokedSkills) != 1 || !state.InvokedSkills["pdf"] {
			t.Errorf("%s: InvokedSkills = %v, want exactly one entry pdf=true", c.name, state.InvokedSkills)
		}
	}
}

// restore.go:55 — a cron task needs both skill_name and cron_expr; either one
// alone must not append a half-restored task.
func TestRestoreSkillStateFromMessages_CronTaskNeedsNameAndExpr(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want *CronTask
	}{
		{
			name: "both fields",
			body: map[string]any{"type": "cron_task", "skill_name": "daily", "cron_expr": "0 9 * * *", "durable": true},
			want: &CronTask{SkillName: "daily", CronExpr: "0 9 * * *", Durable: true},
		},
		{name: "name only", body: map[string]any{"type": "cron_task", "skill_name": "daily", "durable": true}},
		{name: "expr only", body: map[string]any{"type": "cron_task", "cron_expr": "0 9 * * *"}},
	}

	for _, c := range cases {
		state := RestoreSkillStateFromMessages([]*TranscriptMessage{
			{UUID: "att1", Type: "attachment", Content: mustMarshal(c.body)},
		})
		if c.want == nil {
			if state != nil {
				t.Errorf("%s: state = %+v, want nil: an incomplete cron task is not restored", c.name, state)
			}
			continue
		}
		if state == nil {
			t.Fatalf("%s: got nil state, want the cron task restored", c.name)
		}
		if len(state.CronTasks) != 1 || state.CronTasks[0] != *c.want {
			t.Errorf("%s: CronTasks = %v, want exactly [%v]", c.name, state.CronTasks, *c.want)
		}
	}
}

// --- RestoreAgentFromSession (restore.go:74-121) ---

// restore.go:76-77 — agent settings are only read from attachment messages.
func TestRestoreAgentFromSession_NonAttachmentTypeIsIgnored(t *testing.T) {
	body := mustMarshal(map[string]any{"type": "agent-setting", "agent_type": "Explore", "model": "haiku"})
	state := RestoreAgentFromSession([]*TranscriptMessage{{UUID: "u1", Type: "user", Content: body}})
	if state != nil {
		t.Errorf("state = %+v, want nil: a user message is not an attachment", state)
	}
}

// restore.go:85-86 — an attachment whose type is not agent-setting must not
// produce an AgentState, even when it carries the setting fields.
func TestRestoreAgentFromSession_OtherAttachmentTypeIsIgnored(t *testing.T) {
	body := mustMarshal(map[string]any{"type": "agent-memory", "agent_type": "Explore", "model": "haiku"})
	state := RestoreAgentFromSession([]*TranscriptMessage{{UUID: "att1", Type: "attachment", Content: body}})
	if state != nil {
		t.Errorf("state = %+v, want nil: agent-memory is not agent-setting", state)
	}
}

// restore.go:77,82,86 — the scan keeps walking until it finds the agent-setting
// attachment; a skipped message must not end it.
func TestRestoreAgentFromSession_SkippedEntriesDoNotEndTheScan(t *testing.T) {
	setting := mustMarshal(map[string]any{"type": "agent-setting", "agent_type": "Explore", "model": "haiku"})
	cases := []struct {
		name     string
		messages []*TranscriptMessage
	}{
		{"non-attachment first", []*TranscriptMessage{
			{UUID: "u1", Type: "user", Content: `[{"type":"text","text":"hi"}]`},
			{UUID: "att1", Type: "attachment", Content: setting},
		}},
		{"unreadable attachment first", []*TranscriptMessage{
			{UUID: "att0", Type: "attachment", Content: `{"type":"agent-setting"`},
			{UUID: "att1", Type: "attachment", Content: setting},
		}},
		{"other attachment type first", []*TranscriptMessage{
			{UUID: "att0", Type: "attachment", Content: `{"type":"invoked_skills","skills":[]}`},
			{UUID: "att1", Type: "attachment", Content: setting},
		}},
	}

	for _, c := range cases {
		state := RestoreAgentFromSession(c.messages)
		if state == nil {
			t.Fatalf("%s: got nil state, want the agent setting from the later attachment", c.name)
		}
		if state.AgentType != "Explore" || state.Model != "haiku" {
			t.Errorf("%s: state = %+v, want AgentType=Explore Model=haiku", c.name, *state)
		}
		if len(state.Setting) != 0 || len(state.ToolUseIDs) != 0 {
			t.Errorf("%s: Setting=%v ToolUseIDs=%v, want both empty", c.name, state.Setting, state.ToolUseIDs)
		}
	}
}

// --- ExtractTodosFromTranscript (restore.go:126-186) ---

func todoWriteMsg(uuid, input string) *TranscriptMessage {
	return &TranscriptMessage{
		UUID:    uuid,
		Type:    "assistant",
		Content: `[{"type":"tool_use","id":"tu-` + uuid + `","name":"TodoWrite","input":` + input + `}]`,
	}
}

func assertTodos(t *testing.T, got []*TodoItem, want ...TodoItem) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("got %d todos, want %d (%v)", len(got), len(want), want)
	}
	for i, w := range want {
		if got[i] == nil {
			t.Fatalf("todos[%d] is nil, want %+v", i, w)
		}
		if *got[i] != w {
			t.Errorf("todos[%d] = %+v, want %+v", i, *got[i], w)
		}
	}
}

// restore.go:130-131 — only assistant turns are searched for a TodoWrite call;
// the same tool_use block on a user message must stay unextracted.
func TestExtractTodosFromTranscript_NonAssistantIsNotSearched(t *testing.T) {
	got := ExtractTodosFromTranscript([]*TranscriptMessage{
		{UUID: "u1", Type: "user", Content: `[{"type":"tool_use","id":"tu1","name":"TodoWrite","input":{"todos":[{"id":"7","subject":"S","status":"pending"}]}}]`},
	})
	if got != nil {
		t.Errorf("got %d todos, want nil: a user message is not an assistant turn", len(got))
	}
}

// restore.go:131,144,150,155,160 — the backwards search skips a later turn that
// carries no usable TodoWrite and keeps going to the earlier one.
func TestExtractTodosFromTranscript_BrokenLaterTurnFallsBackToEarlier(t *testing.T) {
	earlier := todoWriteMsg("a-old", `{"todos":[{"id":"7","subject":"Ship it","status":"in_progress","description":"done"}]}`)
	want := TodoItem{ID: "7", Subject: "Ship it", Status: "in_progress", Description: "done"}

	cases := []struct {
		name  string
		later *TranscriptMessage
	}{
		{"non-assistant", &TranscriptMessage{UUID: "u-late", Type: "user", Content: `[{"type":"text","text":"later"}]`}},
		{"assistant without tool use", &TranscriptMessage{UUID: "a-late", Type: "assistant", Content: `[{"type":"text","text":"later"}]`}},
		{"input is not an object", todoWriteMsg("a-late", `[1,2]`)},
		{"input without todos key", todoWriteMsg("a-late", `{}`)},
		{"todos is not a list", todoWriteMsg("a-late", `{"todos":"nope"}`)},
	}

	for _, c := range cases {
		got := ExtractTodosFromTranscript([]*TranscriptMessage{earlier, c.later})
		assertTodos(t, got, want)
	}
}

// restore.go:137 — the block has to be a tool_use named TodoWrite: either half
// of that pair alone must not be taken as a TodoWrite call.
func TestExtractTodosFromTranscript_ToolUseNameMustBeTodoWrite(t *testing.T) {
	todos := `{"todos":[{"id":"7","subject":"S","status":"pending"}]}`
	cases := []struct{ name, content string }{
		{"tool_use with another name", `[{"type":"tool_use","id":"tu1","name":"Task","input":` + todos + `}]`},
		{"TodoWrite name on a non tool_use block", `[{"type":"text","name":"TodoWrite","text":"x","input":` + todos + `}]`},
	}

	for _, c := range cases {
		got := ExtractTodosFromTranscript([]*TranscriptMessage{
			{UUID: "a1", Type: "assistant", Content: c.content},
		})
		if got != nil {
			t.Errorf("%s: got %d todos, want nil (only a tool_use named TodoWrite carries todos)", c.name, len(got))
		}
	}
}

// restore.go:139 — the first TodoWrite block of the turn wins; scanning past
// the match would return the last one instead.
func TestExtractTodosFromTranscript_FirstTodoWriteBlockWins(t *testing.T) {
	msg := &TranscriptMessage{UUID: "a1", Type: "assistant", Content: `[` +
		`{"type":"tool_use","id":"tu1","name":"TodoWrite","input":{"todos":[{"id":"1","subject":"first","status":"pending"}]}},` +
		`{"type":"tool_use","id":"tu2","name":"TodoWrite","input":{"todos":[{"id":"2","subject":"second","status":"pending"}]}}]`}

	got := ExtractTodosFromTranscript([]*TranscriptMessage{msg})
	assertTodos(t, got, TodoItem{ID: "1", Subject: "first", Status: "pending"})
}

// restore.go:167 — a non-map entry in the todos list is skipped, the entries
// after it still make it into the result.
func TestExtractTodosFromTranscript_NonMapTodoDoesNotEndTheList(t *testing.T) {
	msg := todoWriteMsg("a1", `{"todos":["not-a-map",{"id":"7","subject":"Ship it","status":"pending","description":"d"}]}`)

	got := ExtractTodosFromTranscript([]*TranscriptMessage{msg})
	assertTodos(t, got, TodoItem{ID: "7", Subject: "Ship it", Status: "pending", Description: "d"})
}

// --- ComputeRestoredAttributionState (restore.go:191-215) ---

// restore.go:193-194 — only attribution-snapshot messages mark a sub-agent;
// the same snapshot body on another type must fall through to the default.
func TestComputeRestoredAttributionState_NonSnapshotTypeIsIgnored(t *testing.T) {
	body := mustMarshal(map[string]any{"parent_agent_id": "p1", "tool_use_id": "t1"})
	got := ComputeRestoredAttributionState([]*TranscriptMessage{{UUID: "u1", Type: "user", Content: body}})
	if *got != (AttributionState{}) {
		t.Errorf("state = %+v, want the zero value: a user message is not an attribution snapshot", *got)
	}
}

// restore.go:194,199 — a skipped snapshot must not end the scan: the real
// snapshot sits later in the transcript.
func TestComputeRestoredAttributionState_SkippedEntriesDoNotEndTheScan(t *testing.T) {
	valid := mustMarshal(map[string]any{"parent_agent_id": "p1", "tool_use_id": "t1"})
	want := AttributionState{IsSubAgent: true, ParentAgentID: "p1", ToolUseID: "t1"}
	cases := []struct {
		name  string
		first *TranscriptMessage
	}{
		{"non-snapshot first", &TranscriptMessage{UUID: "u1", Type: "user", Content: `{}`}},
		{"unreadable snapshot first", &TranscriptMessage{UUID: "s1", Type: "attribution-snapshot", Content: `{"parent_agent_id":"p0"`}},
	}

	for _, c := range cases {
		got := ComputeRestoredAttributionState([]*TranscriptMessage{
			c.first,
			{UUID: "s2", Type: "attribution-snapshot", Content: valid},
		})
		if *got != want {
			t.Errorf("%s: state = %+v, want %+v", c.name, *got, want)
		}
	}
}

// --- ComputeStandaloneAgentContext (restore.go:220-257) ---

// restore.go:224-225 — the agent name is only read from attachment messages.
func TestComputeStandaloneAgentContext_NonAttachmentTypeIsIgnored(t *testing.T) {
	got := ComputeStandaloneAgentContext([]*TranscriptMessage{
		{UUID: "u1", Type: "user", Content: `{"type":"agent-name","name":"worker"}`},
	})
	if got != nil {
		t.Errorf("context = %+v, want nil: a user message is not an attachment", *got)
	}
}

// restore.go:225 — a skipped message must not end the scan for the name.
func TestComputeStandaloneAgentContext_SkippedEntryDoesNotEndTheScan(t *testing.T) {
	got := ComputeStandaloneAgentContext([]*TranscriptMessage{
		{UUID: "u1", Type: "user", Content: `[{"type":"text","text":"hi"}]`},
		{UUID: "att1", Type: "attachment", Content: `{"type":"agent-name","name":"worker"}`},
	})
	if got == nil {
		t.Fatal("got nil context, want one for the name in the later attachment")
	}
	if got.AgentType != "standalone" {
		t.Errorf("AgentType = %q, want standalone", got.AgentType)
	}
}

// restore.go:247-248 — the context's SessionID is taken from messages[0], and
// a single-message session already satisfies the guard.
func TestComputeStandaloneAgentContext_SessionIDComesFromFirstMessage(t *testing.T) {
	got := ComputeStandaloneAgentContext([]*TranscriptMessage{
		{UUID: "att1", SessionID: "sess-9", Type: "attachment", Content: `{"type":"agent-name","name":"worker"}`},
	})
	if got == nil {
		t.Fatal("got nil context, want one for a named standalone agent")
	}
	if got.SessionID != "sess-9" {
		t.Errorf("SessionID = %q, want sess-9 from messages[0] of a one-message session", got.SessionID)
	}
	if got.AgentType != "standalone" {
		t.Errorf("AgentType = %q, want standalone", got.AgentType)
	}
}

// --- ProcessResumedConversation (restore.go:288-314) ---

// restore.go:289-290 — an empty transcript short-circuits before any restore
// pass, so the attribution default (a non-nil zero state) must not appear.
func TestProcessResumedConversation_EmptyTranscriptRestoresNothing(t *testing.T) {
	store := openTestStore(t)

	got, err := store.ProcessResumedConversation("missing-session", nil)
	if err != nil {
		t.Fatalf("ProcessResumedConversation: %v", err)
	}
	if got.AgentState != nil || got.SkillState != nil || got.Todos != nil || got.Attribution != nil {
		t.Errorf("state = %+v, want every field nil for an empty transcript", got)
	}
}

// restore.go:294 — the consistency check is part of the resume orchestration;
// dropping the call silences the mismatch warning.
func TestProcessResumedConversation_RunsResumeConsistencyCheck(t *testing.T) {
	logs := captureLogs(t)
	store := openTestStore(t)

	chain := []*TranscriptMessage{
		{UUID: "u1", Type: "user", Content: `[{"type":"text","text":"hi"}]`},
		{UUID: "c1", Type: "system", Subtype: "turn_duration", Content: `{"messageCount":99}`},
	}
	if _, err := store.ProcessResumedConversation("sess", chain); err != nil {
		t.Fatalf("ProcessResumedConversation: %v", err)
	}

	recs := logs.byMessage("resume consistency check failed")
	if len(recs) != 1 {
		t.Fatalf("got %d consistency records, want 1", len(recs))
	}
	wantLogAttr(t, recs[0], "expected", "99")
	wantLogAttr(t, recs[0], "actual", "1")
	wantLogAttr(t, recs[0], "delta", "-98")
	wantLogAttr(t, recs[0], "chain_length", "2")
	wantLogAttr(t, recs[0], "checkpoint_age_entries", "0")
}

// --- CheckResumeConsistency (restore.go:320-358) ---

// restore.go:323 — a checkpoint is a system message whose subtype is
// turn_duration; either half of that pair alone is not one.
func TestCheckResumeConsistency_NearMissCheckpointsAreIgnored(t *testing.T) {
	logs := captureLogs(t)

	cases := []struct {
		name string
		msg  *TranscriptMessage
	}{
		{"right subtype wrong type", &TranscriptMessage{UUID: "m1", Type: "user", Subtype: "turn_duration", Content: `{"messageCount":99}`}},
		{"right type wrong subtype", &TranscriptMessage{UUID: "m1", Type: "system", Subtype: "informational", Content: `{"messageCount":99}`}},
	}

	for _, c := range cases {
		CheckResumeConsistency([]*TranscriptMessage{
			{UUID: "u0", Type: "user", Content: `[{"type":"text","text":"hi"}]`},
			c.msg,
		})
		if recs := logs.byMessage("resume consistency check failed"); len(recs) != 0 {
			t.Errorf("%s: got %d consistency records (%v), want none", c.name, len(recs), recs[0].attrs)
		}
	}
}

// restore.go:343 — a messageCount that is not a JSON number ends the check
// without a verdict: the zero value must not be compared against the position.
func TestCheckResumeConsistency_NonNumericMessageCountIsSilent(t *testing.T) {
	logs := captureLogs(t)

	CheckResumeConsistency([]*TranscriptMessage{
		{UUID: "u0", Type: "user", Content: `[{"type":"text","text":"hi"}]`},
		{UUID: "c1", Type: "system", Subtype: "turn_duration", Content: `{"messageCount":"99"}`},
	})

	if recs := logs.byMessage("resume consistency check failed"); len(recs) != 0 {
		t.Errorf("got %d consistency records (%v), want none", len(recs), recs[0].attrs)
	}
}

// restore.go:329-330 — an unreadable checkpoint is skipped and the backwards
// search reaches the older one.
func TestCheckResumeConsistency_UnreadableCheckpointDoesNotEndTheSearch(t *testing.T) {
	logs := captureLogs(t)

	CheckResumeConsistency([]*TranscriptMessage{
		{UUID: "c0", Type: "system", Subtype: "turn_duration", Content: `{"messageCount":99}`},
		{UUID: "c1", Type: "system", Subtype: "turn_duration", Content: `{"messageCount":`},
	})

	recs := logs.byMessage("resume consistency check failed")
	if len(recs) != 1 {
		t.Fatalf("got %d consistency records, want 1 (the older checkpoint still decides)", len(recs))
	}
	wantLogAttr(t, recs[0], "expected", "99")
	wantLogAttr(t, recs[0], "actual", "0")
	wantLogAttr(t, recs[0], "delta", "-99")
	wantLogAttr(t, recs[0], "chain_length", "2")
	wantLogAttr(t, recs[0], "checkpoint_age_entries", "1")
}
