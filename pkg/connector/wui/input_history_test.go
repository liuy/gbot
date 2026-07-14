package wui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func writeInputHistoryJSONL(t *testing.T, dir, sessionID string, lines []string) {
	t.Helper()
	histDir := filepath.Join(dir, "history")
	if err := os.MkdirAll(histDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(histDir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadInputHistory_Basic(t *testing.T) {
	dir := t.TempDir()
	const sid = "sess1"
	writeInputHistoryJSONL(t, dir, sid, []string{
		`{"display":"foo","timestamp":1}`,
		`{"display":"bar","timestamp":2}`,
	})
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return sid }

	got := c.loadInputHistory()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0] != "foo" || got[1] != "bar" {
		t.Errorf("got = %v, want [foo bar]", got)
	}
}

func TestLoadInputHistory_DedupConsecutive(t *testing.T) {
	dir := t.TempDir()
	const sid = "s"
	writeInputHistoryJSONL(t, dir, sid, []string{
		`{"display":"a","timestamp":1}`,
		`{"display":"b","timestamp":2}`,
		`{"display":"b","timestamp":3}`,
	})
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return sid }

	got := c.loadInputHistory()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (consecutive dup skipped)", len(got))
	}
	if got[0] != "a" || got[1] != "b" {
		t.Errorf("got = %v, want [a b]", got)
	}
}

func TestLoadInputHistory_EmptyDisplay(t *testing.T) {
	dir := t.TempDir()
	const sid = "s"
	writeInputHistoryJSONL(t, dir, sid, []string{
		`{"display":"","timestamp":1}`,
	})
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return sid }

	got := c.loadInputHistory()
	if got != nil {
		t.Errorf("got = %v, want nil (empty display skipped)", got)
	}
}

func TestLoadInputHistory_Malformed(t *testing.T) {
	dir := t.TempDir()
	const sid = "s"
	writeInputHistoryJSONL(t, dir, sid, []string{
		"bad json",
		`{"display":"good","timestamp":2}`,
	})
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return sid }

	got := c.loadInputHistory()
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (malformed skipped)", len(got))
	}
	if got[0] != "good" {
		t.Errorf("got[0] = %q, want good", got[0])
	}
}

func TestLoadInputHistory_CapAt1000(t *testing.T) {
	dir := t.TempDir()
	const sid = "s"
	var lines []string
	for i := range 1005 {
		lines = append(lines, fmt.Sprintf(`{"display":"entry%d","timestamp":%d}`, i, i))
	}
	writeInputHistoryJSONL(t, dir, sid, lines)
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return sid }

	got := c.loadInputHistory()
	if len(got) != 1000 {
		t.Fatalf("len = %d, want 1000", len(got))
	}
	if got[0] != "entry5" {
		t.Errorf("got[0] = %q, want entry5 (first 5 evicted)", got[0])
	}
}

func TestLoadInputHistory_NoFile(t *testing.T) {
	dir := t.TempDir()
	const sid = "s"
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return sid }

	got := c.loadInputHistory()
	if got != nil {
		t.Errorf("got = %v, want nil (no file)", got)
	}
}

func TestLoadInputHistory_EmptyProjectDir(t *testing.T) {
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return "" }
	c.mock().sessionIDFn = func() string { return "s" }

	got := c.loadInputHistory()
	if got != nil {
		t.Errorf("got = %v, want nil (empty projectDir)", got)
	}
}

// ---------------------------------------------------------------------------
// appendInputHistory
// ---------------------------------------------------------------------------

func TestAppendInputHistory_Writes(t *testing.T) {
	dir := t.TempDir()
	const sid = "s"
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return sid }

	c.appendInputHistory("hello")
	path := filepath.Join(dir, "history", sid+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	matched, err := regexp.MatchString(`^{"display":"hello","timestamp":\d+}\n$`, string(data))
	if err != nil {
		t.Fatalf("regex err: %v", err)
	}
	if !matched {
		t.Errorf("file content = %q, want matching ^{\"display\":\"hello\",\"timestamp\":\\d+}\\n$", string(data))
	}
}

