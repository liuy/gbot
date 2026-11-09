package wechat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

type pinPollCall struct {
	at      time.Duration
	syncBuf string
	timeout time.Duration
}

// TestPinPollLoopTimeline pins the whole retry/backoff/session cadence of the
// long-poll loop: every delay, threshold and reset in pollLoop is a call time
// here, so a shifted constant moves the recorded schedule.
func TestPinPollLoopTimeline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newPollLoopConnector(t)
		var mu sync.Mutex
		var calls []pinPollCall
		script := []struct {
			resp *GetUpdatesResponse
			err  error
		}{
			{nil, errors.New("net down")},                                          // 1 failure → retry
			{nil, errors.New("net down")},                                          // 2 → retry
			{nil, errors.New("net down")},                                          // 3 → backoff + reset
			{nil, errors.New("net down")},                                          // 1 → retry
			{nil, errors.New("net down")},                                          // 2 → retry
			{nil, errors.New("net down")},                                          // 3 → backoff + reset
			{nil, errors.New("net down")},                                          // 1 → retry
			{nil, errors.New("net down")},                                          // 2 → retry
			{&GetUpdatesResponse{LongPollingMs: 1234, GetUpdatesBuf: "buf1"}, nil}, // success resets
			{nil, errors.New("net down")},                                          // 1 → retry
			{nil, errors.New("net down")},                                          // 2 → retry
			{nil, errors.New("net down")},                                          // 3 → backoff + reset
			{&GetUpdatesResponse{ErrCode: 5, ErrMsg: "bad"}, nil},                  // error response → backoff
			{&GetUpdatesResponse{Ret: 5, ErrMsg: "worse"}, nil},                    // count keeps rising
			{&GetUpdatesResponse{LongPollingMs: 0}, nil},                           // 0 keeps the old timeout
			{&GetUpdatesResponse{LongPollingMs: 1}, nil},                           // 1 replaces it
			{nil, errors.New("net down")},                                          // 1 → retry
			{nil, errors.New("net down")},                                          // 2 → retry
			{&GetUpdatesResponse{Ret: SessionExpiredErrCode}, nil},                 // session → pause + reset
			{nil, errors.New("net down")},                                          // 1 → retry
			{nil, errors.New("net down")},                                          // 2 → retry
			{nil, errors.New("net down")},                                          // 3 → backoff + reset
			{nil, errors.New("net down")},                                          // parks in a retry sleep
		}
		var idx int
		start := time.Now()
		c.getUpdatesFn = func(_ context.Context, _ *http.Client, _, _, syncBuf string, timeout time.Duration) (*GetUpdatesResponse, error) {
			mu.Lock()
			calls = append(calls, pinPollCall{at: time.Since(start), syncBuf: syncBuf, timeout: timeout})
			i := idx
			idx++
			mu.Unlock()
			if i >= len(script) {
				return nil, errors.New("net down")
			}
			return script[i].resp, script[i].err
		}

		ctx, cancel := context.WithCancel(context.Background())
		logs := pinCaptureSlog(t, func() {
			startPollLoop(t, c, ctx)
			time.Sleep(805 * time.Second)
			cancel()
			c.pollWg.Wait()
		})

		want := []time.Duration{
			0, 2 * time.Second, 4 * time.Second,
			34 * time.Second, 36 * time.Second, 38 * time.Second,
			68 * time.Second, 70 * time.Second, 72 * time.Second, 72 * time.Second,
			74 * time.Second, 76 * time.Second,
			106 * time.Second, 136 * time.Second,
			166 * time.Second, 166 * time.Second, 166 * time.Second,
			168 * time.Second, 170 * time.Second,
			770 * time.Second, 772 * time.Second, 774 * time.Second,
			804 * time.Second,
		}
		mu.Lock()
		defer mu.Unlock()
		if len(calls) != len(want) {
			t.Fatalf("poll calls = %d, want %d\nschedule=%v", len(calls), len(want), pinCallTimes(calls))
		}
		for i, w := range want {
			if calls[i].at != w {
				t.Errorf("call[%d] at %v, want %v (full schedule=%v)", i, calls[i].at, w, pinCallTimes(calls))
			}
		}
		// The sync buffer advances from the first response that carries one.
		for i, cl := range calls {
			wantBuf := ""
			if i >= 9 {
				wantBuf = "buf1"
			}
			if cl.syncBuf != wantBuf {
				t.Errorf("call[%d] syncBuf = %q, want %q", i, cl.syncBuf, wantBuf)
			}
		}
		// LongPollingMs replaces the timeout only when it is strictly positive.
		for i, cl := range calls {
			wantTO := time.Duration(LongPollTimeoutMs) * time.Millisecond
			switch {
			case i >= 16:
				wantTO = time.Millisecond
			case i >= 9:
				wantTO = 1234 * time.Millisecond
			}
			if cl.timeout != wantTO {
				t.Errorf("call[%d] timeout = %v, want %v", i, cl.timeout, wantTO)
			}
		}
		for _, wantLog := range []string{
			"msg=\"wechat: poll error\" error=\"net down\" failures=1 max=3",
			"backoff after consecutive failures",
			"msg=\"wechat: poll error response\" ret=0 errcode=5 errmsg=bad failures=1",
			"msg=\"wechat: poll error response\" ret=5 errcode=0 errmsg=worse failures=2",
			"session expired, pausing for 10 minutes",
		} {
			if !strings.Contains(logs, wantLog) {
				t.Errorf("logs missing %q:\n%s", wantLog, logs)
			}
		}
		if c.state.SyncBuf != "buf1" {
			t.Errorf("state.SyncBuf = %q, want buf1", c.state.SyncBuf)
		}
		// The advance must also reach disk, not just the in-memory state.
		st, err := LoadState(c.state.AccountID, c.projectDir)
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st == nil {
			t.Fatal("no state file written by pollLoop")
		}
		if st.SyncBuf != "buf1" {
			t.Errorf("saved SyncBuf = %q, want buf1", st.SyncBuf)
		}
	})
}

