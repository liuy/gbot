package fileread

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/mholt/archives"
)

const wantWhereKeywordErr = "sqlite 'where' clause must not contain LIMIT/OFFSET/UNION/INTERSECT/EXCEPT/ATTACH/DETACH/PRAGMA; use '?q=SELECT ...' for raw SQL"
const wantWhereTerminatorErr = "sqlite 'where' clause must not contain comments or statement terminators; use '?q=SELECT ...' for raw SQL"

// validateWhereClause is the injection guard for ?where=. Quote-state tracking
// and comment detection must not be fooled by escaped quotes or near-miss bytes.
func TestValidateWhereClause_Table(t *testing.T) {
	tests := []struct {
		name    string
		where   string
		wantErr string // "" means must be accepted
	}{
		{"empty", "", ""},
		{"blank", "   ", ""},
		{"plain", "name = 'x'", ""},
		{"keyword substring", "limited = 1", ""},
		{"keyword with underscore suffix", "a = offset_x", ""},
		{"keyword with digit suffix", "a = limit9", ""},
		{"keyword with zero suffix", "a = limit0", ""},
		{"keyword with z suffix", "a = limitz", ""},
		{"keyword with Z suffix", "a = LIMITZ", ""},
		{"keyword inside single quotes", "a = 'LIMIT'", ""},
		{"keyword inside escaped single quotes", "a = 'it''s LIMIT'", ""},
		{"keyword inside double quotes", `a = "LIMIT"`, ""},
		{"keyword inside escaped double quotes", `a = "col""LIMIT"`, ""},
		{"single dash", "a = 1-2", ""},
		{"trailing dash", "a = 1-", ""},
		{"dash before space", "a - 1", ""},
		{"slash star near miss", "a = 1/x", ""},
		{"trailing slash", "a = 1/", ""},
		{"star after space", "a = 1 *2", ""},
		{"keyword after plain string", "a = 'x' LIMIT 1", wantWhereKeywordErr},
		{"keyword after escaped-quote string", "a = 'it''s' LIMIT 1", wantWhereKeywordErr},
		{"keyword after quoted identifier", `a = "x" LIMIT 1`, wantWhereKeywordErr},
		{"keyword after escaped-quote identifier", `a = "col""name" LIMIT 1`, wantWhereKeywordErr},
		{"lowercase limit", "a limit 1", wantWhereKeywordErr},
		{"mixed case offset", "a LiMiT 1", wantWhereKeywordErr},
		{"union", "a UNION SELECT", wantWhereKeywordErr},
		{"intersect", "a INTERSECT", wantWhereKeywordErr},
		{"except", "a EXCEPT", wantWhereKeywordErr},
		{"attach", "a ATTACH", wantWhereKeywordErr},
		{"detach", "a DETACH", wantWhereKeywordErr},
		{"pragma", "a PRAGMA", wantWhereKeywordErr},
		{"offset", "a OFFSET 1", wantWhereKeywordErr},
		{"statement terminator", "a = 1; b = 2", wantWhereTerminatorErr},
		{"line comment", "a = 1 -- comment", wantWhereTerminatorErr},
		{"line comment at end", "a = 1--", wantWhereTerminatorErr},
		{"block comment", "a = 1 /* c */", wantWhereTerminatorErr},
		{"block comment at end", "a = 1/*", wantWhereTerminatorErr},
	}
	for _, tc := range tests {
		err := validateWhereClause(tc.where)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: validateWhereClause(%q) = %v, want nil", tc.name, tc.where, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: validateWhereClause(%q) = nil, want %q", tc.name, tc.where, tc.wantErr)
			continue
		}
		if err.Error() != tc.wantErr {
			t.Errorf("%s: validateWhereClause(%q) error = %q, want %q", tc.name, tc.where, err.Error(), tc.wantErr)
		}
	}
}

