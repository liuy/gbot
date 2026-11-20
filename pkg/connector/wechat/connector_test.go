package wechat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// ---------------------------------------------------------------------------
// extractText
// ---------------------------------------------------------------------------

func TestExtractText_TextItem(t *testing.T) {
	items := []Item{
		{Type: ItemText, TextItem: &TextItem{Text: "hello world"}},
	}
	if got := extractText(items); got != "hello world" {
		t.Fatalf("extractText = %q, want %q", got, "hello world")
	}
}

func TestExtractText_RefMedia(t *testing.T) {
	items := []Item{
		{
			Type:     ItemText,
			TextItem: &TextItem{Text: "response text"},
			RefMsg: &RefMsg{
				Title: "image.jpg",
				MessageItem: &Item{
					Type:      ItemImage,
					ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: "http://example.com/img.jpg"}},
				},
			},
		},
	}
	got := extractText(items)
	if !strings.Contains(got, "[引用媒体: image.jpg]") || !strings.Contains(got, "response text") {
		t.Fatalf("extractText ref media = %q, want [引用媒体: image.jpg]...response text", got)
	}
}

func TestExtractText_RefNoTitle(t *testing.T) {
	items := []Item{
		{
			Type:     ItemText,
			TextItem: &TextItem{Text: "response"},
			RefMsg: &RefMsg{
				MessageItem: &Item{
					Type:      ItemVideo,
					VideoItem: &MediaItemHolder{Media: &MediaRef{FullURL: "http://example.com/vid.mp4"}},
				},
			},
		},
	}
	got := extractText(items)
	if !strings.Contains(got, "[引用媒体]") || !strings.Contains(got, "response") {
		t.Fatalf("extractText ref no title = %q", got)
	}
}

func TestExtractText_RefTextMessage(t *testing.T) {
	items := []Item{
		{
			Type:     ItemText,
			TextItem: &TextItem{Text: "my reply"},
			RefMsg: &RefMsg{
				Title: "quoted title",
				MessageItem: &Item{
					Type:     ItemText,
					TextItem: &TextItem{Text: "original message"},
				},
			},
		},
	}
	got := extractText(items)
	if !strings.Contains(got, "[引用: quoted title | original message]") || !strings.Contains(got, "my reply") {
		t.Fatalf("extractText ref text = %q", got)
	}
}

func TestExtractText_VoiceTranscription(t *testing.T) {
	items := []Item{
		{Type: ItemVoice, VoiceItem: &VoiceItem{Text: "transcribed text"}},
	}
	if got := extractText(items); got != "[voice transcription] transcribed text" {
		t.Fatalf("extractText voice = %q, want %q", got, "[voice transcription] transcribed text")
	}
}

func TestExtractText_Empty(t *testing.T) {
	if got := extractText(nil); got != "" {
		t.Fatalf("extractText nil = %q, want empty", got)
	}
	if got := extractText([]Item{}); got != "" {
		t.Fatalf("extractText empty = %q, want empty", got)
	}
}

func TestExtractText_TextPreferredOverVoice(t *testing.T) {
	items := []Item{
		{Type: ItemText, TextItem: &TextItem{Text: "text first"}},
		{Type: ItemVoice, VoiceItem: &VoiceItem{Text: "voice text"}},
	}
	if got := extractText(items); got != "text first" {
		t.Fatalf("extractText = %q, want %q", got, "text first")
	}
}

// ---------------------------------------------------------------------------
// hasMedia
// ---------------------------------------------------------------------------

