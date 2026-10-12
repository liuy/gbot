package wechat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

// ---- slog capture (same shape as pkg/lsp; kept local to this package) ----

type sweepLogRec struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

type sweepLogHandler struct {
	mu   sync.Mutex
	recs []sweepLogRec
}

func (h *sweepLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *sweepLogHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, sweepLogRec{level: r.Level, msg: r.Message, attrs: attrs})
	h.mu.Unlock()
	return nil
}

func (h *sweepLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *sweepLogHandler) WithGroup(string) slog.Handler      { return h }

func (h *sweepLogHandler) find(msg string) []sweepLogRec {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []sweepLogRec
	for _, r := range h.recs {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

func sweepCaptureLogs(t *testing.T) *sweepLogHandler {
	t.Helper()
	h := &sweepLogHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// ---- types.go: protocol constants --------------------------------------

// AppClientVer is packed as (major<<16)|(minor<<8); a mutated shift or
// operator is sent verbatim to iLink and the API rejects the client.
func TestProtocolConstants(t *testing.T) {
	if AppClientVer != 131584 {
		t.Errorf("AppClientVer = %d, want 131584 (2<<16 | 2<<8)", AppClientVer)
	}
	if ChannelVersion != "2.2.0" {
		t.Errorf("ChannelVersion = %q, want 2.2.0", ChannelVersion)
	}
	if ILINKBaseURL != "https://ilinkai.weixin.qq.com" {
		t.Errorf("ILINKBaseURL = %q", ILINKBaseURL)
	}
	if CDNBaseURL != "https://novac2c.cdn.weixin.qq.com/c2c" {
		t.Errorf("CDNBaseURL = %q", CDNBaseURL)
	}
	endpoints := map[string]string{
		EPGetUpdates:   "ilink/bot/getupdates",
		EPSendMessage:  "ilink/bot/sendmessage",
		EPSendTyping:   "ilink/bot/sendtyping",
		EPGetConfig:    "ilink/bot/getconfig",
		EPGetUploadURL: "ilink/bot/getuploadurl",
		EPGetBotQR:     "ilink/bot/get_bot_qrcode",
		EPGetQRStatus:  "ilink/bot/get_qrcode_status",
	}
	for got, want := range endpoints {
		if got != want {
			t.Errorf("endpoint = %q, want %q", got, want)
		}
	}
	items := map[int]int{ItemText: 1, ItemImage: 2, ItemVoice: 3, ItemFile: 4, ItemVideo: 5}
	for got, want := range items {
		if got != want {
			t.Errorf("item type = %d, want %d", got, want)
		}
	}
	media := map[int]int{MediaImage: 1, MediaVideo: 2, MediaFile: 3, MediaVoice: 4}
	for got, want := range media {
		if got != want {
			t.Errorf("media type = %d, want %d", got, want)
		}
	}
	if MsgTypeUser != 1 || MsgTypeBot != 2 || MsgStateFinish != 2 {
		t.Errorf("message type/state = %d/%d/%d, want 1/2/2", MsgTypeUser, MsgTypeBot, MsgStateFinish)
	}
	if TypingStart != 1 || TypingStop != 2 {
		t.Errorf("typing status = %d/%d, want 1/2", TypingStart, TypingStop)
	}
	if SessionExpiredErrCode != -14 || RateLimitErrCode != -2 {
		t.Errorf("error codes = %d/%d, want -14/-2", SessionExpiredErrCode, RateLimitErrCode)
	}
}

// ---- api.go: HTTP error preview truncation ------------------------------

func sweepErrorServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAPIPost_HTTPErrorPreviewTruncatedAt200(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"long body truncated", strings.Repeat("E", 250)},
		{"exactly 200 kept", strings.Repeat("F", 200)},
		{"short body kept", "boom"},
	}
	for _, tc := range cases {
		srv := sweepErrorServer(t, tc.body)
		_, err := apiPost(context.Background(), srv.Client(), srv.URL, "ilink/bot/sendmessage",
			map[string]any{"x": 1}, "tok", 5*time.Second)
		if err == nil {
			t.Fatalf("%s: expected error for HTTP 500", tc.name)
		}
		want := fmt.Sprintf("iLink ilink/bot/sendmessage: HTTP 500: %s", tc.body[:min(200, len(tc.body))])
		if err.Error() != want {
			t.Errorf("%s: error = %q, want %q", tc.name, err.Error(), want)
		}
	}
}

func TestAPIGet_HTTPErrorPreviewTruncatedAt200(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"long body truncated", strings.Repeat("G", 300)},
		{"exactly 200 kept", strings.Repeat("H", 200)},
		{"short body kept", "nope"},
	}
	for _, tc := range cases {
		srv := sweepErrorServer(t, tc.body)
		_, err := apiGet(context.Background(), srv.Client(), srv.URL, "ilink/bot/get_qrcode_status", 5*time.Second)
		if err == nil {
			t.Fatalf("%s: expected error for HTTP 500", tc.name)
		}
		want := fmt.Sprintf("iLink GET ilink/bot/get_qrcode_status: HTTP 500: %s", tc.body[:min(200, len(tc.body))])
		if err.Error() != want {
			t.Errorf("%s: error = %q, want %q", tc.name, err.Error(), want)
		}
	}
}