func TestStripSingleQuotedStrings(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"SELECT * FROM t", "SELECT * FROM t"},
		{"SELECT 'a' FROM t", "SELECT  FROM t"},
		{"SELECT 'it''s' FROM t", "SELECT  FROM t"},
		{"a''b", "ab"},
		{"''", ""},
		{"SELECT 'unterminated", "SELECT "},
		{"'lead' tail", " tail"},
	}
	for _, tc := range tests {
		if got := stripSingleQuotedStrings(tc.in); got != tc.want {
			t.Errorf("stripSingleQuotedStrings(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSQLHasExplicitLimit(t *testing.T) {
	tests := []struct {
		sqlText string
		want    bool
	}{
		{"SELECT * FROM t", false},
		{"SELECT * FROM t LIMIT 5", true},
		{"select * from t limit 5", true},
		{"SELECT * FROM t WHERE x = 'limit 5'", false},
		{"SELECT * FROM t WHERE x = 'it''s limit 5'", false},
		{"SELECT 'LIMIT' AS l", false},
	}
	for _, tc := range tests {
		if got := sqlHasExplicitLimit(tc.sqlText); got != tc.want {
			t.Errorf("sqlHasExplicitLimit(%q) = %v, want %v", tc.sqlText, got, tc.want)
		}
	}
}

func TestParseSqliteSelector_KindsAndErrors(t *testing.T) {
	tests := []struct {
		name      string
		subPath   string
		query     string
		wantKind  sqliteSelectorKind
		wantTable string
		wantKey   string
		wantLimit int
		wantOff   int
		wantOrder string
		wantWhere string
		wantRaw   string
		wantSamp  int
		wantErr   string
	}{
		{name: "list", subPath: "", query: "", wantKind: sqliteSelList},
		{name: "bare colon is list", subPath: ":", query: "", wantKind: sqliteSelList},
		{name: "schema", subPath: "users", query: "", wantKind: sqliteSelSchema, wantTable: "users", wantSamp: sqliteDefaultSchemaSample},
		{name: "leading colon schema", subPath: ":users", query: "", wantKind: sqliteSelSchema, wantTable: "users", wantSamp: sqliteDefaultSchemaSample},
		{name: "row by key", subPath: "users:7", query: "", wantKind: sqliteSelRow, wantTable: "users", wantKey: "7"},
		{name: "query limit only", subPath: "users", query: "limit=5", wantKind: sqliteSelQuery, wantTable: "users", wantLimit: 5, wantOff: 0},
		{name: "query offset only", subPath: "users", query: "offset=9", wantKind: sqliteSelQuery, wantTable: "users", wantLimit: sqliteDefaultQueryLimit, wantOff: 9},
		{name: "query order only", subPath: "users", query: "order=name", wantKind: sqliteSelQuery, wantTable: "users", wantLimit: sqliteDefaultQueryLimit, wantOrder: "name"},
		{name: "query where only", subPath: "users", query: "where=a=1", wantKind: sqliteSelQuery, wantTable: "users", wantLimit: sqliteDefaultQueryLimit, wantWhere: "a=1"},
		{name: "query all params", subPath: "users", query: "limit=3&offset=6&order=name&where=a=1", wantKind: sqliteSelQuery, wantTable: "users", wantLimit: 3, wantOff: 6, wantOrder: "name", wantWhere: "a=1"},
		{name: "limit clamped to max", subPath: "users", query: "limit=99999", wantKind: sqliteSelQuery, wantTable: "users", wantLimit: sqliteMaxQueryLimit},
		{name: "raw query", subPath: "", query: "q=SELECT 1", wantKind: sqliteSelRaw, wantRaw: "SELECT 1"},
		{name: "empty param value is rejected", subPath: "users", query: "where=", wantErr: "unsupported SQLite query parameter 'where'"},
		{name: "unsupported param", subPath: "users", query: "bogus=1", wantErr: "unsupported SQLite query parameter 'bogus'"},
		{name: "unsupported alongside limit", subPath: "users", query: "limit=5&bogus=1", wantErr: "unsupported SQLite query parameter 'bogus'"},
		{name: "params without selector", subPath: "", query: "limit=5", wantErr: "sqlite query parameters require a table selector or q=SELECT"},
		{name: "raw with table", subPath: "users", query: "q=SELECT 1", wantErr: "sqlite raw queries cannot be combined with table selectors or pagination"},
		{name: "raw with pagination", subPath: "", query: "q=SELECT 1&limit=5", wantErr: "sqlite raw queries cannot be combined with table selectors or pagination"},
		{name: "raw blank", subPath: "", query: "q=   ", wantErr: "sqlite query parameter 'q' cannot be empty"},
		{name: "row lookup with params", subPath: "users:7", query: "limit=5", wantErr: "sqlite row lookups cannot be combined with query parameters"},
		{name: "zero limit", subPath: "users", query: "limit=0", wantErr: "sqlite limit must be a positive integer; got '0'"},
		{name: "negative offset", subPath: "users", query: "offset=-1", wantErr: "sqlite offset must be a non-negative integer; got '-1'"},
	}
	for _, tc := range tests {
		sel, err := parseSqliteSelector(tc.subPath, tc.query)
		if tc.wantErr != "" {
			if err == nil {
				t.Errorf("%s: parseSqliteSelector(%q, %q) = %+v, want error %q", tc.name, tc.subPath, tc.query, sel, tc.wantErr)
				continue
			}
			if err.Error() != tc.wantErr {
				t.Errorf("%s: error = %q, want %q", tc.name, err.Error(), tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: parseSqliteSelector(%q, %q): %v", tc.name, tc.subPath, tc.query, err)
			continue
		}
		if sel.kind != tc.wantKind {
			t.Errorf("%s: kind = %d, want %d", tc.name, sel.kind, tc.wantKind)
		}
		if sel.table != tc.wantTable || sel.key != tc.wantKey || sel.limit != tc.wantLimit ||
			sel.offset != tc.wantOff || sel.order != tc.wantOrder || sel.where != tc.wantWhere ||
			sel.rawSQL != tc.wantRaw || sel.sampleLimit != tc.wantSamp {
			t.Errorf("%s: selector = %+v, want kind=%d table=%q key=%q limit=%d offset=%d order=%q where=%q raw=%q sample=%d",
				tc.name, sel, tc.wantKind, tc.wantTable, tc.wantKey, tc.wantLimit, tc.wantOff,
				tc.wantOrder, tc.wantWhere, tc.wantRaw, tc.wantSamp)
		}
	}
}

// Error paths must return the zero row count, not a mutated one, and must not
// fall through into the query that follows the guard.
func TestSqliteSelectRows_ErrorPaths(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (name) VALUES ('ann'), ('bob')`); err != nil {
		t.Fatal(err)
	}

	// Happy path pins the baseline before the failure cases.
	cols, rows, count, err := sqliteSelectRows(db, "users", 10, 0, "", "")
	if err != nil {
		t.Fatalf("sqliteSelectRows happy path: %v", err)
	}
	if !equalStrings(cols, []string{"id", "name"}) {
		t.Errorf("columns = %q, want [id name]", cols)
	}
	if len(rows) != 2 || count != 2 {
		t.Errorf("rows=%d count=%d, want 2/2", len(rows), count)
	}

	// where rejected by the validator.
	cols, rows, count, err = sqliteSelectRows(db, "users", 10, 0, "name LIMIT 1", "")
	if err == nil || err.Error() != wantWhereKeywordErr {
		t.Errorf("bad where error = %v, want %q", err, wantWhereKeywordErr)
	}
	if cols != nil || rows != nil || count != 0 {
		t.Errorf("bad where returns cols=%q rows=%v count=%d, want nil/nil/0", cols, rows, count)
	}

	// order rejected against the column list.
	const wantOrderErr = "sqlite order column 'nosuchcol' not found in table schema"
	_, _, count, err = sqliteSelectRows(db, "users", 10, 0, "", "nosuchcol")
	if err == nil || err.Error() != wantOrderErr {
		t.Errorf("bad order error = %v, want %q", err, wantOrderErr)
	}
	if count != 0 {
		t.Errorf("bad order count = %d, want 0", count)
	}

	// Missing table: the COUNT query fails.
	_, _, count, err = sqliteSelectRows(db, "nosuchtable", 10, 0, "", "")
	if err == nil {
		t.Fatal("missing table: expected error")
	}
	if count != 0 {
		t.Errorf("missing table count = %d, want 0", count)
	}

	// Unknown where column: the COUNT query fails on the unknown identifier.
	_, _, count, err = sqliteSelectRows(db, "users", 10, 0, "nosuchcol = 1", "")
	if err == nil {
		t.Fatal("unknown where column: expected error")
	}
	if count != 0 {
		t.Errorf("unknown where column count = %d, want 0", count)
	}
}

func TestScanHelpers_ErrorPaths(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE t (a INTEGER, b TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t (a, b) VALUES (1, 'x'), (2, 'y')`); err != nil {
		t.Fatal(err)
	}

	rendered, count, err := scanRowsWithCount(db, "SELECT * FROM nosuchtable", nil)
	if err == nil {
		t.Fatal("scanRowsWithCount: expected error for a missing table")
	}
	if rendered != "" || count != 0 {
		t.Errorf("scanRowsWithCount = (%q, %d), want (\"\", 0) on error", rendered, count)
	}

	// Baseline: two matching rows render the first and count both.
	rendered, count, err = scanRowsWithCount(db, "SELECT a, b FROM t", nil)
	if err != nil {
		t.Fatalf("scanRowsWithCount: %v", err)
	}
	if rendered != "a: 1\nb: x" || count != 2 {
		t.Errorf("scanRowsWithCount = (%q, %d), want (\"a: 1\\nb: x\", 2)", rendered, count)
	}

	// No rows: empty render, zero count, no error.
	rendered, count, err = scanRowsWithCount(db, "SELECT a, b FROM t WHERE a = 99", nil)
	if err != nil {
		t.Fatalf("scanRowsWithCount empty: %v", err)
	}
	if rendered != "" || count != 0 {
		t.Errorf("scanRowsWithCount empty = (%q, %d), want (\"\", 0)", rendered, count)
	}

	if got, err := scanSingleRow(db, "SELECT * FROM nosuchtable", nil); err == nil {
		t.Errorf("scanSingleRow = %q, want error for a missing table", got)
	} else if got != "" {
		t.Errorf("scanSingleRow returned %q alongside error %v, want empty", got, err)
	}
	if got, err := scanSingleRow(db, "SELECT a, b FROM t WHERE a = 2", nil); err != nil {
		t.Fatalf("scanSingleRow: %v", err)
	} else if got != "a: 2\nb: y" {
		t.Errorf("scanSingleRow = %q, want %q", got, "a: 2\nb: y")
	}
	if got, err := scanSingleRow(db, "SELECT a, b FROM t WHERE a = 99", nil); err != nil {
		t.Fatalf("scanSingleRow empty: %v", err)
	} else if got != "" {
		t.Errorf("scanSingleRow empty = %q, want empty string", got)
	}
}

// sqlite_stat1 holds one row per index; the estimate per table is the largest.
func TestLoadRowEstimates_KeepsLargestPerTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stat.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// ANALYZE is the only supported way to materialise sqlite_stat1.
	for _, s := range []string{
		`CREATE TABLE big (id INTEGER PRIMARY KEY, v TEXT)`,
		`CREATE TABLE small (id INTEGER PRIMARY KEY, v TEXT)`,
		`CREATE INDEX big_i1 ON big (v)`,
		`CREATE INDEX big_i2 ON big (v)`,
		`CREATE INDEX small_i1 ON small (v)`,
		`INSERT INTO big (v) VALUES ('a'), ('b')`,
		`ANALYZE`,
		`DELETE FROM sqlite_stat1`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	// Descending for big (a "keep the last" bug lands on 7), ascending for
	// small (a "keep the first" bug lands on 7).
	for _, s := range []string{
		`INSERT INTO sqlite_stat1 (tbl, idx, stat) VALUES ('big', 'big_i1', '99 10')`,
		`INSERT INTO sqlite_stat1 (tbl, idx, stat) VALUES ('big', 'big_i2', '42 10')`,
		`INSERT INTO sqlite_stat1 (tbl, idx, stat) VALUES ('big', 'big_i3', '7 10')`,
		`INSERT INTO sqlite_stat1 (tbl, idx, stat) VALUES ('big', 'big_i4', '')`,
		`INSERT INTO sqlite_stat1 (tbl, idx, stat) VALUES ('big', 'big_i5', 'garbage')`,
		`INSERT INTO sqlite_stat1 (tbl, idx, stat) VALUES ('small', 'small_i1', '7 10')`,
		`INSERT INTO sqlite_stat1 (tbl, idx, stat) VALUES ('small', 'small_i2', '55 10')`,
		`INSERT INTO sqlite_stat1 (tbl, idx, stat) VALUES ('small', 'small_i3', '99 10')`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	got := loadRowEstimates(db)
	if len(got) != 2 || got["big"] != 99 || got["small"] != 99 {
		t.Errorf("loadRowEstimates = %v, want big=99 small=99", got)
	}
}

// executeDocument slices the converted markdown with offset/limit.
func TestExecuteDocument_OffsetLimitSlicing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.csv")
	if err := os.WriteFile(path, []byte("name,age\nann,3\nbob,4\ncid,5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	full := "| name | age |\n| --- | --- |\n| ann | 3 |\n| bob | 4 |\n| cid | 5 |"

	cases := []struct {
		offset, limit int
		wantContent   string
		wantNumLines  int
		wantStartLine int
	}{
		{0, 0, full, 5, 1},
		{1, 0, full, 5, 1},
		{2, 2, "| --- | --- |\n| ann | 3 |", 2, 2},
		{4, 10, "| bob | 4 |\n| cid | 5 |", 2, 4},
		{5, 1, "| cid | 5 |", 1, 5},
		{6, 0, "", 0, 6},
	}
	for _, tc := range cases {
		res, err := executeDocument(context.Background(), Input{FilePath: path, Offset: tc.offset, Limit: tc.limit}, info)
		if err != nil {
			t.Errorf("offset=%d limit=%d: %v", tc.offset, tc.limit, err)
			continue
		}
		out, ok := res.Data.(TextOutput)
		if !ok {
			t.Errorf("offset=%d limit=%d: Data = %T, want TextOutput", tc.offset, tc.limit, res.Data)
			continue
		}
		if out.Content != tc.wantContent || out.NumLines != tc.wantNumLines ||
			out.StartLine != tc.wantStartLine || out.TotalLines != 5 {
			t.Errorf("offset=%d limit=%d: got content=%q numLines=%d startLine=%d totalLines=%d; want content=%q numLines=%d startLine=%d totalLines=5",
				tc.offset, tc.limit, out.Content, out.NumLines, out.StartLine, out.TotalLines,
				tc.wantContent, tc.wantNumLines, tc.wantStartLine)
		}
	}
}

// The 256KB pre-read guard: exact size boundary, explicit limit, and the
// uncapped-output escape hatch.
func TestExecute_TextFileSizeGuard(t *testing.T) {
	dir := t.TempDir()
	over := filepath.Join(dir, "over.txt")
	exact := filepath.Join(dir, "exact.txt")
	padded := filepath.Join(dir, "padded.txt")
	if err := os.WriteFile(over, []byte(strings.Repeat("z", MaxFileReadBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exact, []byte(strings.Repeat("z", MaxFileReadBytes)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Same size class but many short lines, so a limit=1 read stays small.
	if err := os.WriteFile(padded, []byte(strings.Repeat("abcdefghij\n", 26215)), 0o644); err != nil {
		t.Fatal(err)
	}

	wantSizeErr := "file content (256.0KB) exceeds maximum allowed size (256.0KB). Use offset and limit parameters to read specific portions of the file"

	// Over the limit with no limit parameter: rejected by the size guard.
	_, err := Execute(context.Background(), json.RawMessage(`{"file_path":"`+over+`"}`), nil)
	if err == nil || err.Error() != wantSizeErr {
		t.Errorf("oversized file error = %v, want %q", err, wantSizeErr)
	}

	// A capped tctx does not unlock the guard.
	_, err = Execute(context.Background(), json.RawMessage(`{"file_path":"`+over+`"}`), &tool.ToolUseContext{})
	if err == nil || err.Error() != wantSizeErr {
		t.Errorf("oversized file with capped tctx error = %v, want %q", err, wantSizeErr)
	}

	// Exactly at the limit: the size guard must not fire — the failure that
	// follows is the token cap, which reports a different message.
	_, err = Execute(context.Background(), json.RawMessage(`{"file_path":"`+exact+`"}`), nil)
	if err == nil {
		t.Fatal("exactly-max file: expected the token cap to reject it")
	}
	if err.Error() == wantSizeErr {
		t.Errorf("exactly-max file hit the size guard: %v", err)
	}
	if !strings.HasSuffix(err.Error(), " exceeds maximum allowed tokens (25000). Use offset and limit parameters to read specific portions of the file") {
		t.Errorf("exactly-max file error = %q, want the token-cap message", err.Error())
	}

	// An explicit limit bypasses the size guard.
	res, err := Execute(context.Background(), json.RawMessage(`{"file_path":"`+padded+`","limit":1}`), nil)
	if err != nil {
		t.Errorf("oversized multi-line file with limit=1: %v", err)
	} else if out, ok := res.Data.(TextOutput); !ok || out.NumLines != 1 || out.Content != "abcdefghij" {
		t.Errorf("oversized multi-line file with limit=1: got %+v, want one short line", res.Data)
	}

	// Uncapped output bypasses both caps.
	uncapped := &tool.ToolUseContext{UncappedOutput: true}
	res, err = Execute(context.Background(), json.RawMessage(`{"file_path":"`+over+`"}`), uncapped)
	if err != nil {
		t.Errorf("oversized file with UncappedOutput: %v", err)
	} else if out, ok := res.Data.(TextOutput); !ok || out.NumLines != 1 || len(out.Content) != MaxFileReadBytes+1 {
		t.Errorf("oversized file with UncappedOutput: got %+v, want the whole file", res.Data)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- archive.go -------------------------------------------------------

// The member cap is a size guard against malicious archive metadata.
func TestArchiveMaxMemberBytesConstant(t *testing.T) {
	if archiveMaxMemberBytes != 64*1024*1024 {
		t.Errorf("archiveMaxMemberBytes = %d, want 67108864", archiveMaxMemberBytes)
	}
}

func TestReadArchiveFS_MemberSlicing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "g.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	notes, err := w.Create("dir/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := notes.Write([]byte("l1\nl2\nl3\nl4\n")); err != nil {
		t.Fatal(err)
	}
	bin, err := w.Create("dir/bin.dat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bin.Write([]byte{0x00, 0x01, 0xff, 0xfe}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	full := "l1\nl2\nl3\nl4\n"
	cases := []struct {
		name           string
		sub            string
		offset, limit  int
		wantContent    string
		wantNumLines   int
		wantStartLine  int
		wantTotalLines int
	}{
		{name: "whole member", sub: "dir/notes.txt", wantContent: full, wantNumLines: 4, wantStartLine: 1, wantTotalLines: 4},
		{name: "offset 1 is not a slice", sub: "dir/notes.txt", offset: 1, wantContent: full, wantNumLines: 4, wantStartLine: 1, wantTotalLines: 4},
		{name: "offset 2", sub: "dir/notes.txt", offset: 2, wantContent: "l2\nl3\nl4\n", wantNumLines: 4, wantStartLine: 2, wantTotalLines: 4},
		{name: "limit 2", sub: "dir/notes.txt", limit: 2, wantContent: "l1\nl2", wantNumLines: 2, wantStartLine: 1, wantTotalLines: 4},
		{name: "offset 3 limit 1", sub: "dir/notes.txt", offset: 3, limit: 1, wantContent: "l3", wantNumLines: 1, wantStartLine: 3, wantTotalLines: 4},
		{name: "offset past the end", sub: "dir/notes.txt", offset: 9, wantContent: "", wantNumLines: 0, wantStartLine: 9, wantTotalLines: 4},
		{name: "binary member", sub: "dir/bin.dat", wantContent: "[Cannot read binary archive entry 'dir/bin.dat' (4 bytes)]", wantNumLines: 0, wantStartLine: 1, wantTotalLines: 0},
	}
	for _, tc := range cases {
		res, err := readArchiveFS(mustArchiveFS(t, path), tc.sub, Input{FilePath: path + ":" + tc.sub, Offset: tc.offset, Limit: tc.limit})
		if err != nil {
			t.Errorf("%s: readArchiveFS: %v", tc.name, err)
			continue
		}
		out, ok := res.Data.(TextOutput)
		if !ok {
			t.Errorf("%s: Data = %T, want TextOutput", tc.name, res.Data)
			continue
		}
		if out.Content != tc.wantContent || out.NumLines != tc.wantNumLines ||
			out.StartLine != tc.wantStartLine || out.TotalLines != tc.wantTotalLines {
			t.Errorf("%s: got content=%q numLines=%d startLine=%d totalLines=%d; want content=%q numLines=%d startLine=%d totalLines=%d",
				tc.name, out.Content, out.NumLines, out.StartLine, out.TotalLines,
				tc.wantContent, tc.wantNumLines, tc.wantStartLine, tc.wantTotalLines)
		}
	}

	// A missing member reports the raw subpath, not the normalized one.
	if _, err := readArchiveFS(mustArchiveFS(t, path), "missing.txt", Input{FilePath: path + ":missing.txt"}); err == nil {
		t.Error("missing member: expected error")
	} else if want := "path 'missing.txt' not found inside archive"; err.Error() != want {
		t.Errorf("missing member error = %q, want %q", err.Error(), want)
	}
}

func mustArchiveFS(t *testing.T, path string) fs.FS {
	t.Helper()
	fsys, err := archives.FileSystem(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("archives.FileSystem(%s): %v", path, err)
	}
	return fsys
}

// A file whose bytes do not match its archive extension must fail with the
// "open archive:" wrapper around the decoder error.
func TestTryArchivePath_OpenFailureWrapsDecoderError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.tar.gz")
	if err := os.WriteFile(path, []byte("garbage content here"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, handled, err := tryArchivePath(context.Background(), Input{FilePath: path})
	if !handled {
		t.Fatal("tryArchivePath did not claim a .tar.gz path")
	}
	if err == nil {
		t.Fatal("tryArchivePath: expected error for garbage bytes")
	}
	if !strings.HasPrefix(err.Error(), "open archive: ") {
		t.Errorf("error = %q, want the open archive wrapper", err.Error())
	}
	if !errors.Is(err, gzip.ErrHeader) {
		t.Errorf("error %q does not wrap gzip.ErrHeader", err.Error())
	}
}
