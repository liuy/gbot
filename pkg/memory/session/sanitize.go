package session

import (
	"log/slog"
	"os"
	"strings"
)

// allowedHeaders is the exhaustive list of ## headers allowed in SESSION_NOTES.md.
// sanitizeNotes removes or merges anything outside this list.
var allowedHeaders = []string{
	"Session Title",
	"Current State",
	"Task specification",
	"Files and Functions",
	"Workflow",
	"Errors & Corrections",
	"Codebase and System Documentation",
	"Learnings",
	"Side Discoveries",
	"Key results",
	"Worklog",
}

// headerAliases maps stray headers (created by the SM agent) to their
// canonical allowed header. Unknown strays are dropped.
var headerAliases = map[string]string{
	"task specification":                "Task specification",
	"files and functions":               "Files and Functions",
	"files & functions":                 "Files and Functions",
	"key files & functions":             "Files and Functions",
	"errors":                            "Errors & Corrections",
	"errors and corrections":            "Errors & Corrections",
	"codebase":                          "Codebase and System Documentation",
	"codebase documentation":            "Codebase and System Documentation",
	"codebase and system documentation": "Codebase and System Documentation",
	"learnings":                         "Learnings",
	"key learnings":                     "Learnings",
	"key learnings (this session)":      "Learnings",
	"side discoveries":                  "Side Discoveries",
	"workflow":                          "Workflow",
	"worklog":                           "Worklog",
	"worklog (recent)":                  "Worklog",
	"worklog (2026-06-19, recent)":      "Worklog",
	"worklog (recent only — full history in git log)": "Worklog",
	"current state":        "Current State",
	"session title":        "Session Title",
	"key results":          "Key results",
	"commits this session": "",
	"commits this session (all pushed to origin/master)":   "",
	"commits earlier this session":                         "",
	"reference":                                            "",
	"key references":                                       "",
	"open follow-up":                                       "",
	"user feedback":                                        "",
	"user feedback (this session, must remember)":          "",
	"critical user feedback":                               "",
	"critical user feedback (this session, must remember)": "",
	"sub-agent event propagation chain":                    "Learnings",
	"latest":                                               "",
}

// SanitizeNotes reads SESSION_NOTES.md and enforces structural invariants:
//
//  1. Only allowed ## headers are present (see allowedHeaders).
//  2. Each allowed header appears at most once — duplicates are merged
//     (bodies concatenated, separated by a blank line).
//  3. Stray headers with known aliases are merged into their canonical
//     header. Unknown strays are dropped.
//
// This runs after the SM agent edits the file, as a safety net. If the
// file already conforms, it's a no-op.
func SanitizeNotes(notesPath string) error {
	data, err := os.ReadFile(notesPath)
	if err != nil {
		return err
	}
	content := string(data)

	// Split into preamble (before first ##) + sections.
	preamble := ""
	firstHeaderIdx := strings.Index(content, "\n## ")
	if firstHeaderIdx < 0 {
		return nil // no headers — nothing to sanitize
	}
	// Include the "# Session Notes" title line in preamble.
	preamble = content[:firstHeaderIdx+1] // +1 to keep the \n

	// Parse sections: each is "## Header\nbody\n" until next "## ".
	sections := parseSections(content[firstHeaderIdx+1:])

	// Merge sections by canonical header name. Track counts for logging.
	merged := make(map[string][]string) // canonical → list of bodies
	var dropped, aliased, duplicates, empty int

	for _, sec := range sections {
		canonical := resolveHeader(sec.header)
		if canonical == "" {
			dropped++
			continue
		}
		body := strings.TrimSpace(sec.body)
		if body == "" {
			empty++
			continue
		}
		if _, exists := merged[canonical]; exists {
			duplicates++
		} else if sec.header != canonical {
			aliased++
		}
		merged[canonical] = append(merged[canonical], body)
	}

	// Rebuild file in canonical order.
	var out strings.Builder
	out.WriteString(strings.TrimRight(preamble, "\n"))
	out.WriteString("\n")
	for _, name := range allowedHeaders {
		bodies, exists := merged[name]
		if !exists {
			continue
		}
		out.WriteString("\n## ")
		out.WriteString(name)
		out.WriteString("\n\n")
		for i, b := range bodies {
			if i > 0 {
				out.WriteString("\n")
			}
			out.WriteString(b)
			out.WriteString("\n")
		}
	}

	result := out.String()
	if result == content {
		if dropped+aliased+duplicates+empty > 0 {
			// Edge case: parse differences exist but reorder/normalization
			// produces byte-identical output. Still useful for visibility.
			slog.Info("session notes: sanitize",
				"dropped", dropped, "aliased", aliased,
				"duplicates_merged", duplicates, "empty_removed", empty,
				"status", "no_change")
		}
		return nil // no changes needed
	}
	slog.Info("session notes: sanitize",
		"dropped", dropped, "aliased", aliased,
		"duplicates_merged", duplicates, "empty_removed", empty,
		"size_before", len(content), "size_after", len(result),
		"status", "rewritten")
	return os.WriteFile(notesPath, []byte(result), 0o644)
}

type parsedSection struct {
	header string
	body   string
}

func parseSections(content string) []parsedSection {
	var sections []parsedSection
	lines := strings.Split(content, "\n")
	var current *parsedSection
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			if current != nil {
				sections = append(sections, *current)
			}
			current = &parsedSection{header: strings.TrimPrefix(line, "## ")}
		} else if current != nil {
			current.body += line + "\n"
		}
	}
	if current != nil {
		sections = append(sections, *current)
	}
	return sections
}

func resolveHeader(raw string) string {
	// Strip date suffixes, parenthetical notes, em-dash suffixes.
	base := raw
	if idx := strings.Index(base, " —"); idx >= 0 {
		base = base[:idx]
	}
	if idx := strings.Index(base, " ("); idx >= 0 {
		base = base[:idx]
	}
	if idx := strings.Index(base, "（"); idx >= 0 {
		base = base[:idx]
	}
	base = strings.TrimSpace(base)

	// Exact match against allowed list.
	for _, allowed := range allowedHeaders {
		if base == allowed {
			return allowed
		}
	}
	// Alias lookup.
	if canonical, ok := headerAliases[strings.ToLower(base)]; ok {
		return canonical
	}
	return "" // unknown — drop
}