// ---- api.go: sendmessage response classification ------------------------

func TestCheckSendMessageResponse_Table(t *testing.T) {
	tests := []struct {
		name          string
		ret, errcode  int
		errmsg        string
		wantErr       error
		wantMsgPrefix string
	}{
		{"ok", 0, 0, "", nil, ""},
		{"session expired in ret", SessionExpiredErrCode, 0, "expired", ErrSessionExpired, ""},
		{"session expired in errcode", 0, SessionExpiredErrCode, "expired", ErrSessionExpired, ""},
		{"rate limit with unknown error is session", RateLimitErrCode, 0, "unknown error", ErrSessionExpired, ""},
		{"rate limit unknown error case-insensitive", 0, RateLimitErrCode, "Unknown Error", ErrSessionExpired, ""},
		{"rate limit other message", RateLimitErrCode, 0, "too many requests", ErrRateLimited, ""},
		{"rate limit errcode other message", 0, RateLimitErrCode, "slow down", ErrRateLimited, ""},
		{"unknown error without rate limit code", 0, 5, "unknown error", nil, "iLink sendmessage: ret=0 errcode=5 errmsg=unknown error"},
		{"generic failure", 7, 9, "nope", nil, "iLink sendmessage: ret=7 errcode=9 errmsg=nope"},
	}
	for _, tc := range tests {
		err := checkSendMessageResponse(tc.ret, tc.errcode, tc.errmsg)
		if tc.wantErr != nil {
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("%s: err = %v, want %v", tc.name, err, tc.wantErr)
			}
			continue
		}
		if tc.wantMsgPrefix == "" {
			if err != nil {
				t.Errorf("%s: err = %v, want nil", tc.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: err = nil, want %q", tc.name, tc.wantMsgPrefix)
			continue
		}
		if err.Error() != tc.wantMsgPrefix {
			t.Errorf("%s: err = %q, want %q", tc.name, err.Error(), tc.wantMsgPrefix)
		}
	}
}

// ---- connector.go: pickMediaItem --------------------------------------

func TestPickMediaItem_PriorityAndGuards(t *testing.T) {
	dl := &MediaRef{EncryptQueryParam: "q"}
	media := func(ref *MediaRef) *MediaItemHolder { return &MediaItemHolder{Media: ref} }

	// IMAGE wins over VIDEO and FILE.
	items := []Item{
		{Type: ItemFile, FileItem: &FileItem{Media: dl}},
		{Type: ItemVideo, VideoItem: media(dl)},
		{Type: ItemImage, ImageItem: media(dl)},
	}
	if got := pickMediaItem(items); got != &items[2] {
		t.Errorf("pickMediaItem picked %+v, want the image item", got)
	}

	// A video-typed item with a nil holder must be skipped, not dereferenced.
	items = []Item{
		{Type: ItemVideo},
		{Type: ItemFile, FileItem: &FileItem{Media: dl}},
	}
	if got := pickMediaItem(items); got != &items[1] {
		t.Errorf("pickMediaItem picked %+v, want the file item", got)
	}

	// A video holder without downloadable media must be skipped.
	items = []Item{
		{Type: ItemVideo, VideoItem: media(&MediaRef{})},
		{Type: ItemFile, FileItem: &FileItem{Media: dl}},
	}
	if got := pickMediaItem(items); got != &items[1] {
		t.Errorf("pickMediaItem picked %+v, want the file item (no CDN ref)", got)
	}

	// A non-video item carrying a video holder must not win the video pass.
	items = []Item{
		{Type: ItemImage, ImageItem: media(&MediaRef{}), VideoItem: media(dl)},
		{Type: ItemVideo, VideoItem: media(dl)},
	}
	if got := pickMediaItem(items); got != &items[1] {
		t.Errorf("pickMediaItem picked %+v, want the video-typed item", got)
	}

	// A non-file item carrying a file holder must not win the file pass.
	items = []Item{
		{Type: ItemImage, ImageItem: media(&MediaRef{}), FileItem: &FileItem{Media: dl}},
		{Type: ItemFile, FileItem: &FileItem{Media: dl}},
	}
	if got := pickMediaItem(items); got != &items[1] {
		t.Errorf("pickMediaItem picked %+v, want the file-typed item", got)
	}

	// VOICE is only picked when it carries no transcription text.
	items = []Item{
		{Type: ItemVoice, VoiceItem: &VoiceItem{Media: dl, Text: "spoken"}},
		{Type: ItemVoice, VoiceItem: &VoiceItem{Media: dl}},
	}
	if got := pickMediaItem(items); got != &items[1] {
		t.Errorf("pickMediaItem picked %+v, want the voice item without text", got)
	}

	if got := pickMediaItem(nil); got != nil {
		t.Errorf("pickMediaItem(nil) = %+v, want nil", got)
	}
}

// ---- connector.go: extractText voice transcription --------------------

func TestExtractText_VoiceTranscriptionGuards(t *testing.T) {
	got := extractText([]Item{{Type: ItemVoice, VoiceItem: &VoiceItem{Text: "hello there"}}})
	if want := "[voice transcription] hello there"; got != want {
		t.Errorf("extractText = %q, want %q", got, want)
	}

	// A voice-typed item with a nil holder must not be dereferenced.
	items := []Item{
		{Type: ItemVoice},
		{Type: ItemText, TextItem: &TextItem{Text: "hi"}},
	}
	if got := extractText(items); got != "hi" {
		t.Errorf("extractText = %q, want hi", got)
	}

	// An empty transcription must not produce a bare prefix.
	items = []Item{
		{Type: ItemVoice, VoiceItem: &VoiceItem{Media: &MediaRef{FullURL: "u"}}},
		{Type: ItemText, TextItem: &TextItem{Text: "hi"}},
	}
	if got := extractText(items); got != "hi" {
		t.Errorf("extractText = %q, want hi", got)
	}

	// A non-voice item carrying a voice holder must not be read as transcription.
	items = []Item{{Type: ItemFile, FileItem: &FileItem{FileName: "a.pdf"}, VoiceItem: &VoiceItem{Text: "x"}}}
	if got := extractText(items); got != "" {
		t.Errorf("extractText = %q, want empty", got)
	}

	// A voice holder with text but no text item still yields the transcription.
	items = []Item{{Type: ItemVoice, VoiceItem: &VoiceItem{Text: "only"}}}
	if got := extractText(items); got != "[voice transcription] only" {
		t.Errorf("extractText = %q, want the transcription", got)
	}
}

// ---- connector.go: Handle typing-refresh and flush gates ---------------

func TestHandle_TypingRefreshGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mock := &mockTypingAPI{}
		c := &WeChatConnector{
			hub:          hub.NewHub(),
			typingCache:  newTypingTicketCache(time.Minute),
			typingAPI:    mock,
			activeUserID: "user1",
		}

		c.lastTypingRefresh = time.Now().Add(-5*time.Second + time.Nanosecond)
		c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "x"})
		if n := len(mock.getCalls()); n != 0 {
			t.Errorf("typing refreshed 1ns before the 5s gate: %d calls, want 0", n)
		}

		c.lastTypingRefresh = time.Now().Add(-5 * time.Second)
		c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "y"})
		calls := mock.getCalls()
		if len(calls) != 1 {
			t.Fatalf("typing refresh at exactly 5s: %d calls, want 1", len(calls))
		}
		if calls[0].userID != "user1" || calls[0].status != TypingStart {
			t.Errorf("typing call = %+v, want user1/TypingStart", calls[0])
		}

		// No active user: nothing to refresh.
		c.activeUserID = ""
		c.lastTypingRefresh = time.Now().Add(-time.Hour)
		c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "z"})
		if n := len(mock.getCalls()); n != 1 {
			t.Errorf("typing refreshed with no active user: %d calls, want 1 total", n)
		}

		// Zero lastTypingRefresh (query not started): nothing to refresh.
		c2 := &WeChatConnector{
			hub:          hub.NewHub(),
			typingCache:  newTypingTicketCache(time.Minute),
			typingAPI:    mock,
			activeUserID: "user1",
		}
		c2.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "w"})
		if n := len(mock.getCalls()); n != 1 {
			t.Errorf("typing refreshed with zero lastTypingRefresh: %d calls, want 1 total", n)
		}

		// Nil typingAPI must not panic and must not refresh.
		c3 := &WeChatConnector{hub: hub.NewHub(), activeUserID: "user1"}
		c3.lastTypingRefresh = time.Now().Add(-time.Hour)
		c3.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "v"})
		if n := len(mock.getCalls()); n != 1 {
			t.Errorf("typing refreshed with nil typingAPI: %d calls, want 1 total", n)
		}
	})
}

