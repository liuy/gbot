package wui

import (
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// TestBuildHistoryChatMsg_DocumentBlock_ReferenceOnly pins the history wire
// shape for document blocks: a "document" block carrying name/mime/size —
// reference metadata for the frontend chip, never the parsed markdown and
// never merged into the legacy Text field.
func TestBuildHistoryChatMsg_DocumentBlock_ReferenceOnly(t *testing.T) {
	c := newTestConnector(t)

	m := types.Message{
		ID:        "m1",
		Role:      types.RoleUser,
		Timestamp: fixedTimestamp,
		Content: []types.ContentBlock{
			types.NewTextBlock("summarize this"),
			types.NewDocumentBlock("report.pdf", "/home/u/.gbot/cache/documents/0123456789abcdef.pdf", "application/pdf", 4321, 300),
		},
	}
	hm := c.buildHistoryChatMsg(m, nil, nil, nil)
	if len(hm.Blocks) != 2 {
		t.Fatalf("Blocks len = %d, want 2 (text + document)", len(hm.Blocks))
	}
	if hm.Blocks[0].Kind != "text" || hm.Blocks[0].Text != "summarize this" {
		t.Errorf("Blocks[0] = %+v, want text 'summarize this'", hm.Blocks[0])
	}
	b := hm.Blocks[1]
	if b.Kind != "document" {
		t.Fatalf("Blocks[1].Kind = %q, want document", b.Kind)
	}
	if b.Name != "report.pdf" || b.Mime != "application/pdf" || b.Size != 4321 {
		t.Errorf("document block = name:%q mime:%q size:%d, want report.pdf/application/pdf/4321", b.Name, b.Mime, b.Size)
	}
	if b.Text != "" {
		t.Errorf("document block must not carry text, got %q", b.Text)
	}
	if hm.Text != "summarize this" {
		t.Errorf("legacy Text = %q, want only the text block content (no document markdown)", hm.Text)
	}
}
