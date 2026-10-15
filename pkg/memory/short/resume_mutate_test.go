package short

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// captureSlogDefault points the default logger at a buffer for the test's
// duration. Not safe alongside t.Parallel().
func captureSlogDefault(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// resume.go:24 — the empty-transcript short-circuit is keyed on length 0 only:
// a one-message session still runs the full resume pipeline.
func TestResumeSession_SingleMessageRunsPipeline(t *testing.T) {
	store := openTestStore(t)
	sessionID := "resume-one"
	createTestSession(t, store, sessionID)

	if err := store.AppendMessage(sessionID,
		testMessage(0, "user", "u1", "", `[{"type":"text","text":"only"}]`)); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	_, loaded, err := store.ResumeSession(sessionID)
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("ResumeSession returned %d messages, want 1", len(loaded))
	}
	if loaded[0].UUID != "u1" {
		t.Errorf("message uuid = %q, want u1", loaded[0].UUID)
	}
}

// resume.go:29 — ResumeSession must run the API-compatibility filters, not
// hand the raw transcript back. The unresolved tool_use sits mid-conversation,
// so only the filter (never the interrupted-turn truncation) can remove it.
func TestResumeSession_AppliesResumeFilters(t *testing.T) {
	store := openTestStore(t)
	sessionID := "resume-filter"
	createTestSession(t, store, sessionID)

	msgs := []*TranscriptMessage{
		testMessage(0, "user", "u1", "", `[{"type":"text","text":"hello"}]`),
		testMessage(0, "assistant", "a1", "", `[{"type":"tool_use","id":"tu1","name":"Read"}]`),
		testMessage(0, "user", "u2", "", `[{"type":"text","text":"and then"}]`),
	}
	for _, m := range msgs {
		if err := store.AppendMessage(sessionID, m); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	_, loaded, err := store.ResumeSession(sessionID)
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("ResumeSession returned %d messages, want 2 (unresolved tool_use filtered)", len(loaded))
	}
	if loaded[0].UUID != "u1" || loaded[1].UUID != "u2" {
		t.Errorf("remaining uuids = %q, %q, want u1, u2", loaded[0].UUID, loaded[1].UUID)
	}
}

// resume.go:34-35 — an interrupted trailing turn is truncated, and the
// truncation is logged with the surviving count.
func TestResumeSession_TruncatesInterruptedTurn(t *testing.T) {
	logs := captureSlogDefault(t)

	store := openTestStore(t)
	sessionID := "resume-interrupted"
	createTestSession(t, store, sessionID)

	msgs := []*TranscriptMessage{
		testMessage(0, "user", "u1", "", `[{"type":"text","text":"hello"}]`),
		testMessage(0, "assistant", "a1", "", `[]`),
	}
	for _, m := range msgs {
		if err := store.AppendMessage(sessionID, m); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	_, loaded, err := store.ResumeSession(sessionID)
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("ResumeSession returned %d messages, want 1 (interrupted turn truncated)", len(loaded))
	}
	if loaded[0].UUID != "u1" {
		t.Errorf("remaining message uuid = %q, want u1", loaded[0].UUID)
	}
	if got := logs.String(); !strings.Contains(got, `msg="resume: detected interrupted turn, truncated messages" count=1`) {
		t.Errorf("log output = %q, want the interrupted-turn record with count=1", got)
	}
}

// resume.go:49 — FilterForResume drops orphaned thinking-only assistants.
func TestFilterForResume_OrphanedThinkingFilterRuns(t *testing.T) {
	filtered := FilterForResume([]*TranscriptMessage{
		{UUID: "u1", Type: "user", Content: `[{"type":"text","text":"hello"}]`},
		{UUID: "a1", Type: "assistant", Content: `[{"type":"thinking","thinking":"hmm"}]`},
	})
	if len(filtered) != 1 {
		t.Fatalf("got %d messages, want 1 (orphaned thinking filtered)", len(filtered))
	}
	if filtered[0].UUID != "u1" {
		t.Errorf("remaining uuid = %q, want u1", filtered[0].UUID)
	}
}

