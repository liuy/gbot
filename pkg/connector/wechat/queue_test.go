package wechat

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

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