func TestAppendInputHistory_AppendsMultiple(t *testing.T) {
	dir := t.TempDir()
	const sid = "s"
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return sid }

	c.appendInputHistory("a")
	c.appendInputHistory("b")
	path := filepath.Join(dir, "history", sid+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	var e1, e2 inputHistoryEntry
	if err := json.Unmarshal([]byte(lines[0]), &e1); err != nil {
		t.Fatalf("unmarshal line1: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &e2); err != nil {
		t.Fatalf("unmarshal line2: %v", err)
	}
	if e1.Display != "a" {
		t.Errorf("line1 display = %q, want a", e1.Display)
	}
	if e2.Display != "b" {
		t.Errorf("line2 display = %q, want b", e2.Display)
	}
}

func TestAppendInputHistory_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	const sid = "s"
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return sid }

	// history/ dir does not exist yet
	histDir := filepath.Join(dir, "history")
	if _, err := os.Stat(histDir); !os.IsNotExist(err) {
		t.Fatalf("history dir should not exist yet")
	}
	c.appendInputHistory("x")
	if _, err := os.Stat(histDir); err != nil {
		t.Fatalf("history dir not created: %v", err)
	}
	path := filepath.Join(histDir, sid+".jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestAppendInputHistory_NoPath(t *testing.T) {
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return "" }
	c.mock().sessionIDFn = func() string { return "s" }

	// Should not panic, no file created.
	c.appendInputHistory("x")
}

func TestAppendInputHistory_DuplicateStillAppended(t *testing.T) {
	dir := t.TempDir()
	const sid = "s"
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return sid }

	c.appendInputHistory("x")
	c.appendInputHistory("x")
	got := c.loadInputHistory()
	// Dedup on read collapses to 1.
	if len(got) != 1 {
		t.Errorf("loadInputHistory len = %d, want 1 (consecutive dup collapsed on read)", len(got))
	}
	// But the raw file has 2 lines.
	path := filepath.Join(dir, "history", sid+".jsonl")
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("raw file lines = %d, want 2 (append-only)", len(lines))
	}
}

// ---------------------------------------------------------------------------
// buildConnectStatusMessage includes inputHistory
// ---------------------------------------------------------------------------