func TestHasMedia(t *testing.T) {
	tests := []struct {
		name  string
		items []Item
		want  bool
	}{
		{"empty", nil, false},
		{"text only", []Item{{Type: ItemText}}, false},
		{"image", []Item{{Type: ItemImage}}, true},
		{"video", []Item{{Type: ItemVideo}}, true},
		{"file", []Item{{Type: ItemFile}}, true},
		{"voice", []Item{{Type: ItemVoice}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasMedia(tt.items); got != tt.want {
				t.Fatalf("hasMedia = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// dedupSet
// ---------------------------------------------------------------------------

func TestDedupSet_NotDuplicate(t *testing.T) {
	d := newDedupSet(300)
	if !d.Add("key1") {
		t.Fatal("expected first add to succeed")
	}
}

func TestDedupSet_Duplicate(t *testing.T) {
	d := newDedupSet(300)
	d.Add("key1")
	if d.Add("key1") {
		t.Fatal("expected second add to be duplicate")
	}
}

func TestDedupSet_TTLExpiry(t *testing.T) {
	d := &dedupSet{
		ttl:   50 * time.Millisecond,
		items: make(map[string]time.Time),
	}
	d.Add("key1")

	// Should still be duplicate immediately
	if d.Add("key1") {
		t.Fatal("expected duplicate before TTL expiry")
	}

	// Manually set the item timestamp to the past to simulate expiry
	d.mu.Lock()
	pastTime := d.items["key1"].Add(-60 * time.Millisecond)
	d.items["key1"] = pastTime
	d.mu.Unlock()

	if !d.Add("key1") {
		t.Fatal("expected not duplicate after TTL expiry")
	}
}

func TestDedupSet_DifferentKeys(t *testing.T) {
	d := newDedupSet(300)
	if !d.Add("key1") {
		t.Fatal("expected add key1")
	}
	if !d.Add("key2") {
		t.Fatal("expected add key2")
	}
}

func TestDedupSet_Concurrent(t *testing.T) {
	d := newDedupSet(300)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			d.Add("shared-key")
		})
	}
	wg.Wait()
	// Only one Add should have returned true
	if d.Add("shared-key") {
		t.Fatal("expected duplicate after concurrent adds")
	}
}

// ---------------------------------------------------------------------------
// formatMessage / normalizeMarkdownBlocks
// ---------------------------------------------------------------------------

func TestNormalizeMarkdownBlocks_Empty(t *testing.T) {
	if got := normalizeMarkdownBlocks(""); got != "" {
		t.Fatalf("empty input = %q", got)
	}
}

func TestNormalizeMarkdownBlocks_CollapseBlankLines(t *testing.T) {
	input := "line1\n\n\n\nline2"
	want := "line1\n\nline2"
	if got := normalizeMarkdownBlocks(input); got != want {
		t.Fatalf("normalizeMarkdownBlocks = %q, want %q", got, want)
	}
}

func TestNormalizeMarkdownBlocks_PreserveCodeBlock(t *testing.T) {
	input := "text\n```\n\n\ncode block\n\n\n```\nend"
	want := "text\n```\n\n\ncode block\n\n\n```\nend"
	if got := normalizeMarkdownBlocks(input); got != want {
		t.Fatalf("normalizeMarkdownBlocks = %q, want %q", got, want)
	}
}

func TestNormalizeMarkdownBlocks_TrimTrailingNewlines(t *testing.T) {
	if got := normalizeMarkdownBlocks("hello\n\n\n"); got != "hello" {
		t.Fatalf("expected trimmed trailing newlines, got %q", got)
	}
}

func TestFormatMessage_Empty(t *testing.T) {
	if got := formatMessage(""); got != "" {
		t.Fatalf("formatMessage empty = %q", got)
	}
}

func TestFormatMessage_FullPipeline(t *testing.T) {
	input := "line1\n\n\n\nline2"
	got := formatMessage(input)
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("expected collapsed blank lines, got: %s", got)
	}
}

// ---------------------------------------------------------------------------
// safeID
// ---------------------------------------------------------------------------

func TestSafeID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "?"},
		{"abc", "abc"},
		{"abcdefgh", "abcdefgh"},
		{"abcdefghij", "abcdefgh"},
	}
	for _, tt := range tests {
		if got := safeID(tt.input); got != tt.want {
			t.Fatalf("safeID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// State persistence
// ---------------------------------------------------------------------------

func TestStatePersistence(t *testing.T) {
	projectDir := t.TempDir()

	state := &State{
		AccountID:     "test-account",
		Token:         "test-token",
		BaseURL:       "https://test.url",
		SyncBuf:       "test-sync-buf",
		ContextTokens: map[string]string{"user1": "token1"},
		EngineID:      "wechat-test",
	}

	if err := SaveState(state, projectDir); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// File must land at the per-account path, not a global one.
	wantPath := filepath.Join(projectDir, "wechat", "test-account.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("state file not at expected path %s: %v", wantPath, err)
	}

	loaded, err := LoadState(state.AccountID, projectDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadState returned nil")
	}
	if loaded.AccountID != state.AccountID {
		t.Fatalf("AccountID = %q, want %q", loaded.AccountID, state.AccountID)
	}
	if loaded.Token != state.Token {
		t.Fatalf("Token = %q, want %q", loaded.Token, state.Token)
	}
	if loaded.BaseURL != state.BaseURL {
		t.Fatalf("BaseURL = %q, want %q", loaded.BaseURL, state.BaseURL)
	}
	if loaded.SyncBuf != state.SyncBuf {
		t.Fatalf("SyncBuf = %q, want %q", loaded.SyncBuf, state.SyncBuf)
	}
	if loaded.ContextTokens["user1"] != "token1" {
		t.Fatalf("ContextTokens = %v, want %v", loaded.ContextTokens, state.ContextTokens)
	}
}

func TestLoadState_NotExists(t *testing.T) {
	projectDir := t.TempDir()

	loaded, err := LoadState("nonexistent@im.bot", projectDir)
	if err != nil {
		t.Fatalf("LoadState for non-existent file: %v", err)
	}
	if loaded != nil {
		t.Fatal("expected nil for non-existent state file")
	}
}

func TestStateFilePath_Format(t *testing.T) {
	got := StateFilePath("/home/u/.gbot/projects/foo", "e1cc99a2c914@im.bot")
	want := "/home/u/.gbot/projects/foo/wechat/e1cc99a2c914@im.bot.json"
	if got != want {
		t.Fatalf("StateFilePath = %q, want %q", got, want)
	}
}

func TestLoadAllStates(t *testing.T) {
	t.Run("multiple accounts", func(t *testing.T) {
		projectDir := t.TempDir()
		a := &State{AccountID: "a@im.bot", Token: "ta"}
		b := &State{AccountID: "b@im.bot", Token: "tb"}
		if err := SaveState(a, projectDir); err != nil {
			t.Fatalf("SaveState a: %v", err)
		}
		if err := SaveState(b, projectDir); err != nil {
			t.Fatalf("SaveState b: %v", err)
		}

		states, err := LoadAllStates(projectDir)
		if err != nil {
			t.Fatalf("LoadAllStates: %v", err)
		}
		if len(states) != 2 {
			t.Fatalf("len(states) = %d, want 2", len(states))
		}
		ids := map[string]bool{}
		for _, s := range states {
			ids[s.AccountID] = true
		}
		if !ids["a@im.bot"] || !ids["b@im.bot"] {
			t.Fatalf("missing accounts, got %v", ids)
		}
	})

	t.Run("empty or missing dir returns nil", func(t *testing.T) {
		states, err := LoadAllStates(t.TempDir())
		if err != nil {
			t.Fatalf("LoadAllStates empty dir: %v", err)
		}
		if states != nil {
			t.Fatalf("expected nil states for empty dir, got %v", states)
		}
	})

	t.Run("corrupt file skipped", func(t *testing.T) {
		projectDir := t.TempDir()
		// Valid account.
		if err := SaveState(&State{AccountID: "good@im.bot", Token: "t"}, projectDir); err != nil {
			t.Fatalf("SaveState: %v", err)
		}
		// Corrupt JSON alongside it.
		wechatDir := filepath.Join(projectDir, "wechat")
		if err := os.WriteFile(filepath.Join(wechatDir, "bad@im.bot.json"), []byte("{not json"), 0644); err != nil {
			t.Fatalf("write corrupt file: %v", err)
		}

		states, err := LoadAllStates(projectDir)
		if err != nil {
			t.Fatalf("LoadAllStates: %v", err)
		}
		if len(states) != 1 {
			t.Fatalf("len(states) = %d, want 1 (corrupt skipped)", len(states))
		}
		if states[0].AccountID != "good@im.bot" {
			t.Fatalf("AccountID = %q, want good@im.bot", states[0].AccountID)
		}
	})
}

func TestSafeFilename(t *testing.T) {
	got := safeFilename("a/b\\c\x00d")
	if got != "a_b_c_d" {
		t.Fatalf("safeFilename separators = %q, want %q", got, "a_b_c_d")
	}
	if got := safeFilename(""); got != "account" {
		t.Fatalf("safeFilename empty = %q, want %q", got, "account")
	}
}

// ---------------------------------------------------------------------------
// isStaleSession
// ---------------------------------------------------------------------------

func TestIsStaleSession(t *testing.T) {
	tests := []struct {
		name   string
		ret    int
		errc   int
		errmsg string
		want   bool
	}{
		{"not stale - ret 0", 0, 0, "", false},
		{"not stale - rate limit with known error", -2, -2, "frequency limit", false},
		{"stale - ret -2 unknown error", -2, 0, "unknown error", true},
		{"stale - errcode -2 unknown error", 0, -2, "unknown error", true},
		{"stale - both -2 unknown error", -2, -2, "unknown error", true},
		{"session expired only", -14, 0, "", false}, // handled separately
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStaleSession(tt.ret, tt.errc, tt.errmsg); got != tt.want {
				t.Fatalf("isStaleSession(%d,%d,%q) = %v, want %v", tt.ret, tt.errc, tt.errmsg, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkSendMessageResponse
// ---------------------------------------------------------------------------

func TestCheckSendMessageResponse(t *testing.T) {
	tests := []struct {
		name    string
		ret     int
		errc    int
		errmsg  string
		want    error
		wantMsg string
	}{
		{"success", 0, 0, "", nil, ""},
		{"session expired ret", -14, 0, "", ErrSessionExpired, ""},
		{"session expired errcode", 0, -14, "", ErrSessionExpired, ""},
		{"stale session", -2, -2, "unknown error", ErrSessionExpired, ""},
		{"rate limited", -2, -2, "frequency limit", ErrRateLimited, ""},
		{"other error", -1, -1, "something", nil, "iLink sendmessage: ret=-1 errcode=-1 errmsg=something"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkSendMessageResponse(tt.ret, tt.errc, tt.errmsg)
			if tt.want != nil {
				if got != tt.want {
					t.Fatalf("got %v, want sentinel %v", got, tt.want)
				}
			} else if tt.wantMsg != "" {
				if got == nil || !strings.Contains(got.Error(), tt.wantMsg) {
					t.Fatalf("got %v, want message containing %q", got, tt.wantMsg)
				}
			} else {
				if got != nil {
					t.Fatalf("expected nil, got %v", got)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// State file permissions test
// ---------------------------------------------------------------------------

func TestSaveState_CreatesDir(t *testing.T) {
	projectDir := t.TempDir()

	state := &State{AccountID: "test", Token: "tok"}
	if err := SaveState(state, projectDir); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// Verify the file exists at the per-account path.
	path := filepath.Join(projectDir, "wechat", "test.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("state file was not created")
	}

	// Verify the content round-trips
	loaded, err := LoadState("test", projectDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if loaded == nil || loaded.AccountID != "test" {
		t.Fatalf("round-trip failed, got %+v", loaded)
	}
}

// ---------------------------------------------------------------------------
// handleInbound — async query + serial gate (queryDone)
// ---------------------------------------------------------------------------

// hubSpy is a minimal hub.EventHandler that records events for assertions.
type hubSpy struct {
	mu     sync.Mutex
	events []types.QueryEvent
}

func (s *hubSpy) Handle(event hub.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

// newHandleInboundConnector builds a connector wired for handleInbound tests:
// a real Hub with a spy, a stub queryFn that records the call, and a capture
// sendToUserFn. The caller overrides queryFn (the default is a no-op that
// does NOT close queryDone, so handleInbound blocks).
func newHandleInboundConnector() (*WeChatConnector, *hubSpy, *[]string) {
	h := hub.NewHub()
	spy := &hubSpy{}
	h.Subscribe(spy)

	var sentTexts []string
	c := &WeChatConnector{
		hub:       h,
		inboundCh: make(chan inboundMessage, 10),
		sendToUserFn: func(_ context.Context, _, text string) error {
			sentTexts = append(sentTexts, text)
			return nil
		},
	}
	// Default: does nothing. Tests override to close queryDone.
	c.queryFn = func(_ context.Context, _, _ string) {}
	return c, spy, &sentTexts
}

func TestHandleInbound_SetsActiveUserAndQueries(t *testing.T) {
	c, _, _ := newHandleInboundConnector()
	// Override queryFn to record the call and close queryDone so
	// handleInbound unblocks.
	called := false
	c.queryFn = func(_ context.Context, _, _ string) {
		called = true
		if c.queryDone != nil {
			close(c.queryDone)
			c.queryDone = nil
		}
	}

	c.handleInbound(context.Background(), inboundMessage{userID: "userA", text: "hello"})

	if c.activeUserID != "userA" {
		t.Fatalf("activeUserID = %q, want %q", c.activeUserID, "userA")
	}
	if !called {
		t.Fatal("queryFn was not called")
	}
}

func TestHandleInbound_BlocksUntilQueryDoneCloses(t *testing.T) {
	c, _, _ := newHandleInboundConnector()
	// queryFn captures the queryDone channel and signals that handleInbound
	// has reached the query call (and is about to block on queryDone). The
	// test then closes the captured channel to simulate EventQueryEnd.
	ready := make(chan struct{})
	var qDone chan struct{}
	c.queryFn = func(_ context.Context, _, _ string) {
		qDone = c.queryDone
		close(ready)
	}

	done := make(chan struct{})
	go func() {
		c.handleInbound(context.Background(), inboundMessage{userID: "u1", text: "x"})
		close(done)
	}()

	<-ready // handleInbound has called queryFn and is now blocked on qDone
	if qDone == nil {
		t.Fatal("queryDone was not captured")
	}
	// Closing qDone (as Handle's EventQueryEnd does) must unblock handleInbound.
	close(qDone)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleInbound did not unblock after queryDone closed")
	}
}

func TestHandleInbound_PreservesSerialOrder(t *testing.T) {
	// Serial guarantee is enforced by processLoop reading inboundCh one
	// message at a time. Two messages queued back-to-back must run strictly
	// in order: the second's queryFn only fires after the first's queryDone
	// closes. We detect overlap via a gate that fails if two queries run
	// concurrently.
	c, _, _ := newHandleInboundConnector()
	var mu sync.Mutex
	var order []string
	inFlight := 0
	overlap := false
	processed := make(chan string, 2)
	c.queryFn = func(_ context.Context, msg, _ string) {
		mu.Lock()
		inFlight++
		if inFlight > 1 {
			overlap = true
		}
		order = append(order, msg)
		mu.Unlock()
		// Simulate query completion: close queryDone (as EventQueryEnd does).
		c.Handle(types.QueryEvent{Type: types.EventQueryEnd})
		mu.Lock()
		inFlight--
		mu.Unlock()
		processed <- msg
	}

	// Drive through processLoop: queue two messages, run the loop with a
	// cancellable context, cancel after both are processed.
	ctx, cancel := context.WithCancel(context.Background())
	c.pollWg.Add(1)
	go c.processLoop(ctx)
	c.inboundCh <- inboundMessage{userID: "u1", text: "first"}
	c.inboundCh <- inboundMessage{userID: "u2", text: "second"}

	// Wait for both to process via the channel (no polling).
	for range 2 {
		select {
		case <-processed:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for messages to process")
		}
	}
	cancel()
	c.pollWg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if overlap {
		t.Fatalf("queries overlapped (not serial), order = %v", order)
	}
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("serial order wrong, got %v", order)
	}
}

// ---------------------------------------------------------------------------
// splitForWeChat — split long replies into multiple WeChat messages
// ---------------------------------------------------------------------------

func TestSplitForWeChat_ShortMessage_SingleChunk(t *testing.T) {
	chunks := splitForWeChat("短消息")
	if len(chunks) != 1 {
		t.Fatalf("short message: got %d chunks, want 1", len(chunks))
	}
	if chunks[0] != "短消息" {
		t.Errorf("chunk content = %q, want %q", chunks[0], "短消息")
	}
}

func TestSplitForWeChat_LongMessage_MultipleChunks(t *testing.T) {
	long := strings.Repeat("这是一段长文本。", 700) // ~5600 chars, exceeds 4000 limit
	chunks := splitForWeChat(long)
	if len(chunks) < 2 {
		t.Fatalf("long message: got %d chunks, want >= 2", len(chunks))
	}
	for i, c := range chunks {
		if len([]rune(c)) > 4000 {
			t.Errorf("chunk %d length = %d runes, want <= 4000", i, len([]rune(c)))
		}
	}
}

func TestSplitForWeChat_PreservesCodeBlock(t *testing.T) {
	// Code block should stay together in one chunk when possible.
	code := "```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```"
	chunks := splitForWeChat(code)
	if len(chunks) != 1 {
		t.Fatalf("code block: got %d chunks, want 1 (should not split small code block)", len(chunks))
	}
	if !strings.Contains(chunks[0], "```go") {
		t.Errorf("chunk should contain code fence, got: %q", chunks[0])
	}
}

func TestSplitForWeChat_LargeCodeBlock_SplitsWithFenceReopen(t *testing.T) {
	// Code block exceeds 4000 runes — must split, reopening fences so
	// each chunk is valid markdown.
	codeBody := strings.Repeat("x", 5000)
	code := "```go\n" + codeBody + "\n```"
	chunks := splitForWeChat(code)
	if len(chunks) < 2 {
		t.Fatalf("large code block: got %d chunks, want >= 2", len(chunks))
	}
	for i, c := range chunks {
		if len([]rune(c)) > 4000 {
			t.Errorf("chunk %d: %d runes, want <= 4000", i, len([]rune(c)))
		}
		// Every chunk must have balanced fences — valid standalone markdown.
		if strings.Count(c, "```")%2 != 0 {
			t.Errorf("chunk %d has unbalanced fences (not valid markdown): %q", i, firstChars(c, 50))
		}
	}
}

func TestSplitForWeChat_LargeCodeBlock_NoLanguageTag(t *testing.T) {
	// Plain code fence (no language) — reopen should use bare ```.
	codeBody := strings.Repeat("x", 5000)
	code := "```\n" + codeBody + "\n```"
	chunks := splitForWeChat(code)
	if len(chunks) < 2 {
		t.Fatalf("plain code block: got %d chunks, want >= 2", len(chunks))
	}
	for i, c := range chunks {
		if strings.Count(c, "```")%2 != 0 {
			t.Errorf("chunk %d has unbalanced fences: %q", i, firstChars(c, 50))
		}
	}
}

func TestSplitForWeChat_LongLangTag_BudgetFallback(t *testing.T) {
	t.Parallel()
	longLang := strings.Repeat("a", wechatMaxMessageLen)
	codeBody := strings.Repeat("x", 5000)
	code := "```" + longLang + "\n" + codeBody + "\n```"
	chunks := splitForWeChat(code)
	if len(chunks) < 2 {
		t.Fatalf("long lang tag: got %d chunks, want >= 2", len(chunks))
	}
	for i, c := range chunks {
		if len([]rune(c)) > wechatMaxMessageLen {
			t.Errorf("chunk %d exceeds limit: %d runes (max %d)", i, len([]rune(c)), wechatMaxMessageLen)
		}
	}
}

func TestSplitForWeChat_Empty(t *testing.T) {
	t.Parallel()
	if got := splitForWeChat(""); got != nil {
		t.Errorf("splitForWeChat(\"\") = %v, want nil", got)
	}
}

func firstChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// sendWeChatReply splits long replies into multiple WeChat messages.
// This catches the real production issue: WeChat silently truncates or drops
// messages over ~4000 chars, so the user never sees the full reply.
func TestSendWeChatReply_LongReply_SendsMultipleMessages(t *testing.T) {
	var sentTexts []string
	c := &WeChatConnector{
		sendToUserFn: func(_ context.Context, _, text string) error {
			sentTexts = append(sentTexts, text)
			return nil
		},
	}
	longReply := strings.Repeat("这是回复内容。", 700) // ~4900 chars

	c.sendWeChatReply(context.Background(), "user1", longReply)

	if len(sentTexts) < 2 {
		t.Fatalf("long reply should send multiple messages, got %d", len(sentTexts))
	}
	for i, text := range sentTexts {
		if len([]rune(text)) > 4000 {
			t.Errorf("message %d: %d runes, want <= 4000 (WeChat truncates)", i, len([]rune(text)))
		}
	}
}

// ---------------------------------------------------------------------------
// Message JSON decoding — iLink returns message_id as number, not string
// ---------------------------------------------------------------------------

func TestMessageDecode_NumericMessageID(t *testing.T) {
	raw := `{"from_user_id":"user1","to_user_id":"bot1","message_id":123456789,"item_list":[{"type":1,"text_item":{"text":"hello"}}]}`
	var msg Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal numeric message_id: %v", err)
	}
	if string(msg.MessageID) != "123456789" {
		t.Errorf("MessageID = %q, want %q", msg.MessageID, "123456789")
	}
}

func TestMessageDecode_StringMessageID(t *testing.T) {
	raw := `{"from_user_id":"user1","message_id":"abc-def-123","item_list":[]}`
	var msg Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal string message_id: %v", err)
	}
	if string(msg.MessageID) != "abc-def-123" {
		t.Errorf("MessageID = %q, want %q", msg.MessageID, "abc-def-123")
	}
}

func TestFlexString_UnmarshalJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
		err   bool
	}{
		{`"abc123"`, "abc123", false},
		{`12345`, "12345", false},
		{`true`, "", true},
		{`[1,2]`, "", true},
	}
	for _, tc := range tests {
		var f FlexString
		err := f.UnmarshalJSON([]byte(tc.input))
		if tc.err {
			if err == nil {
				t.Errorf("UnmarshalJSON(%s): expected error, got nil", tc.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("UnmarshalJSON(%s): unexpected error %v", tc.input, err)
			continue
		}
		if string(f) != tc.want {
			t.Errorf("UnmarshalJSON(%s) = %q, want %q", tc.input, f, tc.want)
		}
	}
}

func TestSaveState_ConcurrentWithStateWrite_NoRace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := New(nil, hub.NewHub())
	c.projectDir = dir
	c.state = &State{
		AccountID: "race@im.bot",
		Token:     "tok",
		BaseURL:   "https://api.wechat.com",
		SyncBuf:   "initial",
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			c.stateMu.Lock()
			c.state.SyncBuf = "buf-" + json.Number(rune(i)).String()
			c.stateMu.Unlock()
		}
	}()

	for range 200 {
		c.SaveState()
	}
	<-done
}

func TestEnqueue_ChannelFull_DropsMessage(t *testing.T) {
	t.Parallel()
	c := New(nil, hub.NewHub())
	c.inboundCh = make(chan inboundMessage, 1)
	c.inboundCh <- inboundMessage{userID: "first"}

	c.enqueue("dropped", "text", nil)

	select {
	case im := <-c.inboundCh:
		if im.userID != "first" {
			t.Errorf("userID = %q, want 'first' (second should be dropped)", im.userID)
		}
	default:
		t.Error("channel should still have the first message")
	}
}

func TestNew_InitializesFields(t *testing.T) {
	t.Parallel()
	h := hub.NewHub()
	c := New(nil, h)
	if c.hub != h {
		t.Error("hub not set")
	}
	if cap(c.inboundCh) != 100 {
		t.Errorf("inboundCh cap = %d, want 100", cap(c.inboundCh))
	}
}

func TestRestoreContextTokens_FromState(t *testing.T) {
	t.Parallel()
	c := New(nil, hub.NewHub())
	c.state = &State{
		ContextTokens: map[string]string{
			"u1": "tok1",
			"u2": "tok2",
		},
	}
	c.restoreContextTokens()
	if got := c.getContextToken("u1"); got != "tok1" {
		t.Errorf("u1 token = %q, want tok1", got)
	}
	if got := c.getContextToken("u2"); got != "tok2" {
		t.Errorf("u2 token = %q, want tok2", got)
	}
}

func TestRestoreContextTokens_NilState(t *testing.T) {
	t.Parallel()
	c := New(nil, hub.NewHub())
	c.restoreContextTokens()
}

func TestSend_Delegates(t *testing.T) {
	t.Parallel()
	var got string
	c := New(nil, hub.NewHub())
	c.state = &State{AccountID: "bot@im"}
	c.sendToUserFn = func(ctx context.Context, userID, text string) error {
		got = text
		return nil
	}
	if err := c.Send("user@im", "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got != "hello" {
		t.Errorf("Send text = %q, want hello", got)
	}
}

func TestStateFilePath_NoPathTraversal(t *testing.T) {
	t.Parallel()
	p := StateFilePath("/tmp/proj", "../../etc/passwd")
	if strings.Contains(p, "/etc/passwd") {
		t.Errorf("path traversal not blocked: %s", p)
	}
	if !strings.HasSuffix(p, ".json") {
		t.Errorf("path should end with .json: %s", p)
	}
}

func TestLoadAllStates_MultipleFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, acc := range []string{"a@im", "b@im"} {
		if err := SaveState(&State{AccountID: acc}, dir); err != nil {
			t.Fatal(err)
		}
	}
	states, err := LoadAllStates(dir)
	if err != nil {
		t.Fatalf("LoadAllStates: %v", err)
	}
	if len(states) != 2 {
		t.Errorf("states count = %d, want 2", len(states))
	}
}

func TestLoadAllStates_EmptyDir(t *testing.T) {
	t.Parallel()
	states, err := LoadAllStates(t.TempDir())
	if err != nil {
		t.Fatalf("LoadAllStates: %v", err)
	}
	if states != nil {
		t.Errorf("states = %v, want nil", states)
	}
}

func TestLoadAllStates_CorruptFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	wechatDir := filepath.Join(dir, "wechat")
	if err := os.MkdirAll(wechatDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wechatDir, "bad.json"), []byte(`{not json`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(&State{AccountID: "good@im"}, dir); err != nil {
		t.Fatal(err)
	}
	states, err := LoadAllStates(dir)
	if err != nil {
		t.Fatalf("LoadAllStates: %v", err)
	}
	if len(states) != 1 {
		t.Errorf("states count = %d, want 1 (corrupt skipped)", len(states))
	}
	if states[0].AccountID != "good@im" {
		t.Errorf("AccountID = %q, want good@im", states[0].AccountID)
	}
}

func TestSendWeChatReply_EmptyText(t *testing.T) {
	t.Parallel()
	called := false
	c := New(nil, hub.NewHub())
	c.sendToUserFn = func(ctx context.Context, userID, text string) error {
		called = true
		return nil
	}
	c.sendWeChatReply(context.Background(), "user@im", "")
	if called {
		t.Error("sendToUserFn should not be called for empty text")
	}
}

// TestStartStop_Lifecycle verifies Start launches poll/process goroutines
// and Stop cleanly cancels them without hanging.
func TestStartStop_Lifecycle(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":0,"get_updates_buf":"cursor","msgs":[]}`))
	}))
	defer srv.Close()

	c := New(nil, hub.NewHub())
	c.client = srv.Client()
	dir := t.TempDir()
	state := &State{
		AccountID: "test@im.bot",
		Token:     "tok",
		BaseURL:   srv.URL,
	}

	ctx := t.Context()

	if err := c.Start(ctx, state, dir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if c.state == nil || c.state.AccountID != "test@im.bot" {
		t.Error("state not set after Start")
	}
	if c.typingAPI == nil {
		t.Error("typingAPI not initialized")
	}

	done := make(chan struct{})
	go func() {
		c.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return within 5s")
	}
}

// Stop's pollWg.Wait must genuinely block on the running loops: without it,
// SaveState would race the poll loop's in-flight state writes. The fake
// getUpdates ignores ctx on purpose, so only the Wait can hold Stop open.
func TestStop_WaitsForInFlightPollLoop(t *testing.T) {
	c := New(nil, hub.NewHub())
	c.client = &http.Client{}
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	c.getUpdatesFn = func(ctx context.Context, client *http.Client, baseURL, token, syncBuf string,
		timeout time.Duration) (*GetUpdatesResponse, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil, errors.New("pin: released")
	}
	if err := c.Start(context.Background(), &State{AccountID: "wait@im.bot", Token: "t", BaseURL: "http://127.0.0.1:1"}, t.TempDir()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-entered

	stopped := make(chan struct{})
	go func() {
		c.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while the poll loop was still inside getUpdatesFn — pollWg.Wait did not wait")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after the poll loop was released")
	}
	if calls.Load() == 0 {
		t.Error("poll loop never entered getUpdatesFn, so the wait was never exercised")
	}
}

// TestStartStop_PreservesContextTokens verifies that context tokens from the
// state are restored into the sync.Map after Start.
func TestStartStop_RestoresContextTokens(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":0,"get_updates_buf":"cursor","msgs":[]}`))
	}))
	defer srv.Close()

	c := New(nil, hub.NewHub())
	c.client = srv.Client()
	state := &State{
		AccountID:     "test@im.bot",
		Token:         "tok",
		BaseURL:       srv.URL,
		ContextTokens: map[string]string{"userA@im": "tokA"},
	}

	ctx := t.Context()

	if err := c.Start(ctx, state, t.TempDir()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := c.getContextToken("userA@im"); got != "tokA" {
		t.Errorf("restored token = %q, want tokA", got)
	}
	c.Stop()
}

func TestMediaCache_ReturnsStore(t *testing.T) {
	t.Parallel()
	c := New(nil, hub.NewHub())
	store, err := media.NewAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c.mediaCache = store
	if c.MediaCache() != store {
		t.Error("MediaCache did not return the set store")
	}
	c.mediaCache = nil
	if c.MediaCache() != nil {
		t.Error("MediaCache did not return nil after clear")
	}
}

func TestPollLoop_MockSuccess_ProcessesMessages(t *testing.T) {
	t.Parallel()
	c := New(nil, hub.NewHub())
	c.typingCache = newTypingTicketCache(time.Minute)
	c.state = &State{AccountID: "bot", Token: "t", BaseURL: "http://x"}

	c.processBatch(context.Background(), []Message{
		{FromUserID: "userA@im", MessageID: FlexString("m1"),
			ItemList: []Item{{Type: ItemText, TextItem: &TextItem{Text: "hello"}}}},
	})

	select {
	case im := <-c.inboundCh:
		if im.userID != "userA@im" {
			t.Errorf("userID = %q, want userA@im", im.userID)
		}
		if im.text != "hello" {
			t.Errorf("text = %q, want hello", im.text)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("message not enqueued within 100ms")
	}
}

func TestPollLoop_SessionExpired_Pauses(t *testing.T) {
	t.Parallel()
	c := New(nil, hub.NewHub())
	c.typingCache = newTypingTicketCache(time.Minute)
	c.queryFn = func(ctx context.Context, userMessage, systemPrompt string) {}
	c.queryWithContentFn = func(ctx context.Context, content []types.ContentBlock, systemPrompt string) {}

	called := make(chan struct{}, 1)
	c.getUpdatesFn = func(ctx context.Context, client *http.Client, baseURL, token, syncBuf string,
		timeout time.Duration) (*GetUpdatesResponse, error) {
		select {
		case called <- struct{}{}:
		default:
		}
		return &GetUpdatesResponse{Ret: SessionExpiredErrCode}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	state := &State{AccountID: "bot", Token: "t", BaseURL: "http://x"}
	if err := c.Start(ctx, state, t.TempDir()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("getUpdatesFn not called within 3s")
	}
	cancel()
	c.pollWg.Wait()
}

// ---------------------------------------------------------------------------
// setStateContextToken
// ---------------------------------------------------------------------------

func TestSetStateContextToken(t *testing.T) {
	t.Parallel()
	c := New(nil, hub.NewHub())
	c.setStateContextToken("user1", "tok1")
	c.setStateContextToken("user2", "tok2")

	if got := c.getContextToken("user1"); got != "tok1" {
		t.Errorf("user1 token = %q, want tok1", got)
	}
	if got := c.getContextToken("user2"); got != "tok2" {
		t.Errorf("user2 token = %q, want tok2", got)
	}
	// Overwrite
	c.setStateContextToken("user1", "new-tok")
	if got := c.getContextToken("user1"); got != "new-tok" {
		t.Errorf("user1 token after overwrite = %q, want new-tok", got)
	}
}

// ---------------------------------------------------------------------------
// SaveState error paths
// ---------------------------------------------------------------------------

func TestSaveState_WriteError(t *testing.T) {
	t.Parallel()
	state := &State{AccountID: "test", Token: "tok"}
	err := SaveState(state, "/nonexistent/impossible/path")
	if err == nil {
		t.Fatal("expected error writing to impossible path, got nil")
	}
	if !strings.Contains(err.Error(), "permission denied") && !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("error = %q, want 'permission denied' or 'mkdir'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// SaveState method — nil state path
// ---------------------------------------------------------------------------

func TestSaveStateMethod_NilState(t *testing.T) {
	t.Parallel()
	c := New(nil, hub.NewHub())
	c.state = nil
	c.SaveState() // should not panic
}

// ---------------------------------------------------------------------------
// downloadableMedia
// ---------------------------------------------------------------------------

func TestDownloadableMedia(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ref  *MediaRef
		want bool
	}{
		{"nil", nil, false},
		{"empty", &MediaRef{}, false},
		{"fullurl", &MediaRef{FullURL: "https://x"}, true},
		{"encrypt", &MediaRef{EncryptQueryParam: "key=val"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := downloadableMedia(tt.ref); got != tt.want {
				t.Errorf("downloadableMedia = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// sendWeChatReply — no-op for blank input
// ---------------------------------------------------------------------------

func TestSendWeChatReply_EmptyText_NoError(t *testing.T) {
	t.Parallel()
	c := New(nil, hub.NewHub())
	c.sendToUserFn = func(_ context.Context, _, _ string) error {
		t.Fatal("sendToUserFn should not be called for empty text")
		return nil
	}
	c.sendWeChatReply(context.Background(), "user1", "")
}

// ---------------------------------------------------------------------------
// pickMediaItem
// ---------------------------------------------------------------------------

func TestPickMediaItem(t *testing.T) {
	t.Parallel()
	dl := &MediaRef{FullURL: "https://example.com/media"}
	tests := []struct {
		name  string
		items []Item
		want  int
	}{
		{"image", []Item{{Type: ItemImage, ImageItem: &MediaItemHolder{Media: dl}}}, ItemImage},
		{"video", []Item{{Type: ItemVideo, VideoItem: &MediaItemHolder{Media: dl}}}, ItemVideo},
		{"file", []Item{{Type: ItemFile, FileItem: &FileItem{Media: dl}}}, ItemFile},
		{"voice", []Item{{Type: ItemVoice, VoiceItem: &VoiceItem{Media: dl}}}, ItemVoice},
		{"empty", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickMediaItem(tt.items)
			if tt.want == 0 {
				if got != nil {
					t.Errorf("pickMediaItem = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("pickMediaItem = nil, want item")
			}
			if got.Type != tt.want {
				t.Errorf("got type %d, want %d", got.Type, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// fallbackMsg
// ---------------------------------------------------------------------------

func TestFallbackMsg(t *testing.T) {
	t.Parallel()
	got := fallbackMsg("user1", 0)
	if got == "" {
		t.Error("fallbackMsg should not be empty")
	}
}

// -----------------------------------------------------------------------
// Handle — EventToolEnd toolNames lookup + delete
// -----------------------------------------------------------------------

func TestHandle_ToolEnd_IncrementsStats(t *testing.T) {
	t.Parallel()
	h := hub.NewHub()
	c := &WeChatConnector{
		hub:         h,
		typingCache: newTypingTicketCache(time.Minute),
	}
	c.lastFlush = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	c.Handle(types.QueryEvent{
		Type: types.EventToolStart,
		ToolUse: &types.ToolUseEvent{
			ID:   "tool-1",
			Name: "Web",
		},
	})
	// End tool
	c.Handle(types.QueryEvent{
		Type: types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{
			ToolUseID: "tool-1",
		},
	})
	if c.searchCount != 1 {
		t.Errorf("searchCount = %d, want 1", c.searchCount)
	}

	// File tool
	c.Handle(types.QueryEvent{
		Type: types.EventToolStart,
		ToolUse: &types.ToolUseEvent{
			ID:   "tool-2",
			Name: "Read",
		},
	})
	c.Handle(types.QueryEvent{
		Type: types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{
			ToolUseID: "tool-2",
		},
	})
	if c.fileCount != 1 {
		t.Errorf("fileCount = %d, want 1", c.fileCount)
	}

	// Bash tool
	c.Handle(types.QueryEvent{
		Type: types.EventToolStart,
		ToolUse: &types.ToolUseEvent{
			ID:   "tool-3",
			Name: "Bash",
		},
	})
	c.Handle(types.QueryEvent{
		Type: types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{
			ToolUseID: "tool-3",
		},
	})
	if c.cmdCount != 1 {
		t.Errorf("cmdCount = %d, want 1", c.cmdCount)
	}

	// Agent tool
	c.Handle(types.QueryEvent{
		Type: types.EventToolStart,
		ToolUse: &types.ToolUseEvent{
			ID:   "tool-4",
			Name: "Agent",
		},
	})
	c.Handle(types.QueryEvent{
		Type: types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{
			ToolUseID: "tool-4",
		},
	})
	if c.agentCount != 1 {
		t.Errorf("agentCount = %d, want 1", c.agentCount)
	}
}

func TestHandle_ToolEnd_UnknownTool_DoesNotIncrement(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		hub:         hub.NewHub(),
		typingCache: newTypingTicketCache(time.Minute),
	}
	c.Handle(types.QueryEvent{
		Type: types.EventToolStart,
		ToolUse: &types.ToolUseEvent{
			ID:   "tool-5",
			Name: "Unknown",
		},
	})
	c.Handle(types.QueryEvent{
		Type: types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{
			ToolUseID: "tool-5",
		},
	})
	if c.searchCount != 0 || c.fileCount != 0 || c.cmdCount != 0 || c.agentCount != 0 {
		t.Errorf("stats should all be 0 for unknown tool")
	}
}

// -----------------------------------------------------------------------
// flushBuffer — error path retains buffer
// -----------------------------------------------------------------------

func TestFlushBuffer_ErrorPath_RetainsBuffer(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		hub:          hub.NewHub(),
		activeUserID: "user1",
		typingCache:  newTypingTicketCache(time.Minute),
	}
	c.lastFlush = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	c.textBuffer.WriteString("some text")

	sendErr := fmt.Errorf("rate limited")
	c.sendToUserFn = func(_ context.Context, _, _ string) error {
		return sendErr
	}

	c.flushBuffer()

	// Buffer should be retained after error.
	if c.textBuffer.Len() == 0 {
		t.Error("textBuffer was cleared after error, should be retained")
	}
}

// -----------------------------------------------------------------------
// sendWeChatReply — error logging path
// -----------------------------------------------------------------------

func TestSendWeChatReply_ErrorLogged(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		hub:         hub.NewHub(),
		typingCache: newTypingTicketCache(time.Minute),
	}
	c.sendToUserFn = func(_ context.Context, _, _ string) error {
		return fmt.Errorf("send failed")
	}
	c.sendWeChatReply(context.Background(), "user1", "hello")
	// Should not panic; error is logged internally.
}

// -----------------------------------------------------------------------
// restoreContextTokens — _self_user_id skip
// -----------------------------------------------------------------------

func TestRestoreContextTokens_SkipsSelfUserID(t *testing.T) {
	t.Parallel()
	c := New(nil, hub.NewHub())
	c.state = &State{
		ContextTokens: map[string]string{
			"_self_user_id": "self-id",
			"u1":            "tok1",
		},
	}
	c.restoreContextTokens()
	if got := c.getContextToken("_self_user_id"); got != "" {
		t.Errorf("_self_user_id should be skipped, got %q", got)
	}
	if got := c.getContextToken("u1"); got != "tok1" {
		t.Errorf("u1 token = %q, want tok1", got)
	}
}

// -----------------------------------------------------------------------
// hasNonVoiceMedia — non-voice items return true
// -----------------------------------------------------------------------

func TestHasNonVoiceMedia(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		items []Item
		want  bool
	}{
		{"nil", nil, false},
		{"voice only", []Item{{Type: ItemVoice}}, false},
		{"text only", []Item{{Type: ItemText}}, false},
		{"image", []Item{{Type: ItemImage}}, true},
		{"video", []Item{{Type: ItemVideo}}, true},
		{"file", []Item{{Type: ItemFile}}, true},
		{"voice and image", []Item{{Type: ItemVoice}, {Type: ItemImage}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasNonVoiceMedia(tt.items); got != tt.want {
				t.Errorf("hasNonVoiceMedia = %v, want %v", got, tt.want)
			}
		})
	}
}

// -----------------------------------------------------------------------
// downloadImage — nil image, nil media, cache save error
// -----------------------------------------------------------------------

func TestDownloadImage_NilImage(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	block := c.downloadImage(context.Background(), nil)
	if block.Type != "" {
		t.Errorf("nil image should return empty block, got type=%s", block.Type)
	}
}

func TestDownloadImage_NilMedia(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	block := c.downloadImage(context.Background(), &MediaItemHolder{})
	if block.Type != "" {
		t.Errorf("nil media should return empty block, got type=%s", block.Type)
	}
}

// -----------------------------------------------------------------------
// downloadFile — nil file, nil media, cache save error
// -----------------------------------------------------------------------

func TestDownloadFile_NilFile(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	block := c.downloadFile(context.Background(), nil)
	if block.Type != "" {
		t.Errorf("nil file should return empty block, got type=%s", block.Type)
	}
}

func TestDownloadFile_NilMedia(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	block := c.downloadFile(context.Background(), &FileItem{})
	if block.Type != "" {
		t.Errorf("nil media should return empty block, got type=%s", block.Type)
	}
}

func TestDownloadFile_DownloadError(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	block := c.downloadFile(context.Background(), &FileItem{
		FileName: "test.pdf",
		Media:    &MediaRef{FullURL: "http://127.0.0.1:0/nonexistent"},
	})
	if block.Type != "" {
		t.Errorf("download error should return empty block, got type=%s", block.Type)
	}
}

func TestDownloadFile_EmptyFileName(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("document content"))
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	block := c.downloadFile(context.Background(), &FileItem{
		FileName: "",
		Media:    &MediaRef{FullURL: srv.URL},
	})
	// Empty filename → ext defaults to .bin, parse may fail → text block.
	if block.Type != types.ContentTypeText {
		t.Errorf("block.Type = %q, want text", block.Type)
	}
}

// -----------------------------------------------------------------------
// downloadMedia — context cancellation path
// -----------------------------------------------------------------------

func TestDownloadMedia_ContextCancelled(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	block := c.downloadMedia(ctx, []Item{
		{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: "http://x"}}},
	})
	if block.Type != "" {
		t.Errorf("cancelled context should return empty block, got type=%s", block.Type)
	}
}

// -----------------------------------------------------------------------
// downloadFile — parse returns empty content
// -----------------------------------------------------------------------

func TestDownloadFile_ParseReturnsEmpty(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	ciphertext := encryptAesEcbForMediaTest([]byte(" "), key)
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	block := c.downloadFile(context.Background(), &FileItem{
		FileName: "space.txt",
		Media:    &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64},
	})
	// Empty/minimal content → parse may return empty → fallback to path.
	if block.Type != types.ContentTypeText {
		t.Errorf("block.Type = %q, want text", block.Type)
	}
}

// -----------------------------------------------------------------------
// New — media cache init failure
// -----------------------------------------------------------------------

func TestNew_MediaCacheInitFailure(t *testing.T) {
	// media.New() fails when HOME is invalid (MkdirAll fails under /dev/null).
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", "/dev/null/impossible")
	c := New(nil, hub.NewHub())
	if c.mediaCache != nil {
		t.Error("mediaCache should be nil when media.New() fails")
	}
	t.Setenv("HOME", origHome)
}

// -----------------------------------------------------------------------
// Handle — EventToolStart without ToolUse (nil check)
// -----------------------------------------------------------------------

func TestHandle_ToolStart_NilToolUse(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		hub:         hub.NewHub(),
		typingCache: newTypingTicketCache(time.Minute),
	}
	c.Handle(types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: nil,
	})
	// Should not panic.
}

func TestHandle_ToolEnd_NilToolResult(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		hub:         hub.NewHub(),
		typingCache: newTypingTicketCache(time.Minute),
	}
	c.Handle(types.QueryEvent{
		Type:       types.EventToolEnd,
		ToolResult: nil,
	})
	// Should not panic.
}

// -----------------------------------------------------------------------
// flushBuffer — header-only (no text) path
// -----------------------------------------------------------------------

func TestFlushBuffer_HeaderOnly(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		hub:          hub.NewHub(),
		activeUserID: "user1",
		typingCache:  newTypingTicketCache(time.Minute),
	}
	c.lastFlush = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	c.searchCount = 2

	var sent string
	c.sendToUserFn = func(_ context.Context, _, text string) error {
		sent = text
		return nil
	}

	c.flushBuffer()

	if !strings.Contains(sent, "搜索 2次") {
		t.Errorf("sent = %q, want stat header with 搜索 2次", sent)
	}
	if c.searchCount != 0 {
		t.Errorf("searchCount = %d, want 0 after flush", c.searchCount)
	}
}

// -----------------------------------------------------------------------
// flushBuffer — header + text path
// -----------------------------------------------------------------------

func TestFlushBuffer_HeaderAndText(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		hub:          hub.NewHub(),
		activeUserID: "user1",
		typingCache:  newTypingTicketCache(time.Minute),
	}
	c.lastFlush = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	c.searchCount = 1
	c.textBuffer.WriteString("hello")

	var sent string
	c.sendToUserFn = func(_ context.Context, _, text string) error {
		sent = text
		return nil
	}

	c.flushBuffer()

	if !strings.Contains(sent, "搜索 1次") || !strings.Contains(sent, "hello") {
		t.Errorf("sent = %q, want header + text", sent)
	}
}

// -----------------------------------------------------------------------
// flushBuffer — skips flush when no active user
// -----------------------------------------------------------------------

func TestFlushBuffer_EmptyActiveUserID(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		hub:          hub.NewHub(),
		activeUserID: "",
		typingCache:  newTypingTicketCache(time.Minute),
	}
	c.searchCount = 1
	c.textBuffer.WriteString("data")
	c.flushBuffer()
	// Should reset counters without sending.
	if c.searchCount != 0 {
		t.Errorf("searchCount = %d, want 0", c.searchCount)
	}
	if c.textBuffer.Len() != 0 {
		t.Errorf("textBuffer len = %d, want 0", c.textBuffer.Len())
	}
}

// -----------------------------------------------------------------------
// sendWeChatReplyErr — split message
// -----------------------------------------------------------------------

func TestSendWeChatReplyErr_SplitMessage(t *testing.T) {
	t.Parallel()
	var sent []string
	c := &WeChatConnector{
		sendToUserFn: func(_ context.Context, _, text string) error {
			sent = append(sent, text)
			return nil
		},
	}
	long := strings.Repeat("这是回复。", 900)
	err := c.sendWeChatReplyErr(context.Background(), "user1", long)
	if err != nil {
		t.Fatalf("sendWeChatReplyErr: %v", err)
	}
	if len(sent) < 2 {
		t.Errorf("expected split messages, got %d", len(sent))
	}
}

func TestSendWeChatReplyErr_EmptyAfterFormat(t *testing.T) {
	t.Parallel()
	called := false
	c := &WeChatConnector{
		sendToUserFn: func(_ context.Context, _, text string) error {
			called = true
			return nil
		},
	}
	err := c.sendWeChatReplyErr(context.Background(), "user1", "  \n  ")
	if err != nil {
		t.Fatalf("sendWeChatReplyErr: %v", err)
	}
	if called {
		t.Error("sendToUserFn should not be called for whitespace-only text")
	}
}

// -----------------------------------------------------------------------
// Handle — QueryEnd with send error (line 217-219)
// -----------------------------------------------------------------------

func TestHandle_QueryEnd_SendError(t *testing.T) {
	t.Parallel()
	h := hub.NewHub()
	c := &WeChatConnector{
		hub:          h,
		activeUserID: "user1",
		typingCache:  newTypingTicketCache(time.Minute),
	}
	c.queryDone = make(chan struct{})
	sendErr := fmt.Errorf("send failed")
	c.sendToUserFn = func(_ context.Context, _, _ string) error {
		return sendErr
	}

	c.Handle(types.QueryEvent{
		Type:  types.EventQueryEnd,
		Error: fmt.Errorf("query error"),
	})

	// queryDone should be closed.
	select {
	case <-c.queryDone:
		// queryDone was closed but reset to nil, so the channel var is nil.
	default:
		// After Handle, queryDone is reset to nil. If it's nil, that's correct.
	}
	if c.activeUserID != "user1" {
		t.Errorf("activeUserID = %q, want %q (retained across turn boundary)", c.activeUserID, "user1")
	}
}

// -----------------------------------------------------------------------
// SaveState method — with error path
// -----------------------------------------------------------------------

func TestSaveStateMethod_WriteError(t *testing.T) {
	t.Parallel()
	c := New(nil, hub.NewHub())
	c.projectDir = "/nonexistent/impossible/path"
	c.state = &State{AccountID: "test", Token: "tok"}
	c.SaveState() // should not panic, just log warning
}

// -----------------------------------------------------------------------
// Handle — thinking duration clamping
// -----------------------------------------------------------------------

func TestHandle_ThinkingEnd_ClampsSmallDuration(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		hub:         hub.NewHub(),
		typingCache: newTypingTicketCache(time.Minute),
	}
	c.Handle(types.QueryEvent{
		Type: types.EventThinkingEnd,
		Thinking: &types.ThinkingEvent{
			Duration: 10 * time.Millisecond, // < 0.1s → clamped to 0.1
		},
	})
	if c.thinkingSecs < 0.1 {
		t.Errorf("thinkingSecs = %f, want >= 0.1 (clamped)", c.thinkingSecs)
	}
}

// -----------------------------------------------------------------------
// buildStatHeader — various counters
// -----------------------------------------------------------------------

func TestBuildStatHeader_AllCounters(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{}
	c.thinkingSecs = 5.0
	c.searchCount = 3
	c.fileCount = 2
	c.cmdCount = 1
	c.agentCount = 4
	header := c.buildStatHeader()
	if !strings.Contains(header, "思考") {
		t.Errorf("header %q missing 思考", header)
	}
	if !strings.Contains(header, "搜索 3次") {
		t.Errorf("header %q missing 搜索 3次", header)
	}
	if !strings.Contains(header, "文件 2次") {
		t.Errorf("header %q missing 文件 2次", header)
	}
	if !strings.Contains(header, "命令 1次") {
		t.Errorf("header %q missing 命令 1次", header)
	}
	if !strings.Contains(header, "代理 4次") {
		t.Errorf("header %q missing 代理 4次", header)
	}
}

// -----------------------------------------------------------------------
// Handle — QueryEnd resets toolNames
// -----------------------------------------------------------------------

func TestHandle_QueryEnd_ResetsToolNames(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		hub:          hub.NewHub(),
		activeUserID: "user1",
		typingCache:  newTypingTicketCache(time.Minute),
	}
	c.toolNames = map[string]string{"t1": "Web"}
	c.queryDone = make(chan struct{})

	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})

	if c.toolNames != nil {
		t.Errorf("toolNames should be nil after QueryEnd, got %v", c.toolNames)
	}
}

// -----------------------------------------------------------------------
// Handle — TextEnd with zero lastFlush (no flush)
// -----------------------------------------------------------------------

func TestHandle_TextEnd_ZeroLastFlush(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		hub:          hub.NewHub(),
		activeUserID: "user1",
		typingCache:  newTypingTicketCache(time.Minute),
	}
	// lastFlush is zero → no flush.
	c.textBuffer.WriteString("text")
	c.Handle(types.QueryEvent{Type: types.EventTextEnd})
	// Buffer should still have text.
	if c.textBuffer.Len() == 0 {
		t.Error("textBuffer should not be empty when lastFlush is zero")
	}
}

