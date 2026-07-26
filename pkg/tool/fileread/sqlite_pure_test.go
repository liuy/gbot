package fileread

import (
	"strings"
	"testing"
)

func TestIsTextPK(t *testing.T) {
	tests := []struct {
		typ  string
		want bool
	}{
		{"TEXT", true},
		{"text", true},
		{"VARCHAR(255)", true},
		{"CLOB", true},
		{"INTEGER", false},
		{"BLOB", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isTextPK(tt.typ); got != tt.want {
			t.Errorf("isTextPK(%q) = %v, want %v", tt.typ, got, tt.want)
		}
	}
}

func TestEscapeLikePattern(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"hello", "hello"},
		{"50%", "50\\%"},
		{"a_b", "a\\_b"},
		{"path\\to", "path\\\\to"},
		{"100%_done", "100\\%\\_done"},
	}
	for _, tt := range tests {
		got, err := escapeLikePattern(tt.in)
		if err != nil {
			t.Fatalf("escapeLikePattern(%q): %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("escapeLikePattern(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStringifySqliteValue(t *testing.T) {
	tests := []struct {
		val  any
		want string
	}{
		{nil, "NULL"},
		{"hello", "hello"},
		{int64(42), "42"},
		{float64(3.14), "3.14"},
		{true, "true"},
		{[]byte{1, 2, 3}, "<BLOB 3 bytes>"},
	}
	for _, tt := range tests {
		if got := stringifySqliteValue(tt.val); got != tt.want {
			t.Errorf("stringifySqliteValue(%T %v) = %q, want %q", tt.val, tt.val, got, tt.want)
		}
	}
}

func TestResolveOrderClause(t *testing.T) {
	columns := []string{"id", "name", "age"}

	tests := []struct {
		name      string
		order     string
		want      string
		wantErr   bool
		errSubstr string
	}{
		{"empty", "", "", false, ""},
		{"simple", "name", " ORDER BY \"name\" ASC", false, ""},
		{"asc explicit", "name:asc", " ORDER BY \"name\" ASC", false, ""},
		{"desc", "age:desc", " ORDER BY \"age\" DESC", false, ""},
		{"column not found", "nonexistent", "", true, "not found"},
		{"invalid direction", "name:random", "", true, "direction"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveOrderClause(tt.order, columns)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveOrderClause(%q) = %q, want %q", tt.order, got, tt.want)
			}
		})
	}
}

func TestSanitizeCell(t *testing.T) {
	got := sanitizeCell("a\tb\nc\r\nd")
	// tab→space, \n→literal "\n", \r\n→literal "\n"
	// "a\tb\nc\r\nd" → "a b" + "\n" + "c" + "\n" + "d" (with \n as literal backslash-n)
	want := "a b\\nc\\nd"
	if got != want {
		t.Errorf("sanitizeCell = %q, want %q", got, want)
	}
}

func TestStringWidth(t *testing.T) {
	if stringWidth("héllo") != 5 {
		t.Errorf("stringWidth(héllo) = %d, want 5", stringWidth("héllo"))
	}
}

func TestBuildSqliteOutput(t *testing.T) {
	t.Run("empty columns empty rows", func(t *testing.T) {
		got := buildSqliteOutput(nil, nil)
		if got != "(no rows)" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("rows without columns", func(t *testing.T) {
		got := buildSqliteOutput(nil, []map[string]any{{"x": 1}})
		if got != "(rows returned without named columns)" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("single column raw values", func(t *testing.T) {
		got := buildSqliteOutput([]string{"sql"}, []map[string]any{
			{"sql": "CREATE TABLE foo (x INTEGER)"},
			{"sql": "CREATE TABLE bar (y TEXT)"},
		})
		want := "CREATE TABLE foo (x INTEGER)\nCREATE TABLE bar (y TEXT)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("multi column pipe separated", func(t *testing.T) {
		got := buildSqliteOutput([]string{"id", "name"}, []map[string]any{
			{"id": int64(1), "name": "alice"},
			{"id": int64(2), "name": "bob"},
		})
		want := "id|name\n1|alice\n2|bob"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("multi column empty rows", func(t *testing.T) {
		got := buildSqliteOutput([]string{"a", "b"}, nil)
		if got != "a|b\n(no rows)" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("single column empty rows", func(t *testing.T) {
		got := buildSqliteOutput([]string{"a"}, nil)
		if got != "(no rows)" {
			t.Errorf("got %q", got)
		}
	})
}

func TestFormatRowCount(t *testing.T) {
	tests := []struct {
		name string
		c    tableRowCount
		want string
	}{
		{"exact", tableRowCount{kind: "exact", rows: 100}, "100 rows"},
		{"estimate", tableRowCount{kind: "estimate", rows: 5000}, "~5000 rows"},
		{"atLeast", tableRowCount{kind: "atLeast", rows: 10}, "10+ rows"},
		{"unknown", tableRowCount{kind: "bogus"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatRowCount(tt.c)
			if got != tt.want {
				t.Errorf("formatRowCount() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCoerceLookupValue(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		colType string
		want    any
	}{
		{"int", "42", "INTEGER", int64(42)},
		{"float", "3.14", "REAL", 3.14},
		{"text", "hello", "TEXT", "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := coerceLookupValue(tt.raw, tt.colType)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("coerceLookupValue(%q, %q) = %v (%T), want %v (%T)",
					tt.raw, tt.colType, got, got, tt.want, tt.want)
			}
		})
	}
}

func TestParseSqliteLimit(t *testing.T) {
	tests := []struct {
		in       string
		fallback int
		want     int
		wantErr  bool
	}{
		{"10", 20, 10, false},
		{"1", 20, 1, false},
		{"", 20, 20, false},
		{"abc", 20, 0, true},
		{"-5", 20, 0, true},
		{"0", 20, 0, true},
		{"999999", 20, 500, false},
	}
	for _, tt := range tests {
		got, err := parseSqliteLimit(tt.in, tt.fallback)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseSqliteLimit(%q): expected error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSqliteLimit(%q): %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("parseSqliteLimit(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseSqliteOffset(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"20", 20, false},
		{"0", 0, false},
		{"", 0, false},
		{"abc", 0, true},
		{"-5", 0, true},
	}
	for _, tt := range tests {
		got, err := parseSqliteOffset(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseSqliteOffset(%q): expected error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSqliteOffset(%q): %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("parseSqliteOffset(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestValidateWhereClause(t *testing.T) {
	tests := []struct {
		name    string
		clause  string
		wantErr bool
		errMsg  string
	}{
		{"empty", "", false, ""},
		{"valid", "age > 18", false, ""},
		{"valid AND", "x = 1 AND y = 2", false, ""},
		{"has semicolon", "x = 1; DROP TABLE", true, "terminator"},
		{"has LIMIT", "1=1 LIMIT 10", true, "LIMIT"},
		{"has UNION", "1=1 UNION SELECT", true, "UNION"},
		{"has --", "x = 1 -- comment", true, "comment"},
		{"has /*", "x = 1 /* comment */", true, "comment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWhereClause(tt.clause)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q", tt.errMsg)
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("error %q doesn't contain %q", err.Error(), tt.errMsg)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestMinInt(t *testing.T) {
	if minInt(3, 5) != 3 {
		t.Errorf("minInt(3,5) = %d", minInt(3, 5))
	}
	if minInt(5, 3) != 3 {
		t.Errorf("minInt(5,3) = %d", minInt(5, 3))
	}
}

func TestLooksLikeSqlite(t *testing.T) {
	sqliteHeader := []byte("SQLite format 3\x00")
	txt := []byte("hello world")
	if !looksLikeSqlite(sqliteHeader) {
		t.Errorf("looksLikeSqlite(SQLite header) = false, want true")
	}
	if looksLikeSqlite(txt) {
		t.Errorf("looksLikeSqlite(text) = true, want false")
	}
}

func TestSqlHasExplicitLimit(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want bool
	}{
		{"plain limit", "SELECT * FROM t LIMIT 100", true},
		{"lowercase limit", "select * from t limit 50", true},
		{"limit offset", "SELECT * FROM t LIMIT 10 OFFSET 5", true},
		{"limit newline", "SELECT * FROM t\nLIMIT 1000", true},
		{"no limit", "SELECT * FROM t", false},
		{"limit as column name", "SELECT limit FROM config", false},
		{"limit in string literal", "SELECT 'limit 100' AS hint FROM t", false},
		{"limit in table name", "SELECT * FROM speed_limits", false},
		{"limit without number", "SELECT * FROM t LIMIT ?", false},
		{"nested subquery limit", "SELECT * FROM (SELECT * FROM t LIMIT 5) x", true},
		{"mixed case", "Select * From t LiMiT 25", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sqlHasExplicitLimit(tt.sql); got != tt.want {
				t.Errorf("sqlHasExplicitLimit(%q) = %v, want %v", tt.sql, got, tt.want)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || containsSub(s, sub))
}

func containsSub(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
