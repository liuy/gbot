package wechat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/media"
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

func TestExtractDocName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		name  string
		ok    bool
	}{
		{"[Document: report.pdf saved at /tmp/x]", "report.pdf", true},
		{"[Document: a.docx saved at /tmp/a.docx]", "a.docx", true},
		{"[Document: nope]", "", false},
		{"[Documents: a, b]", "", false},
		{"hello", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		got, ok := extractDocName(tc.input)
		if got != tc.name || ok != tc.ok {
			t.Errorf("extractDocName(%q) = (%q,%v), want (%q,%v)", tc.input, got, ok, tc.name, tc.ok)
		}
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
// sendWeChatReply — empty text
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
