package fileread

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	result, err := executeSqliteRead(context.Background(), Input{FilePath: dbPath}, cand, dbPath)
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
	result, err := executeSqliteRead(context.Background(), Input{FilePath: dbPath}, cand, dbPath)
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
	result, err := executeSqliteRead(context.Background(), Input{FilePath: dbPath}, cand, dbPath)
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
	result, err := executeSqliteRead(context.Background(), Input{FilePath: dbPath}, cand, dbPath)
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
	result, handled, err := trySqlitePath(context.Background(), Input{FilePath: dbPath})
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
	result, handled, err := trySqlitePath(context.Background(), Input{FilePath: dbPath + ":users"})
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
	_, handled, err := trySqlitePath(context.Background(), Input{FilePath: txtPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Error("expected handled=false for non-SQLite file")
	}
}

func TestTrySqlitePath_NoMatch(t *testing.T) {
	_, handled, err := trySqlitePath(context.Background(), Input{FilePath: "/nonexistent/file.db"})
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
	result, err := executeSqliteRead(context.Background(), Input{FilePath: dbPath}, cand, dbPath)
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
