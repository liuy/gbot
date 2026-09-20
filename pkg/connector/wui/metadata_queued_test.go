package wui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

func TestBuildQueuedMsgs_FiltersJobAndMeta(t *testing.T) {
	items := []types.QueuedItem{
		{UUID: "p1", Value: "hello", Mode: types.ItemModePrompt},
		{UUID: "j1", Value: "job text", Mode: types.ItemModeJob},
		{UUID: "p2", Value: "meta prompt", Mode: types.ItemModePrompt, IsMeta: true},
		{UUID: "p3", Value: "world", Mode: types.ItemModePrompt},
	}
	out := buildQueuedMsgs(items)
	if len(out) != 2 {
		t.Fatalf("buildQueuedMsgs returned %d items, want 2 (job and meta filtered)", len(out))
	}
	if out[0].UUID != "p1" || out[0].Text != "hello" {
		t.Errorf("out[0] = {%s, %q}, want {p1, hello}", out[0].UUID, out[0].Text)
	}
	if out[1].UUID != "p3" || out[1].Text != "world" {
		t.Errorf("out[1] = {%s, %q}, want {p3, world}", out[1].UUID, out[1].Text)
	}
}

func TestBuildQueuedMsgs_PrefersContentTextOverValue(t *testing.T) {
	items := []types.QueuedItem{
		{
			UUID:    "c1",
			Value:   "fallback",
			Mode:    types.ItemModePrompt,
			Content: []types.ContentBlock{types.NewTextBlock("from content")},
		},
	}
	out := buildQueuedMsgs(items)
	if len(out) != 1 {
		t.Fatalf("buildQueuedMsgs returned %d items, want 1", len(out))
	}
	if out[0].Text != "from content" {
		t.Errorf("out[0].Text = %q, want %q (Content text must override Value)", out[0].Text, "from content")
	}
}

func TestBuildQueuedMsgs_EmptyInputReturnsNil(t *testing.T) {
	out := buildQueuedMsgs(nil)
	if out != nil {
		t.Errorf("buildQueuedMsgs(nil) = %v, want nil", out)
	}
	out = buildQueuedMsgs([]types.QueuedItem{})
	if out != nil {
		t.Errorf("buildQueuedMsgs(empty) = %v, want nil", out)
	}
}

func TestBuildQueuedMsgs_ContentWithNoTextFallsBackToValue(t *testing.T) {
	items := []types.QueuedItem{
		{
			UUID:  "i1",
			Value: "fallback",
			Mode:  types.ItemModePrompt,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64"}},
			},
		},
	}
	out := buildQueuedMsgs(items)
	if len(out) != 1 {
		t.Fatalf("buildQueuedMsgs returned %d items, want 1", len(out))
	}
	if out[0].Text != "fallback" {
		t.Errorf("out[0].Text = %q, want %q (image-only Content must fall back to Value)", out[0].Text, "fallback")
	}
}

func TestMetadata_QueuedMsgs(t *testing.T) {
	c := newTestConnector(t)
	c.mock().pendingAttachmentsFn = func() []types.QueuedItem {
		return []types.QueuedItem{
			{UUID: "q-1", Value: "first queued", Mode: types.ItemModePrompt},
			{UUID: "q-2", Value: "second queued", Mode: types.ItemModePrompt},
			{UUID: "j-1", Value: "job item", Mode: types.ItemModeJob},
		}
	}
	c.mock().messagesFn = func() []types.Message { return nil }

	mux := http.NewServeMux()
	var handlerWG sync.WaitGroup
	mux.HandleFunc("/ws/chat", func(w http.ResponseWriter, r *http.Request) {
		handlerWG.Add(1)
		defer handlerWG.Done()
		ws, err := chatUpgrader.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, "upgrade failed", http.StatusInternalServerError)
			return
		}
		serveChatWS(ws, c)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer handlerWG.Wait()

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	defer ws.Close()

	meta := readMetadata(t, ws)
	if len(meta.QueuedMsgs) == 0 {
		t.Fatal("metadata.queuedMsgs is empty, want 2 items")
	}
	var queued []queuedMsgJSON
	if err := json.Unmarshal(meta.QueuedMsgs, &queued); err != nil {
		t.Fatalf("unmarshal queuedMsgs: %v", err)
	}
	if len(queued) != 2 {
		t.Fatalf("queuedMsgs has %d items, want 2 (job item filtered)", len(queued))
	}
	if queued[0].UUID != "q-1" || queued[0].Text != "first queued" {
		t.Errorf("queued[0] = {%s, %q}, want {q-1, first queued}", queued[0].UUID, queued[0].Text)
	}
	if queued[1].UUID != "q-2" || queued[1].Text != "second queued" {
		t.Errorf("queued[1] = {%s, %q}, want {q-2, second queued}", queued[1].UUID, queued[1].Text)
	}
}

