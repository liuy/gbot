package project

import (
	"os"
	"path/filepath"
	"strings"
)

const maxSlugLength = 200

// Slug escapes a filesystem path into a directory-safe slug.
// Alphanumeric characters pass through; everything else becomes '-'.
// Truncates to maxSlugLength and appends a DJB2 hash suffix for uniqueness
// when the result exceeds the limit.
func Slug(path string) string {
	var b strings.Builder
	for _, r := range path {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	slug := b.String()

	if len(slug) <= maxSlugLength {
		return slug
	}

	hash := djb2Hash(path)
	prefix := slug[:maxSlugLength]
	return prefix + "-" + hash
}

// Dir returns ~/.gbot/projects/{Slug(workingDir)}/ for the given working directory.
func Dir(workingDir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "~"
	}
	return filepath.Join(home, ".gbot", "projects", Slug(workingDir))
}

// PIDFile returns the PID file path inside the project directory.
func PIDFile(projectDir string) string {
	return filepath.Join(projectDir, "gbot.pid")
}

// djb2Hash implements the DJB2 hash algorithm, matching the TS implementation.
func djb2Hash(str string) string {
	hash := uint32(5381)
	for _, c := range str {
		hash = hash*33 + uint32(c)
	}
	signed := int32(hash)
	if signed < 0 {
		signed = -signed
	}
	return uintToString(uint64(signed), 36)
}

func uintToString(n uint64, base int) string {
	if n == 0 {
		return "0"
	}
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	var buf [64]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = digits[n%uint64(base)]
		n /= uint64(base)
	}
	return string(buf[pos:])
}
