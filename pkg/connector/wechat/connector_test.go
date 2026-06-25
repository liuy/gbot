package wechat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
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
	if got := extractText(items); got != "transcribed text" {
		t.Fatalf("extractText voice = %q, want %q", got, "transcribed text")
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

func TestWrapCopyFriendlyLines_ShortLine(t *testing.T) {
	input := "short line"
	if got := wrapCopyFriendlyLines(input); got != input {
		t.Fatalf("short line should not be wrapped, got %q", got)
	}
}

func TestWrapCopyFriendlyLines_LongLine(t *testing.T) {
	input := strings.Repeat("hello ", 30) // ~180 chars
	got := wrapCopyFriendlyLines(input)
	lines := strings.Split(got, "\n")
	if len(lines) == 1 {
		t.Fatal("expected long line to be wrapped")
	}
	for _, l := range lines {
		if len(l) > weixinCopyLineWidth+5 { // allow small fudge
			t.Fatalf("line too long: %d chars", len(l))
		}
	}
}

func TestWrapLine_ChineseNotCorrupted(t *testing.T) {
	input := strings.Repeat("瑞", 100)
	lines := wrapLine(input, 121)
	for i, l := range lines {
		if !utf8.ValidString(l) {
			t.Errorf("line %d is not valid UTF-8: %q", i, l)
		}
	}
}

func TestWrapCopyFriendlyLines_CodeBlock(t *testing.T) {
	input := "before\n```\nlong line inside code block that should not be wrapped even if it exceeds 120 characters which it definitely does\n```\nafter"
	got := wrapCopyFriendlyLines(input)
	if !strings.Contains(got, input[strings.Index(input, "long"):strings.Index(input, "after")-3]) {
		t.Fatal("code block content should be preserved verbatim")
	}
}

func TestWrapCopyFriendlyLines_TableRow(t *testing.T) {
	input := "| col1 | col2 | col3 |"
	if got := wrapCopyFriendlyLines(input); got != input {
		t.Fatalf("table row should not be wrapped, got %q", got)
	}
}

func TestWrapCopyFriendlyLines_TableRule(t *testing.T) {
	input := "| --- | --- |"
	if got := wrapCopyFriendlyLines(input); got != input {
		t.Fatalf("table rule should not be wrapped, got %q", got)
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
	// Save to a temp dir so we don't pollute ~/.gbot
	origHome := os.Getenv("HOME")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
	})
	tmpHome := t.TempDir()
	_ = os.Setenv("HOME", tmpHome)

	state := &State{
		AccountID:     "test-account",
		Token:         "test-token",
		BaseURL:       "https://test.url",
		SyncBuf:       "test-sync-buf",
		ContextTokens: map[string]string{"user1": "token1"},
		EngineID:      "wechat-test",
	}

	if err := SaveState(state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	loaded, err := LoadState()
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
	origHome := os.Getenv("HOME")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
	})
	tmpHome := t.TempDir()
	_ = os.Setenv("HOME", tmpHome)

	loaded, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState for non-existent file: %v", err)
	}
	if loaded != nil {
		t.Fatal("expected nil for non-existent state file")
	}
}