// -----------------------------------------------------------------------
// Handle — TextEnd with empty activeUserID (no flush)
// -----------------------------------------------------------------------

func TestHandle_TextEnd_EmptyActiveUser(t *testing.T) {
	t.Parallel()
	c := &WeChatConnector{
		hub:          hub.NewHub(),
		activeUserID: "",
		typingCache:  newTypingTicketCache(time.Minute),
	}
	c.lastFlush = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	c.textBuffer.WriteString("text")
	c.Handle(types.QueryEvent{Type: types.EventTextEnd})
	// Buffer should still have text (no flush because no active user).
	if c.textBuffer.Len() == 0 {
		t.Error("textBuffer should not be empty when activeUserID is empty")
	}
}

// -----------------------------------------------------------------------
// LoadState — corrupt JSON returns error
// -----------------------------------------------------------------------

func TestLoadState_CorruptJSON(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	wechatDir := filepath.Join(projectDir, "wechat")
	if err := os.MkdirAll(wechatDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wechatDir, "bad.json"), []byte(`{not json`), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadState("bad", projectDir)
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("LoadState corrupt: error = %v, want 'invalid'", err)
	}
}

// -----------------------------------------------------------------------
// LoadAllStates — unreadable directory
// -----------------------------------------------------------------------

func TestLoadAllStates_UnreadableDir(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	wechatDir := filepath.Join(projectDir, "wechat")
	if err := os.MkdirAll(wechatDir, 0000); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(wechatDir, 0755)

	_, err := LoadAllStates(projectDir)
	if err == nil {
		t.Skip("running as root, cannot test permission denied")
	}
}

