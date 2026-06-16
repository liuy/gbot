package lsptool

import (
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
)

func TestPositionInRange(t *testing.T) {
	r := lsp.Range{
		Start: lsp.Position{Line: 5, Character: 3},
		End:   lsp.Position{Line: 8, Character: 10},
	}
	tests := []struct {
		name string
		pos  lsp.Position
		want bool
	}{
		{"inside", lsp.Position{Line: 6, Character: 0}, true},
		{"before start line", lsp.Position{Line: 4, Character: 0}, false},
		{"after end line", lsp.Position{Line: 9, Character: 0}, false},
		{"start line before char", lsp.Position{Line: 5, Character: 2}, false},
		{"start line at char", lsp.Position{Line: 5, Character: 3}, true},
		{"end line after char", lsp.Position{Line: 8, Character: 11}, false},
		{"end line at char", lsp.Position{Line: 8, Character: 10}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := positionInRange(tt.pos, r); got != tt.want {
				t.Errorf("positionInRange() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindInnermostSymbol(t *testing.T) {
	syms := []lsp.DocumentSymbol{
		{
			Name: "outer",
			Range: lsp.Range{
				Start: lsp.Position{Line: 0},
				End:   lsp.Position{Line: 20},
			},
			SelectionRange: lsp.Range{
				Start: lsp.Position{Line: 0, Character: 5},
				End:   lsp.Position{Line: 0, Character: 10},
			},
			Children: []lsp.DocumentSymbol{
				{
					Name: "inner",
					Range: lsp.Range{
						Start: lsp.Position{Line: 5},
						End:   lsp.Position{Line: 10},
					},
					SelectionRange: lsp.Range{
						Start: lsp.Position{Line: 5, Character: 5},
						End:   lsp.Position{Line: 5, Character: 10},
					},
				},
			},
		},
	}

	// Position inside inner child
	inner := findInnermostSymbol(syms, lsp.Position{Line: 7})
	if inner == nil || inner.Name != "inner" {
		t.Errorf("findInnermostSymbol(line 7) = %v, want inner", inner)
	}

	// Position inside outer but not inner
	outer := findInnermostSymbol(syms, lsp.Position{Line: 15})
	if outer == nil || outer.Name != "outer" {
		t.Errorf("findInnermostSymbol(line 15) = %v, want outer", outer)
	}

	// Position outside everything
	none := findInnermostSymbol(syms, lsp.Position{Line: 30})
	if none != nil {
		t.Errorf("findInnermostSymbol(line 30) = %v, want nil", none)
	}
}

func TestFindInnermostSymbol_Empty(t *testing.T) {
	got := findInnermostSymbol(nil, lsp.Position{Line: 0})
	if got != nil {
		t.Errorf("findInnermostSymbol(nil) = %v, want nil", got)
	}
}

func TestParseSymbolOccurrence(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantOcc  int
	}{
		{"foo", "foo", 1},
		{"foo#2", "foo", 2},
		{"foo#10", "foo", 10},
		{"bar.baz#3", "bar.baz", 3},
		{"foo#0", "foo#0", 1}, // #0 invalid, stays as name
		{"foo#", "foo#", 1},   // trailing # no number
		{"#foo", "#foo", 1},   // leading #
	}
	for _, tt := range tests {
		name, occ := parseSymbolOccurrence(tt.input)
		if name != tt.wantName || occ != tt.wantOcc {
			t.Errorf("parseSymbolOccurrence(%q) = (%q, %d), want (%q, %d)",
				tt.input, name, occ, tt.wantName, tt.wantOcc)
		}
	}
}