func TestHandle_TextEndFlushGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var sent []string
		var mu sync.Mutex
		c := &WeChatConnector{
			hub:          hub.NewHub(),
			typingCache:  newTypingTicketCache(time.Minute),
			activeUserID: "user1",
			sendToUserFn: func(_ context.Context, _, text string) error {
				mu.Lock()
				sent = append(sent, text)
				mu.Unlock()
				return nil
			},
		}
		c.textBuffer.WriteString("chunk")

		c.lastFlush = time.Now().Add(-5*time.Second + time.Nanosecond)
		c.Handle(types.QueryEvent{Type: types.EventTextEnd})
		mu.Lock()
		n := len(sent)
		mu.Unlock()
		if n != 0 {
			t.Errorf("flush fired 1ns before the 5s gate: %q, want no send", sent)
		}

		c.lastFlush = time.Now().Add(-5 * time.Second)
		c.Handle(types.QueryEvent{Type: types.EventTextEnd})
		mu.Lock()
		sentCopy := append([]string(nil), sent...)
		mu.Unlock()
		if len(sentCopy) != 1 {
			t.Fatalf("flush at exactly 5s: %q, want exactly one send", sentCopy)
		}
		if sentCopy[0] != "chunk" {
			t.Errorf("flushed text = %q, want chunk", sentCopy[0])
		}
	})
}

