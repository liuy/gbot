package fileread

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"

	_ "modernc.org/sqlite"
)

func createTestDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_, err = db.Exec(`INSERT INTO users (id, name, email) VALUES (1, 'alice', 'alice@test.com'), (2, 'bob', 'bob@test.com'), (3, 'carol', NULL)`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER, title TEXT)`)
	if err != nil {
		t.Fatalf("create posts: %v", err)
	}
	_, err = db.Exec(`INSERT INTO posts (id, user_id, title) VALUES (1, 1, 'Hello'), (2, 1, 'World')`)
	if err != nil {
		t.Fatalf("insert posts: %v", err)
	}
	return dbPath
}

func TestSqliteListTables(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	tables, err := sqliteListTables(db)
	if err != nil {
		t.Fatalf("sqliteListTables: %v", err)
	}
	if !contains(tables, "users") || !contains(tables, "posts") {
		t.Errorf("expected users and posts, got %q", tables)
	}
}

func TestSqliteGetSchema(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	schema, err := sqliteGetSchema(db, "users", 5)
	if err != nil {
		t.Fatalf("sqliteGetSchema: %v", err)
	}
	if schema == "" {
		t.Error("expected non-empty schema")
	}
	if !contains(schema, "id") || !contains(schema, "name") {
		t.Errorf("schema missing columns: %q", schema)
	}
}

func TestSqliteGetRowByKey(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	result, err := sqliteGetRowByKey(db, "users", "1")
	if err != nil {
		t.Fatalf("sqliteGetRowByKey: %v", err)
	}
	if !contains(result, "alice") {
		t.Errorf("expected alice in result: %q", result)
	}
}

func TestSqliteGetRowByKey_NotFound(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	result, err := sqliteGetRowByKey(db, "users", "999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result, "No row found") {
		t.Errorf("expected 'No row found' message, got %q", result)
	}
}

func TestSqliteSelectRows(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, rows, _, err := sqliteSelectRows(db, "users", 10, 0, "", "")
	if err != nil {
		t.Fatalf("sqliteSelectRows: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("expected 3 rows, got %d", len(rows))
	}
}

func TestSqliteSelectRows_WithWhere(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, rows, _, err := sqliteSelectRows(db, "users", 10, 0, "id = 1", "")
	if err != nil {
		t.Fatalf("sqliteSelectRows with where: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if name, _ := rows[0]["name"].(string); name != "alice" {
		t.Errorf("expected alice, got %v", rows[0]["name"])
	}
}

func TestSqliteSelectRows_WithOrder(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, rows, _, err := sqliteSelectRows(db, "users", 10, 0, "", "name:desc")
	if err != nil {
		t.Fatalf("sqliteSelectRows with order: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	first, _ := rows[0]["name"].(string)
	if first != "carol" {
		t.Errorf("expected carol first with desc order, got %q", first)
	}
}

func TestSqliteSelectRows_WithLimit(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, rows, _, err := sqliteSelectRows(db, "users", 2, 0, "", "")
	if err != nil {
		t.Fatalf("sqliteSelectRows with limit: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(rows))
	}
}

func TestSqliteExecuteRawQuery(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	result, err := sqliteExecuteRawQuery(db, "SELECT COUNT(*) as cnt FROM users")
	if err != nil {
		t.Fatalf("sqliteExecuteRawQuery: %v", err)
	}
	if !contains(result, "3") {
		t.Errorf("expected count 3: %q", result)
	}
}

func TestSqliteExecuteRawQuery_ParameterRejected(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = sqliteExecuteRawQuery(db, "SELECT * FROM users WHERE id = ?")
	if err == nil {
		t.Fatal("expected error for bound parameter")
	}
	if !contains(err.Error(), "bound parameters") {
		t.Errorf("unexpected error: %v", err)
	}
}

// Red test: raw query returning a single long-text column (CREATE TABLE SQL)
// must not be truncated. Mirrors sqlite3 CLI default behavior: single column
// → raw value, no padding, no width cap.
func TestSqliteExecuteRawQuery_SingleLongTextColumnNotTruncated(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	createStmt := "CREATE TABLE sample (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT UNIQUE)"
	if _, err := db.Exec(createStmt); err != nil {
		t.Fatalf("create table: %v", err)
	}

	result, err := sqliteExecuteRawQuery(db, "SELECT sql FROM sqlite_master WHERE type='table' AND name='sample'")
	if err != nil {
		t.Fatalf("sqliteExecuteRawQuery: %v", err)
	}
	if !strings.Contains(result, createStmt) {
		t.Errorf("expected full CREATE statement %q in result, got %q", createStmt, result)
	}
}

// Raw query with explicit LIMIT should be respected — no auto cap when the
// user wrote LIMIT themselves.
func TestSqliteExecuteRawQuery_ExplicitLimitRespected(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE big (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	// Insert more rows than sqliteMaxRawQueryRows so cap would trigger otherwise.
	if _, err := db.Exec(`WITH RECURSIVE cnt(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM cnt WHERE x < ?)
		INSERT INTO big (id) SELECT x FROM cnt`, sqliteMaxRawQueryRows+500); err != nil {
		t.Fatal(err)
	}

	// User wrote explicit LIMIT 1500 (more than the default 1000 cap).
	result, err := sqliteExecuteRawQuery(db, "SELECT id FROM big LIMIT 1500")
	if err != nil {
		t.Fatalf("sqliteExecuteRawQuery: %v", err)
	}
	if contains(result, "truncated") {
		t.Errorf("explicit LIMIT should bypass cap, got truncation marker: %q", result)
	}
	// Sanity: result has more than 1000 lines (1 header-ish line + 1500 rows is wrong, single column = no header)
	// Single column → raw values, one per line → expect ~1500 lines.
	lines := strings.Count(result, "\n") + 1
	if lines <= sqliteMaxRawQueryRows {
		t.Errorf("expected > %d rows when LIMIT 1500 set, got %d lines", sqliteMaxRawQueryRows, lines)
	}
}

// Raw query without LIMIT still gets capped at sqliteMaxRawQueryRows.
func TestSqliteExecuteRawQuery_NoLimitStillCapped(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE big (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`WITH RECURSIVE cnt(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM cnt WHERE x < ?)
		INSERT INTO big (id) SELECT x FROM cnt`, sqliteMaxRawQueryRows+500); err != nil {
		t.Fatal(err)
	}

	result, err := sqliteExecuteRawQuery(db, "SELECT id FROM big")
	if err != nil {
		t.Fatalf("sqliteExecuteRawQuery: %v", err)
	}
	if !contains(result, "truncated") {
		t.Errorf("expected truncation marker without LIMIT, got: %q", result)
	}
}

func TestExecuteSqliteRead_List(t *testing.T) {
	dbPath := createTestDB(t)
	cand := sqlitePathCandidate{sqlitePath: dbPath, subPath: "", queryString: ""}
	result, err := executeSqliteRead(Input{FilePath: dbPath}, cand, dbPath)
	if err != nil {
		t.Fatalf("executeSqliteRead list: %v", err)
	}
	if !contains(result.Data.(TextOutput).Content, "users") {
		t.Errorf("expected users in list: %q", result.Data.(TextOutput).Content)
	}
}

func TestExecuteSqliteRead_Schema(t *testing.T) {
	dbPath := createTestDB(t)
	cand := sqlitePathCandidate{sqlitePath: dbPath, subPath: "users", queryString: ""}
	result, err := executeSqliteRead(Input{FilePath: dbPath}, cand, dbPath)
	if err != nil {
		t.Fatalf("executeSqliteRead schema: %v", err)
	}
	content := result.Data.(TextOutput).Content
	if !contains(content, "id") || !contains(content, "name") {
		t.Errorf("expected schema: %q", content)
	}
}

func TestExecuteSqliteRead_KeyLookup(t *testing.T) {
	dbPath := createTestDB(t)
	cand := sqlitePathCandidate{sqlitePath: dbPath, subPath: "users:1", queryString: ""}
	result, err := executeSqliteRead(Input{FilePath: dbPath}, cand, dbPath)
	if err != nil {
		t.Fatalf("executeSqliteRead key lookup: %v", err)
	}
	if !contains(result.Data.(TextOutput).Content, "alice") {
		t.Errorf("expected alice: %q", result.Data.(TextOutput).Content)
	}
}

func TestExecuteSqliteRead_RawQuery(t *testing.T) {
	dbPath := createTestDB(t)
	cand := sqlitePathCandidate{sqlitePath: dbPath, subPath: "", queryString: "q=SELECT name FROM users WHERE id = 2"}
	result, err := executeSqliteRead(Input{FilePath: dbPath}, cand, dbPath)
	if err != nil {
		t.Fatalf("executeSqliteRead raw query: %v", err)
	}
	if !contains(result.Data.(TextOutput).Content, "bob") {
		t.Errorf("expected bob: %q", result.Data.(TextOutput).Content)
	}
}

func TestParseSqliteSelector_Table(t *testing.T) {
	sel, err := parseSqliteSelector("users", "")
	if err != nil {
		t.Fatalf("parseSqliteSelector: %v", err)
	}
	if sel.kind != sqliteSelSchema {
		t.Errorf("expected kind schema, got %d", sel.kind)
	}
}

func TestParseSqliteSelector_List(t *testing.T) {
	sel, err := parseSqliteSelector("", "")
	if err != nil {
		t.Fatalf("parseSqliteSelector: %v", err)
	}
	if sel.kind != sqliteSelList {
		t.Errorf("expected kind list, got %d", sel.kind)
	}
}

func TestParseSqliteSelector_Key(t *testing.T) {
	sel, err := parseSqliteSelector("users:42", "")
	if err != nil {
		t.Fatalf("parseSqliteSelector: %v", err)
	}
	if sel.kind != sqliteSelRow {
		t.Errorf("expected kind row, got %d", sel.kind)
	}
}

func TestParseSqliteSelector_QueryParams(t *testing.T) {
	sel, err := parseSqliteSelector("users", "limit=5&where=id=1")
	if err != nil {
		t.Fatalf("parseSqliteSelector: %v", err)
	}
	if sel.kind != sqliteSelQuery {
		t.Errorf("expected kind query, got %d", sel.kind)
	}
}

func TestParseSqliteSelector_RawQuery(t *testing.T) {
	sel, err := parseSqliteSelector("", "q=SELECT 1")
	if err != nil {
		t.Fatalf("parseSqliteSelector: %v", err)
	}
	if sel.kind != sqliteSelRaw {
		t.Errorf("expected kind raw, got %d", sel.kind)
	}
}

func TestParseSqliteSelector_Errors(t *testing.T) {
	tests := []struct {
		name       string
		subPath    string
		query      string
		wantErrSub string
	}{
		{"raw with subpath", "users", "q=SELECT 1", "cannot be combined"},
		{"unsupported param", "users", "bogus=1", "unsupported"},
		{"params without table", "", "limit=5", "require a table"},
		{"raw with pagination", "", "q=SELECT 1&limit=5", "cannot be combined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseSqliteSelector(tt.subPath, tt.query)
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantErrSub)
			}
			if !contains(err.Error(), tt.wantErrSub) {
				t.Errorf("error %q doesn't contain %q", err.Error(), tt.wantErrSub)
			}
		})
	}
}

func TestParseSqlitePathCandidates(t *testing.T) {
	tests := []struct {
		path    string
		wantMin int
		wantSub string
		wantQS  string
	}{
		{"db.sqlite:users", 1, "users", ""},
		{"db.sqlite?q=SELECT 1", 1, "", "q=SELECT 1"},
		{"data.db:tbl?limit=5", 1, "tbl", "limit=5"},
		{"notadb.txt", 0, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			cands := parseSqlitePathCandidates(tt.path)
			if len(cands) < tt.wantMin {
				t.Fatalf("expected at least %d candidates, got %d", tt.wantMin, len(cands))
			}
			if tt.wantMin > 0 {
				if cands[0].subPath != tt.wantSub {
					t.Errorf("subPath = %q, want %q", cands[0].subPath, tt.wantSub)
				}
				if cands[0].queryString != tt.wantQS {
					t.Errorf("queryString = %q, want %q", cands[0].queryString, tt.wantQS)
				}
			}
		})
	}
}

func TestSplitSqliteRemainder(t *testing.T) {
	sub, qs := splitSqliteRemainder("users")
	if sub != "users" || qs != "" {
		t.Errorf("got (%q, %q), want (users, '')", sub, qs)
	}
	sub, qs = splitSqliteRemainder(":users")
	if sub != "users" || qs != "" {
		t.Errorf("got (%q, %q), want (users, '')", sub, qs)
	}
	sub, qs = splitSqliteRemainder("users?q=1")
	if sub != "users" || qs != "q=1" {
		t.Errorf("got (%q, %q), want (users, q=1)", sub, qs)
	}
}

func TestParseQueryString(t *testing.T) {
	params := parseQueryString("limit=5&where=id=1&offset=0")
	if params["limit"] != "5" {
		t.Errorf("limit = %q", params["limit"])
	}
	if params["where"] != "id=1" {
		t.Errorf("where = %q", params["where"])
	}
	if params["offset"] != "0" {
		t.Errorf("offset = %q", params["offset"])
	}

	empty := parseQueryString("")
	if len(empty) != 0 {
		t.Errorf("expected empty map, got %v", empty)
	}

	novalue := parseQueryString("flag")
	if novalue["flag"] != "" {
		t.Errorf("expected empty value, got %q", novalue["flag"])
	}
}

func TestGetTableInfo(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cols, err := getTableInfo(db, "users")
	if err != nil {
		t.Fatalf("getTableInfo: %v", err)
	}
	if len(cols) != 3 {
		t.Errorf("expected 3 columns, got %d", len(cols))
	}
}

func TestProbeRowCount(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	count := probeRowCount(db, "users")
	if count.rows != 3 || count.kind != "exact" {
		t.Errorf("probeRowCount = %+v, want {3 exact}", count)
	}
}

func TestLoadRowEstimates(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	estimates := loadRowEstimates(db)
	if len(estimates) != 0 {
		t.Errorf("expected 0 estimates (no sqlite_stat1), got %d", len(estimates))
	}
}

func TestLoadRowEstimates_WithStat1(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE big (id INTEGER PRIMARY KEY, v TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO big (id, v) VALUES (1,'a'),(2,'b'),(3,'c')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`ANALYZE`)
	if err != nil {
		t.Fatal(err)
	}

	estimates := loadRowEstimates(db)
	if len(estimates) == 0 {
		t.Fatal("expected non-empty estimates after ANALYZE")
	}
}

func TestProbeRowCount_AtLeast(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE big (id INTEGER PRIMARY KEY)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 10; i++ {
		_, err = db.Exec(`INSERT INTO big (id) VALUES (?)`, i)
		if err != nil {
			t.Fatal(err)
		}
	}

	count := probeRowCount(db, "big")
	if count.kind != "exact" {
		t.Errorf("expected exact for 10 rows, got %v", count)
	}
}

func TestValidateWhereClause_Quoted(t *testing.T) {
	tests := []struct {
		name   string
		clause string
	}{
		{"single-quoted semicolon", "name = 'a;b'"},
		{"double-quoted semicolon", `name = "a;b"`},
		{"single-quoted limit keyword", "name = 'LIMIT'"},
		{"escaped single quote", "name = 'it''s'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWhereClause(tt.clause)
			if err != nil {
				t.Errorf("validateWhereClause(%q): unexpected error %v", tt.clause, err)
			}
		})
	}
}

func TestBuildSqliteOutput_LongSingleColumn(t *testing.T) {
	longVal := strings.Repeat("x", 200)
	rows := []map[string]any{
		{"col": longVal},
	}
	got := buildSqliteOutput([]string{"col"}, rows)
	if got != longVal {
		t.Errorf("expected full value, got truncated: %q", got)
	}
}

func TestBuildSqliteOutput_NilValue(t *testing.T) {
	rows := []map[string]any{
		{"col": nil},
	}
	got := buildSqliteOutput([]string{"col"}, rows)
	if !contains(got, "NULL") {
		t.Errorf("expected NULL rendering, got %q", got)
	}
}

func TestStringifySqliteValue_Float(t *testing.T) {
	tests := []struct {
		val  any
		want string
	}{
		{float64(3.0), "3"},
		{float64(3.14), "3.14"},
		{int64(-5), "-5"},
		{false, "false"},
	}
	for _, tt := range tests {
		got := stringifySqliteValue(tt.val)
		if got != tt.want {
			t.Errorf("stringifySqliteValue(%v) = %q, want %q", tt.val, got, tt.want)
		}
	}
}

func TestCoerceLookupValue_Float(t *testing.T) {
	got, err := coerceLookupValue("3.14", "REAL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 3.14 {
		t.Errorf("got %v", got)
	}
}

func TestCoerceLookupValue_TextFallback(t *testing.T) {
	got, err := coerceLookupValue("hello", "UNKNOWN_TYPE")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %v", got)
	}
}

func TestCoerceLookupValue_IntInvalid(t *testing.T) {
	_, err := coerceLookupValue("abc", "INTEGER")
	if err == nil {
		t.Error("should fail for non-integer int column")
	}
	if !strings.Contains(err.Error(), "integer") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestSqliteGetRowByKey_TextPK_PrefixAmbiguous(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE items (code TEXT PRIMARY KEY, name TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO items (code, name) VALUES ('abc-001', 'alpha'), ('abc-002', 'beta')`)
	if err != nil {
		t.Fatal(err)
	}

	result, err := sqliteGetRowByKey(db, "items", "abc")
	if err != nil {
		t.Fatalf("ambiguous prefix: %v", err)
	}
	if !contains(result, "Multiple rows") {
		t.Errorf("expected Multiple rows message: %q", result)
	}
}

