package fileread_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool/fileread"
)

func sqliteDB(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "test.sqlite")
}

func TestExecute_Sqlite_ListTables(t *testing.T) {
	t.Parallel()
	fp := sqliteDB(t)
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if !strings.Contains(output.Content, "users") {
		t.Errorf("expected 'users' table in listing, got: %q", output.Content)
	}
	if !strings.Contains(output.Content, "posts") {
		t.Errorf("expected 'posts' table in listing, got: %q", output.Content)
	}
}

func TestExecute_Sqlite_Schema(t *testing.T) {
	t.Parallel()
	fp := sqliteDB(t)
	input := json.RawMessage(`{"file_path":"` + fp + `:users"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if !strings.Contains(output.Content, "CREATE TABLE") {
		t.Errorf("expected CREATE TABLE in schema, got: %q", output.Content)
	}
}

func TestExecute_Sqlite_RowByPK(t *testing.T) {
	t.Parallel()
	fp := sqliteDB(t)
	input := json.RawMessage(`{"file_path":"` + fp + `:users:2"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if !strings.Contains(output.Content, "Bob") {
		t.Errorf("expected 'Bob' in row, got: %q", output.Content)
	}
}

func TestExecute_Sqlite_QueryPagination(t *testing.T) {
	t.Parallel()
	fp := sqliteDB(t)
	input := json.RawMessage(`{"file_path":"` + fp + `:users?limit=2&offset=0"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if !strings.Contains(output.Content, "Alice") || !strings.Contains(output.Content, "Bob") {
		t.Errorf("expected Alice and Bob in first page, got: %q", output.Content)
	}
	if strings.Contains(output.Content, "Charlie") {
		t.Errorf("Charlie should not be in first page with limit=2, got: %q", output.Content)
	}
}

func TestExecute_Sqlite_RawQuery(t *testing.T) {
	t.Parallel()
	fp := sqliteDB(t)
	input := json.RawMessage(`{"file_path":"` + fp + `?q=SELECT COUNT(*) AS cnt FROM users"}`)
	result, err := fileread.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	output, ok := result.Data.(fileread.TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want fileread.TextOutput", result.Data)
	}
	if !strings.Contains(output.Content, "3") {
		t.Errorf("expected count 3 in raw query result, got: %q", output.Content)
	}
}

func TestExecute_Sqlite_NotSqliteFile(t *testing.T) {
	t.Parallel()
	// .sqlite extension but content is plain text — magic byte check should reject
	dir := t.TempDir()
	fp := dir + "/fake.sqlite"
	if err := writeFile(fp, "this is not sqlite"); err != nil {
		t.Fatal(err)
	}
	// Plain "fake.sqlite" matches the path pattern with no subpath, then
	// trySqlitePath checks magic bytes and fails → returns (nil, false),
	// so execution falls through to text path and succeeds. This is OK.
	// But a path with a table selector should error out cleanly.
	input := json.RawMessage(`{"file_path":"` + fp + `:users"}`)
	_, err := fileread.Execute(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error for non-sqlite file with .sqlite:table selector")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected 'does not exist' error, got: %v", err)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