// ---- queue.go: content vs text dispatch --------------------------------

func TestHandleInbound_ContentVsTextDispatch(t *testing.T) {
	// reached is signalled from inside the query stubs, which handleInbound
	// calls synchronously — so it proves the goroutine parked on the
	// done/ctx.Done select before cancel() is allowed to fire.
	newC := func(withContent bool) (*WeChatConnector, *int, *[]types.ContentBlock, *string, chan struct{}) {
		c := &WeChatConnector{
			hub:         hub.NewHub(),
			typingCache: newTypingTicketCache(time.Minute),
			textBuffer:  strings.Builder{},
		}
		c.state = &State{AccountID: "bot", Token: "t", BaseURL: "http://x"}
		var textCalls int
		var contentBlocks []types.ContentBlock
		var gotText string
		reached := make(chan struct{}, 1)
		c.queryFn = func(_ context.Context, text, _ string) {
			textCalls++
			gotText = text
			reached <- struct{}{}
		}
		if withContent {
			c.queryWithContentFn = func(_ context.Context, blocks []types.ContentBlock, _ string) {
				contentBlocks = blocks
				reached <- struct{}{}
			}
		}
		return c, &textCalls, &contentBlocks, &gotText, reached
	}

	run := func(c *WeChatConnector, reached chan struct{}, msg inboundMessage) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() {
			c.handleInbound(ctx, msg)
			close(done)
		}()
		select {
		case <-reached:
		case <-time.After(2 * time.Second):
			t.Fatal("query fn never called")
		}
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("handleInbound did not return after ctx cancel")
		}
	}

	// Content blocks present → QueryWithContent path.
	c, textCalls, contentBlocks, _, reached := newC(true)
	blocks := []types.ContentBlock{types.NewTextBlock("doc block")}
	run(c, reached, inboundMessage{userID: "u1", text: "raw text", content: blocks})
	if *textCalls != 0 {
		t.Errorf("queryFn called %d times with content blocks present, want 0", *textCalls)
	}
	if len(*contentBlocks) != 1 || (*contentBlocks)[0].Text != "doc block" {
		t.Errorf("queryWithContentFn got %+v, want the one doc block", *contentBlocks)
	}

	// No content blocks → text path only.
	c, textCalls, contentBlocks, gotText, reached := newC(true)
	run(c, reached, inboundMessage{userID: "u1", text: "plain question"})
	if *textCalls != 1 {
		t.Errorf("queryFn called %d times for a text-only message, want 1", *textCalls)
	}
	if *gotText != "plain question" {
		t.Errorf("queryFn text = %q, want plain question", *gotText)
	}
	if len(*contentBlocks) != 0 {
		t.Errorf("queryWithContentFn called with no content blocks: %+v", *contentBlocks)
	}

	// Content present but no content-capable query fn → fall back to text.
	c, textCalls, _, gotText, reached = newC(false)
	run(c, reached, inboundMessage{userID: "u1", text: "fallback", content: blocks})
	if *textCalls != 1 {
		t.Errorf("queryFn called %d times without queryWithContentFn, want 1", *textCalls)
	}
	if *gotText != "fallback" {
		t.Errorf("queryFn text = %q, want fallback", *gotText)
	}
}