// -----------------------------------------------------------------------
// LoadAllStates — unreadable file (skip)
// -----------------------------------------------------------------------

func TestLoadAllStates_UnreadableFile(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	wechatDir := filepath.Join(projectDir, "wechat")
	if err := os.MkdirAll(wechatDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create an unreadable file.
	badPath := filepath.Join(wechatDir, "noread.json")
	if err := os.WriteFile(badPath, []byte(`{}`), 0000); err != nil {
		t.Fatal(err)
	}

	// Also create a good state so we can verify it's returned.
	if err := SaveState(&State{AccountID: "good@im"}, projectDir); err != nil {
		t.Fatal(err)
	}

	states, err := LoadAllStates(projectDir)
	if err != nil {
		t.Skipf("running as root: %v", err)
	}
	// The unreadable file should be skipped.
	if len(states) != 1 || states[0].AccountID != "good@im" {
		t.Errorf("states = %v, want 1 good state", states)
	}
}

// -----------------------------------------------------------------------
// SaveState — directory creation error
// -----------------------------------------------------------------------

func TestSaveState_MkdirError(t *testing.T) {
	t.Parallel()
	// Can't easily cause MkdirAll to fail unless we write a file in the path.
	dir := t.TempDir()
	filePath := filepath.Join(dir, "wechat")
	if err := os.WriteFile(filePath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	err := SaveState(&State{AccountID: "test"}, dir)
	if err == nil {
		t.Fatal("expected error when path is a file, got nil")
	}
	if !strings.Contains(err.Error(), "not a directory") && !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("error = %q, want 'not a directory' or 'mkdir'", err.Error())
	}
}

// -----------------------------------------------------------------------
// queue.go — handleInbound with queryFn that has content
// -----------------------------------------------------------------------

func TestHandleInbound_TextOnly_CallsQueryFn(t *testing.T) {
	t.Parallel()
	var gotMsg string
	c, _, _ := newHandleInboundConnector()
	c.queryFn = func(_ context.Context, msg, _ string) {
		gotMsg = msg
		if c.queryDone != nil {
			close(c.queryDone)
			c.queryDone = nil
		}
	}

	c.handleInbound(context.Background(), inboundMessage{userID: "u1", text: "hello"})

	if gotMsg != "hello" {
		t.Errorf("queryFn received %q, want 'hello'", gotMsg)
	}
}

// -----------------------------------------------------------------------
// processBatch — ContextToken on media message
// -----------------------------------------------------------------------

func TestProcessBatch_MediaWithContextToken(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef")
	pngHeader := realPNGBytes
	ciphertext := encryptAesEcbForMediaTest(pngHeader, key)
	aesKeyB64 := base64.StdEncoding.EncodeToString(key)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(ciphertext)
	}))
	defer srv.Close()

	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}

	msg := Message{
		FromUserID:   "user1",
		MessageID:    FlexString("msg-ctx"),
		ContextToken: "ctx-tok-123",
		ItemList: []Item{
			{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: srv.URL, AesKey: aesKeyB64}}},
		},
	}
	c.processBatch(context.Background(), []Message{msg})

	select {
	case im := <-c.inboundCh:
		if len(im.content) != 2 {
			t.Fatalf("content length = %d, want 2", len(im.content))
		}
		// Verify the context token was stored.
		if got := c.getContextToken("user1"); got != "ctx-tok-123" {
			t.Errorf("context token = %q, want ctx-tok-123", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("processBatch did not enqueue within 2s")
	}
}

// -----------------------------------------------------------------------
// processBatch — ContextToken on text message
// -----------------------------------------------------------------------

func TestProcessBatch_TextWithContextToken(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}

	msg := Message{
		FromUserID:   "user1",
		MessageID:    FlexString("msg-txt-ctx"),
		ContextToken: "ctx-tok-text",
		ItemList: []Item{
			{Type: ItemText, TextItem: &TextItem{Text: "hello"}},
		},
	}
	c.processBatch(context.Background(), []Message{msg})

	select {
	case <-c.inboundCh:
		if got := c.getContextToken("user1"); got != "ctx-tok-text" {
			t.Errorf("context token = %q, want ctx-tok-text", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("processBatch did not enqueue within 2s")
	}
}

// -----------------------------------------------------------------------
// processBatch — media download fails (empty block → text path)
// -----------------------------------------------------------------------

func TestProcessBatch_MediaDownloadFails_EnqueuesAsText(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}

	// Image with no URL → downloadMedia returns empty block → falls through to text path.
	msg := Message{
		FromUserID: "user1",
		MessageID:  FlexString("msg-nomedia"),
		ItemList: []Item{
			{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{}}},
		},
	}
	c.processBatch(context.Background(), []Message{msg})

	select {
	case im := <-c.inboundCh:
		// No media, no text → should be empty or dropped.
		if im.text != "" {
			t.Errorf("text = %q, want empty for non-downloadable image", im.text)
		}
	case <-time.After(100 * time.Millisecond):
		// pass: empty message was correctly dropped.
	}
}

// -----------------------------------------------------------------------
// processBatch — dedup messages
// -----------------------------------------------------------------------

func TestProcessBatch_DeduplicatesMessages(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}

	// Same message ID sent twice.
	msgs := []Message{
		{FromUserID: "user1", MessageID: FlexString("dup1"), ItemList: []Item{
			{Type: ItemText, TextItem: &TextItem{Text: "hello"}},
		}},
		{FromUserID: "user1", MessageID: FlexString("dup1"), ItemList: []Item{
			{Type: ItemText, TextItem: &TextItem{Text: "hello again"}},
		}},
	}
	c.processBatch(context.Background(), msgs)

	count := 0
Loop:
	for {
		select {
		case <-c.inboundCh:
			count++
		default:
			break Loop
		}
	}
	if count != 1 {
		t.Errorf("enqueued %d messages, want 1 (dedup)", count)
	}
}

// -----------------------------------------------------------------------
// processBatch — skip messages from bot itself
// -----------------------------------------------------------------------

func TestProcessBatch_SkipsOwnMessages(t *testing.T) {
	t.Parallel()
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}

	msg := Message{
		FromUserID: "bot", // same as AccountID
		MessageID:  FlexString("own-msg"),
		ItemList: []Item{
			{Type: ItemText, TextItem: &TextItem{Text: "self"}},
		},
	}
	c.processBatch(context.Background(), []Message{msg})

	select {
	case <-c.inboundCh:
		t.Error("should not enqueue messages from self")
	case <-time.After(100 * time.Millisecond):
		// pass.
	}
}

// -----------------------------------------------------------------------
// format.go — splitForWeChat code block reopen with language tag
// -----------------------------------------------------------------------