func pinCallTimes(calls []pinPollCall) []time.Duration {
	out := make([]time.Duration, len(calls))
	for i, cl := range calls {
		out[i] = cl.at
	}
	return out
}

// pollLoop must hold the WaitGroup for its whole run so shutdown waits.
func TestPinPollLoopHoldsWaitGroup(t *testing.T) {
	c := newPollLoopConnector(t)
	release := make(chan struct{})
	c.getUpdatesFn = func(_ context.Context, _ *http.Client, _, _, _ string, _ time.Duration) (*GetUpdatesResponse, error) {
		<-release
		return &GetUpdatesResponse{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	startPollLoop(t, c, ctx)

	pending := make(chan struct{})
	go func() {
		c.pollWg.Wait()
		close(pending)
	}()
	select {
	case <-pending:
		t.Fatal("pollWg released while pollLoop is still running: Done is not deferred")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	cancel()
	select {
	case <-pending:
	case <-time.After(2 * time.Second):
		t.Fatal("pollWg still pending after ctx cancel")
	}
}

// Cancelling during any of the loop's sleeps must end it at once; a select
// without the ctx case waits out the whole delay first.
func TestPinPollLoopCancelDuringSleep(t *testing.T) {
	cases := []struct {
		name     string
		resp     *GetUpdatesResponse
		errs     int
		cancelAt time.Duration
	}{
		{"retry delay", nil, 1, time.Second},
		{"failure backoff", nil, 3, 5 * time.Second},
		{"error-response backoff", &GetUpdatesResponse{Ret: 7, ErrMsg: "x"}, 0, 5 * time.Second},
		{"session pause", &GetUpdatesResponse{Ret: SessionExpiredErrCode}, 0, 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := newPollLoopConnector(t)
				var served int
				c.getUpdatesFn = func(_ context.Context, _ *http.Client, _, _, _ string, _ time.Duration) (*GetUpdatesResponse, error) {
					if tc.resp != nil && served == 0 {
						served++
						return tc.resp, nil
					}
					if served < tc.errs {
						served++
						return nil, errors.New("net down")
					}
					return &GetUpdatesResponse{}, nil
				}
				ctx, cancel := context.WithCancel(context.Background())
				start := time.Now()
				startPollLoop(t, c, ctx)
				time.Sleep(tc.cancelAt)
				cancel()
				c.pollWg.Wait()
				if got := time.Since(start); got != tc.cancelAt {
					t.Errorf("%s: loop returned after %v, want %v (the sleep must be interruptible)", tc.name, got, tc.cancelAt)
				}
			})
		})
	}
}

