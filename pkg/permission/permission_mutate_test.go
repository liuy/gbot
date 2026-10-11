package permission

import "testing"

// The safe env var allowlist is pinned name by name (StripSafeWrappers is
// allowlist-gated; StripAllLeadingEnvVars strips everything).
func TestStripSafeWrappers_EnvAllowlist(t *testing.T) {
	t.Parallel()
	allowed := []string{
		"GOEXPERIMENT", "GOOS", "GOARCH", "CGO_ENABLED", "GO111MODULE",
		"RUST_BACKTRACE", "RUST_LOG",
		"NODE_ENV",
		"PYTHONUNBUFFERED", "PYTHONDONTWRITEBYTECODE",
		"PYTEST_DISABLE_PLUGIN_AUTOLOAD", "PYTEST_DEBUG",
		"LANG", "LANGUAGE", "LC_ALL", "LC_CTYPE", "LC_TIME", "CHARSET",
		"TERM", "COLORTERM", "NO_COLOR", "FORCE_COLOR", "TZ",
		"LS_COLORS", "LSCOLORS", "GREP_COLOR", "GREP_COLORS", "GCC_COLORS",
		"TIME_STYLE", "BLOCK_SIZE", "BLOCKSIZE",
	}
	for _, name := range allowed {
		got := StripSafeWrappers(name + "=1 go build")
		if got != "go build" {
			t.Errorf("StripSafeWrappers(%q) = %q, want go build", name+"=1 go build", got)
		}
	}
	for _, name := range []string{"PATH", "HOME", "LD_PRELOAD", "GOFLAGS"} {
		cmd := name + "=1 go build"
		if got := StripSafeWrappers(cmd); got != cmd {
			t.Errorf("unsafe %q stripped to %q, want it kept", name, got)
		}
	}
	// StripAllLeadingEnvVars is the deny-rule hammer: no allowlist.
	for _, name := range []string{"PATH", "LD_PRELOAD"} {
		if got := StripAllLeadingEnvVars(name + "=1 go build"); got != "go build" {
			t.Errorf("StripAllLeadingEnvVars(%q) = %q, want go build", name, got)
		}
	}
}

// Escaped wildcards and backslashes must survive pattern compilation, and a
// trailing lone backslash must not read past the pattern.
func TestMatchWildcardPattern_Escapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern string
		input   string
		want    bool
	}{
		{`a\*b`, "a*b", true},
		{`a\*b`, "axb", false},
		{`a\\b`, `a\b`, true},
		{`a\`, "a", false},
		{`a\`, `a\`, true},
		{`git *`, "git status", true},
		{`git *`, "git", true}, // a trailing " *" is optional
		{`git *`, "gitx", false},
		{`*`, "anything at all", true},
		{`  git *  `, "git push", true},
	}
	for _, c := range cases {
		if got := MatchWildcardPattern(c.pattern, c.input); got != c.want {
			t.Errorf("MatchWildcardPattern(%q, %q) = %v, want %v", c.pattern, c.input, got, c.want)
		}
	}
}