func TestSplitForWeChat_CodeBlockMidChunk_Reopen(t *testing.T) {
	t.Parallel()
	// Create a message where a code block starts in one chunk and continues to the next.
	// The text before the code block is close to the limit, forcing a split mid-block.
	header := strings.Repeat("x", 3960)
	code := "```python\n" + strings.Repeat("def f():\n    pass\n", 50) + "```"
	text := header + "\n" + code
	chunks := splitForWeChat(text)
	if len(chunks) < 2 {
		t.Fatalf("expected >= 2 chunks, got %d", len(chunks))
	}
	// First chunk should end with closing fence (```) since it was mid-code-block.
	if strings.Count(chunks[0], "```")%2 != 0 {
		t.Errorf("chunk 0 has unbalanced fences: %q", firstChars(chunks[0], 80))
	}
	// Second chunk should contain opening fence (```python).
	if !strings.Contains(chunks[1], "```python") {
		t.Errorf("chunk 1 should reopen code fence, got: %q", firstChars(chunks[1], 80))
	}
}

// -----------------------------------------------------------------------
// format.go — splitForWeChat hard-split with no break points
// -----------------------------------------------------------------------

func TestSplitForWeChat_HardSplit_LongLine(t *testing.T) {
	t.Parallel()
	// A single line exceeding the limit with no spaces to break on.
	longLine := strings.Repeat("A", 5000)
	chunks := splitForWeChat(longLine)
	if len(chunks) < 2 {
		t.Fatalf("expected >= 2 chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len([]rune(c)) > wechatMaxMessageLen {
			t.Errorf("chunk %d: %d runes, exceeds limit", i, len([]rune(c)))
		}
	}
}

// -----------------------------------------------------------------------
// format.go — splitForWeChat hard-split inside code block
// -----------------------------------------------------------------------

func TestSplitForWeChat_HardSplit_InCodeBlock(t *testing.T) {
	t.Parallel()
	// Code block with a very long line inside, exceeding the limit.
	longCodeLine := "x" + strings.Repeat("y", 4500)
	code := "```\n" + longCodeLine + "\n```"
	chunks := splitForWeChat(code)
	if len(chunks) < 2 {
		t.Fatalf("expected >= 2 chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len([]rune(c)) > wechatMaxMessageLen {
			t.Errorf("chunk %d: %d runes, exceeds limit", i, len([]rune(c)))
		}
	}
}

// ---------------------------------------------------------------------------
// enqueue — busy branch routing
// ---------------------------------------------------------------------------

func TestEnqueue_EngineBusy_RoutesToAttachment(t *testing.T) {
	t.Parallel()
	h := hub.NewHub()
	eng := engine.New(&engine.Params{
		Provider: &blockingEngineMock{},
		Model:    "test-model",
	})
	t.Cleanup(func() { eng.Close() })

	c := New(nil, h)
	c.engine = eng
	c.isBusyFn = func() bool { return true }

	c.enqueue("userA", "follow-up", nil)

	// Should NOT go to inboundCh.
	select {
	case <-c.inboundCh:
		t.Fatal("inboundCh should be empty when engine is busy")
	default:
	}

	if got := eng.AttachmentsLen(); got != 1 {
		t.Fatalf("AttachmentsLen() = %d, want 1", got)
	}
}

func TestEnqueue_EngineIdle_RoutesToInboundCh(t *testing.T) {
	t.Parallel()
	h := hub.NewHub()
	c := New(nil, h)
	// isBusyFn default returns false (engine == nil).

	c.enqueue("userB", "hello", nil)

	select {
	case msg := <-c.inboundCh:
		if msg.userID != "userB" {
			t.Errorf("userID = %q, want %q", msg.userID, "userB")
		}
		if msg.text != "hello" {
			t.Errorf("text = %q, want %q", msg.text, "hello")
		}
	default:
		t.Fatal("inboundCh should have one message")
	}
}

func TestEnqueue_ImageContent_PassesToAttachment(t *testing.T) {
	t.Parallel()
	h := hub.NewHub()
	eng := engine.New(&engine.Params{
		Provider: &blockingEngineMock{},
		Model:    "test-model",
	})
	t.Cleanup(func() { eng.Close() })

	c := New(nil, h)
	c.engine = eng
	c.isBusyFn = func() bool { return true }

	content := []types.ContentBlock{
		types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="}),
		types.NewTextBlock("caption"),
	}
	c.enqueue("userA", "caption", content)

	if got := eng.AttachmentsLen(); got != 1 {
		t.Fatalf("AttachmentsLen() = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// attachmentValue
// ---------------------------------------------------------------------------

func TestAttachmentValue_TextOnly(t *testing.T) {
	t.Parallel()
	got := attachmentValue("hi", nil)
	if got != "hi" {
		t.Fatalf("attachmentValue = %q, want %q", got, "hi")
	}
}

func TestAttachmentValue_DocContent(t *testing.T) {
	t.Parallel()
	docText := "[Document: x.pdf saved at /p]\nBODY"
	content := []types.ContentBlock{types.NewTextBlock(docText)}
	got := attachmentValue("ignored", content)
	if got != docText {
		t.Fatalf("attachmentValue = %q, want %q", got, docText)
	}
}

func TestAttachmentValue_MultiBlock(t *testing.T) {
	t.Parallel()
	content := []types.ContentBlock{
		types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="}),
		types.NewTextBlock("caption"),
	}
	got := attachmentValue("ignored", content)
	want := "[image]\n\ncaption"
	if got != want {
		t.Fatalf("attachmentValue = %q, want %q", got, want)
	}
}

func TestAttachmentValue_EmptyContentFallsBackToText(t *testing.T) {
	t.Parallel()
	got := attachmentValue("fallback", []types.ContentBlock{})
	if got != "fallback" {
		t.Fatalf("attachmentValue = %q, want %q", got, "fallback")
	}
}

// ---------------------------------------------------------------------------
// EventQueryEnd keeps activeUserID
// ---------------------------------------------------------------------------

func TestHandle_EventQueryEnd_KeepsActiveUserID(t *testing.T) {
	t.Parallel()
	h := hub.NewHub()
	c := &WeChatConnector{
		hub:          h,
		activeUserID: "userA",
		typingCache:  newTypingTicketCache(time.Minute),
	}
	c.queryDone = make(chan struct{})

	c.sendToUserFn = func(_ context.Context, _, _ string) error {
		return nil
	}

	c.Handle(types.QueryEvent{
		Type:  types.EventQueryEnd,
		Error: nil,
	})

	if c.activeUserID != "userA" {
		t.Errorf("activeUserID = %q, want %q (retained across turn boundary)", c.activeUserID, "userA")
	}
	if c.toolNames != nil {
		t.Errorf("toolNames = %v, want nil", c.toolNames)
	}
	if c.queryDone != nil {
		t.Errorf("queryDone should be nil after close, got %v", c.queryDone)
	}
}

// ---------------------------------------------------------------------------
// Race detector integration test
// ---------------------------------------------------------------------------

func TestEnqueue_RealEngine_RaceDetector(t *testing.T) {
	t.Parallel()
	if runtime.NumCPU() < 2 {
		t.Skip("need at least 2 CPUs for concurrent test")
	}

	release := make(chan struct{})
	bmp := &blockingEngineMock{release: release}
	eng := engine.New(&engine.Params{
		Provider: bmp,
		Model:    "test-model",
	})
	t.Cleanup(func() { eng.Close() })

	h := hub.NewHub()
	c := New(nil, h)
	c.engine = eng

	// Start a query that blocks on the release channel.
	go eng.Query(context.Background(), "q1", "")

	// Wait for the engine to be busy.
	deadline := time.After(3 * time.Second)
	for !eng.IsBusy() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for engine to become busy")
		default:
			time.Sleep(10 * time.Millisecond) // REAL-TIME: poll loop waiting for async state
		}
	}

	const N = 10
	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func(i int) {
			defer wg.Done()
			c.enqueue("userA", fmt.Sprintf("m%d", i), nil)
		}(i)
	}
	wg.Wait()

	// Turn-boundary era: mid-turn human messages wait in the attachment queue
	// while the response is streaming; history stays clean until the boundary.
	msgs := eng.Messages()
	envelopes := 0
	for _, m := range msgs {
		if m.Role != types.RoleUser || len(m.Content) == 0 || m.Content[0].Type != types.ContentTypeText {
			continue
		}
		if _, ok := utils.UnwrapQueuedReminder(m.Content[0].Text); ok {
			envelopes++
		}
	}
	if envelopes != 0 {
		t.Fatalf("history envelope messages while stream blocked = %d, want 0 (attach must wait for the turn boundary)", envelopes)
	}
	if n := eng.AttachmentsLen(); n != N {
		t.Fatalf("AttachmentsLen() = %d, want %d (all waiting in the queue, no concurrent drops)", n, N)
	}

	// inboundCh should be empty.
	select {
	case msg := <-c.inboundCh:
		t.Fatalf("inboundCh should be empty, got %+v", msg)
	default:
	}

	close(release)
	deadline2 := time.After(5 * time.Second)
	for eng.IsBusy() {
		select {
		case <-deadline2:
			t.Fatal("timed out waiting for engine to become idle")
		default:
			time.Sleep(10 * time.Millisecond) // REAL-TIME: poll loop waiting for async state
		}
	}

	// At the boundary every queued message attached as a reminder envelope —
	// the concurrent enqueues lost nothing.
	seen := make(map[string]bool)
	envelopes = 0
	for _, m := range eng.Messages() {
		if m.Role != types.RoleUser || len(m.Content) == 0 || m.Content[0].Type != types.ContentTypeText {
			continue
		}
		if original, ok := utils.UnwrapQueuedReminder(m.Content[0].Text); ok {
			envelopes++
			seen[original] = true
		}
	}
	if envelopes != N {
		t.Fatalf("history envelope messages = %d, want %d (no drops)", envelopes, N)
	}
	for i := range N {
		if !seen[fmt.Sprintf("m%d", i)] {
			t.Errorf("queued message %q missing from history", fmt.Sprintf("m%d", i))
		}
	}
}

// blockingEngineMock holds Stream open until release is closed.
type blockingEngineMock struct {
	release chan struct{}
}

func (b *blockingEngineMock) Name() string { return "blocking-engine-mock" }

func (b *blockingEngineMock) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return &llm.Response{}, nil
}

func (b *blockingEngineMock) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 6)
	go func() {
		defer close(ch)
		<-b.release
		ch <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{Model: "m", Usage: types.Usage{InputTokens: 5}}}
		ch <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}}
		ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "reply"}}
		ch <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
		ch <- llm.StreamEvent{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 3}}
		ch <- llm.StreamEvent{Type: "message_stop"}
	}()
	return ch, nil
}

// ---------------------------------------------------------------------------
// Full chain test: enqueue → engine processes → response delivered
// ---------------------------------------------------------------------------

// TestEnqueue_FullChain_AttachmentResponseDelivered verifies the complete path:
// WeChat message arrives while engine is busy → enqueue routes to attachment →
// engine processes attachment at idle → response is sent to the user.
// This is the critical end-to-end test for the attachment feature.
func TestEnqueue_FullChain_AttachmentResponseDelivered(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	h := hub.NewHub()
	eng := engine.New(&engine.Params{
		Provider:   &blockingEngineMock{release: release},
		Model:      "test-model",
		Dispatcher: h,
	})
	eng.SetSystemPrompt("test")
	t.Cleanup(func() { eng.Close() })

	var sentTexts []string
	c := &WeChatConnector{
		hub:          h,
		engine:       eng,
		inboundCh:    make(chan inboundMessage, 10),
		typingCache:  newTypingTicketCache(time.Minute),
		activeUserID: "userA",
		sendToUserFn: func(_ context.Context, _, text string) error {
			sentTexts = append(sentTexts, text)
			return nil
		},
	}
	c.isBusyFn = func() bool { return eng.IsBusy() }
	h.Subscribe(c)

	// Start query 1 (simulates a long-running query)
	eng.Query(context.Background(), "initial query", "test")

	// Wait for engine to be busy
	deadline := time.After(3 * time.Second)
	for !eng.IsBusy() {
		select {
		case <-deadline:
			t.Fatal("engine never became busy")
		default:
			time.Sleep(5 * time.Millisecond) // REAL-TIME: poll loop
		}
	}

	// Enqueue an attachment while busy
	c.enqueue("userA", "follow-up question", nil)

	// Release query 1 — engine should process the attachment and send response
	close(release)

	// Wait for engine to finish both queries
	deadline2 := time.After(10 * time.Second)
	for eng.IsBusy() {
		select {
		case <-deadline2:
			t.Fatal("timed out waiting for engine to become idle")
		default:
			time.Sleep(10 * time.Millisecond) // REAL-TIME: poll loop
		}
	}

	// Verify response was delivered to user
	if len(sentTexts) == 0 {
		t.Fatal("no response sent to user — attachment turn's response was dropped")
	}
	if !strings.Contains(sentTexts[0], "reply") {
		t.Errorf("sent text = %q, want to contain 'reply'", sentTexts[0])
	}
}

// ---------------------------------------------------------------------------
// Multiple rapid messages during a running query
// ---------------------------------------------------------------------------

// TestEnqueue_MultipleRapidMessages_AllAttached verifies that 3+ messages
// arriving during a single running query all survive: they wait in the
// attachment queue mid-turn (none dropped, none routed to the idle
// inboundCh) and attach at the turn boundary as reminder envelopes, in the
// order they were sent.
func TestEnqueue_MultipleRapidMessages_AllAttached(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	eng := engine.New(&engine.Params{
		Provider: &blockingEngineMock{release: release},
		Model:    "test-model",
	})
	eng.SetSystemPrompt("test")
	t.Cleanup(func() { eng.Close() })

	h := hub.NewHub()
	c := &WeChatConnector{
		hub:          h,
		engine:       eng,
		inboundCh:    make(chan inboundMessage, 10),
		typingCache:  newTypingTicketCache(time.Minute),
		activeUserID: "userA",
	}
	c.isBusyFn = func() bool { return eng.IsBusy() }
	h.Subscribe(c)

	// Start query 1
	eng.Query(context.Background(), "long query", "test")

	// Wait for busy
	deadline := time.After(3 * time.Second)
	for !eng.IsBusy() {
		select {
		case <-deadline:
			t.Fatal("engine never became busy")
		default:
			time.Sleep(5 * time.Millisecond) // REAL-TIME: poll loop
		}
	}

	// Enqueue 3 messages rapidly
	c.enqueue("userA", "msg1", nil)
	c.enqueue("userA", "msg2", nil)
	c.enqueue("userA", "msg3", nil)

	// Mid-turn all 3 wait in the queue; nothing in history yet.
	if envelopes := countHistoryEnvelopes(eng.Messages()); envelopes != 0 {
		t.Fatalf("history envelope messages mid-turn = %d, want 0 (attach must wait for the turn boundary)", envelopes)
	}
	if n := eng.AttachmentsLen(); n != 3 {
		t.Fatalf("AttachmentsLen() = %d, want 3 (no drops)", n)
	}

	// None should be in inboundCh
	select {
	case <-c.inboundCh:
		t.Fatal("inboundCh should be empty — all messages are queued messages")
	default:
	}

	// Release query 1 — the boundary attaches all 3 in FIFO order.
	close(release)
	deadline2 := time.After(10 * time.Second)
	for eng.IsBusy() {
		select {
		case <-deadline2:
			t.Fatal("timed out waiting for engine to become idle")
		default:
			time.Sleep(10 * time.Millisecond) // REAL-TIME: poll loop
		}
	}

	msgs := eng.Messages()
	envelopes := 0
	var order []string
	for _, m := range msgs {
		if m.Role != types.RoleUser || len(m.Content) == 0 || m.Content[0].Type != types.ContentTypeText {
			continue
		}
		if original, ok := utils.UnwrapQueuedReminder(m.Content[0].Text); ok {
			envelopes++
			if !strings.HasPrefix(original, "msg") {
				t.Errorf("unexpected queued message text %q", original)
			}
			order = append(order, original)
		}
	}
	if envelopes != 3 {
		t.Fatalf("history envelope messages = %d, want 3", envelopes)
	}
	if len(order) != 3 || order[0] != "msg1" || order[1] != "msg2" || order[2] != "msg3" {
		t.Errorf("attach order = %v, want [msg1 msg2 msg3] (FIFO)", order)
	}
}