func TestSqliteSelectRows_BadColumn(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, _, _, err = sqliteSelectRows(db, "users", 10, 0, "", "badcol:asc")
	if err == nil {
		t.Error("should fail for bad order column")
	}
	if !strings.Contains(err.Error(), "order") && !strings.Contains(err.Error(), "column") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestSqliteSelectRows_BadWhere(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, _, _, err = sqliteSelectRows(db, "users", 10, 0, "badcol = ;", "")
	if err == nil {
		t.Error("should fail for invalid where")
	}
	if !strings.Contains(err.Error(), "where") && !strings.Contains(err.Error(), "syntax") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestTrySqlitePath_RealFile(t *testing.T) {
	dbPath := createTestDB(t)
	result, handled, err := trySqlitePath(Input{FilePath: dbPath})
	if err != nil {
		t.Fatalf("trySqlitePath: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true for real SQLite file")
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestTrySqlitePath_TableSelector(t *testing.T) {
	dbPath := createTestDB(t)
	result, handled, err := trySqlitePath(Input{FilePath: dbPath + ":users"})
	if err != nil {
		t.Fatalf("trySqlitePath: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}
	content := result.Data.(TextOutput).Content
	if !contains(content, "alice") {
		t.Errorf("expected alice: %q", content)
	}
}

func TestTrySqlitePath_NotSqlite(t *testing.T) {
	dir := t.TempDir()
	txtPath := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(txtPath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	_, handled, err := trySqlitePath(Input{FilePath: txtPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Error("expected handled=false for non-SQLite file")
	}
}

func TestTrySqlitePath_NoMatch(t *testing.T) {
	_, handled, err := trySqlitePath(Input{FilePath: "/nonexistent/file.db"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Error("expected handled=false for nonexistent path")
	}
}

func TestBuildSqliteOutput_WideTable(t *testing.T) {
	cols := make([]string, 10)
	row := map[string]any{}
	for i := range 10 {
		cols[i] = "col" + string(rune('A'+i))
		row[cols[i]] = strings.Repeat("x", 50)
	}
	got := buildSqliteOutput(cols, []map[string]any{row})
	if !contains(got, "colA|colB") {
		t.Errorf("expected pipe-separated header, got %q", got)
	}
}

func TestBuildSqliteOutput_EmptyRows(t *testing.T) {
	got := buildSqliteOutput([]string{"a", "b"}, nil)
	if !contains(got, "no rows") {
		t.Errorf("expected 'no rows' marker: %q", got)
	}
}

func TestSqliteListTables_Empty(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "empty.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	result, err := sqliteListTables(db)
	if err != nil {
		t.Fatalf("sqliteListTables: %v", err)
	}
	if result != "(no tables)" {
		t.Errorf("expected '(no tables)', got %q", result)
	}
}

func TestSqliteExecuteRawQuery_Truncation(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE big (id INTEGER PRIMARY KEY)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 1001; i++ {
		_, err = db.Exec(`INSERT INTO big (id) VALUES (?)`, i)
		if err != nil {
			t.Fatal(err)
		}
	}

	result, err := sqliteExecuteRawQuery(db, "SELECT * FROM big")
	if err != nil {
		t.Fatalf("sqliteExecuteRawQuery: %v", err)
	}
	if !contains(result, "truncated") {
		t.Errorf("expected truncation marker: %q", result)
	}
}

func TestSqliteGetRowByKey_IntPK_NotFound(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO t (id, v) VALUES (1, 'hello')`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = sqliteGetRowByKey(db, "t", "abc")
	if err == nil {
		t.Error("should fail for non-integer INTEGER PK")
	}
	if !strings.Contains(err.Error(), "invalid") && !strings.Contains(err.Error(), "integer") {
		t.Errorf("wrong error: %v", err)
	}

	result, err := sqliteGetRowByKey(db, "t", "999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result, "No row found") {
		t.Errorf("expected No row found: %q", result)
	}
}

func TestSqliteSelectRows_Offset(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, rows, _, err := sqliteSelectRows(db, "users", 10, 1, "", "id:asc")
	if err != nil {
		t.Fatalf("sqliteSelectRows with offset: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows after offset 1, got %d", len(rows))
	}
	first, _ := rows[0]["name"].(string)
	if first != "bob" {
		t.Errorf("expected bob first after offset, got %q", first)
	}
}

func TestSqliteQueryRows_WithWhere(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	sel := &sqliteSelector{kind: sqliteSelQuery, table: "users", limit: 10, offset: 0, where: "id = 2"}
	result, err := sqliteQueryRows(db, sel)
	if err != nil {
		t.Fatalf("sqliteQueryRows: %v", err)
	}
	if !contains(result, "bob") {
		t.Errorf("expected bob: %q", result)
	}
}

func TestParseSqlitePathCandidates_MultipleSplits(t *testing.T) {
	cands := parseSqlitePathCandidates("a/b/db.sqlite:users:1")
	if len(cands) < 1 {
		t.Fatalf("expected at least 1 candidate, got %d", len(cands))
	}
}

func TestExecuteSqliteRead_QueryMode(t *testing.T) {
	dbPath := createTestDB(t)
	cand := sqlitePathCandidate{sqlitePath: dbPath, subPath: "users", queryString: "limit=2&order=id:desc"}
	result, err := executeSqliteRead(Input{FilePath: dbPath}, cand, dbPath)
	if err != nil {
		t.Fatalf("executeSqliteRead query: %v", err)
	}
	content := result.Data.(TextOutput).Content
	if !contains(content, "carol") {
		t.Errorf("expected carol: %q", content)
	}
}

func TestSqliteGetSchema_SampleFail(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE v (id INTEGER PRIMARY KEY, name TEXT)`)
	if err != nil {
		t.Fatal(err)
	}

	schema, err := sqliteGetSchema(db, "v", 5)
	if err != nil {
		t.Fatalf("sqliteGetSchema: %v", err)
	}
	if !contains(schema, "CREATE TABLE") {
		t.Errorf("expected CREATE TABLE in schema: %q", schema)
	}
}

func TestGetTableColumns(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cols, err := getTableColumns(db, "users")
	if err != nil {
		t.Fatalf("getTableColumns: %v", err)
	}
	if len(cols) != 3 {
		t.Errorf("expected 3 columns, got %d", len(cols))
	}
	if cols[0] != "id" {
		t.Errorf("expected first column 'id', got %q", cols[0])
	}
}

func TestGetTablePrimaryKey_Composite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE junction (a INTEGER, b INTEGER, PRIMARY KEY (a, b))`)
	if err != nil {
		t.Fatal(err)
	}

	pkCol, pkType, err := getTablePrimaryKey(db, "junction")
	if err != nil {
		t.Fatalf("getTablePrimaryKey: %v", err)
	}
	if pkCol != "" || pkType != "" {
		t.Errorf("expected empty PK for composite, got %q/%q", pkCol, pkType)
	}
}

func TestIsSqliteFile(t *testing.T) {
	dir := t.TempDir()
	sqlitePath := filepath.Join(dir, "test.db")
	txtPath := filepath.Join(dir, "test.txt")

	db, err := sql.Open("sqlite", sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TABLE x (id INTEGER)")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(txtPath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	if !isSqliteFile(sqlitePath) {
		t.Error("expected isSqliteFile=true for valid SQLite db")
	}
	if isSqliteFile(txtPath) {
		t.Error("expected isSqliteFile=false for text file")
	}
	if isSqliteFile("/nonexistent/path.db") {
		t.Error("expected isSqliteFile=false for nonexistent file")
	}
}

func TestScanRowsWithCount(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	result, count, err := scanRowsWithCount(db, "SELECT id, name FROM users ORDER BY id LIMIT 2", nil)
	if err != nil {
		t.Fatalf("scanRowsWithCount: %v", err)
	}
	if count != 2 {
		t.Errorf("expected count 2, got %d", count)
	}
	if !contains(result, "alice") {
		t.Errorf("expected alice: %q", result)
	}
}

func TestScanSingleRow(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	result, err := scanSingleRow(db, "SELECT id, name FROM users WHERE id = 1", nil)
	if err != nil {
		t.Fatalf("scanSingleRow: %v", err)
	}
	if !contains(result, "alice") {
		t.Errorf("expected alice: %q", result)
	}
}

func TestScanSingleRow_NoMatch(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	result, err := scanSingleRow(db, "SELECT id, name FROM users WHERE id = 999", nil)
	if err != nil {
		t.Fatalf("scanSingleRow: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}
}

func TestSqliteGetRowByKey_TextPK_PrefixMatch(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE items (code TEXT PRIMARY KEY, name TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO items (code, name) VALUES ('abc-001', 'alpha'), ('abc-002', 'beta')`)
	if err != nil {
		t.Fatal(err)
	}

	result, err := sqliteGetRowByKey(db, "items", "abc-001")
	if err != nil {
		t.Fatalf("exact match: %v", err)
	}
	if !contains(result, "alpha") {
		t.Errorf("expected alpha: %q", result)
	}

	result, err = sqliteGetRowByKey(db, "items", "xyz")
	if err != nil {
		t.Fatalf("not found: %v", err)
	}
	if !contains(result, "No row found") {
		t.Errorf("expected not found: %q", result)
	}
}

func TestSqliteGetRowByKey_NoPK_RowID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE log (msg TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO log (msg) VALUES ('hello')`)
	if err != nil {
		t.Fatal(err)
	}

	result, err := sqliteGetRowByKey(db, "log", "1")
	if err != nil {
		t.Fatalf("rowid lookup: %v", err)
	}
	if !contains(result, "hello") {
		t.Errorf("expected hello: %q", result)
	}

	_, err = sqliteGetRowByKey(db, "log", "notanumber")
	if err == nil {
		t.Error("should fail for non-integer rowid")
	}
	if !strings.Contains(err.Error(), "invalid") && !strings.Contains(err.Error(), "integer") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestSqliteGetSchema_NotFound(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = sqliteGetSchema(db, "nonexistent", 5)
	if err == nil {
		t.Fatal("should fail for nonexistent table")
	}
	if !strings.Contains(err.Error(), "no such table") && !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestSqliteQueryRows(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	sel := &sqliteSelector{kind: sqliteSelQuery, table: "users", limit: 2, offset: 0}
	result, err := sqliteQueryRows(db, sel)
	if err != nil {
		t.Fatalf("sqliteQueryRows: %v", err)
	}
	if !contains(result, "alice") {
		t.Errorf("expected alice: %q", result)
	}
}

func TestQuoteIdent(t *testing.T) {
	got := quoteIdent("users")
	if got != `"users"` {
		t.Errorf("quoteIdent(users) = %q, want %q", got, `"users"`)
	}
	got = quoteIdent(`a"b`)
	if got != `"a""b"` {
		t.Errorf("quoteIdent(a\"b) = %q, want %q", got, `"a""b"`)
	}
}

func TestStringifySqliteValue_BLOB(t *testing.T) {
	got := stringifySqliteValue([]byte{1, 2, 3})
	if got != "<BLOB 3 bytes>" {
		t.Errorf("got %q", got)
	}
}

func TestBytesEqual(t *testing.T) {
	if !bytesEqual([]byte{1, 2, 3}, []byte{1, 2, 3}) {
		t.Error("equal slices returned false")
	}
	if bytesEqual([]byte{1, 2}, []byte{1, 2, 3}) {
		t.Error("different lengths returned true")
	}
	if bytesEqual([]byte{1, 2, 3}, []byte{1, 2, 4}) {
		t.Error("different values returned true")
	}
}

// TestProbeRowCount_AtLeastCap covers the "counted > cap" branch in
// probeRowCount — table with more rows than the cap returns kind="atLeast".
func TestProbeRowCount_AtLeastCap(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE big (id INTEGER PRIMARY KEY)`)
	if err != nil {
		t.Fatal(err)
	}
	// Use recursive CTE for fast bulk insert — individual INSERTs are too slow.
	_, err = db.Exec(`WITH RECURSIVE cnt(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM cnt WHERE x < ?)
		INSERT INTO big (id) SELECT x FROM cnt`, sqliteRowCountProbeCap+100)
	if err != nil {
		t.Fatal(err)
	}

	got := probeRowCount(db, "big")
	if got.kind != "atLeast" {
		t.Errorf("probeRowCount kind = %q, want \"atLeast\"", got.kind)
	}
	if got.rows != sqliteRowCountProbeCap {
		t.Errorf("probeRowCount rows = %d, want %d", got.rows, sqliteRowCountProbeCap)
	}
}

// TestProbeRowCount_QueryError covers the err != nil branch — querying a
// nonexistent table should yield {exact, 0} silently.
func TestProbeRowCount_QueryError(t *testing.T) {
	dbPath := createTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	got := probeRowCount(db, "does_not_exist")
	if got.kind != "exact" {
		t.Errorf("kind = %q, want \"exact\"", got.kind)
	}
	if got.rows != 0 {
		t.Errorf("rows = %d, want 0", got.rows)
	}
}

// TestProbeRowCount_WithStat1Estimate covers the estimate > cap short-circuit.
// When sqlite_stat1 reports a count above the cap, probeRowCount returns
// kind="estimate" without doing a COUNT query.
func TestProbeRowCount_WithStat1Estimate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE big (id INTEGER PRIMARY KEY)`)
	if err != nil {
		t.Fatal(err)
	}
	// ANALYZE populates sqlite_stat1 with a row count estimate.
	for range 10 {
		_, err = db.Exec("INSERT INTO big DEFAULT VALUES")
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec("ANALYZE")
	if err != nil {
		t.Fatal(err)
	}

	// Manually inflate the stat1 row count so it exceeds the cap.
	_, err = db.Exec("UPDATE sqlite_stat1 SET stat = ? WHERE tbl = 'big'",
		fmt.Sprintf("%d", sqliteRowCountProbeCap+5))
	if err != nil {
		t.Fatal(err)
	}

	got := probeRowCount(db, "big")
	if got.kind != "estimate" {
		t.Errorf("kind = %q, want \"estimate\"", got.kind)
	}
	if got.rows != int64(sqliteRowCountProbeCap+5) {
		t.Errorf("rows = %d, want %d", got.rows, sqliteRowCountProbeCap+5)
	}
}

// Pin tests for the sqlite.go mutation sweep; assertions are exact so a single
// changed byte flips the test. Mutant line:mutator pairs are in the ledger.

func pinDB(t *testing.T, name string, script ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range script {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func pinExec(t *testing.T, filePath string) (*tool.ToolResult, error) {
	t.Helper()
	return Execute(context.Background(), mustPinJSON(t, Input{FilePath: filePath}), nil)
}

func pinContent(t *testing.T, filePath string) string {
	t.Helper()
	res, err := pinExec(t, filePath)
	if err != nil {
		t.Fatalf("Execute(%q): %v", filePath, err)
	}
	to, ok := res.Data.(TextOutput)
	if !ok {
		t.Fatalf("Execute(%q) data = %T, want TextOutput", filePath, res.Data)
	}
	return to.Content
}

// ---------------------------------------------------------------------------
// header probe and path candidates
// ---------------------------------------------------------------------------

func TestPinSqlite_MagicOnlyFile(t *testing.T) {
	t.Parallel()
	// A file of exactly the 16 magic bytes reports as SQLite (the header
	// check only reads 16 bytes); a 15-byte truncation does not.
	dir := t.TempDir()
	p := filepath.Join(dir, "magic.db")
	if err := os.WriteFile(p, append([]byte("SQLite format 3"), 0), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isSqliteFile(p) {
		t.Error("isSqliteFile(16-byte magic) = false, want true")
	}
	short := filepath.Join(dir, "short.db")
	if err := os.WriteFile(short, []byte("SQLite format"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isSqliteFile(short) {
		t.Error("isSqliteFile(15 bytes) = true, want false")
	}
}

func TestPinSqlite_PathCandidateLongestFirst(t *testing.T) {
	t.Parallel()
	cands := parseSqlitePathCandidates("x.db:y.db:z")
	if len(cands) != 2 {
		t.Fatalf("candidates = %+v, want 2", cands)
	}
	if cands[0].sqlitePath != "x.db:y.db" || cands[0].subPath != "z" {
		t.Errorf("first candidate = %+v, want {x.db:y.db z}", cands[0])
	}
	if cands[1].sqlitePath != "x.db" || cands[1].subPath != "y.db:z" {
		t.Errorf("second candidate = %+v, want {x.db y.db:z}", cands[1])
	}
	if got := parseSqlitePathCandidates("plain.txt"); len(got) != 0 {
		t.Errorf("non-sqlite path candidates = %+v, want none", got)
	}
}

// ---------------------------------------------------------------------------
// selector / query-string parsing
// ---------------------------------------------------------------------------

func TestPinSqlite_SelectorParse(t *testing.T) {
	t.Parallel()
	sel, err := parseSqliteSelector("users:5", "")
	if err != nil {
		t.Fatalf("users:5 err = %v", err)
	}
	if sel.kind != sqliteSelRow || sel.table != "users" || sel.key != "5" {
		t.Errorf("row selector = %+v", *sel)
	}

	sel, err = parseSqliteSelector("", "")
	if err != nil {
		t.Fatal(err)
	}
	if sel.kind != sqliteSelList {
		t.Errorf("empty selector kind = %v, want list", sel.kind)
	}

	sel, err = parseSqliteSelector("t", "")
	if err != nil {
		t.Fatalf("bare table selector err = %v, want schema selector", err)
	}
	if sel.kind != sqliteSelSchema || sel.table != "t" || sel.sampleLimit != sqliteDefaultSchemaSample {
		t.Errorf("bare table selector = %+v", *sel)
	}

	sel, err = parseSqliteSelector("t", "limit=1")
	if err != nil {
		t.Fatal(err)
	}
	if sel.kind != sqliteSelQuery || sel.limit != 1 || sel.offset != 0 {
		t.Errorf("limit=1 selector = %+v", *sel)
	}

	for _, c := range []struct {
		sub, qs, wantErr string
	}{
		{"t", "limit=0", "sqlite limit must be a positive integer; got '0'"},
		{"t", "offset=0", ""}, // offset=0 is valid
		{"t", "where=x;drop", "must not contain comments or statement terminators"},
	} {
		sel, err := parseSqliteSelector(c.sub, c.qs)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("(%q,%q) err = %v, want nil", c.sub, c.qs, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("(%q,%q) err = %v, want %q", c.sub, c.qs, err, c.wantErr)
		}
		if err != nil && sel != nil {
			t.Errorf("(%q,%q) selector = %+v alongside error, want nil", c.sub, c.qs, *sel)
		}
	}
}

func TestPinSqlite_QueryStringShapes(t *testing.T) {
	t.Parallel()
	got := parseQueryString("a=1&&b=2")
	if len(got) != 2 || got["a"] != "1" || got["b"] != "2" {
		t.Errorf("parseQueryString(a=1&&b=2) = %v, want a=1 b=2", got)
	}
	if got := parseQueryString(""); len(got) != 0 {
		t.Errorf("parseQueryString(\"\") = %v, want empty", got)
	}
	if got := parseQueryString("flag"); len(got) != 1 || got["flag"] != "" {
		t.Errorf("parseQueryString(flag) = %v, want {flag:}", got)
	}
}

func TestPinSqlite_ValidateWhereQuoteEscapes(t *testing.T) {
	t.Parallel()
	if err := validateWhereClause("limit"); err == nil || !strings.Contains(err.Error(), "LIMIT/OFFSET") {
		t.Errorf("validateWhereClause(limit) = %v, want forbidden-keyword error", err)
	}
	if err := validateWhereClause("x limit y"); err == nil {
		t.Error("validateWhereClause(x limit y) = nil, want error")
	}
	for _, ok := range []string{"a''", "a\"\"", "'it''s fine'", "'x'' limit y'"} {
		if err := validateWhereClause(ok); err != nil {
			t.Errorf("validateWhereClause(%q) = %v, want nil", ok, err)
		}
	}
	// An adjacent quote pair is an empty string: the keyword after it is
	// outside quotes and must be scanned.
	for _, bad := range []string{"''limit", "\"\"limit", "attach", "ATTACH", "LIMIT"} {
		if err := validateWhereClause(bad); err == nil {
			t.Errorf("validateWhereClause(%q) = nil, want error", bad)
		}
	}
	// A keyword glued to an identifier-range boundary char stays one token
	// (no false flush mid-keyword), and boundary chars do break tokens so
	// the keyword alone still errors.
	for _, bad := range []string{"limit{", "limit[", "limit/", "limit:", "limit!", "limit\x80"} {
		if err := validateWhereClause(bad); err == nil {
			t.Errorf("validateWhereClause(%q) = nil, want error", bad)
		}
	}
	for _, ok := range []string{"limitz", "limitZ", "limit0", "limit9", "limit_", "'a''", "\"a\"\""} {
		if err := validateWhereClause(ok); err != nil {
			t.Errorf("validateWhereClause(%q) = %v, want nil", ok, err)
		}
	}
}

func TestPinSqlite_SqlHasExplicitLimit(t *testing.T) {
	t.Parallel()
	if !sqlHasExplicitLimit("SELECT 1 LIMIT 5") {
		t.Error("explicit limit not detected")
	}
	if sqlHasExplicitLimit("SELECT 'limit 5'") {
		t.Error("limit inside string literal must not count")
	}
	if sqlHasExplicitLimit("x''limit 5") {
		// The escaped '' keeps the quote open; everything after is string content.
		t.Error("escaped-quote handling broke: limit inside string counted")
	}
	if sqlHasExplicitLimit("'limit 5'") {
		t.Error("fully-quoted limit counted as explicit")
	}
	if !sqlHasExplicitLimit("'a'limit 5'") {
		t.Error("limit after a closed string literal not detected")
	}
	for _, no := range []string{"a'b'", "a'", "'a''", "a\"b\"", "\"a\""} {
		if sqlHasExplicitLimit(no) {
			t.Errorf("sqlHasExplicitLimit(%q) = true, want false", no)
		}
	}
	if !sqlHasExplicitLimit("SELECT 'it''s' LIMIT 3") {
		t.Error("explicit limit after string literal not detected")
	}
}

// ---------------------------------------------------------------------------
// Execute over real databases
// ---------------------------------------------------------------------------

func TestPinSqlite_TextPKRowLookups(t *testing.T) {
	t.Parallel()
	db := pinDB(t, "text.sqlite",
		"CREATE TABLE t (name TEXT PRIMARY KEY)",
		"INSERT INTO t (name) VALUES ('abc'), ('abd'), ('xyz')",
	)
	if got := pinContent(t, db+":t:abc"); got != "name: abc" {
		t.Errorf("exact key lookup = %q, want 'name: abc'", got)
	}
	// Unique prefix resolves to the single matching row.
	if got := pinContent(t, db+":t:xy"); got != "name: xyz" {
		t.Errorf("unique prefix lookup = %q, want 'name: xyz'", got)
	}
	if got := pinContent(t, db+":t:ab"); got != "Multiple rows in 't' have PK starting with 'ab'; provide a longer prefix." {
		t.Errorf("ambiguous prefix lookup = %q, want multiple-rows message", got)
	}
	if got := pinContent(t, db+":t:zzz"); got != "No row found in table 't' for key 'zzz'." {
		t.Errorf("missing key lookup = %q, want no-row message", got)
	}
}

func TestPinSqlite_RowidAndIntPKLookups(t *testing.T) {
	t.Parallel()
	noPK := pinDB(t, "nopk.sqlite",
		"CREATE TABLE t (v TEXT)",
		"INSERT INTO t (v) VALUES ('first'), ('second')",
	)
	if got := pinContent(t, noPK+":t:1"); got != "v: first" {
		t.Errorf("rowid 1 = %q, want 'v: first'", got)
	}
	if got := pinContent(t, noPK+":t:8"); got != "No row found in table 't' for rowid '8'." {
		t.Errorf("missing rowid = %q, want no-row message", got)
	}
	if got := pinContent(t, noPK+":t:999"); got != "No row found in table 't' for rowid '999'." {
		t.Errorf("out-of-range rowid = %q, want no-row message", got)
	}
	_, err := pinExec(t, noPK+":t:9223372036854775807")
	if err != nil {
		t.Errorf("max-int64 rowid err = %v, want nil (valid integer)", err)
	}

	intPK := pinDB(t, "intpk.sqlite",
		"CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)",
		"INSERT INTO t (id, v) VALUES (1, 'one')",
	)
	if got := pinContent(t, intPK+":t:8"); got != "No row found in table 't' for key '8'." {
		t.Errorf("missing int key = %q, want no-row message", got)
	}
	if _, err := pinExec(t, intPK+":t:9223372036854775807"); err != nil {
		t.Errorf("max-int64 key err = %v, want nil", err)
	}
}

func TestPinSqlite_SchemaAndWireFields(t *testing.T) {
	t.Parallel()
	db := pinDB(t, "schema.sqlite",
		"CREATE TABLE t (a INTEGER, b TEXT)",
		"INSERT INTO t (a, b) VALUES (1, 'x'), (2, 'y'), (3, 'z'), (4, 'w'), (5, 'v'), (6, 'u')",
	)
	res, err := pinExec(t, db+":t")
	if err != nil {
		t.Fatal(err)
	}
	to, ok := res.Data.(TextOutput)
	if !ok {
		t.Fatalf("data = %T", res.Data)
	}
	if to.Type != "text" || to.FilePath != db+":t" || to.StartLine != 1 {
		t.Errorf("wire fields = %+v", to)
	}
	if !strings.HasPrefix(to.Content, "CREATE TABLE t") {
		t.Errorf("schema output missing CREATE: %q", to.Content)
	}
	if !strings.Contains(to.Content, "Sample rows:") {
		t.Errorf("schema output missing samples: %q", to.Content)
	}
	// Default sample limit is 5 rows for a 6-row table.
	sample := to.Content[strings.Index(to.Content, "Sample rows:"):]
	if got := strings.Count(sample, "\n") - 1; got != 5 { // "Sample rows:" line + header + 5 rows
		t.Errorf("sample rows = %d lines, want 5 (sample=%q)", got, sample)
	}
}

func TestPinSqlite_ListRowCounts(t *testing.T) {
	t.Parallel()
	// Exactly 50000 rows reports an exact count, not "50000+".
	exact := pinDB(t, "exact.sqlite",
		"CREATE TABLE t (x INTEGER)",
		"INSERT INTO t SELECT 1",
		"CREATE TABLE big AS WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 50000) SELECT x FROM c",
	)
	got := pinContent(t, exact)
	if !strings.Contains(got, "t (1 rows)") {
		t.Errorf("list = %q, want 't (1 rows)'", got)
	}
	if !strings.Contains(got, "big (50000 rows)") {
		t.Errorf("list = %q, want 'big (50000 rows)' exact count at cap", got)
	}

	// sqlite_stat1 estimates over the probe cap surface as "~N rows";
	// whitespace-only and huge stats must not break listing.
	est := pinDB(t, "est.sqlite",
		"CREATE TABLE e (x INTEGER)",
		"INSERT INTO e (x) VALUES (1)",
		"CREATE TABLE w (x INTEGER)",
		"INSERT INTO w (x) VALUES (1)",
		"CREATE TABLE h (x INTEGER)",
		"INSERT INTO h (x) VALUES (1)",
		"CREATE TABLE s (x INTEGER)",
		"INSERT INTO s (x) VALUES (1)",
		"CREATE TABLE c (x INTEGER)",
		"INSERT INTO c (x) VALUES (1)",
		"ANALYZE",
		"UPDATE sqlite_stat1 SET stat = '99999 1' WHERE tbl = 'e'",
		"UPDATE sqlite_stat1 SET stat = '  ' WHERE tbl = 'w'",
		"UPDATE sqlite_stat1 SET stat = '9223372036854775807 1' WHERE tbl = 'h'",
		"UPDATE sqlite_stat1 SET stat = '10 1' WHERE tbl = 's'",
		"UPDATE sqlite_stat1 SET stat = '50000 1' WHERE tbl = 'c'",
	)
	got = pinContent(t, est)
	if !strings.Contains(got, "e (~99999 rows)") {
		t.Errorf("list = %q, want 'e (~99999 rows)' estimate", got)
	}
	if !strings.Contains(got, "w (1 rows)") {
		t.Errorf("list = %q, want 'w (1 rows)' exact (whitespace stat skipped)", got)
	}
	if !strings.Contains(got, "h (~9223372036854775807 rows)") {
		t.Errorf("list = %q, want 'h (~9223372036854775807 rows)' max-int estimate", got)
	}
	// Estimates at or below the probe cap fall back to exact counts.
	if !strings.Contains(got, "s (1 rows)") {
		t.Errorf("list = %q, want 's (1 rows)' exact (small estimate ignored)", got)
	}
	if !strings.Contains(got, "c (1 rows)") {
		t.Errorf("list = %q, want 'c (1 rows)' exact (estimate at cap not above)", got)
	}
}

func TestPinSqlite_QueryPagination(t *testing.T) {
	t.Parallel()
	db := pinDB(t, "page.sqlite",
		"CREATE TABLE t (id INTEGER)",
		"INSERT INTO t (id) VALUES (1), (2), (3), (4), (5)",
	)
	got := pinContent(t, db+":t?limit=2")
	want := "1\n2\n[3 more rows; append :t?limit=2&offset=2 to the database path to continue]"
	if got != want {
		t.Errorf("limit=2 page = %q, want %q", got, want)
	}
	// A page covering all rows carries no continuation footer.
	got = pinContent(t, db+":t?limit=5")
	if strings.Contains(got, "more rows") {
		t.Errorf("full page has footer: %q", got)
	}
	// Default limit is 20 when only a where is present; the footer names
	// the effective limit and next offset.
	defaults := pinDB(t, "defaults.sqlite",
		"CREATE TABLE t (id INTEGER)",
		"INSERT INTO t (id) WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 25) SELECT x FROM c",
	)
	got = pinContent(t, defaults+":t?where=id > 0")
	if !strings.HasSuffix(got, "\n[5 more rows; append :t?limit=20&offset=20 to the database path to continue]") {
		t.Errorf("where-only default page tail = %q, want 5-more footer with limit=20", got[len(got)-90:])
	}
	if lines := strings.Count(got, "\n"); lines != 20 { // 20 values + footer line
		t.Errorf("default page lines = %d, want 20", lines)
	}
	// The 500 limit cap shows through the footer of a limit=500 page.
	capDB := pinDB(t, "cap.sqlite",
		"CREATE TABLE t (id INTEGER)",
		"INSERT INTO t (id) WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 505) SELECT x FROM c",
	)
	got = pinContent(t, capDB+":t?limit=500")
	if !strings.HasSuffix(got, "\n[5 more rows; append :t?limit=500&offset=500 to the database path to continue]") {
		t.Errorf("limit=500 page tail = %q, want 5-more footer with limit=500", got[len(got)-90:])
	}
	// Unknown order column errors instead of querying.
	_, err := pinExec(t, db+":t?order=nocol")
	if err == nil || !strings.Contains(err.Error(), "order column 'nocol' not found in table schema") {
		t.Errorf("order=nocol err = %v, want schema error", err)
	}
	// Invalid limit inside a query selector errors through Execute.
	_, err = pinExec(t, db+":t?limit=abc")
	if err == nil || !strings.Contains(err.Error(), "must be a positive integer") {
		t.Errorf("limit=abc err = %v, want limit error", err)
	}
}

func TestPinSqlite_RawQuery(t *testing.T) {
	t.Parallel()
	db := pinDB(t, "raw.sqlite",
		"CREATE TABLE t (id INTEGER, v TEXT)",
		"INSERT INTO t (id, v) VALUES (1, 'a')",
	)
	if got := pinContent(t, db+"?q=SELECT id FROM t"); got != "id\n1" && got != "1" {
		t.Errorf("raw query = %q", got)
	}
	_, err := pinExec(t, db+"?q=SELECT * FROM missing")
	if err == nil || !strings.HasPrefix(err.Error(), "raw query:") {
		t.Fatalf("raw missing-table err = %v, want 'raw query:' prefix", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("raw query err not wrapped (%%w): %v", err)
	}

	// 1002 rows without LIMIT truncates at 1000 with the marker appended.
	trunc := pinDB(t, "trunc.sqlite",
		"CREATE TABLE t (id INTEGER, v TEXT)",
		"INSERT INTO t (id, v) WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 1002) SELECT x, 'v' FROM c",
	)
	res, err := pinExec(t, trunc+"?q=SELECT id FROM t")
	if err != nil {
		t.Fatal(err)
	}
	to, _ := res.Data.(TextOutput)
	if !strings.HasSuffix(to.Content, "\n[result truncated at 1000 rows]") {
		t.Errorf("raw truncation marker missing; tail = %q", to.Content[len(to.Content)-60:])
	}
	if lines := strings.Count(to.Content, "\n"); lines != 1000 { // 1000 single-col values + marker
		t.Errorf("truncated output lines = %d, want 1000", lines)
	}
	// An explicit LIMIT opts out of the cap.
	got := pinContent(t, trunc+"?q=SELECT id FROM t LIMIT 1002")
	if strings.Contains(got, "truncated") {
		t.Error("explicit limit output truncated")
	}
	if lines := strings.Count(got, "\n"); lines != 1001 { // 1002 single-col values
		t.Errorf("explicit limit lines = %d, want 1001", lines)
	}
}

func TestPinSqlite_CandidateFallbacks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Longest candidate missing: fall through to the shorter real database.
	real := pinDB(t, "x.db", "CREATE TABLE t (v TEXT)", "INSERT INTO t (v) VALUES ('hit')")
	_, err := pinExec(t, real+":missing.db")
	if err == nil || !strings.Contains(err.Error(), "sqlite table 'missing.db' not found") {
		t.Fatalf("missing-candidate err = %v, want table-not-found for the shorter db", err)
	}

	// A non-SQLite file with a .db name must not stop the candidate scan:
	// "a.db" is plain text, "a.db:b.db" (literal filename) is a real database.
	fakeRoot := filepath.Join(dir, "a.db")
	if err := os.WriteFile(fakeRoot, []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	nestedName := filepath.Join(dir, "a.db:b.db")
	// Write the real database under the literal name a.db:b.db.
	db, err := sql.Open("sqlite", nestedName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE t (v TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO t (v) VALUES ('nested')"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	// The literal "a.db:b.db" database is the longest candidate and is tried
	// first; reaching its schema output proves the candidate scan works when
	// a shorter ".db" sibling is not a database.
	got := pinContent(t, nestedName+":t")
	if !strings.Contains(got, "CREATE TABLE t") || !strings.Contains(got, "nested") {
		t.Errorf("nested db read = %q, want schema with nested sample", got)
	}

	// Longest candidate exists on disk but is NOT SQLite (plain text): the
	// scan must continue to the shorter real database instead of stopping.
	t1 := pinDB(t, "t1.db", "CREATE TABLE rows (v TEXT)", "INSERT INTO rows (v) VALUES ('deep')")
	flat := filepath.Join(t.TempDir(), "t1.db:t2.db")
	if err := os.WriteFile(flat, []byte("plain text, not sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The real db must live at the shorter candidate path of the SAME input:
	// build input "<dir>/t1.db:t2.db" whose shorter candidate is "<dir>/t1.db".
	dir2 := filepath.Dir(flat)
	if err := os.Rename(t1, filepath.Join(dir2, "t1.db")); err != nil {
		t.Fatal(err)
	}
	_, err = pinExec(t, filepath.Join(dir2, "t1.db:t2.db"))
	if err == nil || !strings.Contains(err.Error(), "sqlite table 't2.db' not found") {
		t.Fatalf("non-sqlite longer candidate err = %v, want table-not-found via shorter db", err)
	}
}

func TestPinSqlite_NonSqliteFallbackText(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// .db path that is not SQLite falls through to the plain-text reader
	// (NUL bytes rejected) rather than erroring as SQLite.
	p := filepath.Join(dir, "plain.db")
	if err := os.WriteFile(p, []byte{0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := pinExec(t, p)
	if err == nil || !strings.Contains(err.Error(), "null bytes") {
		t.Errorf("non-sqlite .db err = %v, want null-bytes text error", err)
	}
}

func TestPinSqlite_ListEmptyDatabase(t *testing.T) {
	t.Parallel()
	db := pinDB(t, "empty.sqlite", "CREATE TABLE t (x INTEGER)")
	if got := pinContent(t, db); got != "t (0 rows)" {
		t.Errorf("list = %q, want 't (0 rows)'", got)
	}
}

func TestPinSqlite_SingleColumnOutput(t *testing.T) {
	t.Parallel()
	db := pinDB(t, "single.sqlite",
		"CREATE TABLE t (v TEXT)",
		"INSERT INTO t (v) VALUES ('a''b')",
	)
	// SQL 'a''b' stores a'b verbatim; the single-column form emits raw values.
	if got := pinContent(t, db+":t?limit=5"); got != "a'b" {
		t.Errorf("single column output = %q", got)
	}
}