func TestStatePath_Format(t *testing.T) {
	origHome := os.Getenv("HOME")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
	})
	_ = os.Setenv("HOME", "/home/testuser")

	path, err := StatePath()
	if err != nil {
		t.Fatalf("StatePath: %v", err)
	}
	want := "/home/testuser/.gbot/wechat/state.json"
	if path != want {
		t.Fatalf("StatePath = %q, want %q", path, want)
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
// wrapLine
// ---------------------------------------------------------------------------

func TestWrapLine_Short(t *testing.T) {
	got := wrapLine("short", 120)
	if len(got) != 1 || got[0] != "short" {
		t.Fatalf("wrapLine short = %v, want [short]", got)
	}
}

func TestWrapLine_Empty(t *testing.T) {
	got := wrapLine("", 120)
	if len(got) != 1 || got[0] != "" {
		t.Fatalf("wrapLine empty = %v, want [\"\"]", got)
	}
}

func TestWrapLine_WithPrefix(t *testing.T) {
	got := wrapLine("  hello world this is a very long line that should get wrapped properly at a word boundary", 20)
	if len(got) < 2 {
		t.Fatal("expected multiple wrapped lines")
	}
	for _, l := range got {
		if len(l) > 25 {
			t.Fatalf("line too long: %d chars in %q", len(l), l)
		}
	}
}

// ---------------------------------------------------------------------------
// State file permissions test
// ---------------------------------------------------------------------------

func TestSaveState_CreatesDir(t *testing.T) {
	origHome := os.Getenv("HOME")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
	})
	tmpHome := t.TempDir()
	_ = os.Setenv("HOME", tmpHome)

	state := &State{AccountID: "test", Token: "tok"}
	if err := SaveState(state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// Verify the file exists
	path := filepath.Join(tmpHome, ".gbot", "wechat", "state.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("state file was not created")
	}

	// Verify the content round-trips
	loaded, err := LoadState()
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

func (s *hubSpy) connectorUserMessages() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.events {
		if e.Type == types.EventConnectorUserMessage {
			n++
		}
	}
	return n
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

func TestHandleInbound_SetsActiveUserAndDispatchesUserMessage(t *testing.T) {
	c, spy, _ := newHandleInboundConnector()
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
	if got := spy.connectorUserMessages(); got != 1 {
		t.Fatalf("connector user messages dispatched = %d, want 1", got)
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
	long := strings.Repeat("这是一段长文本。", 300) // ~3000 chars, exceeds 2000 limit
	chunks := splitForWeChat(long)
	if len(chunks) < 2 {
		t.Fatalf("long message: got %d chunks, want >= 2", len(chunks))
	}
	for i, c := range chunks {
		if len([]rune(c)) > 2000 {
			t.Errorf("chunk %d length = %d runes, want <= 2000", i, len([]rune(c)))
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
	// Code block exceeds 2000 runes — must split, reopening fences so
	// each chunk is valid markdown.
	codeBody := strings.Repeat("x", 2500)
	code := "```go\n" + codeBody + "\n```"
	chunks := splitForWeChat(code)
	if len(chunks) < 2 {
		t.Fatalf("large code block: got %d chunks, want >= 2", len(chunks))
	}
	for i, c := range chunks {
		if len([]rune(c)) > 2000 {
			t.Errorf("chunk %d: %d runes, want <= 2000", i, len([]rune(c)))
		}
		// Every chunk must have balanced fences — valid standalone markdown.
		if strings.Count(c, "```")%2 != 0 {
			t.Errorf("chunk %d has unbalanced fences (not valid markdown): %q", i, firstChars(c, 50))
		}
	}
}

func TestSplitForWeChat_LargeCodeBlock_NoLanguageTag(t *testing.T) {
	// Plain code fence (no language) — reopen should use bare ```.
	codeBody := strings.Repeat("x", 2500)
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

func firstChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// sendWeChatReply splits long replies into multiple WeChat messages.
// This catches the real production issue: WeChat silently truncates or drops
// messages over ~2000 chars, so the user never sees the full reply.
func TestSendWeChatReply_LongReply_SendsMultipleMessages(t *testing.T) {
	var sentTexts []string
	c := &WeChatConnector{
		sendToUserFn: func(_ context.Context, _, text string) error {
			sentTexts = append(sentTexts, text)
			return nil
		},
	}
	longReply := strings.Repeat("这是回复内容。", 300) // ~2100 chars

	c.sendWeChatReply(context.Background(), "user1", longReply)

	if len(sentTexts) < 2 {
		t.Fatalf("long reply should send multiple messages, got %d", len(sentTexts))
	}
	for i, text := range sentTexts {
		if len([]rune(text)) > 2000 {
			t.Errorf("message %d: %d runes, want <= 2000 (WeChat truncates)", i, len([]rune(text)))
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
