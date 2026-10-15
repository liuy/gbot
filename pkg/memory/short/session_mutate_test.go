package short

import (
	"errors"
	"strings"
	"testing"
)

// assertWrappedErr pins the three properties every wrapped error in this file
// must have: the exact context prefix, the cause's message appended verbatim,
// and a real %w chain. The %w→%v downgrade keeps the message byte-identical,
// so only the Unwrap check separates them.
func assertWrappedErr(t *testing.T, err error, prefix string) {
	t.Helper()

	if err == nil {
		t.Fatalf("got nil error, want an error wrapped with prefix %q", prefix)
	}
	cause := errors.Unwrap(err)
	if cause == nil {
		t.Fatalf("error %q does not wrap its cause (%%w downgraded to %%v)", err.Error())
	}
	if want := prefix + cause.Error(); err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// insertRawSession writes a session row directly so nullable/defaulted columns
// can hold values CreateSession never produces.
func insertRawSession(t *testing.T, store *Store, cols string, args ...any) {
	t.Helper()

	query := `INSERT INTO sessions (session_id, project_dir, created_at, updated_at, ` + cols + `) VALUES (`
	placeholders := make([]string, len(args))
	for i := range args {
		placeholders[i] = "?"
	}
	query += strings.Join(placeholders, ", ") + ")"
	if _, err := store.db.Exec(query, args...); err != nil {
		t.Fatalf("insert raw session: %v", err)
	}
}

// --- error wrapping (%w) on every session.go Errorf site ---

func TestCreateSession_InsertErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec("DROP TABLE sessions"); err != nil {
		t.Fatalf("drop sessions: %v", err)
	}

	_, err := store.CreateSession("/p", "m")
	assertWrappedErr(t, err, "insert session: ")
}

func TestGetSession_QueryErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := store.GetSession("any")
	assertWrappedErr(t, err, "query session: ")
}

func TestGetSession_SettingsErrorWraps(t *testing.T) {
	store := openTestStore(t)
	insertRawSession(t, store, "settings", "bad-settings", "/p", testTimeBase, testTimeBase, "not-json")

	_, err := store.GetSession("bad-settings")
	assertWrappedErr(t, err, "unmarshal settings: ")
}

func TestListSessions_QueryErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := store.ListSessions("/p", 10)
	assertWrappedErr(t, err, "query sessions: ")
}

func TestListSessions_ScanErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec(
		`INSERT INTO sessions (session_id, project_dir, created_at, updated_at) VALUES ('s1', '/p', 'not-a-ts', 'not-a-ts')`,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}

	_, err := store.ListSessions("/p", 10)
	assertWrappedErr(t, err, "scan session: ")
}

func TestListSessions_SettingsErrorWraps(t *testing.T) {
	store := openTestStore(t)
	insertRawSession(t, store, "settings", "s1", "/p", testTimeBase, testTimeBase, "not-json")

	_, err := store.ListSessions("/p", 10)
	assertWrappedErr(t, err, "unmarshal settings: ")
}

func TestUpdateSessionTitle_QueryErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := store.UpdateSessionTitle("any", "title")
	assertWrappedErr(t, err, "update title: ")
}

func TestUpdateSessionTimestamp_QueryErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := store.UpdateSessionTimestamp("any")
	assertWrappedErr(t, err, "update timestamp: ")
}

func TestDeleteSession_QueryErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := store.DeleteSession("any")
	assertWrappedErr(t, err, "delete session: ")
}

// UpdateContextTokens returns the driver error unwrapped; zeroing that return
// would silently swallow every write failure.
func TestUpdateContextTokens_QueryError(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := store.UpdateContextTokens("any", 5)
	if err == nil {
		t.Fatal("UpdateContextTokens should fail with a closed store")
	}
	if err.Error() != "sql: database is closed" {
		t.Errorf("error = %q, want %q", err.Error(), "sql: database is closed")
	}
}

// --- listSessionsFiltered: nullable column materialisation ---

func TestListSessions_NullableColumnsRoundTrip(t *testing.T) {
	store := openTestStore(t)
	insertRawSession(t, store,
		"parent_session_id, fork_point_seq, agent_type, mode, engine_id, settings",
		"nr-1", "/p", testTimeBase, testTimeBase,
		"parent-x", 42, "Explore", "plan", "e7", `{"k":"v"}`,
	)

	sessions, err := store.ListSessions("/p", 10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(sessions))
	}

	got := sessions[0]
	if got.ParentSessionID != "parent-x" {
		t.Errorf("ParentSessionID = %q, want parent-x", got.ParentSessionID)
	}
	if got.ForkPointSeq != 42 {
		t.Errorf("ForkPointSeq = %d, want 42", got.ForkPointSeq)
	}
	if got.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want Explore", got.AgentType)
	}
	if got.Mode != "plan" {
		t.Errorf("Mode = %q, want plan", got.Mode)
	}
	if got.EngineID != "e7" {
		t.Errorf("EngineID = %q, want e7", got.EngineID)
	}
	if len(got.Settings) != 1 || got.Settings["k"] != "v" {
		t.Errorf("Settings = %v, want map with one entry k=v", got.Settings)
	}
}