// ---- queue.go: retry logging ------------------------------------------

func TestSendToUser_RetryLogAttemptAndBackoff(t *testing.T) {
	logs := sweepCaptureLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	c := &WeChatConnector{
		hub:         hub.NewHub(),
		typingCache: newTypingTicketCache(time.Minute),
	}
	c.state = &State{AccountID: "bot", Token: "t", BaseURL: "http://x"}

	var calls atomic.Int32
	c.sendMsgFn = func(_ context.Context, _ *http.Client, _, _, _, _, _, _, _ string) error {
		calls.Add(1)
		cancel() // stop before the second attempt's sleep
		return errors.New("connection refused")
	}

	err := c.sendToUser(ctx, "user1", "hello")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("sendToUser err = %v, want context.Canceled", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("sendMsgFn called %d times, want 1 before ctx cancel", n)
	}

	recs := logs.find("wechat: send retry")
	if len(recs) != 1 {
		t.Fatalf("send retry logged %d times, want 1", len(recs))
	}
	if got, ok := recs[0].attrs["attempt"].(int64); !ok || got != 1 {
		t.Errorf("attempt attr = %#v, want 1", recs[0].attrs["attempt"])
	}
	backoff, ok := recs[0].attrs["backoff"].(time.Duration)
	if !ok {
		t.Fatalf("backoff attr = %#v, want time.Duration", recs[0].attrs["backoff"])
	}
	if backoff < time.Second || backoff >= 1500*time.Millisecond {
		t.Errorf("backoff = %v, want 1s plus at most 500ms jitter", backoff)
	}
	if recs[0].level != slog.LevelWarn {
		t.Errorf("retry log level = %v, want Warn", recs[0].level)
	}
}

// ---- poll.go: session-expired branch selection ------------------------

func TestPollLoop_SessionExpiredPausesTenMinutesNotBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, tc := range []struct {
			name string
			resp *GetUpdatesResponse
		}{
			{"ret session expired", &GetUpdatesResponse{Ret: SessionExpiredErrCode}},
			{"errcode session expired", &GetUpdatesResponse{ErrCode: SessionExpiredErrCode}},
			{"stale session", &GetUpdatesResponse{Ret: RateLimitErrCode, ErrMsg: "unknown error"}},
			{"stale session via errcode", &GetUpdatesResponse{ErrCode: RateLimitErrCode, ErrMsg: "unknown error"}},
		} {
			c := newPollLoopConnector(t)
			var calls atomic.Int32
			c.getUpdatesFn = func(_ context.Context, _ *http.Client, _, _, _ string, _ time.Duration) (*GetUpdatesResponse, error) {
				calls.Add(1)
				return tc.resp, nil
			}

			ctx, cancel := context.WithCancel(context.Background())
			startPollLoop(t, c, ctx)

			time.Sleep(11 * time.Minute)
			cancel()
			c.pollWg.Wait()

			// The session-expired branch sleeps 10 minutes; the generic error
			// branch sleeps at most 30s and would have polled hundreds more times.
			if got := calls.Load(); got != 2 {
				t.Errorf("%s: getUpdates called %d times in 11min, want exactly 2 (10-minute pause)", tc.name, got)
			}
		}
	})
}