func TestBuildQueuedMsgs_AttachmentMetadata(t *testing.T) {
	items := []types.QueuedItem{
		{
			UUID: "att-1",
			Mode: types.ItemModePrompt,
			Content: []types.ContentBlock{
				types.NewTextBlock("check these files"),
				{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", MediaType: "image/png", Data: "SECRETDATA"}},
				types.NewDocumentBlock("report.pdf", "/cache/report.pdf", "application/pdf", 2048, 512),
			},
		},
	}
	out := buildQueuedMsgs(items)
	if len(out) != 1 {
		t.Fatalf("buildQueuedMsgs returned %d items, want 1", len(out))
	}
	if out[0].Text != "check these files" {
		t.Errorf("out[0].Text = %q, want %q", out[0].Text, "check these files")
	}
	if len(out[0].Attachments) != 2 {
		t.Fatalf("out[0].Attachments has %d entries, want 2 (image + document)", len(out[0].Attachments))
	}
	if out[0].Attachments[0].Name != "" || out[0].Attachments[0].Mime != "image/png" {
		t.Errorf("Attachments[0] = {%q, %q}, want {\"\", image/png}", out[0].Attachments[0].Name, out[0].Attachments[0].Mime)
	}
	if out[0].Attachments[1].Name != "report.pdf" || out[0].Attachments[1].Mime != "application/pdf" {
		t.Errorf("Attachments[1] = {%q, %q}, want {report.pdf, application/pdf}", out[0].Attachments[1].Name, out[0].Attachments[1].Mime)
	}
	wire, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal queuedMsgs: %v", err)
	}
	if strings.Contains(string(wire), "SECRETDATA") {
		t.Errorf("queuedMsgs wire leaks image base64 data: %s", string(wire))
	}
}

func TestBuildQueuedMsgs_TextOnlyContentHasNoAttachments(t *testing.T) {
	items := []types.QueuedItem{
		{
			UUID:    "t-1",
			Mode:    types.ItemModePrompt,
			Content: []types.ContentBlock{types.NewTextBlock("plain")},
		},
	}
	out := buildQueuedMsgs(items)
	if len(out) != 1 {
		t.Fatalf("buildQueuedMsgs returned %d items, want 1", len(out))
	}
	if len(out[0].Attachments) != 0 {
		t.Errorf("Attachments = %v, want none for text-only content", out[0].Attachments)
	}
	wire, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal queuedMsgs: %v", err)
	}
	if strings.Contains(string(wire), "attachments") {
		t.Errorf("text-only wire should omit attachments field: %s", string(wire))
	}
}

