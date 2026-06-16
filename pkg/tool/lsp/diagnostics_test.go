package lsptool

import (
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
)

func TestSeverityName(t *testing.T) {
	tests := []struct {
		severity int
		want     string
	}{
		{1, "ERROR"},
		{2, "WARN"},
		{3, "INFO"},
		{4, "HINT"},
		{0, "?(0)"},
		{99, "?(99)"},
	}
	for _, tt := range tests {
		got := severityName(tt.severity)
		if got != tt.want {
			t.Errorf("severityName(%d) = %q, want %q", tt.severity, got, tt.want)
		}
	}
}

func TestFormatDiagnostic(t *testing.T) {
	d := lsp.Diagnostic{
		Range:    lsp.Range{Start: lsp.Position{Line: 4, Character: 9}},
		Severity: 1,
		Source:   "gopls",
		Message:  "cannot use string as int",
	}
	got := formatDiagnostic(d, "foo.go")
	if !contains(got, "[ERROR]") || !contains(got, "foo.go:5:10") || !contains(got, "cannot use string as int") || !contains(got, "(gopls)") {
		t.Errorf("formatDiagnostic unexpected: %q", got)
	}
}

func TestFormatDiagnostic_NoSource(t *testing.T) {
	d := lsp.Diagnostic{
		Range:    lsp.Range{Start: lsp.Position{Line: 0, Character: 0}},
		Severity: 2,
		Message:  "unused variable",
	}
	got := formatDiagnostic(d, "bar.go")
	if !contains(got, "[WARN]") || !contains(got, "bar.go:1:1") {
		t.Errorf("formatDiagnostic no-source unexpected: %q", got)
	}
	if contains(got, "(") {
		t.Errorf("formatDiagnostic should not have source suffix: %q", got)
	}
}

func TestRangesOverlap(t *testing.T) {
	tests := []struct {
		name string
		a, b lsp.Range
		want bool
	}{
		{
			"no overlap different lines",
			lsp.Range{Start: lsp.Position{Line: 0}, End: lsp.Position{Line: 3}},
			lsp.Range{Start: lsp.Position{Line: 5}, End: lsp.Position{Line: 7}},
			false,
		},
		{
			"full overlap",
			lsp.Range{Start: lsp.Position{Line: 0}, End: lsp.Position{Line: 10}},
			lsp.Range{Start: lsp.Position{Line: 2}, End: lsp.Position{Line: 5}},
			true,
		},
		{
			"partial overlap",
			lsp.Range{Start: lsp.Position{Line: 0}, End: lsp.Position{Line: 5}},
			lsp.Range{Start: lsp.Position{Line: 3}, End: lsp.Position{Line: 8}},
			true,
		},
		{
			"adjacent lines no overlap",
			lsp.Range{Start: lsp.Position{Line: 0}, End: lsp.Position{Line: 2, Character: 10}},
			lsp.Range{Start: lsp.Position{Line: 2, Character: 20}, End: lsp.Position{Line: 3}},
			false,
		},
		{
			"same line different chars overlap",
			lsp.Range{Start: lsp.Position{Line: 5, Character: 0}, End: lsp.Position{Line: 5, Character: 10}},
			lsp.Range{Start: lsp.Position{Line: 5, Character: 5}, End: lsp.Position{Line: 5, Character: 15}},
			true,
		},
		{
			"same line different chars no overlap",
			lsp.Range{Start: lsp.Position{Line: 5, Character: 0}, End: lsp.Position{Line: 5, Character: 5}},
			lsp.Range{Start: lsp.Position{Line: 5, Character: 10}, End: lsp.Position{Line: 5, Character: 20}},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rangesOverlap(tt.a, tt.b); got != tt.want {
				t.Errorf("rangesOverlap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
