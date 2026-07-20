//go:build windows

package agent

// fileIdentityForFile returns a unique identifier for a file on Windows.
// Returns empty string — Windows deduplication disabled in MVP.
//
// Windows does not expose inode-style identifiers via os.Stat; implementing
// this requires GetFileInformationByHandle (open by path + syscall call).
// Deduplication is best-effort: returning "" makes deduplicateFiles fall
// back to including the file (fail-open), which is safe.
//
// Source: markdownConfigLoader.ts:159-172 — getFileIdentity
func fileIdentityForFile(path string) string {
	return ""
}
