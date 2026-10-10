package reload

import (
	"fmt"
	"strings"
	"time"
)

// FileStatus is the per-file outcome of a reload.
type FileStatus string

const (
	StatusReloaded  FileStatus = "reloaded"
	StatusUnchanged FileStatus = "unchanged"
	StatusFailed    FileStatus = "failed"
)

// FileEntry is one config surface's outcome row.
type FileEntry struct {
	Path   string     `json:"path"`
	Status FileStatus `json:"status"`
	Note   string     `json:"note,omitempty"`
	// NoteKey is a client-localizable note identifier (e.g.
	// "effectiveNextRequest"); the wui maps it through its i18n table.
	// Free-form Note stays for machine output (diffs, error hints).
	NoteKey string `json:"noteKey,omitempty"`
	ErrLine string `json:"errLine,omitempty"`
}

// Report is the full reload outcome: the TUI overlay text and the wui
// POST /api/settings/reload response body share this shape.
type Report struct {
	Applied bool        `json:"applied"`
	Phase   int         `json:"phase"` // 0..4, last phase reached / failure attribution
	Err     string      `json:"err,omitempty"`
	Files   []FileEntry `json:"files"`
	Counts  struct {
		Reloaded  int `json:"reloaded"`
		Unchanged int `json:"unchanged"`
		Failed    int `json:"failed"`
	} `json:"counts"`
	PromptChanged    bool      `json:"promptChanged"`
	EnginesRefreshed int       `json:"enginesRefreshed"`
	SettingsChanged  bool      `json:"settingsChanged"`
	LastReloadAt     time.Time `json:"lastReloadAt"`
}

// Render formats the report for the TUI info overlay (English).
func (r *Report) Render() string {
	var b strings.Builder
	if r.Applied {
		b.WriteString("Reload applied\n")
	} else {
		fmt.Fprintf(&b, "Reload aborted at phase %d of 5: %s\n", r.Phase+1, r.Err)
	}
	fmt.Fprintf(&b, "%d reloaded · %d unchanged · %d failed\n",
		r.Counts.Reloaded, r.Counts.Unchanged, r.Counts.Failed)
	for _, f := range r.Files {
		line := fmt.Sprintf("- %s [%s]", f.Path, f.Status)
		if f.Note != "" {
			line += " " + f.Note
		}
		if f.NoteKey != "" {
			line += " · " + noteKeyText(f.NoteKey)
		}
		b.WriteString(line + "\n")
		if f.ErrLine != "" {
			b.WriteString("  error: " + f.ErrLine + "\n")
		}
	}
	if r.PromptChanged {
		b.WriteString("MEMORY.md/CLAUDE.md changed: effective on next request\n")
	}
	if r.SettingsChanged {
		b.WriteString("engine params apply to new sessions only\n")
	}
	if r.EnginesRefreshed > 0 {
		fmt.Fprintf(&b, "%d engine(s) context refreshed\n", r.EnginesRefreshed)
	}
	return strings.TrimRight(b.String(), "\n")
}

// noteKeyText renders a localizable note key for the TUI (terminal stays
// English; the wui maps the same key through its i18n table).
func noteKeyText(key string) string {
	switch key {
	case "effectiveNextRequest":
		return "effective on next request"
	case "newSessionsOnly":
		return "engine params apply to new sessions only"
	}
	return key
}