// pollLoop must hand its context to the batch processor: a nil ctx makes the
// media download's request construction fail, so the turn never arrives.
func TestPinPollLoopThreadsContextToBatch(t *testing.T) {
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(encryptAesEcbForMediaTest(realPNGBytes, []byte("0123456789abcdef")))
	}))
	defer srv.Close()

	var calls int
	c.getUpdatesFn = func(_ context.Context, _ *http.Client, _, _, _ string, _ time.Duration) (*GetUpdatesResponse, error) {
		calls++
		if calls == 1 {
			return &GetUpdatesResponse{Msgs: []Message{pinMediaMessage("m1", "userA", srv.URL, "")}}, nil
		}
		return nil, errors.New("no more updates")
	}
	ctx, cancel := context.WithCancel(context.Background())
	startPollLoop(t, c, ctx)
	var im inboundMessage
	select {
	case im = <-c.inboundCh:
	case <-time.After(3 * time.Second):
		t.Fatal("no media turn enqueued: the download did not get the loop's ctx")
	}
	cancel()
	done := make(chan struct{})
	go func() {
		c.pollWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pollLoop did not stop after ctx cancel")
	}
	if len(im.content) != 2 {
		t.Fatalf("enqueued content = %d blocks, want image + prompt (%+v)", len(im.content), im)
	}
}

// A stale-session response (rate-limit code with the "unknown error" text) is
// a session pause, not a plain error; any other rate-limit code is.
func TestPinIsStaleSession(t *testing.T) {
	cases := []struct {
		ret, errcode int
		msg          string
		want         bool
	}{
		{RateLimitErrCode, 0, "unknown error", true},
		{0, RateLimitErrCode, "unknown error", true},
		{RateLimitErrCode, 0, "rate limited", false},
		{0, 0, "unknown error", false},
		{SessionExpiredErrCode, SessionExpiredErrCode, "unknown error", false},
	}
	for _, tc := range cases {
		if got := isStaleSession(tc.ret, tc.errcode, tc.msg); got != tc.want {
			t.Errorf("isStaleSession(%d,%d,%q) = %v, want %v", tc.ret, tc.errcode, tc.msg, got, tc.want)
		}
	}
}

// pinMediaMessage builds a one-image message served from url.
func pinMediaMessage(id, user, url, token string) Message {
	return Message{
		FromUserID:   user,
		MessageID:    FlexString(id),
		ContextToken: token,
		ItemList: []Item{
			{Type: ItemImage, ImageItem: &MediaItemHolder{Media: &MediaRef{FullURL: url, AesKey: "MDEyMzQ1Njc4OWFiY2RlZg=="}}},
		},
	}
}

func pinTextMessage(id, user, text, token string) Message {
	return Message{
		FromUserID:   user,
		MessageID:    FlexString(id),
		ContextToken: token,
		ItemList:     []Item{{Type: ItemText, TextItem: &TextItem{Text: text}}},
	}
}

func pinDrain(c *WeChatConnector) []inboundMessage {
	var out []inboundMessage
	for {
		select {
		case im := <-c.inboundCh:
			out = append(out, im)
		default:
			return out
		}
	}
}

