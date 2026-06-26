package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlug_Alphanumeric(t *testing.T) {
	t.Parallel()
	got := Slug("/home/user/go")
	want := "-home-user-go"
	if got != want {
		t.Errorf("Slug(%q) = %q, want %q", "/home/user/go", got, want)
	}
}

func TestSlug_SpecialChars(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"/path with spaces", "-path-with-spaces"},
		{"/a.b:c", "-a-b-c"},
		{"/home/user/my-project", "-home-user-my-project"},
	}
	for _, tc := range tests {
		got := Slug(tc.input)
		if got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestSlug_Daemon(t *testing.T) {
	t.Parallel()
	got := Slug("daemon")
	if got != "daemon" {
		t.Errorf("Slug(%q) = %q, want %q", "daemon", got, "daemon")
	}
}

func TestSlug_LongPath(t *testing.T) {
	t.Parallel()
	input := strings.Repeat("/very-long-path-component", 20)
	got := Slug(input)
	// Prefix is maxSlugLength + hash suffix makes it slightly longer
	if len(got) > maxSlugLength+20 {
		t.Errorf("slug too long: %d chars", len(got))
	}
	if !strings.Contains(got, "-") {
		t.Error("expected hash suffix separator after truncation")
	}
}

func TestSlug_RootPath(t *testing.T) {
	t.Parallel()
	got := Slug("/")
	if got != "-" {
		t.Errorf("Slug(%q) = %q, want %q", "/", got, "-")
	}
}

func TestDir_ProjectMode(t *testing.T) {
	homeDir := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", homeDir)

	workingDir := "/home/user/repos/gbot"
	got := Dir(workingDir)
	want := filepath.Join(homeDir, ".gbot", "projects", "-home-user-repos-gbot")
	if got != want {
		t.Errorf("Dir(%q) = %q, want %q", workingDir, got, want)
	}
}

func TestDir_DaemonMode(t *testing.T) {
	homeDir := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", homeDir)

	got := Dir("daemon")
	want := filepath.Join(homeDir, ".gbot", "projects", "daemon")
	if got != want {
		t.Errorf("Dir(%q) = %q, want %q", "daemon", got, want)
	}
}

func TestDir_HomeFallback(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	got := Dir("/test")
	if !strings.Contains(got, ".gbot") || !strings.Contains(got, "projects") {
		t.Errorf("Dir should contain .gbot/projects, got %q", got)
	}
}

func TestPIDFile(t *testing.T) {
	t.Parallel()
	projectDir := filepath.Join(t.TempDir(), "project")
	got := PIDFile(projectDir)
	want := filepath.Join(projectDir, "gbot.pid")
	if got != want {
		t.Errorf("PIDFile(%q) = %q, want %q", projectDir, got, want)
	}
}

func TestSlug_SamePathSameSlug(t *testing.T) {
	t.Parallel()
	slug1 := Slug("/home/user/project")
	slug2 := Slug("/home/user/project")
	if slug1 != slug2 {
		t.Errorf("same path produced different slugs: %q vs %q", slug1, slug2)
	}
}

func TestSlug_DifferentPathsDifferentSlugs(t *testing.T) {
	t.Parallel()
	slug1 := Slug("/home/user/project-a")
	slug2 := Slug("/home/user/project-b")
	if slug1 == slug2 {
		t.Errorf("different paths produced same slug: %q", slug1)
	}
}

func TestDir_Consistency(t *testing.T) {
	homeDir := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", homeDir)

	dir1 := Dir("/home/user/project")
	dir2 := Dir("/home/user/project")
	if dir1 != dir2 {
		t.Errorf("Dir should be deterministic: %q != %q", dir1, dir2)
	}
}

func TestSlug_NonASCII(t *testing.T) {
	t.Parallel()
	got := Slug("/home/user/项目")
	want := "-home-user---"
	if got != want {
		t.Errorf("Slug(%q) = %q, want %q", "/home/user/项目", got, want)
	}
}

func TestDir_UsesProjectDir(t *testing.T) {
	// Verify that Dir result is under ~/.gbot/projects/
	homeDir := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", homeDir)

	got := Dir("/some/path")
	if !strings.HasPrefix(got, filepath.Join(homeDir, ".gbot", "projects")) {
		t.Errorf("Dir should be under ~/.gbot/projects/, got %q", got)
	}
	// Ensure directory doesn't exist yet (Dir doesn't create)
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Errorf("Dir should not create the directory, got %q", got)
	}
}
