package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
)

func newTestTool(t *testing.T) (tool.Tool, *Manager) {
	t.Helper()
	pinLocalCST(t)
	m := NewManager(t.TempDir())
	// Tool-level creates start the scheduler; stop it or the leaked ticker
	// races the time.Local restore under -race.
	t.Cleanup(m.Stop)
	return New(m), m
}

func TestCronActionRouting(t *testing.T) {
	tl, _ := newTestTool(t)
	cases := []struct {
		input string
		want  string
	}{
		{`{}`, `action is required — valid actions are "create", "list", "delete"`},
		{`{"action":"bogus"}`, `unknown action "bogus" — valid actions are "create", "list", "delete"`},
		{`{"action":"create","prompt":"p"}`, `action "create" requires cron`},
		{`{"action":"delete"}`, `action "delete" requires id`},
	}
	for _, tc := range cases {
		_, err := tl.Call(context.Background(), json.RawMessage(tc.input), nil)
		if err == nil {
			t.Errorf("%s: no error, want %q", tc.input, tc.want)
			continue
		}
		if err.Error() != tc.want {
			t.Errorf("%s: error = %q, want %q", tc.input, err.Error(), tc.want)
		}
	}
}

func TestCronCreateHappy(t *testing.T) {
	tl, m := newTestTool(t)
	res, err := tl.Call(context.Background(), json.RawMessage(`{"action":"create","cron":"0 9 * * 1-5","prompt":"standup","recurring":true,"durable":true}`), nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cre, ok := res.Data.(*CronOutput)
	if !ok || cre.Create == nil {
		t.Fatalf("result = %+v, want Create", res.Data)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(cre.Create.ID) {
		t.Errorf("id = %q, want 8 hex chars", cre.Create.ID)
	}
	if cre.Create.HumanSchedule != "Weekdays at 09:00" {
		t.Errorf("humanSchedule = %q, want Weekdays at 09:00", cre.Create.HumanSchedule)
	}
	if !cre.Create.Recurring || !cre.Create.Durable {
		t.Errorf("flags = recurring:%v durable:%v", cre.Create.Recurring, cre.Create.Durable)
	}
	got := wireText(cre)
	want := fmt.Sprintf("Scheduled task %s\n- Fires: Weekdays at 09:00\n- Prompt: standup\n- Durable", cre.Create.ID)
	if got != want {
		t.Errorf("recurring durable wire =\n%q\nwant\n%q", got, want)
	}

	// One-shot session variant (pinned dom+month → date-form human schedule).
	res, err = tl.Call(context.Background(), json.RawMessage(`{"action":"create","cron":"30 14 28 2 *","prompt":"leap check","recurring":false}`), nil)
	if err != nil {
		t.Fatalf("create one-shot: %v", err)
	}
	out2 := res.Data.(*CronOutput)
	if out2.Create.HumanSchedule != "14:30, Feb 28" {
		t.Errorf("humanSchedule = %q, want 14:30, Feb 28 (pinned-date arm)", out2.Create.HumanSchedule)
	}
	got = wireText(out2)
	want = fmt.Sprintf("Scheduled one-shot task %s\n- Fires: 14:30, Feb 28\n- Prompt: leap check\n- Session-only", out2.Create.ID)
	if got != want {
		t.Errorf("one-shot session wire =\n%q\nwant\n%q", got, want)
	}

	// Defaults: recurring nil → true; durable nil → false.
	res, err = tl.Call(context.Background(), json.RawMessage(`{"action":"create","cron":"0 9 * * 3","prompt":"weekly"}`), nil)
	if err != nil {
		t.Fatalf("create defaults: %v", err)
	}
	out3 := res.Data.(*CronOutput)
	if !out3.Create.Recurring || out3.Create.Durable {
		t.Errorf("defaults = recurring:%v durable:%v, want true/false", out3.Create.Recurring, out3.Create.Durable)
	}
	if got, want := wireText(out3), fmt.Sprintf("Scheduled task %s\n- Fires: Every Wednesday at 09:00\n- Prompt: weekly\n- Session-only", out3.Create.ID); got != want {
		t.Errorf("recurring session wire =\n%q\nwant\n%q", got, want)
	}
	// The scheduler was enabled by create (CC setScheduledTasksEnabled).
	m.mu.Lock()
	started := m.started
	m.mu.Unlock()
	if !started {
		t.Error("create did not enable the scheduler")
	}
}

func TestCronCreateValidation(t *testing.T) {
	tl, _ := newTestTool(t)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"action":"create","cron":"nope","prompt":"p"}`), nil)
	if err == nil || err.Error() != "Invalid cron expression 'nope'. Expected 5 fields: M H DoM Mon DoW." {
		t.Errorf("invalid cron error = %v", err)
	}
	_, err = tl.Call(context.Background(), json.RawMessage(`{"action":"create","cron":"0 0 31 2 *","prompt":"p"}`), nil)
	if err == nil || err.Error() != "Cron expression '0 0 31 2 *' does not match any calendar date in the next year." {
		t.Errorf("no-match error = %v", err)
	}

	// MAX_JOBS: 50 existing (49 file + 1 session) blocks create.
	tl2, m := newTestTool(t)
	base := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local).UnixMilli()
	for i := range 49 {
		if err := m.store.Add(Task{ID: fmt.Sprintf("seed%04d", i), Cron: "0 9 * * *", Prompt: "p", CreatedAt: base, Recurring: true}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if _, err := m.Create("0 9 * * *", "p", false, false); err != nil {
		t.Fatalf("seed session task: %v", err)
	}
	_, err = tl2.Call(context.Background(), json.RawMessage(`{"action":"create","cron":"0 9 * * *","prompt":"one too many"}`), nil)
	if err == nil || err.Error() != "Too many scheduled jobs (max 50). Cancel one first." {
		t.Errorf("max jobs error = %v", err)
	}

	// 49 total (48 file + 1 session) still succeeds.
	tl3, m3 := newTestTool(t)
	for i := range 48 {
		if err := m3.store.Add(Task{ID: fmt.Sprintf("fit%04d", i), Cron: "0 9 * * *", Prompt: "p", CreatedAt: base, Recurring: true}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if _, err := m3.Create("0 9 * * *", "p", false, false); err != nil {
		t.Fatalf("seed session task: %v", err)
	}
	if _, err := tl3.Call(context.Background(), json.RawMessage(`{"action":"create","cron":"0 9 * * *","prompt":"fits"}`), nil); err != nil {
		t.Errorf("create at 49 jobs failed: %v", err)
	}
}

func TestCronCreateEmptyPromptRejected(t *testing.T) {
	tl, m := newTestTool(t)
	const wantErr = "cron prompt must not be empty — provide the instruction to run at fire time"
	for name, prompt := range map[string]string{
		"empty string":    "",
		"whitespace-only": "  \t\n ",
		"newline-only":    "\n",
		"unicode spaces":  "\u3000\u00a0",
	} {
		_, err := tl.Call(context.Background(), json.RawMessage(
			fmt.Sprintf(`{"action":"create","cron":"0 9 * * *","prompt":%q}`, prompt)), nil)
		if err == nil || err.Error() != wantErr {
			t.Errorf("%s: error = %v, want %q", name, err, wantErr)
		}
	}
	if len(m.List()) != 0 {
		t.Errorf("rejected creates left %d tasks behind, want 0", len(m.List()))
	}

	// Non-empty (even whitespace-padded) prompts still create; the stored
	// prompt keeps its original bytes — validation rejects, it never rewrites.
	res, err := tl.Call(context.Background(), json.RawMessage(`{"action":"create","cron":"0 9 * * *","prompt":"  padded prompt  "}`), nil)
	if err != nil {
		t.Fatalf("padded-prompt create: %v", err)
	}
	id := res.Data.(*CronOutput).Create.ID
	var stored string
	for _, task := range m.List() {
		if task.ID == id {
			stored = task.Prompt
		}
	}
	if stored != "  padded prompt  " {
		t.Errorf("stored prompt = %q, want the original untrimmed bytes", stored)
	}
}

func TestCronSubAgentDurableReject(t *testing.T) {
	pinLocalCST(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Stop)
	m.SetSessionOnly()
	tl := New(m)
	_, err := tl.Call(context.Background(), json.RawMessage(`{"action":"create","cron":"0 9 * * *","prompt":"p","durable":true}`), nil)
	if err == nil || err.Error() != "durable crons are not supported in sub-agent sessions (sub-agents do not persist across sessions)" {
		t.Fatalf("durable reject error = %v", err)
	}
	res, err := tl.Call(context.Background(), json.RawMessage(`{"action":"create","cron":"0 9 * * *","prompt":"p","durable":false}`), nil)
	if err != nil {
		t.Fatalf("session create on sub-agent manager: %v", err)
	}
	out := res.Data.(*CronOutput)
	if out.Create == nil || out.Create.Durable {
		t.Errorf("sub-agent session create = %+v", out.Create)
	}
}

func TestCronList(t *testing.T) {
	tl, m := newTestTool(t)
	// Empty.
	res, err := tl.Call(context.Background(), json.RawMessage(`{"action":"list"}`), nil)
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if got := wireText(res.Data.(*CronOutput)); got != "No scheduled jobs." {
		t.Errorf("empty list wire = %q", got)
	}
	// Seeded: 1 durable recurring + 1 session one-shot.
	if err := m.store.Add(Task{ID: "dura0001", Cron: "0 9 * * 1-5", Prompt: "standup", CreatedAt: 1000, Recurring: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := m.Create("30 14 * * *", "lunch walk", false, false); err != nil {
		t.Fatalf("session create: %v", err)
	}
	res, err = tl.Call(context.Background(), json.RawMessage(`{"action":"list"}`), nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	out := res.Data.(*CronOutput)
	if out.List == nil || len(out.List.Jobs) != 2 {
		t.Fatalf("list jobs = %+v", out.List)
	}
	if out.List.Jobs[0].Durable != true || out.List.Jobs[1].Durable != false {
		t.Errorf("durable flags = %v/%v, want true/false", out.List.Jobs[0].Durable, out.List.Jobs[1].Durable)
	}
	got := wireText(out)
	want := "dura0001 — recurring, Weekdays at 09:00 [durable]\n  standup\n" +
		out.List.Jobs[1].ID + " — one-shot, Every day at 14:30 [session-only]\n  lunch walk"
	if got != want {
		t.Errorf("list wire =\n%q\nwant\n%q", got, want)
	}
}

// TestCronListPinnedSchedule pins the list-side schedule form: the pinned-date
// arm's comma is dropped after the kind word (user ruling — the kind's comma
// already separates the fields).
func TestCronListPinnedSchedule(t *testing.T) {
	tl, m := newTestTool(t)
	if _, err := m.Create("18 12 12 11 *", "reminder text", false, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	res, err := tl.Call(context.Background(), json.RawMessage(`{"action":"list"}`), nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	out := res.Data.(*CronOutput)
	if got, want := out.List.Jobs[0].HumanSchedule, "12:18 Nov 12"; got != want {
		t.Errorf("list humanSchedule = %q, want %q (comma-less pinned form)", got, want)
	}
	if got, want := wireText(out), out.List.Jobs[0].ID+" — one-shot, 12:18 Nov 12 [session-only]\n  reminder text"; got != want {
		t.Errorf("pinned list wire =\n%q\nwant\n%q", got, want)
	}
}

func TestCronListTruncation(t *testing.T) {
	pinLocalCST(t)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Stop)
	tl := New(m)
	long := strings.Repeat("x", 100)
	if _, err := m.Create("0 9 * * *", long, false, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	multi := "first line\nsecond line"
	if _, err := m.Create("0 9 * * 1", multi, false, false); err != nil {
		t.Fatalf("create multi: %v", err)
	}
	cjk := strings.Repeat("水", 85)
	if _, err := m.Create("0 9 * * 2", cjk, false, false); err != nil {
		t.Fatalf("create cjk: %v", err)
	}
	res, err := tl.Call(context.Background(), json.RawMessage(`{"action":"list"}`), nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	lines := strings.Split(wireText(res.Data.(*CronOutput)), "\n")
	if len(lines) != 6 {
		t.Fatalf("lines = %d, want 6 (header + prompt per job)", len(lines))
	}
	promptAt := func(i int) string {
		t.Helper()
		p, ok := strings.CutPrefix(lines[i], "  ")
		if !ok {
			t.Fatalf("line[%d] = %q, want two-space prompt indent", i, lines[i])
		}
		return p
	}
	// 100 runes, no newline → first 79 + ellipsis = 80 runes.
	if got, want := promptAt(1), strings.Repeat("x", 79)+"…"; got != want {
		t.Errorf("long prompt = %d runes, want exactly %q", len([]rune(got)), want)
	}
	// Newline → snip at first line, prefix + ellipsis.
	if got, want := promptAt(3), "first line…"; got != want {
		t.Errorf("multi-line prompt = %q, want %q", got, want)
	}
	// 85 CJK runes (rune-count divergence: 80 incl. ellipsis).
	if got := promptAt(5); len([]rune(got)) != 80 || !strings.HasSuffix(got, "…") {
		t.Errorf("cjk prompt = %q (%d runes), want 80 runes ending with ellipsis", got, len([]rune(got)))
	}
}

func TestCronDelete(t *testing.T) {
	tl, m := newTestTool(t)
	id, err := m.Create("0 9 * * *", "doomed", false, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	res, err := tl.Call(context.Background(), json.RawMessage(fmt.Sprintf(`{"action":"delete","id":%q}`, id)), nil)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := wireText(res.Data.(*CronOutput)); got != "Cancelled job "+id+"." {
		t.Errorf("delete wire = %q", got)
	}
	if tasks := m.List(); len(tasks) != 0 {
		t.Errorf("task survived delete: %+v", tasks)
	}
	_, err = tl.Call(context.Background(), json.RawMessage(`{"action":"delete","id":"nosuch00"}`), nil)
	if err == nil || err.Error() != "No scheduled job with id 'nosuch00'" {
		t.Errorf("unknown id error = %v", err)
	}
}

func TestCronFlagsPerAction(t *testing.T) {
	tl, _ := newTestTool(t)
	if !tl.IsReadOnly(json.RawMessage(`{"action":"list"}`)) {
		t.Error("list is not read-only")
	}
	if tl.IsReadOnly(json.RawMessage(`{"action":"create","cron":"0 9 * * *"}`)) {
		t.Error("create must not be read-only")
	}
	if tl.IsReadOnly(json.RawMessage(`{"action":"delete","id":"x"}`)) {
		t.Error("delete must not be read-only")
	}
	if !tl.IsConcurrencySafe(json.RawMessage(`{"action":"list"}`)) {
		t.Error("list must be concurrency-safe")
	}
	if tl.IsConcurrencySafe(json.RawMessage(`{"action":"create","cron":"0 9 * * *"}`)) {
		t.Error("create must not be concurrency-safe")
	}
	if tl.IsConcurrencySafe(json.RawMessage(`{"action":"delete","id":"x"}`)) {
		t.Error("delete must not be concurrency-safe")
	}
}

func TestCronDescriptionPerAction(t *testing.T) {
	tl, _ := newTestTool(t)
	desc, err := tl.Description(json.RawMessage(`{"action":"create","cron":"0 9 * * 1-5"}`))
	if err != nil {
		t.Fatalf("description: %v", err)
	}
	if desc != "Schedule 0 9 * * 1-5" {
		t.Errorf("create description = %q", desc)
	}
	if desc, _ := tl.Description(json.RawMessage(`{"action":"list"}`)); desc != "List cron jobs" {
		t.Errorf("list description = %q", desc)
	}
	if desc, _ := tl.Description(json.RawMessage(`{"action":"delete","id":"ab12cd34"}`)); desc != "Cancel ab12cd34" {
		t.Errorf("delete description = %q", desc)
	}
}

func TestCronDecodeWire(t *testing.T) {
	cases := []struct {
		name string
		wire string
		want *CronOutput
	}{
		{
			name: "create recurring durable",
			wire: "Scheduled task ab12cd34\n- Fires: Every 5 minutes\n- Prompt: check the deploy\n- Durable",
			want: &CronOutput{Create: &CreateResult{ID: "ab12cd34", HumanSchedule: "Every 5 minutes", Prompt: "check the deploy", Recurring: true, Durable: true}},
		},
		{
			name: "create one-shot session",
			wire: "Scheduled one-shot task ef567890\n- Fires: 12:18, Nov 12\n- Prompt: remind me\n- Session-only",
			want: &CronOutput{Create: &CreateResult{ID: "ef567890", HumanSchedule: "12:18, Nov 12", Prompt: "remind me"}},
		},
		{
			name: "create multi-line prompt",
			wire: "Scheduled task beef0001\n- Fires: Every day at 09:57\n- Prompt: line one\nline two\n- Session-only",
			want: &CronOutput{Create: &CreateResult{ID: "beef0001", HumanSchedule: "Every day at 09:57", Prompt: "line one\nline two", Recurring: true}},
		},
		{
			name: "list two jobs",
			wire: "ab12cd34 — recurring, Every 5 minutes [durable]\n  p1\nef567890 — one-shot, Every hour [session-only]\n  p2",
			want: &CronOutput{List: &ListResult{Jobs: []ListJob{
				{ID: "ab12cd34", HumanSchedule: "Every 5 minutes", Prompt: "p1", Recurring: true, Durable: true},
				{ID: "ef567890", HumanSchedule: "Every hour", Prompt: "p2"},
			}}},
		},
		{
			name: "list pinned schedule",
			wire: "ab12cd34 — one-shot, 12:18 Nov 12 [session-only]\n  p",
			want: &CronOutput{List: &ListResult{Jobs: []ListJob{
				{ID: "ab12cd34", HumanSchedule: "12:18 Nov 12", Prompt: "p"},
			}}},
		},
		{
			name: "delete",
			wire: "Cancelled job ab12cd34.",
			want: &CronOutput{Delete: &DeleteResult{ID: "ab12cd34"}},
		},
	}
	for _, tc := range cases {
		got, err := decodeWire(tc.wire)
		if err != nil {
			t.Errorf("%s: decodeWire: %v", tc.name, err)
			continue
		}
		if got.Create == nil || tc.want.Create == nil {
			if (got.Create == nil) != (tc.want.Create == nil) {
				t.Errorf("%s: create = %+v, want %+v", tc.name, got.Create, tc.want.Create)
			}
		} else if *got.Create != *tc.want.Create {
			t.Errorf("%s: create = %+v, want %+v", tc.name, got.Create, tc.want.Create)
		}
		if got.Delete == nil || tc.want.Delete == nil {
			if (got.Delete == nil) != (tc.want.Delete == nil) {
				t.Errorf("%s: delete = %+v, want %+v", tc.name, got.Delete, tc.want.Delete)
			}
		} else if *got.Delete != *tc.want.Delete {
			t.Errorf("%s: delete = %+v, want %+v", tc.name, got.Delete, tc.want.Delete)
		}
		gotJobs := 0
		wantJobs := 0
		if got.List != nil {
			gotJobs = len(got.List.Jobs)
		}
		if tc.want.List != nil {
			wantJobs = len(tc.want.List.Jobs)
		}
		if gotJobs != wantJobs {
			t.Errorf("%s: jobs = %d, want %d", tc.name, gotJobs, wantJobs)
			continue
		}
		if got.List == nil || tc.want.List == nil {
			continue
		}
		for i := range tc.want.List.Jobs {
			if got.List.Jobs[i] != tc.want.List.Jobs[i] {
				t.Errorf("%s: job[%d] = %+v, want %+v", tc.name, i, got.List.Jobs[i], tc.want.List.Jobs[i])
			}
		}
	}
	if _, err := decodeWire("random text"); err == nil {
		t.Error("decodeWire accepted unknown text")
	}
}

func TestCronDescriptionFallbacks(t *testing.T) {
	tl, _ := newTestTool(t)
	for _, input := range []string{`{}`, `{"action":"bogus"}`, `not json`} {
		desc, err := tl.Description(json.RawMessage(input))
		if err != nil {
			t.Errorf("%s: Description error: %v", input, err)
		}
		if !strings.HasPrefix(desc, "Schedule a prompt to run at a future time") {
			t.Errorf("%s: description = %q, want the full tool-list description", input, desc)
		}
	}
}

func TestCronMalformedInputError(t *testing.T) {
	tl, _ := newTestTool(t)
	_, err := tl.Call(context.Background(), json.RawMessage(`{bad json`), nil)
	if err == nil || !strings.Contains(err.Error(), "parse input") {
		t.Errorf("malformed input error = %v, want parse input prefix", err)
	}
}

func TestTruncatePromptBoundaries(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{strings.Repeat("x", 79), strings.Repeat("x", 79)},                    // ≤80 no newline: verbatim
		{strings.Repeat("x", 80), strings.Repeat("x", 80)},                    // exactly 80: verbatim
		{strings.Repeat("x", 81), strings.Repeat("x", 79) + "…"},              // 81: 79+…
		{strings.Repeat("x", 79) + "\nsecond", strings.Repeat("x", 79) + "…"}, // newline prefix 79: prefix+…
		{strings.Repeat("x", 80) + "\nsecond", strings.Repeat("x", 79) + "…"}, // newline prefix 80: cut
		{"short\nrest", "short…"},                                             // short prefix + …
	}
	for _, tc := range cases {
		if got := truncatePrompt(tc.in); got != tc.want {
			t.Errorf("truncatePrompt(%d chars) = %q, want %q", len(tc.in), got, tc.want)
		}
	}
}

func TestDecodeWireMalformed(t *testing.T) {
	bad := []struct{ wire, wantErr string }{
		{"Scheduled task no-lines-here", "cron: create wire lacks id head"},
		{"Scheduled one-shot task \n- Fires: x\n- Prompt: p\n- Durable", "cron: create wire lacks id head"},
		{"Scheduled task ab12cd34\n- Fires: x\n- Prompt: p", "cron: create wire lacks Fires/Prompt/persistence body"},
		{"Scheduled task ab12cd34\n- Fires: x", "cron: create wire lacks Fires/Prompt/persistence body"},
		{"Scheduled task ab12cd34\n- Not-fires: x\n- Prompt: p\n- Durable", "cron: create wire lacks Fires line"},
		{"Scheduled task ab12cd34\n- Fires: \n- Prompt: p\n- Durable", "cron: create wire lacks Fires line"},
		{"Scheduled task ab12cd34\n- Fires: x\n- Nope: p\n- Durable", "cron: create wire lacks Prompt line"},
		{"Scheduled task ab12cd34\n- Fires: x\n- Prompt: p\n- Maybe", "cron: create wire lacks persistence line"},
		{"ab12cd34 — recurring, Every 5 minutes [durable]", "cron: list wire is not header+prompt pairs"},
		{"ab12cd34 — recurring, Every 5 minutes [durable]\np1", `cron: list job lacks indented prompt line: "p1"`},
		{" — recurring, Every 5 minutes [durable]\n  p1", `cron: list header lacks id: " — recurring, Every 5 minutes [durable]"`},
		{"ab12cd34 — Every 5 minutes [durable]\n  p1", `cron: list header lacks kind: "ab12cd34 — Every 5 minutes [durable]"`},
		{"ab12cd34 — daily, Every 5 minutes [durable]\n  p1", `cron: list header has unknown kind "daily": "ab12cd34 — daily, Every 5 minutes [durable]"`},
		{"ab12cd34 — recurring, Every 5 minutes\n  p1", `cron: list header lacks persistence tag: "ab12cd34 — recurring, Every 5 minutes"`},
		{"Cancelled job ", "cron: delete wire lacks id"}, // prefix matches, no closing dot
		{"", "cron: unrecognized wire text"},
	}
	for _, tc := range bad {
		_, err := decodeWire(tc.wire)
		if err == nil {
			t.Errorf("decodeWire(%q) accepted malformed wire", tc.wire)
			continue
		}
		if err.Error() != tc.wantErr {
			t.Errorf("decodeWire(%q) error = %q, want %q", tc.wire, err.Error(), tc.wantErr)
		}
	}
	// Empty list round-trips.
	out, err := decodeWire("No scheduled jobs.")
	if err != nil || out.List == nil || len(out.List.Jobs) != 0 {
		t.Errorf("decodeWire(No scheduled jobs.) = %+v, %v", out, err)
	}
}

// TestListSchedule pins the comma-strip contract: exactly the pinned-date
// arm's comma goes; raw-cron commas (no trailing space) and any later
// comma-space pairs survive.
func TestListSchedule(t *testing.T) {
	cases := []struct{ in, want string }{
		{"12:18, Nov 12", "12:18 Nov 12"},
		{"Every 5 minutes", "Every 5 minutes"},
		{"Every day at 09:57", "Every day at 09:57"},
		{"1,2 9 * * *", "1,2 9 * * *"},
		{"12:18, Nov 12, tail", "12:18 Nov 12, tail"},
	}
	for _, tc := range cases {
		if got := listSchedule(tc.in); got != tc.want {
			t.Errorf("listSchedule(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCronWireRoundTrip drives the full emit → decode chain for all four
// kind × persistence combos through the tool's own FormatWireBlocks /
// DecodeResult (the replay path), asserting the decoded fields equal the
// tool-result fields and the re-render reproduces the wire byte-for-byte.
func TestCronWireRoundTrip(t *testing.T) {
	combos := []struct {
		name      string
		cron      string
		prompt    string
		recurring bool
		durable   bool
	}{
		{"one-shot session", "18 12 12 11 *", "remind me to check the deploy", false, false},
		{"one-shot durable", "30 14 28 2 *", "leap day check", false, true},
		{"recurring session", "*/5 * * * *", "water the plants", true, false},
		{"recurring durable", "57 9 * * *", "standup note", true, true},
	}
	for _, tc := range combos {
		t.Run(tc.name, func(t *testing.T) {
			tl, _ := newTestTool(t)
			input := fmt.Sprintf(`{"action":"create","cron":%q,"prompt":%q,"recurring":%t,"durable":%t}`, tc.cron, tc.prompt, tc.recurring, tc.durable)
			res, err := tl.Call(context.Background(), json.RawMessage(input), nil)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			out := res.Data.(*CronOutput)

			// decodeAndCheck: wire blocks (the persisted replay form) must
			// decode back to the same fields and re-render byte-for-byte.
			wb, ok := tl.(tool.ToolWithWireBlocks)
			if !ok {
				t.Fatal("Cron tool does not implement ToolWithWireBlocks")
			}
			dt, ok := tl.(tool.ToolWithDecodeResult)
			if !ok {
				t.Fatal("Cron tool does not implement ToolWithDecodeResult")
			}
			decodeAndCheck := func(want *CronOutput) {
				t.Helper()
				wire := wireText(want)
				blocks := wb.FormatWireBlocks(want)
				if len(blocks) != 1 || blocks[0].Text != wire {
					t.Fatalf("FormatWireBlocks = %+v, want one block of the wire text", blocks)
				}
				dec, derr := dt.DecodeResult(tool.WrapSingleBlock(blocks[0].Text))
				if derr != nil {
					t.Fatalf("DecodeResult: %v", derr)
				}
				got, ok := dec.(*CronOutput)
				if !ok {
					t.Fatalf("DecodeResult type = %T, want *CronOutput", dec)
				}
				if (got.Create == nil) != (want.Create == nil) || (got.List == nil) != (want.List == nil) {
					t.Fatalf("decode section presence = create:%v list:%v, want create:%v list:%v",
						got.Create != nil, got.List != nil, want.Create != nil, want.List != nil)
				}
				if got.Create != nil && *got.Create != *want.Create {
					t.Errorf("create decode = %+v, want %+v", got.Create, want.Create)
				}
				if got.List != nil {
					if len(got.List.Jobs) != len(want.List.Jobs) {
						t.Fatalf("list decode jobs = %d, want %d", len(got.List.Jobs), len(want.List.Jobs))
					}
					for i := range want.List.Jobs {
						exp := want.List.Jobs[i]
						exp.Prompt = truncatePrompt(exp.Prompt) // the wire carries the truncated form
						exp.Cron = ""                           // the raw expression is not on the wire (user-ruled cut)
						if got.List.Jobs[i] != exp {
							t.Errorf("list decode job[%d] = %+v, want %+v", i, got.List.Jobs[i], exp)
						}
					}
				}
				if re := renderCronOutput(got); re != wire {
					t.Errorf("replayed render =\n%q\nwant the original wire\n%q", re, wire)
				}
			}

			decodeAndCheck(out)

			lres, err := tl.Call(context.Background(), json.RawMessage(`{"action":"list"}`), nil)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			lout := lres.Data.(*CronOutput)
			if len(lout.List.Jobs) != 1 {
				t.Fatalf("list jobs = %d, want 1", len(lout.List.Jobs))
			}
			decodeAndCheck(lout)
		})
	}
}

func TestCronRenderAndWireFallbacks(t *testing.T) {
	if got := renderCronOutput("plain string"); got != "plain string" {
		t.Errorf("renderCronOutput(string) = %q, want verbatim", got)
	}
	blocks := cronFormatWireBlocks("not an output")
	if len(blocks) != 1 || blocks[0].Text != `"not an output"` {
		t.Errorf("FormatWireBlocks fallback = %+v, want the JSON-marshaled text", blocks)
	}
}

func TestCronPromptText(t *testing.T) {
	p := cronPrompt()
	for _, want := range []string{
		"5-field cron",
		`"remind me at 2:30pm today to check the deploy"`,
		"Avoid the :00 and :30 minute marks",
		"## Durability",
		"auto-expire after 7 days",
		`cancel with action:"delete"`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestCronToolIdentity(t *testing.T) {
	tl, _ := newTestTool(t)
	if tl.Name() != "Cron" {
		t.Errorf("tool name = %q, want Cron", tl.Name())
	}
	if len(tl.InputSchema()) == 0 {
		t.Error("input schema is empty")
	}
}
