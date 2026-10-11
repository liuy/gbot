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
	// os.UserHomeDir fails, so the literal "~" stands in for the home
	// segment; anything else (empty string, cwd) would silently relocate
	// the project directory.
	want := filepath.Join("~", ".gbot", "projects", "-test")
	if got != want {
		t.Errorf("Dir(%q) = %q, want %q", "/test", got, want)
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

// The pass-through class is exactly [A-Za-z0-9]; each of the six boundary
// characters sits one code point outside a range, so an off-by-one in any
// comparison flips one of these.
func TestSlug_CharacterClasses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   rune
		want string
	}{
		{'a', "a"}, {'z', "z"}, {'m', "m"},
		{'A', "A"}, {'Z', "Z"}, {'M', "M"},
		{'0', "0"}, {'9', "9"}, {'5', "5"},
		{'`', "-"}, // 'a' - 1
		{'{', "-"}, // 'z' + 1
		{'@', "-"}, // 'A' - 1
		{'[', "-"}, // 'Z' + 1
		{'/', "-"}, // '0' - 1
		{':', "-"}, // '9' + 1
		{'-', "-"}, {'_', "-"}, {'.', "-"}, {'/', "-"},
		{'\xe9', "-"}, // é is alphanumeric to Unicode but not to the slug
	}
	for _, tc := range tests {
		got := Slug(string(tc.in))
		if got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// maxSlugLength is the boundary itself: at exactly the limit the slug is
// returned untouched, one over it gets the hash suffix.
func TestSlug_TruncationBoundary(t *testing.T) {
	t.Parallel()
	if maxSlugLength != 200 {
		t.Fatalf("maxSlugLength = %d, want 200", maxSlugLength)
	}

	at := Slug(strings.Repeat("a", maxSlugLength))
	if at != strings.Repeat("a", maxSlugLength) {
		t.Errorf("Slug(200 chars) = %q, want the input unchanged (no hash suffix)", at)
	}

	over := Slug(strings.Repeat("a", maxSlugLength+1))
	want := strings.Repeat("a", maxSlugLength) + "-h42roy"
	if over != want {
		t.Errorf("Slug(201 chars) = %q, want %q", over, want)
	}
}

func TestSlug_LongPathExact(t *testing.T) {
	t.Parallel()
	input := strings.Repeat("/very-long-path-component", 20)
	want := "-very-long-path-component-very-long-path-component-very-long-path-component-very-long-path-component-very-long-path-component-very-long-path-component-very-long-path-component-very-long-path-component-gttdor"
	got := Slug(input)
	if got != want {
		t.Errorf("Slug(%d-char path) = %q, want %q", len(input), got, want)
	}
	if len(got) != 207 {
		t.Errorf("len(Slug) = %d, want 207 (200 prefix + '-' + 6-char hash)", len(got))
	}
}

// Reference values computed independently (32-bit unsigned DJB2, then
// abs(int32), then base 36). "daemon" is the case whose hash has the sign bit
// set, so it exercises the negation branch.
func TestDjb2Hash(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{"", "45h"},
		{"a", "3t3a"},
		{"/home/user/go", "1fs4fk"},
		{"/test", "37lsxg"},
		{"daemon", "25qw93"},
		// Sign-boundary preimages found by meet-in-the-middle over 32-bit
		// DJB2: the only inputs where the `signed < 0` comparison's
		// off-by-one variants can be observed at all.
		{"glk$0(", "1"}, // hash = 0xFFFFFFFF -> int32 -1 -> abs 1
		{"glk$0)", "0"}, // hash = 0
	}
	for _, tc := range tests {
		got := djb2Hash(tc.in)
		if got != tc.want {
			t.Errorf("djb2Hash(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUintToString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		n    uint64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{9, "9"},
		{10, "a"},
		{35, "z"},
		{36, "10"},
		{1295, "zz"},
		{1296, "100"},
		{130585287, "25qw93"},
		{18446744073709551615, "3w5e11264sgsf"},
	}
	for _, tc := range tests {
		got := uintToString(tc.n, 36)
		if got != tc.want {
			t.Errorf("uintToString(%d, 36) = %q, want %q", tc.n, got, tc.want)
		}
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