// resume.go:50 — FilterForResume drops whitespace-only assistants and merges
// the user messages the removal leaves adjacent.
func TestFilterForResume_WhitespaceFilterRuns(t *testing.T) {
	filtered := FilterForResume([]*TranscriptMessage{
		{UUID: "u1", Type: "user", Content: `[{"type":"text","text":"one"}]`},
		{UUID: "a1", Type: "assistant", Content: `[{"type":"text","text":"   "}]`},
		{UUID: "u2", Type: "user", Content: `[{"type":"text","text":"two"}]`},
	})
	if len(filtered) != 1 {
		t.Fatalf("got %d messages, want 1 (whitespace assistant removed, users merged)", len(filtered))
	}
	blocks := ParseContentBlocks(filtered[0].Content)
	if len(blocks) != 2 {
		t.Fatalf("merged message has %d blocks, want 2", len(blocks))
	}
	if blocks[0].Text != "one" || blocks[1].Text != "two" {
		t.Errorf("merged blocks = %q, %q, want one, two", blocks[0].Text, blocks[1].Text)
	}
}

// resume.go:112 — the empty-input guard must trigger only at length 0; a
// single assistant message is an interrupted turn that gets removed.
func TestTruncateInterruptedTurn_SingleAssistant(t *testing.T) {
	got := TruncateInterruptedTurn([]*TranscriptMessage{
		{UUID: "a1", Type: "assistant", Content: `[{"type":"thinking","thinking":"hmm"}]`},
	})
	if len(got) != 0 {
		t.Errorf("got %d messages, want 0 (the only message is the interrupted turn)", len(got))
	}
}

// resume.go:113 — the empty-input guard returns the caller's slice, so a
// non-nil empty input stays non-nil.
func TestTruncateInterruptedTurn_EmptyInputKeepsSlice(t *testing.T) {
	got := TruncateInterruptedTurn([]*TranscriptMessage{})
	if got == nil {
		t.Fatal("got nil slice, want the non-nil empty slice that was passed in")
	}
	if len(got) != 0 {
		t.Errorf("got %d messages, want 0", len(got))
	}
}

// resume.go:20-21 — a load failure aborts the resume: the guard returns the
// error and the %w verb keeps the driver's cause reachable through Unwrap.
func TestResumeSession_LoadFailureIsWrapped(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "resume-closed")
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	state, messages, err := store.ResumeSession("resume-closed")
	if err == nil {
		t.Fatal("ResumeSession returned a nil error on a closed store, want the load failure")
	}
	if state != nil || messages != nil {
		t.Errorf("state/messages = %+v/%v, want nil/nil alongside the error", state, messages)
	}
	want := "load messages: get last boundary: query last boundary: sql: database is closed"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	// The %w verb is the only thing that keeps the cause reachable: %v prints
	// the identical message but drops the chain.
	cause := errors.Unwrap(err)
	if cause == nil {
		t.Fatalf("error %q does not unwrap, want the load failure chain below the prefix", err.Error())
	}
	if cause.Error() != want[len("load messages: "):] {
		t.Errorf("unwrapped error = %q, want %q", cause.Error(), want[len("load messages: "):])
	}
}

// resume.go:69-70 — only an assistant turn can end in an interruption; a
// non-assistant last message is never one, even with empty content.
func TestDetectInterruptedTurn_NonAssistantLastIsNeverInterrupted(t *testing.T) {
	for _, msgType := range []string{"user", "system", "attachment", "progress"} {
		if DetectInterruptedTurn([]*TranscriptMessage{{UUID: "m1", Type: msgType, Content: `[]`}}) {
			t.Errorf("interrupted = true for a trailing %s message, want false", msgType)
		}
	}
}

// resume.go:98 — thinking and redacted_thinking are the two thinking kinds; a
// turn made only of either one is an interruption, and mixing in any other
// block kind ends that.
func TestDetectInterruptedTurn_ThinkingOnlyKinds(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"thinking only", `[{"type":"thinking","thinking":"hmm","signature":"sig"}]`, true},
		{"redacted thinking only", `[{"type":"redacted_thinking","data":"redacted"}]`, true},
		{"thinking and redacted thinking", `[{"type":"thinking","thinking":"hmm"},{"type":"redacted_thinking","data":"redacted"}]`, true},
		{"thinking and text", `[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"out loud"}]`, false},
		{"redacted thinking and text", `[{"type":"redacted_thinking","data":"redacted"},{"type":"text","text":"out loud"}]`, false},
	}

	for _, c := range cases {
		got := DetectInterruptedTurn([]*TranscriptMessage{
			{UUID: "a1", Type: "assistant", Content: c.content},
		})
		if got != c.want {
			t.Errorf("%s: interrupted = %v, want %v", c.name, got, c.want)
		}
	}
}