func TestMetadata_QueuedMsgsAttachmentMetadataOnWire(t *testing.T) {
	c := newTestConnector(t)
	c.mock().pendingAttachmentsFn = func() []types.QueuedItem {
		return []types.QueuedItem{
			{
				UUID: "q-att",
				Mode: types.ItemModePrompt,
				Content: []types.ContentBlock{
					types.NewTextBlock("with files"),
					{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "base64", MediaType: "image/jpeg", Data: "SECRETDATA"}},
					types.NewDocumentBlock("notes.pdf", "/cache/notes.pdf", "application/pdf", 4096, 1024),
				},
			},
		}
	}
	c.mock().messagesFn = func() []types.Message { return nil }

	mux := http.NewServeMux()
	var handlerWG sync.WaitGroup
	mux.HandleFunc("/ws/chat", func(w http.ResponseWriter, r *http.Request) {
		handlerWG.Add(1)
		defer handlerWG.Done()
		ws, err := chatUpgrader.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, "upgrade failed", http.StatusInternalServerError)
			return
		}
		serveChatWS(ws, c)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer handlerWG.Wait()

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	defer ws.Close()

	meta := readMetadata(t, ws)
	raw := string(meta.QueuedMsgs)
	if raw == "" {
		t.Fatal("metadata.queuedMsgs is empty, want 1 item")
	}
	if strings.Contains(raw, "SECRETDATA") {
		t.Errorf("queuedMsgs wire frame leaks base64 data: %s", raw)
	}
	var queued []queuedMsgJSON
	if err := json.Unmarshal(meta.QueuedMsgs, &queued); err != nil {
		t.Fatalf("unmarshal queuedMsgs: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("queuedMsgs has %d items, want 1", len(queued))
	}
	if len(queued[0].Attachments) != 2 {
		t.Fatalf("queued[0].Attachments has %d entries, want 2", len(queued[0].Attachments))
	}
	if queued[0].Attachments[0].Mime != "image/jpeg" {
		t.Errorf("Attachments[0].Mime = %q, want image/jpeg", queued[0].Attachments[0].Mime)
	}
	if queued[0].Attachments[1].Name != "notes.pdf" || queued[0].Attachments[1].Mime != "application/pdf" {
		t.Errorf("Attachments[1] = {%q, %q}, want {notes.pdf, application/pdf}", queued[0].Attachments[1].Name, queued[0].Attachments[1].Mime)
	}
}

func TestMetadata_NoQueuedMsgsWhenEmpty(t *testing.T) {
	c := newTestConnector(t)
	c.mock().messagesFn = func() []types.Message { return nil }

	mux := http.NewServeMux()
	var handlerWG sync.WaitGroup
	mux.HandleFunc("/ws/chat", func(w http.ResponseWriter, r *http.Request) {
		handlerWG.Add(1)
		defer handlerWG.Done()
		ws, err := chatUpgrader.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, "upgrade failed", http.StatusInternalServerError)
			return
		}
		serveChatWS(ws, c)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer handlerWG.Wait()

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	defer ws.Close()

	meta := readMetadata(t, ws)
	if len(meta.QueuedMsgs) != 0 {
		t.Errorf("metadata.queuedMsgs = %s, want omitted/empty when queue is empty", string(meta.QueuedMsgs))
	}
}

func TestMetadata_QueuedMsgsOnEngineSwitch(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	c.mock().messagesFn = func() []types.Message { return nil }

	// Add a second engine slot with its own pending attachments
	e2Mock := &mockEngine{}
	e2Mock.messagesFn = func() []types.Message { return nil }
	e2Mock.pendingAttachmentsFn = func() []types.QueuedItem {
		return []types.QueuedItem{
			{UUID: "e2-1", Value: "engine 2 queued", Mode: types.ItemModePrompt},
		}
	}
	e2Slot := &engineSlot{
		engineID:    "e2",
		engine:      e2Mock,
		hub:         h,
		taskToolIDs: make(map[string]bool),
	}
	c.slots["e2"] = e2Slot

	ws := dialAndStore(t, c)
	defer ws.Close()

	switchMsg, _ := json.Marshal(map[string]string{
		"type":     "engine_switch",
		"engineID": "e2",
	})
	if err := ws.WriteMessage(websocket.TextMessage, switchMsg); err != nil {
		t.Fatalf("write engine_switch: %v", err)
	}

	meta := readMetadata(t, ws)
	if len(meta.QueuedMsgs) == 0 {
		t.Fatal("engine_switch metadata.queuedMsgs is empty, want 1 item")
	}
	var queued []queuedMsgJSON
	if err := json.Unmarshal(meta.QueuedMsgs, &queued); err != nil {
		t.Fatalf("unmarshal queuedMsgs: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("queuedMsgs has %d items, want 1", len(queued))
	}
	if queued[0].UUID != "e2-1" || queued[0].Text != "engine 2 queued" {
		t.Errorf("queued[0] = {%s, %q}, want {e2-1, engine 2 queued}", queued[0].UUID, queued[0].Text)
	}
}