// Media from one user merges into a single inbound message; a different user
// flushes the pending media first.
func TestPinProcessBatchMediaMerge(t *testing.T) {
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(encryptAesEcbForMediaTest(realPNGBytes, []byte("0123456789abcdef")))
	}))
	defer srv.Close()

	c.processBatch(context.Background(), []Message{
		pinMediaMessage("m1", "userA", srv.URL, "tokA"),
		pinMediaMessage("m2", "userA", srv.URL, "tokA2"),
		pinMediaMessage("m3", "userB", srv.URL, "tokB"),
		pinMediaMessage("m4", "userC", srv.URL, ""),
	})
	got := pinDrain(c)
	if len(got) != 3 {
		t.Fatalf("enqueued messages = %d, want 3 (one per user, userA merged)", len(got))
	}
	if got[0].userID != "userA" || len(got[0].content) != 3 {
		t.Errorf("userA turn = user %q, %d blocks; want userA with 2 images + prompt", got[0].userID, len(got[0].content))
	}
	if got[1].userID != "userB" || len(got[1].content) != 2 {
		t.Errorf("userB turn = user %q, %d blocks; want userB with 1 image + prompt", got[1].userID, len(got[1].content))
	}
	if tok := c.getContextToken("userA"); tok != "tokA2" {
		t.Errorf("context token for userA = %q, want tokA2 (the merged turn's token)", tok)
	}
	if tok := c.getContextToken("userB"); tok != "tokB" {
		t.Errorf("context token for userB = %q, want tokB", tok)
	}
	// userC sent no context token: the flushed token from the previous turn
	// must not be attributed to it.
	if tok := c.getContextToken("userC"); tok != "" {
		t.Errorf("context token for userC = %q, want empty", tok)
	}
}

// A voice item alongside real media is not voice-only: the media still
// downloads.
func TestPinProcessBatchVoiceWithMedia(t *testing.T) {
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(encryptAesEcbForMediaTest(realPNGBytes, []byte("0123456789abcdef")))
	}))
	defer srv.Close()

	msg := pinMediaMessage("m1", "userA", srv.URL, "")
	msg.ItemList = append(msg.ItemList, Item{Type: ItemVoice, VoiceItem: &VoiceItem{}})
	c.processBatch(context.Background(), []Message{msg})
	got := pinDrain(c)
	if len(got) != 1 || len(got[0].content) != 2 {
		t.Fatalf("enqueued = %+v, want one turn with the image plus the prompt", got)
	}
	if got[0].content[0].Type != types.ContentTypeImage {
		t.Errorf("content[0].Type = %v, want the downloaded image", got[0].content[0].Type)
	}
}

// Without a media cache a media message must degrade to its text, not vanish.
func TestPinProcessBatchNoMediaCacheDegradesToText(t *testing.T) {
	c, _ := newMediaTestConnector(t)
	c.mediaCache = nil
	c.state = &State{AccountID: "bot"}
	msg := pinMediaMessage("m1", "userA", "http://127.0.0.1:1/x", "")
	msg.ItemList = append(msg.ItemList, Item{Type: ItemText, TextItem: &TextItem{Text: "caption"}})
	c.processBatch(context.Background(), []Message{msg})
	got := pinDrain(c)
	if len(got) != 1 || got[0].text != "caption" {
		t.Fatalf("enqueued = %+v, want one text turn carrying the caption", got)
	}
}

// Voice-only messages are text turns; the bot's own messages and duplicate
// message IDs are skipped without ending the batch.
func TestPinProcessBatchSkips(t *testing.T) {
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}
	voice := Message{
		FromUserID: "userA",
		MessageID:  FlexString("v1"),
		ItemList:   []Item{{Type: ItemVoice, VoiceItem: &VoiceItem{}}},
	}
	c.processBatch(context.Background(), []Message{
		{FromUserID: "bot", MessageID: FlexString("self"), ItemList: []Item{{Type: ItemText, TextItem: &TextItem{Text: "mine"}}}},
		voice,
		pinTextMessage("t1", "userA", "hello", ""),
		pinTextMessage("t1", "userA", "hello again", ""),
		pinTextMessage("t2", "userA", "second turn", ""),
	})
	got := pinDrain(c)
	if len(got) != 2 {
		t.Fatalf("enqueued = %d (%+v), want 2: t1 and t2 (self, voice-without-text and the duplicate all drop)", len(got), got)
	}
	if got[0].text != "hello" {
		t.Errorf("first turn = %q, want hello", got[0].text)
	}
	if got[1].text != "second turn" {
		t.Errorf("second turn = %q, want the message after the duplicate", got[1].text)
	}
}

