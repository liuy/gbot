package wechat

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

// pinRunInbound drives handleInbound with a query stub that closes queryDone,
// and returns the events dispatched on the hub plus the recorded query args.
func pinRunInbound(t *testing.T, c *WeChatConnector, msg inboundMessage) ([]types.QueryEvent, []context.Context) {
	t.Helper()
	h := hub.NewHub()
	spy := &hubSpy{}
	h.Subscribe(spy)
	c.hub = h
	var ctxs []context.Context
	var mu sync.Mutex
	c.queryFn = func(ctx context.Context, _, _ string) {
		mu.Lock()
		ctxs = append(ctxs, ctx)
		mu.Unlock()
		close(c.queryDone)
	}
	c.queryWithContentFn = func(ctx context.Context, _ []types.ContentBlock, _ string) {
		mu.Lock()
		ctxs = append(ctxs, ctx)
		mu.Unlock()
		close(c.queryDone)
	}
	c.handleInbound(context.Background(), msg)
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return append([]types.QueryEvent(nil), spy.events...), ctxs
}

// pinDisplayText extracts the single text block of the EventConnectorUserMessage.
func pinDisplayText(t *testing.T, events []types.QueryEvent) types.Message {
	t.Helper()
	for _, e := range events {
		if e.Type == types.EventConnectorUserMessage && e.Message != nil {
			return *e.Message
		}
	}
	t.Fatalf("no EventConnectorUserMessage dispatched in %d events", len(events))
	return types.Message{}
}

// The rendered user-message header is built from the content blocks: first
// line only, document names collapsed into one header, images as placeholders,
// and the caption as a last-resort fallback.
func TestPinHandleInboundDisplayText(t *testing.T) {
	doc := func(name string) types.ContentBlock {
		return types.NewTextBlock("[Document: " + name + " saved at /tmp/" + name + "]")
	}
	img := types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="})
	cases := []struct {
		name string
		msg  inboundMessage
		want string
	}{
		{
			name: "plain text block",
			msg:  inboundMessage{userID: "u1", text: "caption", content: []types.ContentBlock{types.NewTextBlock("hello")}},
			want: "hello",
		},
		{
			name: "leading newline keeps the block text",
			msg:  inboundMessage{userID: "u1", text: "caption", content: []types.ContentBlock{types.NewTextBlock("\nabc")}},
			want: "\nabc",
		},
		{
			name: "multi-line block shows first line only",
			msg:  inboundMessage{userID: "u1", text: "caption", content: []types.ContentBlock{types.NewTextBlock("a\nbc")}},
			want: "a",
		},
		{
			name: "single document",
			msg:  inboundMessage{userID: "u1", text: "caption", content: []types.ContentBlock{doc("f.pdf")}},
			want: "[Document: f.pdf]",
		},
		{
			name: "two documents",
			msg:  inboundMessage{userID: "u1", text: "caption", content: []types.ContentBlock{doc("a.pdf"), doc("b.pdf")}},
			want: "[Documents: a.pdf, b.pdf]",
		},
		{
			name: "document plus image",
			msg:  inboundMessage{userID: "u1", text: "caption", content: []types.ContentBlock{doc("a.pdf"), img}},
			want: "[Document: a.pdf] [image]",
		},
		{
			name: "image plus text",
			msg:  inboundMessage{userID: "u1", text: "caption", content: []types.ContentBlock{img, types.NewTextBlock("hi")}},
			want: "[image] hi",
		},
		{
			name: "empty block falls back to caption",
			msg:  inboundMessage{userID: "u1", text: "caption", content: []types.ContentBlock{types.NewTextBlock("")}},
			want: "caption",
		},
		{
			name: "no content blocks uses caption",
			msg:  inboundMessage{userID: "u1", text: "caption"},
			want: "caption",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &WeChatConnector{typingCache: newTypingTicketCache(typingTTL)}
			events, _ := pinRunInbound(t, c, tc.msg)
			m := pinDisplayText(t, events)
			if m.Role != types.RoleUser {
				t.Errorf("Message.Role = %v, want %v", m.Role, types.RoleUser)
			}
			if len(m.Content) != 1 {
				t.Fatalf("len(Message.Content) = %d, want 1: %+v", len(m.Content), m.Content)
			}
			cb := m.Content[0]
			if cb.Type != types.ContentTypeText {
				t.Errorf("Content[0].Type = %v, want %v", cb.Type, types.ContentTypeText)
			}
			if cb.Text != tc.want {
				t.Errorf("displayText = %q, want %q", cb.Text, tc.want)
			}
		})
	}
}