// limit == 1 must still take the LIMIT branch; widening the guard to limit > 1
// drops the clause and returns the whole project.
func TestListSessions_LimitOneReturnsOne(t *testing.T) {
	store := openTestStore(t)
	for i := range 3 {
		if _, err := store.CreateSession("/p", "m"); err != nil {
			t.Fatalf("CreateSession %d: %v", i, err)
		}
	}

	sessions, err := store.ListSessions("/p", 1)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("len(sessions) = %d, want 1 (limit 1 must apply)", len(sessions))
	}
}

// --- SessionsTouchedSince ---

func TestSessionsTouchedSince_QueryErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := store.SessionsTouchedSince("/p", testTimeBase, "")
	assertWrappedErr(t, err, "query sessions touched since: ")
}

// --- PruneEmptySessions ---

func TestPruneEmptySessions_QueryErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := store.PruneEmptySessions("/p")
	assertWrappedErr(t, err, "query empty sessions: ")
}

func TestPruneEmptySessions_ScanErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec(
		`INSERT INTO sessions (session_id, project_dir, title, created_at, updated_at)
		 VALUES (NULL, '/p', '', datetime('now'), datetime('now'))`,
	); err != nil {
		t.Fatalf("insert null-id session: %v", err)
	}

	_, err := store.PruneEmptySessions("/p")
	assertWrappedErr(t, err, "scan session id: ")
}

func TestPruneEmptySessions_DeleteErrorWraps(t *testing.T) {
	store := openTestStore(t)
	empty, err := store.CreateSession("/p", "m")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := store.db.Exec(
		`CREATE TRIGGER block_sess_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'boom'); END;`,
	); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	_, err = store.PruneEmptySessions("/p")
	assertWrappedErr(t, err, "delete session "+empty.SessionID+": ")
}

// --- getSession (unexported, used by ForkSession / MergeForkBack) ---

func TestGetSessionUnexported_NullableColumns(t *testing.T) {
	store := openTestStore(t)
	insertRawSession(t, store,
		"parent_session_id, fork_point_seq, agent_type, mode, engine_id, settings",
		"gs-1", "/p", testTimeBase, testTimeBase,
		"parent-x", 42, "Explore", "plan", "e7", "{}",
	)

	got, err := store.getSession("gs-1")
	if err != nil {
		t.Fatalf("getSession: %v", err)
	}
	if got.ParentSessionID != "parent-x" {
		t.Errorf("ParentSessionID = %q, want parent-x", got.ParentSessionID)
	}
	if got.ForkPointSeq != 42 {
		t.Errorf("ForkPointSeq = %d, want 42", got.ForkPointSeq)
	}
	if got.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want Explore", got.AgentType)
	}
	if got.Mode != "plan" {
		t.Errorf("Mode = %q, want plan", got.Mode)
	}
	if got.EngineID != "e7" {
		t.Errorf("EngineID = %q, want e7", got.EngineID)
	}
}

func TestGetSessionUnexported_SettingsParsedWithoutWarning(t *testing.T) {
	store := openTestStore(t)
	insertRawSession(t, store, "settings", "gs-2", "/p", testTimeBase, testTimeBase, `{"k":"v"}`)

	logBuf := captureSlog(t)
	got, err := store.getSession("gs-2")
	if err != nil {
		t.Fatalf("getSession: %v", err)
	}
	if len(got.Settings) != 1 || got.Settings["k"] != "v" {
		t.Errorf("Settings = %v, want map with one entry k=v", got.Settings)
	}
	if logBuf.String() != "" {
		t.Errorf("getSession logged %q, want no record for valid settings", logBuf.String())
	}
}

func TestGetSessionUnexported_CorruptSettingsLogsWarning(t *testing.T) {
	store := openTestStore(t)
	insertRawSession(t, store, "settings", "gs-3", "/p", testTimeBase, testTimeBase, "not-json")

	logBuf := captureSlog(t)
	got, err := store.getSession("gs-3")
	if err != nil {
		t.Fatalf("getSession must tolerate corrupt settings, got %v", err)
	}
	if got.Settings == nil || len(got.Settings) != 0 {
		t.Errorf("Settings = %v, want empty map", got.Settings)
	}
	if !strings.Contains(logBuf.String(), `msg="session: failed to parse settings"`) {
		t.Errorf("getSession logged %q, want a parse-failure record", logBuf.String())
	}
}

