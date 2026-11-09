package context

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Pin tests for pkg/context mutation sweep; assertions are exact.

func pinWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPinLoadSoulFile(t *testing.T) {
	// Success path.
	home := t.TempDir()
	t.Setenv("HOME", home)
	pinWrite(t, filepath.Join(home, ".gbot", "SOUL.md"), "  soul content \n")
	got, err := LoadSoulFile()
	if err != nil || got != "soul content" {
		t.Errorf("LoadSoulFile = %q err=%v, want trimmed soul content", got, err)
	}
	// Missing file → ("", nil).
	if got, err := loadSystemFileFrom(home); err != nil || got != "" {
		t.Errorf("missing SYSTEM.md = %q err=%v, want empty/nil", got, err)
	}
	pinWrite(t, filepath.Join(home, ".gbot", "SYSTEM.md"), " base \n")
	if got, err := LoadSystemFile(); err != nil || got != "base" {
		t.Errorf("LoadSystemFile = %q err=%v, want trimmed base", got, err)
	}
	// UserHomeDir failure → ("", nil), not an error.
	t.Setenv("HOME", "")
	if got, err := LoadSoulFile(); err != nil || got != "" {
		t.Errorf("soul with no HOME = %q err=%v, want empty/nil", got, err)
	}
	if got, err := LoadSystemFile(); err != nil || got != "" {
		t.Errorf("system with no HOME = %q err=%v, want empty/nil", got, err)
	}
}

func TestPinLoadContextFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pinWrite(t, filepath.Join(home, ".gbot", "AGENTS.md"), "user agents")
	pinWrite(t, filepath.Join(home, ".gbot", "CLAUDE.md"), "user claude")

	proj := filepath.Join(t.TempDir(), "a", "b")
	pinWrite(t, filepath.Join(proj, "CLAUDE.md"), "cwd claude")
	pinWrite(t, filepath.Join(proj, "..", "CLAUDE.md"), "parent claude")
	pinWrite(t, filepath.Join(proj, "AGENTS.md"), "cwd agents")
	pinWrite(t, filepath.Join(proj, "empty.md"), "")

	m := LoadContextFiles(proj)
	if m[KeyClaudeMd] != "Contents of "+filepath.Join(home, ".gbot", "AGENTS.md")+":\n\nuser agents\n\nContents of "+filepath.Join(home, ".gbot", "CLAUDE.md")+":\n\nuser claude" {
		t.Errorf("KeyClaudeMd = %q", m[KeyClaudeMd])
	}
	// Root-first order: parent before cwd; within a dir AGENTS before CLAUDE.
	want := "Contents of " + filepath.Join(proj, "..", "CLAUDE.md") + ":\n\nparent claude\n\nContents of " +
		filepath.Join(proj, "AGENTS.md") + ":\n\ncwd agents\n\nContents of " + filepath.Join(proj, "CLAUDE.md") + ":\n\ncwd claude"
	if m[KeyProjectClaudeMd] != want {
		t.Errorf("KeyProjectClaudeMd = %q\nwant %q", m[KeyProjectClaudeMd], want)
	}
}

func TestPinBuildPrependUserContext(t *testing.T) {
	if got := BuildPrependUserContext(nil); got != "" {
		t.Errorf("empty map = %q", got)
	}
	got := BuildPrependUserContext(map[string]string{KeyProjectClaudeMd: "proj", KeyClaudeMd: "user"})
	if !strings.HasPrefix(got, "<system-reminder>\n") || !strings.HasSuffix(got, "</system-reminder>") {
		t.Errorf("wrapper malformed: %q", got[:50])
	}
	if !strings.Contains(got, "# claudeMd\nuser") || !strings.Contains(got, "# projectClaudeMd\nproj") {
		t.Errorf("sections missing or misordered: %q", got)
	}
}