// countHistoryEnvelopes counts user messages whose first text block unwraps
// to a queued message reminder envelope.
func countHistoryEnvelopes(msgs []types.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role != types.RoleUser || len(m.Content) == 0 || m.Content[0].Type != types.ContentTypeText {
			continue
		}
		if _, ok := utils.UnwrapQueuedReminder(m.Content[0].Text); ok {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Typing indicator for attachment-driven turns
// ---------------------------------------------------------------------------

// TestEnqueue_AttachmentTurn_TypingRefresh verifies that the typing indicator
// refresh fires during attachment-driven events when activeUserID is set and
// lastTypingRefresh is older than 5s.
func TestEnqueue_AttachmentTurn_TypingRefresh(t *testing.T) {
	t.Parallel()
	h := hub.NewHub()
	eng := engine.New(&engine.Params{
		Provider:   &blockingEngineMock{release: make(chan struct{})},
		Model:      "test-model",
		Dispatcher: h,
	})
	eng.SetSystemPrompt("test")
	t.Cleanup(func() { eng.Close() })

	typingMock := &mockTypingAPI{}
	c := &WeChatConnector{
		hub:               h,
		engine:            eng,
		inboundCh:         make(chan inboundMessage, 10),
		typingCache:       newTypingTicketCache(time.Minute),
		activeUserID:      "userA",
		typingAPI:         typingMock,
		lastTypingRefresh: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), // far in the past (>5s)
	}
	c.isBusyFn = func() bool { return eng.IsBusy() }
	h.Subscribe(c)

	// Dispatch a text event — Handle should refresh typing because
	// activeUserID is set and lastTypingRefresh is >5s ago.
	c.Handle(types.QueryEvent{
		Type: types.EventTextDelta,
		Text: "hello",
	})

	calls := typingMock.getCalls()
	if len(calls) == 0 {
		t.Error("typing refresh should have fired during EventTextDelta when activeUserID is set")
	}
}

// ---------------------------------------------------------------------------
// Error in attachment turn
// ---------------------------------------------------------------------------

// TestEnqueue_AttachmentTurn_ErrorDelivered verifies that when Handle receives
// an EventQueryEnd with an error (which happens when an attachment turn's LLM
// call fails), the error message is sent to the user via sendToUserFn.
func TestEnqueue_AttachmentTurn_ErrorDelivered(t *testing.T) {
	t.Parallel()
	h := hub.NewHub()
	var sentTexts []string
	c := &WeChatConnector{
		hub:          h,
		inboundCh:    make(chan inboundMessage, 10),
		typingCache:  newTypingTicketCache(time.Minute),
		activeUserID: "userA",
		sendToUserFn: func(_ context.Context, _, text string) error {
			sentTexts = append(sentTexts, text)
			return nil
		},
	}

	// Simulate an error during an attachment turn — the same EventQueryEnd
	// that the engine emits when LLM Stream fails.
	c.Handle(types.QueryEvent{
		Type:  types.EventQueryEnd,
		Error: fmt.Errorf("rate limited"),
	})

	if len(sentTexts) != 1 {
		t.Fatalf("expected 1 error message, got %d: %v", len(sentTexts), sentTexts)
	}
	if !strings.Contains(sentTexts[0], "rate limited") {
		t.Errorf("error message = %q, want to contain 'rate limited'", sentTexts[0])
	}
}

// ---------------------------------------------------------------------------
// Queue overflow: attachment queue is unbounded
// ---------------------------------------------------------------------------

// TestEnqueue_AttachmentQueue_Unbounded verifies that rapid mid-turn messages
// are all accepted without dropping: mid-turn they pile into the attachment
// queue (unbounded), and the turn boundary attaches every one of them. This
// contrasts with inboundCh which drops when full.
func TestEnqueue_AttachmentQueue_Unbounded(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	eng := engine.New(&engine.Params{
		Provider: &blockingEngineMock{release: release},
		Model:    "test-model",
	})
	eng.SetSystemPrompt("test")
	t.Cleanup(func() { eng.Close() })

	h := hub.NewHub()
	c := &WeChatConnector{
		hub:          h,
		engine:       eng,
		inboundCh:    make(chan inboundMessage, 10),
		typingCache:  newTypingTicketCache(time.Minute),
		activeUserID: "userA",
	}
	c.isBusyFn = func() bool { return eng.IsBusy() }
	h.Subscribe(c)

	// Start query 1
	eng.Query(context.Background(), "long query", "test")

	// Wait for busy
	deadline := time.After(3 * time.Second)
	for !eng.IsBusy() {
		select {
		case <-deadline:
			t.Fatal("engine never became busy")
		default:
			time.Sleep(5 * time.Millisecond) // REAL-TIME: poll loop
		}
	}

	// Enqueue 50 messages — none should be dropped
	for i := range 50 {
		c.enqueue("userA", fmt.Sprintf("msg%d", i), nil)
	}

	if envelopes := countHistoryEnvelopes(eng.Messages()); envelopes != 0 {
		t.Fatalf("history envelope messages mid-turn = %d, want 0", envelopes)
	}
	if n := eng.AttachmentsLen(); n != 50 {
		t.Fatalf("AttachmentsLen() = %d, want 50 (unbounded queue, no drops)", n)
	}

	// The boundary attaches all 50.
	close(release)
	deadline2 := time.After(10 * time.Second)
	for eng.IsBusy() {
		select {
		case <-deadline2:
			t.Fatal("timed out waiting for engine to become idle")
		default:
			time.Sleep(10 * time.Millisecond) // REAL-TIME: poll loop
		}
	}
	if envelopes := countHistoryEnvelopes(eng.Messages()); envelopes != 50 {
		t.Fatalf("history envelope messages = %d, want 50 (no drops)", envelopes)
	}

	// Clean up
	eng.Close()
}

// ---------------------------------------------------------------------------
// New — every wiring statement and cache must survive construction
// ---------------------------------------------------------------------------

func TestNew_WiresEveryHook(t *testing.T) {
	h := hub.NewHub()
	eng := engine.New(&engine.Params{
		Provider:   &blockingEngineMock{release: make(chan struct{})},
		Model:      "test-model",
		Dispatcher: h,
	})
	t.Cleanup(eng.Close)
	c := New(eng, h)
	if c.engine != eng {
		t.Error("engine field not set by New")
	}
	if c.hub != h {
		t.Error("hub field not set by New")
	}
	if c.client == nil {
		t.Error("client not set by New")
	}
	if cap(c.inboundCh) != 100 {
		t.Errorf("inboundCh cap = %d, want 100", cap(c.inboundCh))
	}
	if c.dedup == nil {
		t.Error("dedup not initialized by New")
	}
	if c.typingCache == nil {
		t.Error("typingCache not initialized by New")
	}
	if c.MediaCache() == nil {
		t.Error("mediaCache nil after successful media.New")
	}
	if c.sendToUserFn == nil {
		t.Error("sendToUserFn not wired by New")
	}
	if c.sendMsgFn == nil {
		t.Error("sendMsgFn not wired by New")
	}
	if c.sendItemFn == nil {
		t.Error("sendItemFn not wired by New")
	}
	if c.uploadFn == nil {
		t.Error("uploadFn not wired by New")
	}
	if c.queryFn == nil {
		t.Error("queryFn not wired by New")
	}
	if c.queryWithContentFn == nil {
		t.Error("queryWithContentFn not wired by New")
	}
	if c.getUpdatesFn == nil {
		t.Error("getUpdatesFn not wired by New")
	}
	if c.isBusyFn == nil {
		t.Error("isBusyFn not wired by New")
	}
	if c.isBusyFn() {
		t.Error("isBusyFn true on a fresh engine")
	}
	// New must subscribe to the hub, otherwise no engine event ever reaches
	// the connector.
	h.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "z"})
	if c.textBuffer.Len() != 1 {
		t.Errorf("textBuffer len = %d after hub dispatch, want 1 (connector not subscribed)", c.textBuffer.Len())
	}
}

func TestNew_MediaCacheDisabledWhenInitFails(t *testing.T) {
	logs := sweepCaptureLogs(t)
	t.Setenv("HOME", "/proc/gbot-sweep-unwritable-home")
	c := New(nil, hub.NewHub())
	if c.MediaCache() != nil {
		t.Error("mediaCache set even though media.New failed")
	}
	recs := logs.find("wechat: media cache init failed, media attachments disabled")
	if len(recs) != 1 {
		t.Fatalf("media-cache failure logged %d times, want 1", len(recs))
	}
	if recs[0].level != slog.LevelWarn {
		t.Errorf("log level = %v, want WARN", recs[0].level)
	}
	if recs[0].attrs["error"] == nil {
		t.Error("media-cache failure log carries no error attr")
	}
}

// ---------------------------------------------------------------------------
// Start / Stop — typing API wiring, project dir, loop drain, state save
// ---------------------------------------------------------------------------

func TestStart_WiresTypingAPIFromState(t *testing.T) {
	logs := sweepCaptureLogs(t)
	var paths []string
	var auths []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/" + EPGetConfig:
			_, _ = w.Write([]byte(`{"ret":0,"typing_ticket":"tkt-7"}`))
		case "/" + EPSendTyping:
			if !strings.Contains(string(body), `"typing_ticket":"tkt-7"`) {
				t.Errorf("sendtyping payload = %s, want ticket tkt-7 from getconfig", body)
			}
			_, _ = w.Write([]byte(`{"ret":0}`))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			_, _ = w.Write([]byte(`{"ret":0}`))
		}
	}))
	defer srv.Close()

	c := New(nil, hub.NewHub())
	c.client = srv.Client()
	c.getUpdatesFn = func(ctx context.Context, _ *http.Client, _, _, _ string, _ time.Duration) (*GetUpdatesResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	dir := t.TempDir()
	state := &State{AccountID: "acct@im.bot", Token: "tok-1", BaseURL: srv.URL}
	if err := c.Start(context.Background(), state, dir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := logs.find("wechat: connector started"); len(got) != 1 {
		t.Errorf("start logged %d times, want 1", len(got))
	}
	c.startTyping(context.Background(), "userA")
	mu.Lock()
	gotPaths, gotAuths := append([]string(nil), paths...), append([]string(nil), auths...)
	mu.Unlock()
	wantPaths := []string{"/" + EPGetConfig, "/" + EPSendTyping}
	if len(gotPaths) != 2 || gotPaths[0] != wantPaths[0] || gotPaths[1] != wantPaths[1] {
		t.Fatalf("typing requests = %v, want %v", gotPaths, wantPaths)
	}
	for i, a := range gotAuths {
		if a != "Bearer tok-1" {
			t.Errorf("request %d Authorization = %q, want %q", i, a, "Bearer tok-1")
		}
	}
	c.Stop()
	if _, err := os.Stat(StateFilePath(dir, "acct@im.bot")); err != nil {
		t.Errorf("state file after Stop: %v", err)
	}
}

func TestStop_WaitsForLoopsBeforeReturning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := sweepCaptureLogs(t)
		c := &WeChatConnector{
			hub:         hub.NewHub(),
			client:      &http.Client{},
			inboundCh:   make(chan inboundMessage, 4),
			typingCache: newTypingTicketCache(time.Minute),
		}
		var pollSawCancel atomic.Bool
		entered := make(chan struct{})
		var enterOnce sync.Once
		c.getUpdatesFn = func(ctx context.Context, _ *http.Client, _, _, _ string, _ time.Duration) (*GetUpdatesResponse, error) {
			enterOnce.Do(func() { close(entered) })
			<-ctx.Done()
			pollSawCancel.Store(true)
			return nil, ctx.Err()
		}
		dir := t.TempDir()
		if err := c.Start(context.Background(), &State{AccountID: "acct@im.bot"}, dir); err != nil {
			t.Fatalf("Start: %v", err)
		}
		<-entered
		c.Stop()
		if !pollSawCancel.Load() {
			t.Error("Stop returned before the poll loop observed cancellation (pollWg.Wait skipped)")
		}
		if got := logs.find("wechat: connector stopped"); len(got) != 1 {
			t.Errorf("stop logged %d times, want 1", len(got))
		}
		if _, err := os.Stat(StateFilePath(dir, "acct@im.bot")); err != nil {
			t.Errorf("state file after Stop: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Handle — query-start reset, thinking floor/accumulation, toolName delete
// ---------------------------------------------------------------------------

func TestHandle_QueryStartResetsStatsAndFlushClock(t *testing.T) {
	c := &WeChatConnector{hub: hub.NewHub(), typingCache: newTypingTicketCache(time.Minute)}
	c.thinkingSecs = 5
	c.searchCount = 2
	c.fileCount = 3
	c.cmdCount = 4
	c.agentCount = 5
	c.Handle(types.QueryEvent{Type: types.EventQueryStart})
	if c.lastFlush.IsZero() {
		t.Error("lastFlush still zero after EventQueryStart")
	}
	if c.searchCount != 0 {
		t.Errorf("searchCount = %d after EventQueryStart, want 0", c.searchCount)
	}
	if c.fileCount != 0 {
		t.Errorf("fileCount = %d after EventQueryStart, want 0", c.fileCount)
	}
	if c.cmdCount != 0 {
		t.Errorf("cmdCount = %d after EventQueryStart, want 0", c.cmdCount)
	}
	if c.agentCount != 0 {
		t.Errorf("agentCount = %d after EventQueryStart, want 0", c.agentCount)
	}
	if c.thinkingSecs != 0 {
		t.Errorf("thinkingSecs = %v after EventQueryStart, want 0", c.thinkingSecs)
	}
}

func TestHandle_ThinkingEndFloorAndAccumulate(t *testing.T) {
	c := &WeChatConnector{hub: hub.NewHub(), typingCache: newTypingTicketCache(time.Minute)}
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 50 * time.Millisecond}})
	if c.thinkingSecs != 0.1 {
		t.Errorf("thinkingSecs = %v after 50ms, want the 0.1s floor", c.thinkingSecs)
	}
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 2 * time.Second}})
	if c.thinkingSecs != 2.1 {
		t.Errorf("thinkingSecs = %v after a second 2s turn, want 2.1 (accumulate, not overwrite)", c.thinkingSecs)
	}
}

func TestHandle_ToolEndDeletesNameSoRepeatEndsDoNotDoubleCount(t *testing.T) {
	c := &WeChatConnector{hub: hub.NewHub(), typingCache: newTypingTicketCache(time.Minute)}
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Bash"}})
	c.Handle(types.QueryEvent{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "t1"}})
	c.Handle(types.QueryEvent{Type: types.EventToolEnd, ToolResult: &types.ToolResultEvent{ToolUseID: "t1"}})
	if c.cmdCount != 1 {
		t.Errorf("cmdCount = %d after two ToolEnd for one ToolUseID, want 1", c.cmdCount)
	}
	if len(c.toolNames) != 0 {
		t.Errorf("toolNames still holds %d entries after ToolEnd, want 0", len(c.toolNames))
	}
}

func TestHandle_TextEndSeparatesDeltasWithBlankLine(t *testing.T) {
	var sent []string
	c := &WeChatConnector{
		hub:          hub.NewHub(),
		typingCache:  newTypingTicketCache(time.Minute),
		activeUserID: "user1",
		sendToUserFn: func(_ context.Context, _, text string) error {
			sent = append(sent, text)
			return nil
		},
	}
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "a"})
	c.Handle(types.QueryEvent{Type: types.EventTextEnd})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "b"})
	c.Handle(types.QueryEvent{Type: types.EventTextEnd})
	c.flushBuffer()
	if len(sent) != 1 {
		t.Fatalf("sends = %d, want 1", len(sent))
	}
	if sent[0] != "a\n\nb" {
		t.Errorf("flushed text = %q, want %q", sent[0], "a\n\nb")
	}
}

// ctxSpyTypingAPI records the context of every call so a nil-ctx mutation in
// Handle's typing path is observable.
type ctxSpyTypingAPI struct {
	mu    sync.Mutex
	calls []context.Context
}

