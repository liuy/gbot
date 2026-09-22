package app

// Injected via -ldflags -X at build time (Makefile). Plain `go build`
// (tests, CI without the Makefile) keeps the dev defaults.
var (
	version = "dev"
	commit  = "unknown"
)

// VersionInfo returns the build identity for the admin restart endpoint as
// a single "version · commit" string. Makefile injects a short commit hash,
// so the joined form fits one UI row without truncation.
func VersionInfo() string { return version + " · " + commit }
