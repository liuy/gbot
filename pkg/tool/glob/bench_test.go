package glob_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/tool/glob"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Glob Execute benchmarks
// ---------------------------------------------------------------------------

// benchDir creates a temporary directory tree for benchmarking.
// Layout:
//
//	benchroot/
//	  file00.go .. file09.go
//	  sub1/
//	    file10.go .. file14.go
//	    deep/
//	      file20.go .. file25.go
//	  sub2/
//	    file30.go .. file34.go
//	  readme.md
//	  go.mod
func benchDir(b *testing.B) string {
	b.Helper()

	dir := b.TempDir()

	// Root-level .go files
	for i := range 10 {
		name := filepath.Join(dir, "file"+pad2(i)+".go")
		if err := os.WriteFile(name, []byte("package bench\n"), 0o644); err != nil {
			b.Fatal(err)
		}
	}

	// Non-Go files at root
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# bench"), 0o644); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module bench\n"), 0o644); err != nil {
		b.Fatal(err)
	}

	// sub1 with .go files
	sub1 := filepath.Join(dir, "sub1")
	if err := os.MkdirAll(sub1, 0o755); err != nil {
		b.Fatal(err)
	}
	for i := 10; i < 15; i++ {
		name := filepath.Join(sub1, "file"+pad2(i)+".go")
		if err := os.WriteFile(name, []byte("package bench\n"), 0o644); err != nil {
			b.Fatal(err)
		}
	}

	// sub1/deep with .go files
	deep := filepath.Join(sub1, "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		b.Fatal(err)
	}
	for i := 20; i < 26; i++ {
		name := filepath.Join(deep, "file"+pad2(i)+".go")
		if err := os.WriteFile(name, []byte("package bench\n"), 0o644); err != nil {
			b.Fatal(err)
		}
	}

	// sub2 with .go files
	sub2 := filepath.Join(dir, "sub2")
	if err := os.MkdirAll(sub2, 0o755); err != nil {
		b.Fatal(err)
	}
	for i := 30; i < 35; i++ {
		name := filepath.Join(sub2, "file"+pad2(i)+".go")
		if err := os.WriteFile(name, []byte("package bench\n"), 0o644); err != nil {
			b.Fatal(err)
		}
	}

	return dir
}

func pad2(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func BenchmarkExecute_SimpleGlob(b *testing.B) {
	dir := benchDir(b)
	input := json.RawMessage(`{"pattern":"*.go","path":"` + dir + `"}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = glob.Execute(context.Background(), input, nil)
	}
}

func BenchmarkExecute_RecursiveGlob(b *testing.B) {
	dir := benchDir(b)
	input := json.RawMessage(`{"pattern":"**/*.go","path":"` + dir + `"}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = glob.Execute(context.Background(), input, nil)
	}
}

func BenchmarkExecute_SubdirectoryGlob(b *testing.B) {
	dir := benchDir(b)
	input := json.RawMessage(`{"pattern":"sub1/**/*.go","path":"` + dir + `"}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = glob.Execute(context.Background(), input, nil)
	}
}

func BenchmarkExecute_NoMatches(b *testing.B) {
	dir := benchDir(b)
	input := json.RawMessage(`{"pattern":"*.xyz","path":"` + dir + `"}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = glob.Execute(context.Background(), input, nil)
	}
}

func BenchmarkExecute_WithToolUseContext(b *testing.B) {
	dir := benchDir(b)
	input := json.RawMessage(`{"pattern":"**/*.go"}`)
	tctx := &types.ToolUseContext{WorkingDir: dir}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = glob.Execute(context.Background(), input, tctx)
	}
}

// ---------------------------------------------------------------------------
// Glob tool construction benchmark
// ---------------------------------------------------------------------------

func BenchmarkNew(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = glob.New()
	}
}

// ---------------------------------------------------------------------------
// Output JSON marshaling benchmark
// ---------------------------------------------------------------------------

func BenchmarkOutputMarshal(b *testing.B) {
	files := make([]string, 100)
	for i := range files {
		files[i] = "path/to/file_" + string(rune('0'+i%10)) + ".go"
	}
	output := &glob.Output{Files: files, Count: len(files)}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(output)
	}
}