func TestBuildConnectStatusMessage_InputHistory(t *testing.T) {
	dir := t.TempDir()
	const sid = "s"
	writeInputHistoryJSONL(t, dir, sid, []string{
		`{"display":"foo","timestamp":1}`,
		`{"display":"bar","timestamp":2}`,
	})
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return sid }

	payload := c.buildConnectStatus(c.activeSlotTest(t))
	var env struct {
		Type         string   `json:"type"`
		InputHistory []string `json:"inputHistory"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.InputHistory) != 2 {
		t.Fatalf("inputHistory len = %d, want 2", len(env.InputHistory))
	}
	if env.InputHistory[0] != "foo" || env.InputHistory[1] != "bar" {
		t.Errorf("inputHistory = %v, want [foo bar]", env.InputHistory)
	}
}

func TestBuildConnectStatusMessage_InputHistoryOmitted(t *testing.T) {
	dir := t.TempDir()
	const sid = "s"
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return sid }

	payload := c.buildConnectStatus(c.activeSlotTest(t))
	if strings.Contains(string(payload), "inputHistory") {
		t.Errorf("payload should omit inputHistory when empty; got: %s", string(payload))
	}
	// Stats fields must NOT be in connect_status — they are sent as a
	// separate "stats" frame after replay.
	for _, field := range []string{`"usage"`, `"queryStartMs"`, `"toolCount"`, `"thinkingMs"`} {
		if strings.Contains(string(payload), field) {
			t.Errorf("connect_status should NOT contain %s; got: %s", field, string(payload))
		}
	}
}

// ---------------------------------------------------------------------------
// handleMessageInbound appends history
// ---------------------------------------------------------------------------

func TestHandleMessageInbound_AppendsHistory(t *testing.T) {
	dir := t.TempDir()
	const sid = "s"
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return sid }
	c.mock().isBusyFn = func() bool { return false }
	c.mock().systemPromptFn = func() string { return "" }

	c.handleMessageInbound("hello")

	// JSONL file should exist with one entry — synchronous, assert immediately.
	path := filepath.Join(dir, "history", sid+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(string(data), `"display":"hello"`) {
		t.Errorf("file content = %q, want display hello", string(data))
	}

	// Query dispatched asynchronously.
	if !waitFor(time.Second, func() bool {
		c.mock().mu.Lock()
		defer c.mock().mu.Unlock()
		return len(c.mock().queryCalls) >= 1
	}) {
		c.mock().mu.Lock()
		t.Fatalf("queryCalls = %d, want >= 1", len(c.mock().queryCalls))
	}
}

func TestHandleMessageInbound_BusyStillAppendsHistory(t *testing.T) {
	dir := t.TempDir()
	const sid = "s"
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return sid }
	c.mock().isBusyFn = func() bool { return true }

	c.handleMessageInbound("queued")

	path := filepath.Join(dir, "history", sid+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(string(data), `"display":"queued"`) {
		t.Errorf("file content = %q, want display queued (busy path still appends)", string(data))
	}
}

// ---------------------------------------------------------------------------
// EngineID vs SessionID: history file must use EngineID (e.g. "main"),
// NOT SessionID (e.g. a UUID). This is the key for TUI↔webchat sync.
// ---------------------------------------------------------------------------

func TestInputHistoryPath_UsesEngineID(t *testing.T) {
	dir := t.TempDir()
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return "main" }
	c.mock().sessionIDFn = func() string { return "d210b02c-9e85-4829-8ed7-c402adc5c56f" }

	got := c.inputHistoryPath()
	want := filepath.Join(dir, "history", "main.jsonl")
	if got != want {
		t.Errorf("inputHistoryPath() = %q, want %q (EngineID, not SessionID)", got, want)
	}
}

func TestLoadInputHistory_ReadsEngineIDFile(t *testing.T) {
	dir := t.TempDir()
	// TUI writes to main.jsonl (EngineID)
	writeInputHistoryJSONL(t, dir, "main", []string{
		`{"display":"from-tui-1","timestamp":1}`,
		`{"display":"from-tui-2","timestamp":2}`,
	})
	// webchat session uses a different UUID
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return "main" }
	c.mock().sessionIDFn = func() string { return "d210b02c-uuid-here" }

	got := c.loadInputHistory()
	if len(got) != 2 {
		t.Fatalf("loadInputHistory len = %d, want 2 (should read main.jsonl, not UUID.jsonl)", len(got))
	}
	if got[0] != "from-tui-1" || got[1] != "from-tui-2" {
		t.Errorf("loadInputHistory = %v, want [from-tui-1 from-tui-2]", got)
	}
}

func TestAppendInputHistory_WritesToEngineIDFile(t *testing.T) {
	dir := t.TempDir()
	c := newTestConnector(t)
	c.mock().projectDirFn = func() string { return dir }
	c.mock().engineIDFn = func() string { return "main" }
	c.mock().sessionIDFn = func() string { return "d210b02c-uuid-here" }

	c.appendInputHistory("from-webchat")

	// File must be main.jsonl (EngineID), not d210b02c-uuid-here.jsonl (SessionID)
	mainPath := filepath.Join(dir, "history", "main.jsonl")
	uuidPath := filepath.Join(dir, "history", "d210b02c-uuid-here.jsonl")

	if _, err := os.Stat(uuidPath); err == nil {
		t.Error("SessionID file should NOT exist, append must use EngineID")
	}
	data, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read main.jsonl: %v", err)
	}
	if !strings.Contains(string(data), `"display":"from-webchat"`) {
		t.Errorf("main.jsonl = %q, want display from-webchat", string(data))
	}
}
