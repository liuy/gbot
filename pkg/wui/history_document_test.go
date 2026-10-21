package wui

import (
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// TestBuildHistoryChatMsg_APIErrorFlagToErrorField pins the restart styling
// contract: a FlagAPIError assistant message must serialize its text into the
// wire `error` field (frontend errorBox) and NOT as chat text — matching the
// live query_end rendering, with no content sniffing anywhere.
func TestBuildHistoryChatMsg_APIErrorFlagToErrorField(t *testing.T) {
	c := newTestConnector(t)

	m := types.Message{
		ID:        "a1",
		Role:      types.RoleAssistant,
		Timestamp: fixedTimestamp,
		Content:   []types.ContentBlock{types.NewTextBlock("API Error 402: Insufficient account balance")},
	}
	m.Flags |= types.FlagAPIError

	hm := c.buildHistoryChatMsg(m, nil, nil, nil)
	if hm.Error != "API Error 402: Insufficient account balance" {
		t.Errorf("Error = %q, want the failure text", hm.Error)
	}
	if hm.Text != "" {
		t.Errorf("Text = %q, want empty — error text must not double as chat text", hm.Text)
	}
	if len(hm.Blocks) != 0 {
		t.Errorf("Blocks = %+v, want none — error renders via the error field only", hm.Blocks)
	}
}

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
