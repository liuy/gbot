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