func TestGetSessionUnexported_EmptySettingsBecomesEmptyMap(t *testing.T) {
	store := openTestStore(t)
	insertRawSession(t, store, "settings", "gs-4", "/p", testTimeBase, testTimeBase, "")

	got, err := store.getSession("gs-4")
	if err != nil {
		t.Fatalf("getSession: %v", err)
	}
	if got.Settings == nil {
		t.Error("Settings = nil, want non-nil empty map")
	}
	if len(got.Settings) != 0 {
		t.Errorf("Settings = %v, want empty map", got.Settings)
	}
}

func TestGetSessionUnexported_QueryErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := store.getSession("any")
	assertWrappedErr(t, err, "query session: ")
}

// --- insertSession (unexported) ---

func TestInsertSession_EmptyEngineIDNormalizesToMain(t *testing.T) {
	store := openTestStore(t)

	if err := store.insertSession(&Session{
		SessionID:  "is-1",
		ProjectDir: "/p",
		CreatedAt:  testTimeBase,
		UpdatedAt:  testTimeBase,
		Settings:   map[string]string{},
	}); err != nil {
		t.Fatalf("insertSession: %v", err)
	}

	got, err := store.GetSession("is-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.EngineID != "main" {
		t.Errorf("EngineID = %q, want main", got.EngineID)
	}
}

func TestInsertSession_ExplicitEngineIDPreserved(t *testing.T) {
	store := openTestStore(t)

	if err := store.insertSession(&Session{
		SessionID:  "is-2",
		ProjectDir: "/p",
		EngineID:   "e9",
		CreatedAt:  testTimeBase,
		UpdatedAt:  testTimeBase,
		Settings:   map[string]string{},
	}); err != nil {
		t.Fatalf("insertSession: %v", err)
	}

	got, err := store.GetSession("is-2")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.EngineID != "e9" {
		t.Errorf("EngineID = %q, want e9 (an explicit engine must not be rewritten)", got.EngineID)
	}
}

// --- ExtractFirstPrompt loop and truncation boundaries ---

// A whitespace-only text block collapses to "" and must be skipped, not
// returned as the prompt.
func TestExtractFirstPrompt_WhitespaceBlockSkipped(t *testing.T) {
	content := `{"type":"user","message":{"content":[{"type":"text","text":"\n\n"},{"type":"text","text":"real prompt"}]}}`

	got := ExtractFirstPrompt(content)
	if got != "real prompt" {
		t.Errorf("ExtractFirstPrompt = %q, want %q", got, "real prompt")
	}
}

// A command-name block only seeds the fallback when nothing usable follows.
func TestExtractFirstPrompt_CommandNameThenRealText(t *testing.T) {
	content := `{"type":"user","message":{"content":[{"type":"text","text":"<command-name>cmd</command-name>"},{"type":"text","text":"real prompt"}]}}`

	got := ExtractFirstPrompt(content)
	if got != "real prompt" {
		t.Errorf("ExtractFirstPrompt = %q, want %q (the command fallback must not win)", got, "real prompt")
	}
}

func TestExtractFirstPrompt_SkippedTagThenRealText(t *testing.T) {
	content := `{"type":"user","message":{"content":[{"type":"text","text":"<session-hook>meta</session-hook>"},{"type":"text","text":"real prompt"}]}}`

	got := ExtractFirstPrompt(content)
	if got != "real prompt" {
		t.Errorf("ExtractFirstPrompt = %q, want %q", got, "real prompt")
	}
}

// 200 is the boundary: exactly 200 chars survives untouched, 201 truncates.
func TestExtractFirstPrompt_TruncationBoundary(t *testing.T) {
	exact := strings.Repeat("a", 200)
	content := `{"type":"user","message":{"content":[{"type":"text","text":"` + exact + `"}]}}`
	if got := ExtractFirstPrompt(content); got != exact {
		t.Errorf("200-char prompt = %q (len %d), want the input unchanged", got, len(got))
	}

	over := strings.Repeat("a", 201)
	content = `{"type":"user","message":{"content":[{"type":"text","text":"` + over + `"}]}}`
	want := strings.Repeat("a", 200) + "…"
	if got := ExtractFirstPrompt(content); got != want {
		t.Errorf("201-char prompt = %q (len %d), want %q", got, len(got), want)
	}
}

// Only text blocks contribute prompt text; a non-text block carrying a text
// field must not leak into the prompt.
func TestExtractFirstPrompt_NonTextBlockTextIgnored(t *testing.T) {
	content := `{"type":"user","message":{"content":[{"type":"tool_use","id":"tu1","text":"leaked"}]}}`

	if got := ExtractFirstPrompt(content); got != "" {
		t.Errorf("ExtractFirstPrompt = %q, want empty", got)
	}
}