// Pending media must be flushed before a text turn from the same user.
func TestPinProcessBatchTextFlushesPendingMedia(t *testing.T) {
	c, _ := newMediaTestConnector(t)
	c.state = &State{AccountID: "bot"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(encryptAesEcbForMediaTest(realPNGBytes, []byte("0123456789abcdef")))
	}))
	defer srv.Close()

	c.processBatch(context.Background(), []Message{
		pinMediaMessage("m1", "userA", srv.URL, ""),
		pinTextMessage("t1", "userA", "what is this?", ""),
	})
	got := pinDrain(c)
	if len(got) != 2 {
		t.Fatalf("enqueued = %d, want 2 (media turn then text turn)", len(got))
	}
	if len(got[0].content) != 2 {
		t.Errorf("media turn blocks = %d, want image + prompt", len(got[0].content))
	}
	if got[1].text != "what is this?" || got[1].content != nil {
		t.Errorf("text turn = %+v, want the bare text turn", got[1])
	}
}

// Mid-query turns go to the engine attachment queue with the full item shape.
func TestPinEnqueueBusyAttachmentShape(t *testing.T) {
	eng := engine.New(&engine.Params{Provider: &blockingEngineMock{}, Model: "test-model"})
	t.Cleanup(func() { eng.Close() })
	c := New(nil, hub.NewHub())
	c.engine = eng
	c.isBusyFn = func() bool { return true }

	content := []types.ContentBlock{types.NewTextBlock("attached body")}
	c.enqueue("userA", "caption", content)

	items := eng.PendingAttachments()
	if len(items) != 1 {
		t.Fatalf("pending attachments = %d, want 1", len(items))
	}
	it := items[0]
	if it.Value != "attached body" {
		t.Errorf("Value = %q, want the attachment value built from the content blocks", it.Value)
	}
	if len(it.Content) != 1 || it.Content[0].Text != "attached body" {
		t.Errorf("Content = %+v, want the content blocks carried verbatim", it.Content)
	}
	if it.Mode != types.ItemModePrompt {
		t.Errorf("Mode = %q, want %q", it.Mode, types.ItemModePrompt)
	}
	if it.Priority != types.PriorityNext {
		t.Errorf("Priority = %q, want %q", it.Priority, types.PriorityNext)
	}
	if it.Origin == nil || it.Origin.Kind != types.OriginHuman {
		t.Errorf("Origin = %+v, want kind %q", it.Origin, types.OriginHuman)
	}
	if it.UUID == "" {
		t.Error("UUID is empty, want a generated id")
	}
	if it.Timestamp.IsZero() {
		t.Error("Timestamp is zero, want the enqueue time")
	}
}

// A full inbound channel drops the message with a warning naming the user.
func TestPinEnqueueFullChannelWarns(t *testing.T) {
	c := &WeChatConnector{inboundCh: make(chan inboundMessage, 1)}
	c.inboundCh <- inboundMessage{userID: "first"}
	logs := pinCaptureSlog(t, func() { c.enqueue("userA", "second", nil) })
	if !strings.Contains(logs, "inbound channel full") || !strings.Contains(logs, "userA") {
		t.Errorf("logs = %q, want a drop warning naming userA", logs)
	}
	if got := <-c.inboundCh; got.userID != "first" {
		t.Errorf("queued message = %+v, want the first one to survive", got)
	}
}

// attachmentValue joins block texts, degrades images, and falls back to the
// caption when the blocks carry nothing.
func TestPinAttachmentValue(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		content []types.ContentBlock
		want    string
	}{
		{"no blocks", "caption", nil, "caption"},
		{"text blocks joined", "caption", []types.ContentBlock{types.NewTextBlock("a"), types.NewTextBlock("b")}, "a\n\nb"},
		{"image degrades", "caption", []types.ContentBlock{types.NewImageBlock(types.ImageSource{Type: "base64", MediaType: "image/png", Data: "x"})}, "[image]"},
		{"empty block falls back", "caption", []types.ContentBlock{types.NewTextBlock("")}, "caption"},
	}
	for _, tc := range cases {
		if got := attachmentValue(tc.text, tc.content); got != tc.want {
			t.Errorf("%s: attachmentValue = %q, want %q", tc.name, got, tc.want)
		}
	}
}