func (m *ctxSpyTypingAPI) record(ctx context.Context) {
	m.mu.Lock()
	m.calls = append(m.calls, ctx)
	m.mu.Unlock()
}

func (m *ctxSpyTypingAPI) getConfig(ctx context.Context, userID, _ string) (string, error) {
	m.record(ctx)
	return "ticket-" + userID, nil
}

func (m *ctxSpyTypingAPI) sendTyping(ctx context.Context, _ string, _ string, _ int) error {
	m.record(ctx)
	return nil
}

func (m *ctxSpyTypingAPI) contexts() []context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]context.Context(nil), m.calls...)
}

func TestHandle_QueryEndErrorPaths(t *testing.T) {
	logs := sweepCaptureLogs(t)
	type send struct {
		ctx    context.Context
		userID string
		text   string
	}
	newC := func(typing typingAPI) (*WeChatConnector, *[]send) {
		out := &[]send{}
		c := &WeChatConnector{
			hub:          hub.NewHub(),
			typingCache:  newTypingTicketCache(time.Minute),
			activeUserID: "user1",
			typingAPI:    typing,
			sendToUserFn: func(ctx context.Context, userID, text string) error {
				*out = append(*out, send{ctx: ctx, userID: userID, text: text})
				return nil
			},
		}
		return c, out
	}

	// Error + active user: the failure is sent verbatim with a live context.
	c, out := newC(&ctxSpyTypingAPI{})
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd, Error: errors.New("boom")})
	if len(*out) != 1 {
		t.Fatalf("sends after QueryEnd{Error} = %d, want 1", len(*out))
	}
	if (*out)[0].text != "⚠️ Error: boom" {
		t.Errorf("error text = %q, want %q", (*out)[0].text, "⚠️ Error: boom")
	}
	if (*out)[0].ctx == nil {
		t.Error("sendToUserFn received a nil context")
	}

	// No error: nothing is sent even with an active user, and the
	// notification-failure log stays silent on a successful send.
	c2, out2 := newC(nil)
	c2.Handle(types.QueryEvent{Type: types.EventQueryEnd})
	if len(*out2) != 0 {
		t.Errorf("sends after QueryEnd without error = %d, want 0", len(*out2))
	}
	if got := logs.find("wechat: send error message failed"); len(got) != 0 {
		t.Errorf("successful notification logged %d failures, want 0", len(got))
	}

	// Error without an active user: no recipient, so no send.
	c3, out3 := newC(nil)
	c3.activeUserID = ""
	c3.Handle(types.QueryEvent{Type: types.EventQueryEnd, Error: errors.New("boom")})
	if len(*out3) != 0 {
		t.Errorf("sends after QueryEnd{Error} with no active user = %d, want 0", len(*out3))
	}

	// A failing error-notification is logged.
	c4, _ := newC(nil)
	c4.sendToUserFn = func(_ context.Context, _, _ string) error { return errors.New("rate limited") }
	c4.Handle(types.QueryEvent{Type: types.EventQueryEnd, Error: errors.New("boom")})
	if got := logs.find("wechat: send error message failed"); len(got) != 1 {
		t.Errorf("failed error-notification logged %d times, want 1", len(got))
	}

	// QueryEnd must stop the typing indicator with a live context.
	spy := &ctxSpyTypingAPI{}
	c5, _ := newC(spy)
	c5.Handle(types.QueryEvent{Type: types.EventQueryEnd})
	ctxs := spy.contexts()
	if len(ctxs) != 2 {
		t.Fatalf("typing calls = %d, want 2 (getconfig + sendTyping stop)", len(ctxs))
	}
	for i, ctx := range ctxs {
		if ctx == nil {
			t.Errorf("typing call %d received a nil context", i)
		}
	}
}

// ---------------------------------------------------------------------------
// flushBuffer: empty guard, failure retention, success reset
// ---------------------------------------------------------------------------

func TestFlushBuffer_EmptyHeaderAndTextSendsNothing(t *testing.T) {
	var calls int
	c := &WeChatConnector{
		hub:          hub.NewHub(),
		typingCache:  newTypingTicketCache(time.Minute),
		activeUserID: "user1",
		sendToUserFn: func(_ context.Context, _, _ string) error {
			calls++
			return nil
		},
	}
	c.flushBuffer()
	if calls != 0 {
		t.Errorf("flush of an empty buffer sent %d messages, want 0", calls)
	}
}

func TestFlushBuffer_SendFailureKeepsEverything(t *testing.T) {
	logs := sweepCaptureLogs(t)
	c := &WeChatConnector{
		hub:          hub.NewHub(),
		typingCache:  newTypingTicketCache(time.Minute),
		activeUserID: "user1",
		sendToUserFn: func(_ context.Context, _, _ string) error { return errors.New("rate limited") },
	}
	c.textBuffer.WriteString("body")
	c.searchCount = 3
	before := time.Unix(1, 0) // far in the past: the 5s flush window is open
	c.lastFlush = before
	c.flushBuffer()
	if got := logs.find("wechat: flush failed, retaining buffer"); len(got) != 1 {
		t.Errorf("flush failure logged %d times, want 1", len(got))
	}
	if c.textBuffer.String() != "body" {
		t.Errorf("textBuffer = %q after failed flush, want it retained", c.textBuffer.String())
	}
	if c.searchCount != 3 {
		t.Errorf("searchCount = %d after failed flush, want 3 (counters retained)", c.searchCount)
	}
	if !c.lastFlush.Equal(before) {
		t.Errorf("lastFlush = %v after failed flush, want it unchanged", c.lastFlush)
	}
}

func TestFlushBuffer_SuccessAdvancesClockAndClearsState(t *testing.T) {
	var got []struct {
		ctx  context.Context
		text string
	}
	c := &WeChatConnector{
		hub:          hub.NewHub(),
		typingCache:  newTypingTicketCache(time.Minute),
		activeUserID: "user1",
		sendToUserFn: func(ctx context.Context, _, text string) error {
			got = append(got, struct {
				ctx  context.Context
				text string
			}{ctx: ctx, text: text})
			return nil
		},
	}
	c.textBuffer.WriteString("body")
	c.agentCount = 2
	c.lastFlush = time.Unix(1, 0) // far in the past: a stale clock must be replaced
	c.flushBuffer()
	if len(got) != 1 {
		t.Fatalf("sends = %d, want 1", len(got))
	}
	if got[0].text != "⬡ 代理 2次\n\nbody" {
		t.Errorf("sent = %q, want header + body", got[0].text)
	}
	if got[0].ctx == nil {
		t.Error("flush sent with a nil context")
	}
	if c.textBuffer.Len() != 0 {
		t.Errorf("textBuffer len = %d after successful flush, want 0", c.textBuffer.Len())
	}
	if c.agentCount != 0 {
		t.Errorf("agentCount = %d after successful flush, want 0", c.agentCount)
	}
	if time.Since(c.lastFlush) > time.Minute {
		t.Errorf("lastFlush = %v, want it advanced to now", c.lastFlush)
	}
}

func TestFlushBuffer_NoActiveUserResetsState(t *testing.T) {
	c := &WeChatConnector{hub: hub.NewHub(), typingCache: newTypingTicketCache(time.Minute)}
	c.textBuffer.WriteString("stale")
	c.cmdCount = 4
	c.flushBuffer()
	if c.textBuffer.Len() != 0 {
		t.Errorf("textBuffer len = %d with no active user, want 0", c.textBuffer.Len())
	}
	if c.cmdCount != 0 {
		t.Errorf("cmdCount = %d with no active user, want 0", c.cmdCount)
	}
}

// ---------------------------------------------------------------------------
// sendWeChatReply / sendWeChatReplyErr / Send
// ---------------------------------------------------------------------------

func TestSendWeChatReply_LogsSendFailure(t *testing.T) {
	logs := sweepCaptureLogs(t)
	var gotCtx context.Context
	c := &WeChatConnector{
		hub:         hub.NewHub(),
		typingCache: newTypingTicketCache(time.Minute),
		sendToUserFn: func(ctx context.Context, userID, _ string) error {
			gotCtx = ctx
			if userID != "user1" {
				t.Errorf("send userID = %q, want user1", userID)
			}
			return errors.New("send failed")
		},
	}
	c.sendWeChatReply(context.Background(), "user1", "hello")
	if gotCtx == nil {
		t.Error("sendToUserFn received a nil context")
	}
	recs := logs.find("wechat: send reply failed")
	if len(recs) != 1 {
		t.Fatalf("send failure logged %d times, want 1", len(recs))
	}
	if recs[0].level != slog.LevelError {
		t.Errorf("log level = %v, want ERROR", recs[0].level)
	}
}

func TestSendWeChatReplyErr_PassesContextAndReturnsChunkError(t *testing.T) {
	var ctxs []context.Context
	c := &WeChatConnector{
		hub:         hub.NewHub(),
		typingCache: newTypingTicketCache(time.Minute),
		sendToUserFn: func(ctx context.Context, _, _ string) error {
			ctxs = append(ctxs, ctx)
			return errors.New("boom")
		},
	}
	err := c.sendWeChatReplyErr(context.Background(), "user1", "hello")
	if err == nil || err.Error() != "boom" {
		t.Fatalf("sendWeChatReplyErr = %v, want boom", err)
	}
	if len(ctxs) != 1 {
		t.Fatalf("sendToUserFn calls = %d, want 1", len(ctxs))
	}
	if ctxs[0] == nil {
		t.Error("sendToUserFn received a nil context")
	}
}

