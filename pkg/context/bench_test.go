package context_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/context"
)

// ---------------------------------------------------------------------------
// Build benchmarks
// ---------------------------------------------------------------------------

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

func BenchmarkBuild_WithGBOTMD(b *testing.B) {
	bldr := context.NewBuilder("/work/project")
	bldr.GBOTMDContent = "Always use Go 1.24 idioms.\nPrefer editing existing files over creating new ones.\nUse table-driven tests for all test functions.\nAvoid global state in packages."

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
	bldr.GBOTMDContent = "Always use Go 1.24 idioms.\nPrefer editing existing files over creating new ones.\nUse table-driven tests for all test functions.\nAvoid global state in packages."
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

// ---------------------------------------------------------------------------
// BaseSystemPrompt benchmark
// ---------------------------------------------------------------------------

func BenchmarkBaseSystemPrompt(b *testing.B) {
	bldr := context.NewBuilder("/work")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bldr.BaseSystemPrompt()
	}
}

// ---------------------------------------------------------------------------
// PlatformInfo benchmark
// ---------------------------------------------------------------------------

func BenchmarkPlatformInfo(b *testing.B) {
	bldr := context.NewBuilder("/work/project")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bldr.PlatformInfo()
	}
}

// ---------------------------------------------------------------------------
// GitStatusSection benchmarks
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// LoadGBOTMD benchmark
// ---------------------------------------------------------------------------

func BenchmarkLoadGBOTMD_NoFile(b *testing.B) {
	tmpDir := b.TempDir()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = context.LoadGBOTMD(tmpDir)
	}
}

func BenchmarkLoadGBOTMD_WithFile(b *testing.B) {
	tmpDir := b.TempDir()
	content := "# GBOT Instructions\n\nAlways use Go 1.24 idioms.\nPrefer table-driven tests.\nKeep functions short and focused."
	if err := os.WriteFile(filepath.Join(tmpDir, "GBOT.md"), []byte(content), 0o644); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = context.LoadGBOTMD(tmpDir)
	}
}

func BenchmarkLoadGBOTMD_MultipleFiles(b *testing.B) {
	tmpDir := b.TempDir()

	// Root GBOT.md
	if err := os.WriteFile(filepath.Join(tmpDir, "GBOT.md"), []byte("Root instructions."), 0o644); err != nil {
		b.Fatal(err)
	}

	// .gbot/GBOT.md
	gbotDir := filepath.Join(tmpDir, ".gbot")
	if err := os.MkdirAll(gbotDir, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gbotDir, "GBOT.md"), []byte("Extended instructions with more detail about coding standards and project conventions."), 0o644); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = context.LoadGBOTMD(tmpDir)
	}
}

// Build output JSON unmarshal (round-trip)
// ---------------------------------------------------------------------------

func BenchmarkBuild_Unmarshal(b *testing.B) {
	bldr := context.NewBuilder("/work/project")
	bldr.GitStatus = &context.GitStatusInfo{
		IsGit:  true,
		Branch: "main",
	}
	bldr.GBOTMDContent = "Some instructions."
	result, err := bldr.Build()
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var s string
		_ = json.Unmarshal(result, &s)
	}
}