func TestPinRuntimeInfo(t *testing.T) {
	t.Setenv("SHELL", "")
	b := &Builder{WorkingDir: t.TempDir()}
	info := b.RuntimeInfo()
	for _, want := range []string{"host=", "os=", "go=go", "shell=/bin/bash"} {
		if !strings.Contains(info, want) {
			t.Errorf("RuntimeInfo lacks %q: %q", want, info)
		}
	}
	if strings.Contains(info, "repo=") {
		t.Errorf("RuntimeInfo carries repo for a non-git dir: %q", info)
	}
	t.Setenv("SHELL", "/pin/sh")
	if got := b.RuntimeInfo(); !strings.Contains(got, "shell=/pin/sh") {
		t.Errorf("SHELL override ignored: %q", got)
	}
}

func TestPinMemoryFilesScan(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "1")
	mem := t.TempDir()
	pinWrite(t, filepath.Join(mem, "alpha.md"), " alpha body ")
	pinWrite(t, filepath.Join(mem, "MEMORY.md"), "index")
	pinWrite(t, filepath.Join(mem, "blank.md"), "   ")
	pinWrite(t, filepath.Join(mem, "notes.txt"), "not markdown")
	if err := os.MkdirAll(filepath.Join(mem, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := scanMemoryFiles(mem)
	if len(files) != 1 || !strings.HasSuffix(files[0].Path, "alpha.md") || files[0].Content != "alpha body" {
		t.Errorf("scan = %+v, want only trimmed alpha.md", files)
	}
	if got := FormatMemorySection(nil); got != "" {
		t.Errorf("empty section = %q", got)
	}
}

func TestPinBuildSystemPrompt_Memory(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "1")
	mem := t.TempDir()
	pinWrite(t, filepath.Join(mem, "MEMORY.md"), "pin memory body")
	out := BuildSystemPrompt(t.TempDir(), t.TempDir(), nil, "", mem)
	if !strings.Contains(out, "pin memory body") {
		t.Errorf("BuildSystemPrompt lacks the memory file content:\n%.300s", out)
	}
	if !strings.Contains(out, "{{SOUL}}") {
		t.Error("BuildSystemPrompt lacks the SOUL placeholder")
	}
	b := NewBuilder(t.TempDir())
	b.SkillListing = "pin-skill-listing"
	if got := b.Build(); !strings.Contains(got, "## Available Skills\n\npin-skill-listing") {
		t.Error("Build lacks the skill listing section")
	}
}

// An empty project-level AGENTS/CLAUDE file must not contribute a header.
func TestPinLoadContextFilesSkipsEmptyProjectFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(t.TempDir(), "a", "b")
	pinWrite(t, filepath.Join(proj, "CLAUDE.md"), "cwd claude")
	pinWrite(t, filepath.Join(proj, "..", "CLAUDE.md"), "")
	pinWrite(t, filepath.Join(proj, "..", "AGENTS.md"), "   ")
	m := LoadContextFiles(proj)
	got := m[KeyProjectClaudeMd]
	want := "Contents of " + filepath.Join(proj, "CLAUDE.md") + ":\n\ncwd claude"
	if got != want {
		t.Errorf("KeyProjectClaudeMd = %q, want only the non-empty file", got)
	}
}

// The OS label comes from /etc/os-release PRETTY_NAME, not the raw GOOS.
func TestPinRuntimeInfoOSLabel(t *testing.T) {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		t.Skip("no /etc/os-release on this platform")
	}
	pretty := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		if after, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			pretty = strings.Trim(after, `"`)
		}
	}
	if pretty == "" {
		t.Skip("no PRETTY_NAME in /etc/os-release")
	}
	b := &Builder{WorkingDir: t.TempDir()}
	want := "os=" + pretty + " (" + runtime.GOARCH + ")"
	if info := b.RuntimeInfo(); !strings.Contains(info, want) {
		t.Errorf("RuntimeInfo = %q, want it to contain %q", info, want)
	}
}
