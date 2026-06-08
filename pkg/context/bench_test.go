package context_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/context"
)

func BenchmarkBuild_Minimal(b *testing.B) {
	bldr := context.NewBuilder("/work/project")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bldr.Build()
	}
}

func BenchmarkBuild_WithGitStatus(b *testing.B) {
	bldr := context.NewBuilder("/work/project")
	bldr.GitStatus = &context.GitStatusInfo{
		IsGit:         true,
		Branch:        "feature/benchmark-test",
		DefaultBranch: "main",
		IsDirty:       true,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bldr.Build()
	}
}

func BenchmarkBuild_WithToolPrompts(b *testing.B) {
	bldr := context.NewBuilder("/work/project")
	bldr.ToolPrompts = []string{
		"Bash: Execute shell commands. Use for running builds, tests, and other CLI tools.",
		"Read: Read file contents. Use dedicated tools over Bash for file operations.",
		"Edit: Make targeted edits to existing files. Prefer over Write for modifications.",
		"Write: Create or completely replace files.",
		"Glob: Find files matching a glob pattern using doublestar v4.",
		"Grep: Search file contents using ripgrep. Supports regex, file type, and glob filters.",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bldr.Build()
	}
}

func BenchmarkBuild_Full(b *testing.B) {
	bldr := context.NewBuilder("/work/project")
	bldr.GitStatus = &context.GitStatusInfo{
		IsGit:         true,
		Branch:        "feature/benchmark-test",
		DefaultBranch: "main",
		IsDirty:       true,
	}
	bldr.ToolPrompts = []string{
		"Bash: Execute shell commands. Use for running builds, tests, and other CLI tools.",
		"Read: Read file contents. Use dedicated tools over Bash for file operations.",
		"Edit: Make targeted edits to existing files. Prefer over Write for modifications.",
		"Write: Create or completely replace files.",
		"Glob: Find files matching a glob pattern using doublestar v4.",
		"Grep: Search file contents using ripgrep. Supports regex, file type, and glob filters.",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bldr.Build()
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

func BenchmarkGitStatusSection_Clean(b *testing.B) {
	bldr := context.NewBuilder("/work")
	bldr.GitStatus = &context.GitStatusInfo{
		IsGit:   true,
		Branch:  "main",
		IsDirty: false,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bldr.GitStatusSection()
	}
}

func BenchmarkGitStatusSection_Dirty(b *testing.B) {
	bldr := context.NewBuilder("/work")
	bldr.GitStatus = &context.GitStatusInfo{
		IsGit:         true,
		Branch:        "feature/some-long-branch-name-with-details",
		DefaultBranch: "main",
		IsDirty:       true,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bldr.GitStatusSection()
	}
}

func BenchmarkGitStatusSection_NonGit(b *testing.B) {
	bldr := context.NewBuilder("/work")
	bldr.GitStatus = &context.GitStatusInfo{IsGit: false}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bldr.GitStatusSection()
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
	bldr.GitStatus = &context.GitStatusInfo{
		IsGit:  true,
		Branch: "main",
	}
	result, err := bldr.Build()
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = result
	}
}