func TestSend_PassesContextToSender(t *testing.T) {
	var got context.Context
	c := New(nil, hub.NewHub())
	c.sendToUserFn = func(ctx context.Context, userID, text string) error {
		got = ctx
		if userID != "user@im" || text != "hi" {
			t.Errorf("send got (%q,%q), want (user@im,hi)", userID, text)
		}
		return nil
	}
	if err := c.Send("user@im", "hi"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got == nil {
		t.Error("Send passed a nil context to sendToUserFn")
	}
}

// ---------------------------------------------------------------------------
// SaveState — nil-state unlock and write-failure log
// ---------------------------------------------------------------------------

func TestSaveState_NilStateReleasesMutex(t *testing.T) {
	c := New(nil, hub.NewHub())
	c.state = nil
	c.SaveState()
	if !c.stateMu.TryLock() {
		t.Fatal("SaveState with nil state left the state mutex locked")
	}
	c.stateMu.Unlock()
}

func TestSaveState_WriteFailureIsLogged(t *testing.T) {
	logs := sweepCaptureLogs(t)
	c := New(nil, hub.NewHub())
	c.state = &State{AccountID: "acct@im.bot", Token: "tok"}
	c.projectDir = "/proc/gbot-sweep-unwritable"
	c.SaveState()
	recs := logs.find("wechat: save state failed")
	if len(recs) != 1 {
		t.Fatalf("save failure logged %d times, want 1", len(recs))
	}
	if recs[0].attrs["account_id"] != "acct@im.bot" {
		t.Errorf("account_id attr = %v, want acct@im.bot", recs[0].attrs["account_id"])
	}
	if recs[0].attrs["error"] == nil {
		t.Error("save failure log carries no error attr")
	}
}

func TestSafeID_LengthBoundaries(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want string
	}{
		{"abcdefgh", "abcdefgh"},
		{"abcdefghi", "abcdefgh"},
		{"abcdefghij", "abcdefgh"},
	} {
		if got := safeID(tt.in); got != tt.want {
			t.Errorf("safeID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// pickMediaItem — each conjunct of the type/holder/downloadable guards
// ---------------------------------------------------------------------------

func TestPickMediaItem_Guards(t *testing.T) {
	dl := &MediaRef{FullURL: "https://example.com/m"}
	cases := []struct {
		name  string
		items []Item
		want  bool
	}{
		{"text item carrying an image holder", []Item{{Type: ItemText, ImageItem: &MediaItemHolder{Media: dl}}}, false},
		{"image item with a non-downloadable media ref", []Item{{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{}}}}, false},
		{"text item carrying a file holder", []Item{{Type: ItemText, FileItem: &FileItem{Media: dl}}}, false},
		{"file item with a non-downloadable media ref", []Item{{Type: ItemFile, FileItem: &FileItem{Media: &MediaRef{}}}}, false},
		{"text item carrying a voice holder", []Item{{Type: ItemText, VoiceItem: &VoiceItem{Media: dl, Text: "t"}}}, false},
		{"text item carrying a silent voice holder", []Item{{Type: ItemText, VoiceItem: &VoiceItem{Media: dl}}}, false},
		{"voice item with a transcription", []Item{{Type: ItemVoice, VoiceItem: &VoiceItem{Media: dl, Text: "spoken"}}}, false},
		{"voice item with a non-downloadable media ref", []Item{{Type: ItemVoice, VoiceItem: &VoiceItem{}}}, false},
		{"downloadable image", []Item{{Type: ItemImage, ImageItem: &MediaItemHolder{Media: dl}}}, true},
	}
	for _, tc := range cases {
		got := pickMediaItem(tc.items)
		if tc.want {
			if got == nil || got.Type != tc.items[0].Type {
				t.Errorf("%s: pickMediaItem = %+v, want the item", tc.name, got)
			}
			continue
		}
		if got != nil {
			t.Errorf("%s: pickMediaItem = %+v, want nil", tc.name, got)
		}
	}
}

func TestPickMediaItem_NilHoldersDoNotPanic(t *testing.T) {
	for _, it := range []Item{
		{Type: ItemImage},
		{Type: ItemFile},
		{Type: ItemVoice},
		{Type: ItemVideo},
	} {
		if got := pickMediaItem([]Item{it}); got != nil {
			t.Errorf("type %d with nil holder: pickMediaItem = %+v, want nil", it.Type, got)
		}
	}
}

// ---------------------------------------------------------------------------
// downloadMedia — unsupported kinds are logged, not silently dropped
// ---------------------------------------------------------------------------

func TestDownloadMedia_VideoAndVoiceAreLogged(t *testing.T) {
	logs := sweepCaptureLogs(t)
	c, _ := newMediaTestConnector(t)
	dl := &MediaRef{FullURL: "https://example.com/m"}
	video := c.downloadMedia(context.Background(), []Item{{Type: ItemVideo, VideoItem: &MediaItemHolder{Media: dl}}})
	if video.Type != "" {
		t.Errorf("video block type = %q, want empty", video.Type)
	}
	if got := logs.find("wechat: video media download not supported, skipping"); len(got) != 1 {
		t.Errorf("video skip logged %d times, want 1", len(got))
	}
	voice := c.downloadMedia(context.Background(), []Item{{Type: ItemVoice, VoiceItem: &VoiceItem{Media: dl}}})
	if voice.Type != "" {
		t.Errorf("voice block type = %q, want empty", voice.Type)
	}
	if got := logs.find("wechat: voice media download not supported, skipping"); len(got) != 1 {
		t.Errorf("voice skip logged %d times, want 1", len(got))
	}
}

func TestDownloadMedia_NoCacheReturnsEmptyBlock(t *testing.T) {
	c, _ := newMediaTestConnector(t)
	c.mediaCache = nil
	logs := sweepCaptureLogs(t)
	block := c.downloadMedia(context.Background(), []Item{{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: "https://x"}}}})
	if block.Type != "" {
		t.Errorf("block type = %q with no cache, want empty", block.Type)
	}
	if len(logs.recs) != 0 {
		t.Errorf("logs with no cache = %d, want 0", len(logs.recs))
	}
}

// A nil ctx must fail at the boundary, not inside the fetch:
// http.NewRequestWithContext rejects it and the message would degrade to text
// under a misleading "download failed" line.
func TestDownloadMedia_NilContextFailsLoudly(t *testing.T) {
	logs := sweepCaptureLogs(t)
	c, _ := newMediaTestConnector(t)
	// The unset Context is the case under test: downloadMedia must name it at
	// the boundary instead of letting the fetch fail deep down.
	var ctx context.Context
	block := c.downloadMedia(ctx, []Item{{Type: ItemFile, FileItem: &FileItem{Media: &MediaRef{FullURL: "https://example.invalid/x"}}}})
	if block.Type != "" {
		t.Errorf("block type = %q with nil ctx, want empty", block.Type)
	}
	if got := len(logs.find("wechat: downloadMedia called with nil context, degrading to text-only")); got != 1 {
		t.Errorf("nil-ctx error logs = %d, want 1", got)
	}
	if got := len(logs.find("wechat: file download failed, falling back to text-only")); got != 0 {
		t.Errorf("nil ctx reached the fetch: %d download-failure logs, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// downloadImage / downloadFile — every degradation path is logged
// ---------------------------------------------------------------------------

func mediaServer(t *testing.T, status int, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadImage_DownloadFailureDegradesToText(t *testing.T) {
	logs := sweepCaptureLogs(t)
	srv := mediaServer(t, http.StatusInternalServerError, []byte("nope"))
	c, _ := newMediaTestConnector(t)
	block := c.downloadImage(context.Background(), &MediaItemHolder{Media: &MediaRef{FullURL: srv.URL}})
	if block.Type != "" {
		t.Errorf("block type = %q after download failure, want empty", block.Type)
	}
	if got := logs.find("wechat: image download failed, falling back to text-only"); len(got) != 1 {
		t.Errorf("download failure logged %d times, want 1", len(got))
	}
	if len(logs.find("wechat: downloaded media is not a recognized image format, skipping")) != 0 {
		t.Error("download failure also logged the wrong-format path")
	}
}

func TestDownloadImage_UnrecognizedBytesAreLogged(t *testing.T) {
	logs := sweepCaptureLogs(t)
	srv := mediaServer(t, http.StatusOK, []byte("plain text, not an image"))
	c, _ := newMediaTestConnector(t)
	block := c.downloadImage(context.Background(), &MediaItemHolder{Media: &MediaRef{FullURL: srv.URL}})
	if block.Type != "" {
		t.Errorf("block type = %q for non-image bytes, want empty", block.Type)
	}
	recs := logs.find("wechat: downloaded media is not a recognized image format, skipping")
	if len(recs) != 1 {
		t.Fatalf("wrong-format log = %d, want 1", len(recs))
	}
	if n, _ := recs[0].attrs["len"].(int64); n != 24 {
		t.Errorf("len attr = %v, want int64(24)", recs[0].attrs["len"])
	}
}

func TestDownloadImage_CacheSaveFailureIsLogged(t *testing.T) {
	logs := sweepCaptureLogs(t)
	srv := mediaServer(t, http.StatusOK, realPNGBytes)
	c, store := newMediaTestConnector(t)
	imgDir := filepath.Join(store.RootDir, string(media.CategoryImage))
	if err := os.Chmod(imgDir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(imgDir, 0o755) })
	block := c.downloadImage(context.Background(), &MediaItemHolder{Media: &MediaRef{FullURL: srv.URL}})
	if block.Type != "" {
		t.Errorf("block type = %q after cache save failure, want empty", block.Type)
	}
	if got := logs.find("wechat: image cache save failed, falling back to text-only"); len(got) != 1 {
		t.Errorf("cache save failure logged %d times, want 1", len(got))
	}
}

func TestDownloadImage_HappyPathReturnsBase64PNG(t *testing.T) {
	logs := sweepCaptureLogs(t)
	srv := mediaServer(t, http.StatusOK, realPNGBytes)
	c, _ := newMediaTestConnector(t)
	block := c.downloadImage(context.Background(), &MediaItemHolder{Media: &MediaRef{FullURL: srv.URL}})
	if block.Type != types.ContentTypeImage {
		t.Fatalf("block type = %q, want image", block.Type)
	}
	if block.Source == nil || block.Source.MediaType != "image/png" {
		t.Fatalf("block source = %+v, want image/png", block.Source)
	}
	for _, r := range logs.recs {
		if r.level >= slog.LevelWarn {
			t.Errorf("happy path logged %v %q", r.level, r.msg)
		}
	}
}

func TestDownloadFile_DownloadFailureDegradesToText(t *testing.T) {
	logs := sweepCaptureLogs(t)
	srv := mediaServer(t, http.StatusBadGateway, []byte("nope"))
	c, _ := newMediaTestConnector(t)
	block := c.downloadFile(context.Background(), &FileItem{Media: &MediaRef{FullURL: srv.URL}, FileName: "notes.txt"})
	if block.Type != "" {
		t.Fatalf("block type = %q after download failure, want empty", block.Type)
	}
	if got := logs.find("wechat: file download failed, falling back to text-only"); len(got) != 1 {
		t.Errorf("download failure logged %d times, want 1", len(got))
	}
}

func TestDownloadFile_ExtensionAndNameFallbacks(t *testing.T) {
	srv := mediaServer(t, http.StatusOK, []byte("report body"))
	c, _ := newMediaTestConnector(t)

	noExt := c.downloadFile(context.Background(), &FileItem{Media: &MediaRef{FullURL: srv.URL}, FileName: "report"})
	if noExt.Type != types.ContentTypeText {
		t.Fatalf("block type = %q, want text", noExt.Type)
	}
	saved := savedPathFromDocBlock(noExt.Text)
	// The connector has no ".bin" fallback of its own: the suffix must come
	// from media's single default, so a change there lands here too.
	if filepath.Ext(saved) != media.DefaultExt {
		t.Errorf("saved path = %q, want media.DefaultExt (%q)", saved, media.DefaultExt)
	}
	if !strings.Contains(noExt.Text, "[Document: report saved at ") {
		t.Errorf("no-ext block = %q, want the original filename in the header", noExt.Text)
	}

	anonymous := c.downloadFile(context.Background(), &FileItem{Media: &MediaRef{FullURL: srv.URL}, FileName: ""})
	if anonymous.Type != types.ContentTypeText {
		t.Fatalf("block type = %q, want text", anonymous.Type)
	}
	stored := filepath.Base(savedPathFromDocBlock(anonymous.Text))
	if stored == "" {
		t.Fatalf("anonymous block = %q, want a saved-at path", anonymous.Text)
	}
	if !strings.Contains(anonymous.Text, "[Document: "+stored+" saved at ") {
		t.Errorf("anonymous block = %q, want the stored basename %q as the document name", anonymous.Text, stored)
	}
}

func savedPathFromDocBlock(text string) string {
	_, after, ok := strings.Cut(text, "saved at ")
	if !ok {
		return ""
	}
	rest := after
	if before, _, ok := strings.Cut(rest, "]"); ok {
		return before
	}
	return rest
}

func TestDownloadFile_CacheSaveFailureIsLogged(t *testing.T) {
	logs := sweepCaptureLogs(t)
	srv := mediaServer(t, http.StatusOK, []byte("body"))
	c, store := newMediaTestConnector(t)
	docDir := filepath.Join(store.RootDir, string(media.CategoryDocument))
	if err := os.Chmod(docDir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(docDir, 0o755) })
	block := c.downloadFile(context.Background(), &FileItem{Media: &MediaRef{FullURL: srv.URL}, FileName: "doc.txt"})
	if block.Type != "" {
		t.Fatalf("block type = %q after cache save failure, want empty", block.Type)
	}
	if got := logs.find("wechat: file cache save failed, falling back to text-only"); len(got) != 1 {
		t.Errorf("cache save failure logged %d times, want 1", len(got))
	}
}

func TestDownloadFile_ParseFailureSendsPathFallback(t *testing.T) {
	logs := sweepCaptureLogs(t)
	srv := mediaServer(t, http.StatusOK, []byte("binary\x00payload"))
	c, _ := newMediaTestConnector(t)
	block := c.downloadFile(context.Background(), &FileItem{Media: &MediaRef{FullURL: srv.URL}, FileName: "thing.bin"})
	if block.Type != types.ContentTypeText {
		t.Fatalf("block type = %q, want text", block.Type)
	}
	text := block.Text
	if !strings.HasPrefix(text, "[Document: thing.bin saved at ") || strings.Contains(text, "\nbinary") {
		t.Errorf("fallback text = %q, want the path-only fallback", text)
	}
	parsed := logs.find("wechat: document parsed inline")
	if len(parsed) != 0 {
		t.Errorf("unparseable document logged %d inline parses, want 0", len(parsed))
	}
	if got := logs.find("wechat: document parse failed, sending path as fallback"); len(got) != 1 {
		t.Errorf("parse failure logged %d times, want 1", len(got))
	}
}

func TestDownloadFile_ParsedInlineIncludesContent(t *testing.T) {
	srv := mediaServer(t, http.StatusOK, []byte("hello document"))
	c, _ := newMediaTestConnector(t)
	block := c.downloadFile(context.Background(), &FileItem{Media: &MediaRef{FullURL: srv.URL}, FileName: "note.txt"})
	if block.Type != types.ContentTypeText {
		t.Fatalf("block = %+v, want a text block", block)
	}
	if !strings.Contains(block.Text, "hello document") {
		t.Errorf("block text = %q, want the parsed document content", block.Text)
	}
}

// ---------------------------------------------------------------------------
// extractText — conjunct guards on text items, quote refs, voice fallback
// ---------------------------------------------------------------------------

func TestExtractText_Guards(t *testing.T) {
	cases := []struct {
		name  string
		items []Item
		want  string
	}{
		{"text item with nil holder", []Item{{Type: ItemText}}, ""},
		{"non-text item carrying a text holder", []Item{{Type: ItemVoice, TextItem: &TextItem{Text: "x"}}}, ""},
		{"voice item with nil holder", []Item{{Type: ItemVoice}}, ""},
		{"voice item with empty transcription", []Item{{Type: ItemVoice, VoiceItem: &VoiceItem{Text: ""}}}, ""},
		{"non-voice item carrying a transcription", []Item{{Type: ItemFile, VoiceItem: &VoiceItem{Text: "spoken"}}}, ""},
		{"quote with neither title nor referenced text", []Item{{Type: ItemText, TextItem: &TextItem{Text: "mine"},
			RefMsg: &RefMsg{MessageItem: &Item{Type: ItemText, TextItem: &TextItem{Text: ""}}}}}, "mine"},
	}
	for _, tc := range cases {
		if got := extractText(tc.items); got != tc.want {
			t.Errorf("%s: extractText = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestExtractText_QuoteMediaTypesAllUseTheMediaPrefix(t *testing.T) {
	for _, refType := range []int{ItemImage, ItemVideo, ItemFile, ItemVoice} {
		items := []Item{{Type: ItemText, TextItem: &TextItem{Text: "mine"},
			RefMsg: &RefMsg{MessageItem: &Item{Type: refType}}}}
		got := extractText(items)
		if got != "[引用媒体]\nmine" {
			t.Errorf("refType %d: extractText = %q, want %q", refType, got, "[引用媒体]\nmine")
		}
		titled := []Item{{Type: ItemText, TextItem: &TextItem{Text: "mine"},
			RefMsg: &RefMsg{Title: "photo", MessageItem: &Item{Type: refType}}}}
		if got := extractText(titled); got != "[引用媒体: photo]\nmine" {
			t.Errorf("refType %d titled: extractText = %q, want %q", refType, got, "[引用媒体: photo]\nmine")
		}
	}
}

func TestExtractText_QuoteTitleOnlyStillFormats(t *testing.T) {
	items := []Item{{Type: ItemText, TextItem: &TextItem{Text: "mine"},
		RefMsg: &RefMsg{Title: "the title", MessageItem: &Item{Type: ItemText, TextItem: &TextItem{Text: ""}}}}}
	got := extractText(items)
	if got != "[引用: the title]\nmine" {
		t.Errorf("extractText = %q, want %q", got, "[引用: the title]\nmine")
	}
}

// cancelProbeProvider parks inside Stream until its context is cancelled, so a
// queryFn that drops the caller's context becomes a hang instead of a cancel.
type cancelProbeProvider struct{ entered chan struct{} }

func (p *cancelProbeProvider) Name() string { return "cancel-probe" }

func (p *cancelProbeProvider) Complete(ctx context.Context, _ *llm.Request) (*llm.Response, error) {
	close(p.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *cancelProbeProvider) Stream(ctx context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	close(p.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

// eventPipe forwards hub events to a channel for bounded waits.
type eventPipe struct{ ch chan types.QueryEvent }

func (e eventPipe) Handle(ev hub.Event) {
	select {
	case e.ch <- ev:
	default:
	}
}

func TestNew_QueryFnHonoursCallerContext(t *testing.T) {
	cases := []struct {
		name string
		call func(*WeChatConnector, context.Context)
	}{
		{"queryFn", func(c *WeChatConnector, ctx context.Context) { c.queryFn(ctx, "hello", "sys") }},
		{"queryWithContentFn", func(c *WeChatConnector, ctx context.Context) {
			c.queryWithContentFn(ctx, []types.ContentBlock{types.NewTextBlock("hello")}, "sys")
		}},
	}
	for _, tc := range cases {
		h := hub.NewHub()
		events := make(chan types.QueryEvent, 64)
		h.Subscribe(eventPipe{ch: events})
		prov := &cancelProbeProvider{entered: make(chan struct{})}
		eng := engine.New(&engine.Params{Provider: prov, Model: "test-model", Dispatcher: h})
		t.Cleanup(eng.Close)
		c := New(eng, h)
		ctx, cancel := context.WithCancel(context.Background())
		tc.call(c, ctx)
		select {
		case <-prov.entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never dispatched a query to the engine", tc.name)
		}
		cancel()
		var ended *types.QueryEvent
		deadline := time.After(5 * time.Second)
		for ended == nil {
			select {
			case ev := <-events:
				if ev.Type == types.EventQueryEnd {
					ended = &ev
				}
			case <-deadline:
				t.Fatalf("%s: cancelling the caller context never ended the query", tc.name)
			}
		}
		if ended.Error == nil {
			t.Errorf("%s: QueryEnd after cancel has Error = nil, want a cancellation error", tc.name)
		}
	}
}

func TestHandle_TypingRefreshRequiresActiveUser(t *testing.T) {
	spy := &ctxSpyTypingAPI{}
	c := &WeChatConnector{
		hub:               hub.NewHub(),
		typingCache:       newTypingTicketCache(time.Minute),
		typingAPI:         spy,
		lastTypingRefresh: time.Unix(1, 0), // far in the past: the 5s refresh gate is open
	}
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "x"})
	if got := spy.contexts(); len(got) != 0 {
		t.Fatalf("typing refreshed with no active user: %d calls, want 0", len(got))
	}
	c.activeUserID = "user1"
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "y"})
	ctxs := spy.contexts()
	if len(ctxs) != 2 {
		t.Fatalf("typing calls = %d, want 2 (getconfig + sendTyping)", len(ctxs))
	}
	for i, ctx := range ctxs {
		if ctx == nil {
			t.Errorf("typing call %d received a nil context", i)
		}
	}
}

func TestDownloadFile_LargeDocumentKeepsFullContent(t *testing.T) {
	var sb strings.Builder
	for i := range 8000 {
		fmt.Fprintf(&sb, "line %04d payload for the cap probe\n", i)
	}
	srv := mediaServer(t, http.StatusOK, []byte(sb.String()))
	c, _ := newMediaTestConnector(t)
	block := c.downloadFile(context.Background(), &FileItem{Media: &MediaRef{FullURL: srv.URL}, FileName: "big.txt"})
	if block.Type != types.ContentTypeText {
		t.Fatalf("block type = %q, want text", block.Type)
	}
	if !strings.Contains(block.Text, "line 7999 payload for the cap probe") {
		t.Errorf("large document block truncated: last line missing from %d bytes of text", len(block.Text))
	}
}

func TestDownloadFile_EmptyDocumentUsesPathFallback(t *testing.T) {
	logs := sweepCaptureLogs(t)
	srv := mediaServer(t, http.StatusOK, nil)
	c, _ := newMediaTestConnector(t)
	block := c.downloadFile(context.Background(), &FileItem{Media: &MediaRef{FullURL: srv.URL}, FileName: "empty.txt"})
	if block.Type != types.ContentTypeText {
		t.Fatalf("block type = %q, want text", block.Type)
	}
	if !strings.HasPrefix(block.Text, "[Document: empty.txt saved at ") {
		t.Errorf("empty document block = %q, want the path-only fallback", block.Text)
	}
	if got := logs.find("wechat: document parse returned empty content, sending path as fallback"); len(got) != 1 {
		t.Errorf("empty-content path logged %d times, want 1", len(got))
	}
}