// The error-response branch always backs off 30s, but it must reset the shared
// failure counter at MaxConsecutiveFailures — otherwise the following transport
// errors skip the short retry delay.
func TestPollLoop_ErrorResponseCounterResetsAfterMax(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newPollLoopConnector(t)
		var calls atomic.Int32
		c.getUpdatesFn = func(_ context.Context, _ *http.Client, _, _, _ string, _ time.Duration) (*GetUpdatesResponse, error) {
			if calls.Add(1) <= 3 {
				return &GetUpdatesResponse{Ret: -1, ErrMsg: "boom"}, nil
			}
			return nil, errors.New("connection refused")
		}

		ctx, cancel := context.WithCancel(context.Background())
		startPollLoop(t, c, ctx)

		time.Sleep(100 * time.Second)
		cancel()
		c.pollWg.Wait()

		// Error responses at t=0,30,60 (3rd resets the counter), then transport
		// errors at 90,92,94 (3rd takes the 30s backoff) → 6 calls by t=100.
		if got := calls.Load(); got != 6 {
			t.Errorf("poll calls in 100s = %d, want 6 (failure counter must reset at max)", got)
		}
	})
}

// ---- format.go: splitForWeChat chunking -------------------------------

func TestSplitForWeChat_ExactFitBoundary(t *testing.T) {
	lines := make([]string, 45)
	for i := range lines {
		lines[i] = fmt.Sprintf("%099d", i) // 99 runes, lineLen 100
	}
	text := strings.Join(lines, "\n")

	got := splitForWeChat(text)
	want := []string{strings.Join(lines[:40], "\n"), strings.Join(lines[40:], "\n")}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk %d differs: got %d runes starting %q, want %d runes starting %q",
				i, len([]rune(got[i])), got[i][:20], len([]rune(want[i])), want[i][:20])
		}
	}
}

func TestSplitForWeChat_TwoLineOverflow(t *testing.T) {
	first := strings.Repeat("a", 3000)
	second := strings.Repeat("b", 2000)
	got := splitForWeChat(first + "\n" + second)
	want := []string{first, second}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("splitForWeChat = %d chunks (%q…), want [%q %q] boundaries",
			len(got), strings.Join(got, "|")[:40], first[:20], second[:20])
	}
}

func TestSplitForWeChat_HardSplitThenCodeBlockKeepsFence(t *testing.T) {
	big := strings.Repeat("c", 4001)
	text := big + "\n```go\ntail"

	got := splitForWeChat(text)
	want := []string{big[:4000], big[4000:], "```go\ntail\n```"}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk %d = %q…, want %q…", i, got[i][:20], want[i][:20])
		}
	}
}

func TestSplitForWeChat_HardSplitInsideCodeBlockClosesLoneFence(t *testing.T) {
	big := strings.Repeat("d", 4001)
	text := "```go\n" + big + "\n```\ntail"

	got := splitForWeChat(text)
	want := []string{
		"```go\n```",
		"```go\n" + big[:3990] + "\n```",
		"```go\n" + big[3990:] + "\n```",
		"tail",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk %d = %q…, want %q…", i, got[i][:20], want[i][:20])
		}
	}
}

// The reopened fence's length is counted into the running chunk size, which
// decides where the next break lands.
func TestSplitForWeChat_ReopenedFenceCountsTowardBudget(t *testing.T) {
	x := strings.Repeat("e", 3990)
	y := "why"

	zFlush := strings.Repeat("f", 3990) // 3991 with newline: 10+3991 > 4000 → break
	got := splitForWeChat("```go\n" + x + "\n" + y + "\n" + zFlush + "\n```")
	// The closing fence flips inCodeBlock before the flush decision, so the
	// chunk it triggers is not fence-closed.
	want := []string{
		"```go\n" + x + "\n```",
		"```go\n" + y + "\n```",
		"```go\n" + zFlush,
		"```",
	}
	if len(got) != len(want) {
		t.Fatalf("flush case: got %d chunks, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("flush case chunk %d = %q…, want %q…", i, got[i][:20], want[i][:20])
		}
	}

	zFit := strings.Repeat("g", 3989) // 3990 with newline: 10+3990 == 4000 → no break
	got = splitForWeChat("```go\n" + x + "\n" + y + "\n" + zFit + "\n```")
	want = []string{
		"```go\n" + x + "\n```",
		"```go\n" + y + "\n" + zFit,
		"```",
	}
	if len(got) != len(want) {
		t.Fatalf("fit case: got %d chunks, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("fit case chunk %d = %q…, want %q…", i, got[i][:20], want[i][:20])
		}
	}
}

