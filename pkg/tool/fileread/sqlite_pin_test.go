package fileread

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

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
