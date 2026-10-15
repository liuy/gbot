package context_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/context"
)

// benchSink keeps Build()'s result live so the compiler can't dead-code the call.
var benchSink string

func BenchmarkBuild_Minimal(b *testing.B) {
	bldr := context.NewBuilder("/work/project")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSink = bldr.Build()
	}
}

func BenchmarkBaseSystemPrompt(b *testing.B) {
	bldr := context.NewBuilder("/work")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bldr.BaseSystemPrompt()
	}
}

func BenchmarkPlatformInfo(b *testing.B) {
	bldr := context.NewBuilder("/work/project")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bldr.RuntimeInfo()
	}
}

func BenchmarkLoadContextFiles_NoFile(b *testing.B) {
	tmpDir := b.TempDir()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = context.LoadContextFiles(tmpDir)
	}
}

func BenchmarkLoadContextFiles_WithFile(b *testing.B) {
	tmpDir := b.TempDir()
	content := "# AGENTS Instructions\n\nAlways use Go 1.24 idioms.\nPrefer table-driven tests."
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(content), 0o644); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = context.LoadContextFiles(tmpDir)
	}
}

func BenchmarkBuild_Unmarshal(b *testing.B) {
	bldr := context.NewBuilder("/work/project")
	result := bldr.Build()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = result
	}
}