// Text at or under the rune limit is returned verbatim — no trimming, no split.
func TestSplitForWeChat_UnderLimitIsVerbatim(t *testing.T) {
	exact := strings.Repeat("h", 2000) + "\n" + strings.Repeat("i", 1999) // 4000 runes
	got := splitForWeChat(exact)
	if len(got) != 1 || got[0] != exact {
		t.Errorf("splitForWeChat(4000 runes) = %d chunks, want the text verbatim", len(got))
	}

	got = splitForWeChat("  keep me  ")
	if len(got) != 1 || got[0] != "  keep me  " {
		t.Errorf("splitForWeChat(short) = %q, want the text verbatim (no trim)", got)
	}

	if got := splitForWeChat(""); got != nil {
		t.Errorf("splitForWeChat(\"\") = %q, want nil", got)
	}
}

// A line whose length plus newline is exactly the limit is not hard-split;
// inside a code block that decides whether the fence is reopened per chunk.
func TestSplitForWeChat_LineExactlyAtLimitInCodeBlock(t *testing.T) {
	line := strings.Repeat("j", 3999) // lineLen == 4000 → normal path
	got := splitForWeChat("```go\n" + line + "\n```")
	want := []string{"```go\n```", "```go\n" + line, "```"}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk %d = %q…, want %q…", i, got[i][:20], want[i][:20])
		}
	}
}

// A hard split on an exact multiple of the limit must not emit an empty chunk.
func TestSplitForWeChat_HardSplitExactMultipleNoEmptyChunk(t *testing.T) {
	line := strings.Repeat("k", 8000)
	got := splitForWeChat(line)
	want := []string{line[:4000], line[4000:]}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d: %q", len(got), len(want), chunkHeads(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk %d differs (got %d runes, want %d runes)", i, len([]rune(got[i])), len([]rune(want[i])))
		}
	}
}

// Same inside a code block: the reopened fence and its closing take budget,
// so an exact 2×budget line yields exactly two fenced chunks.
func TestSplitForWeChat_CodeBlockHardSplitExactBudgetMultiple(t *testing.T) {
	line := strings.Repeat("l", 7980) // 2 × (4000 - len("```go\n") - len("\n```"))
	got := splitForWeChat("```go\n" + line + "\n```")
	want := []string{
		"```go\n```",
		"```go\n" + line[:3990] + "\n```",
		"```go\n" + line[3990:] + "\n```",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d: %q", len(got), len(want), chunkHeads(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk %d differs (got %d runes, want %d runes)", i, len([]rune(got[i])), len([]rune(want[i])))
		}
	}
}

// A fence tag so long that reopening it leaves no budget falls back to plain
// rune slicing without any fence decoration.
func TestSplitForWeChat_FenceTagTooLongForReopenBudget(t *testing.T) {
	lang := strings.Repeat("m", 3993) // overhead 8+3993 > 4000 → budget < 1
	line := strings.Repeat("n", 8000)
	got := splitForWeChat("```" + lang + "\n" + line + "\n```")
	want := []string{
		"```" + lang + "\n```",
		line[:4000],
		line[4000:],
	}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d: %q", len(got), len(want), chunkHeads(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk %d differs (got %d runes, want %d runes)", i, len([]rune(got[i])), len([]rune(want[i])))
		}
	}
}

func chunkHeads(chunks []string) []string {
	heads := make([]string, len(chunks))
	for i, c := range chunks {
		r := []rune(c)
		if len(r) > 12 {
			c = string(r[:12])
		}
		heads[i] = fmt.Sprintf("%d:%q", len(r), c)
	}
	return heads
}