// The caller's context must reach startTyping and the engine query, and the
// engine's system prompt must be handed to the query.
func TestPinHandleInboundWiring(t *testing.T) {
	mock := &pinTypingMock{}
	eng := &engine.Engine{}
	eng.SetSystemPrompt("SYS-PROMPT")
	c := &WeChatConnector{
		hub:         hub.NewHub(),
		typingCache: newTypingTicketCache(typingTTL),
		typingAPI:   mock,
		engine:      eng,
	}
	var gotPrompt string
	var gotText string
	var queryCtx context.Context
	c.queryFn = func(ctx context.Context, userMessage, sysPrompt string) {
		queryCtx, gotText, gotPrompt = ctx, userMessage, sysPrompt
		close(c.queryDone)
	}
	logs := pinCaptureSlog(t, func() {
		c.handleInbound(context.Background(), inboundMessage{userID: "u1", text: "hi"})
	})
	if queryCtx == nil {
		t.Error("queryFn ctx = nil, want the caller's ctx")
	}
	if gotText != "hi" {
		t.Errorf("queryFn userMessage = %q, want %q", gotText, "hi")
	}
	if gotPrompt != "SYS-PROMPT" {
		t.Errorf("queryFn systemPrompt = %q, want %q", gotPrompt, "SYS-PROMPT")
	}
	if c.activeUserID != "u1" {
		t.Errorf("activeUserID = %q, want %q", c.activeUserID, "u1")
	}
	if c.lastFlush.IsZero() {
		t.Error("lastFlush not set by handleInbound")
	}
	if !strings.Contains(logs, "process inbound") || !strings.Contains(logs, "u1") {
		t.Errorf("logs = %q, want an inbound line naming u1", logs)
	}
	_, sends, getCtx, _ := mock.snapshot()
	if len(getCtx) != 1 || getCtx[0] == nil {
		t.Errorf("getConfig ctxs = %v, want exactly one non-nil ctx", getCtx)
	}
	if len(sends) != 1 || sends[0].status != TypingStart {
		t.Errorf("sends = %+v, want one TypingStart", sends)
	}
}

// The content-block path must be preferred over the text-only query when both
// are wired, and must carry the caller's context.
func TestPinHandleInboundContentPath(t *testing.T) {
	c := &WeChatConnector{hub: hub.NewHub(), typingCache: newTypingTicketCache(typingTTL)}
	var contentCtx context.Context
	var textCalls int
	c.queryFn = func(_ context.Context, _, _ string) { textCalls++ }
	c.queryWithContentFn = func(ctx context.Context, _ []types.ContentBlock, _ string) {
		contentCtx = ctx
		close(c.queryDone)
	}
	c.handleInbound(context.Background(), inboundMessage{
		userID:  "u1",
		text:    "caption",
		content: []types.ContentBlock{types.NewTextBlock("body")},
	})
	if contentCtx == nil {
		t.Error("queryWithContentFn ctx = nil, want the caller's ctx")
	}
	if textCalls != 0 {
		t.Errorf("queryFn calls = %d, want 0 when content blocks are present", textCalls)
	}
}

// processLoop must hold the WaitGroup for the whole loop and release it when
// ctx is cancelled: Done at entry would let shutdown proceed while a query is
// still running.
func TestPinProcessLoopReleasesWaitGroup(t *testing.T) {
	c := &WeChatConnector{inboundCh: make(chan inboundMessage, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	c.pollWg.Add(1)
	go c.processLoop(ctx)

	pending := make(chan struct{})
	go func() {
		c.pollWg.Wait()
		close(pending)
	}()
	select {
	case <-pending:
		t.Fatal("pollWg released while processLoop is still running: Done is not deferred")
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	select {
	case <-pending:
	case <-time.After(2 * time.Second):
		t.Fatal("pollWg still pending after ctx cancel: processLoop skipped its Done")
	}
}

// sendToUser must pass the caller's context to the send.
func TestPinSendToUserThreadsContext(t *testing.T) {
	var got context.Context
	c := &WeChatConnector{
		state:  &State{AccountID: "acct"},
		client: &http.Client{},
		sendMsgFn: func(ctx context.Context, _ *http.Client, _, _, _, _, _, _, _ string) error {
			got = ctx
			return nil
		},
	}
	ctx := t.Context()
	if err := c.sendToUser(ctx, "u1", "text"); err != nil {
		t.Fatalf("sendToUser: %v", err)
	}
	if got == nil {
		t.Error("sendMsgFn ctx = nil, want the caller's ctx")
	}
}

// extractDocName needs the marker strictly inside the header: at index 0 there
// is no name, and a missing marker must not slice with a negative index.
func TestPinExtractDocName(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantOK   bool
	}{
		{"[Document: f.pdf saved at /tmp/f.pdf]", "f.pdf", true},
		{"[Document: x saved at y", "x", true},
		{"[Document:  saved at y", "", false},
		{"[Document: no marker here]", "", false},
		{"[Document: ", "", false},
		{"plain text", "", false},
	}
	for _, tc := range cases {
		name, ok := extractDocName(tc.in)
		if ok != tc.wantOK || name != tc.wantName {
			t.Errorf("extractDocName(%q) = (%q, %v), want (%q, %v)", tc.in, name, ok, tc.wantName, tc.wantOK)
		}
	}
}
