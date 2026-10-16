package wui

// Mutation-kill tests for games.go and settings.go (batch B). Every test here
// pins an observable contract — status, headers, wire bodies, NDJSON lines,
// log records — so the mutation listed in /tmp/wui-batch-b.txt cannot survive.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// helpers (mb* prefix keeps them collision-free against the other mutate
// batches working in this package in parallel)
// ---------------------------------------------------------------------------

// mbLogHandler captures records emitted through the default slog logger.
type mbLogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *mbLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *mbLogHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *mbLogHandler) WithGroup(string) slog.Handler            { return h }
func (h *mbLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return nil
}

// mbCaptureLogs swaps the default logger for a capturing one and returns a
// finder for the records with a given message.
func mbCaptureLogs(t *testing.T) func(msg string) []map[string]any {
	t.Helper()
	h := &mbLogHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })
	return func(msg string) []map[string]any {
		h.mu.Lock()
		defer h.mu.Unlock()
		var out []map[string]any
		for _, r := range h.records {
			if r.Message != msg {
				continue
			}
			attrs := map[string]any{}
			r.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.Any(); return true })
			out = append(out, attrs)
		}
		return out
	}
}

// mbEventScript is one llm.Stream call: either raw events or a hard error.
type mbEventScript struct {
	events []llm.StreamEvent
	err    error
}

// mbEventProvider plays back raw StreamEvents and records the context of
// every call, so context propagation and event folding are observable.
type mbEventProvider struct {
	mu     sync.Mutex
	ctxs   []context.Context
	script []mbEventScript
}

func (p *mbEventProvider) Name() string { return "mb" }

func (p *mbEventProvider) Complete(context.Context, *llm.Request) (*llm.Response, error) {
	return nil, errors.New("mbEventProvider: tests drive Stream")
}

func (p *mbEventProvider) Stream(ctx context.Context, req *llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	i := len(p.ctxs)
	p.ctxs = append(p.ctxs, ctx)
	p.mu.Unlock()
	if i >= len(p.script) {
		return nil, fmt.Errorf("mbEventProvider: no script for call %d", i)
	}
	s := p.script[i]
	if s.err != nil {
		return nil, s.err
	}
	ch := make(chan llm.StreamEvent, len(s.events)+1)
	go func() {
		defer close(ch)
		for _, ev := range s.events {
			ch <- ev
		}
	}()
	return ch, nil
}

func (p *mbEventProvider) recordedCtxs() []context.Context {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]context.Context(nil), p.ctxs...)
}

func mbTextEv(s string) llm.StreamEvent {
	return llm.StreamEvent{Type: "content_block_delta", Delta: &llm.StreamDelta{Type: "text_delta", Text: s}}
}

// mbCtxKey is the sentinel key for context-propagation asserts.
type mbCtxKey struct{}

// mbPostCtx posts an observe request whose context carries a sentinel value.
func mbPostCtx(t *testing.T, observe ObserveProviderFn, body string) (int, []byte) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("POST /artifacts/{name...}", observerHandler(observe))
	req, err := http.NewRequest(http.MethodPost, "/artifacts/chess", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(context.WithValue(req.Context(), mbCtxKey{}, "sentinel"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// mbLines decodes the NDJSON reply into ordered maps.
func mbLines(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("ndjson line %q not an object: %v", line, err)
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		t.Fatalf("no ndjson lines in %q", string(body))
	}
	return out
}

func mbLine(lines []map[string]any, typ string) map[string]any {
	for _, l := range lines {
		if l["type"] == typ {
			return l
		}
	}
	return nil
}

func mbFatalf(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatalf(format, args...)
}

// ---------------------------------------------------------------------------
// games.go
// ---------------------------------------------------------------------------

func TestMutB_GamesBodyLimit(t *testing.T) {
	bodyOf := func(pad int) string {
		return `{"prompt":"你是黑方棋手","state":"` + strings.Repeat("a", pad) + `\nlegal-moves:\n马8进7"}`
	}
	// Just under the 1MiB cap: accepted and played.
	small := bodyOf(600_000)
	if len(small) <= 1<<19 || len(small) >= 1<<20 {
		mbFatalf(t, "test data size %d must sit between 2^19 and 2^20", len(small))
	}
	sp := &stubProvider{script: []stubScript{{text: "马8进7\n"}}}
	status, _, body := postObserve(t, observeStubFn(sp, "m1", true), "chess", small)
	if status != http.StatusOK {
		mbFatalf(t, "status = %d, want 200 for a body under the cap (body %q)", status, string(body)[:80])
	}
	resp := decodeObserveResp(t, body)
	if resp.Move != "马8进7" {
		mbFatalf(t, "move = %q, want 马8进7", resp.Move)
	}
	// Just over the 1MiB cap: rejected before any provider call.
	big := bodyOf(1_500_000)
	if len(big) <= 1<<20 || len(big) >= 1<<21 {
		mbFatalf(t, "test data size %d must sit between 2^20 and 2^21", len(big))
	}
	status, _, body = postObserve(t, observeStubFn(&stubProvider{}, "m1", true), "chess", big)
	if status != http.StatusRequestEntityTooLarge {
		mbFatalf(t, "status = %d, want 413 for a body over the cap", status)
	}
}

func TestMutB_GamesLogTrail(t *testing.T) {
	run := func(name string, fn func(t *testing.T, find func(string) []map[string]any)) {
		t.Run(name, func(t *testing.T) {
			find := mbCaptureLogs(t)
			fn(t, find)
		})
	}
	one := func(recs []map[string]any) map[string]any {
		if len(recs) != 1 {
			return nil
		}
		return recs[0]
	}

	run("unknown game", func(t *testing.T, find func(string) []map[string]any) {
		status, _, _ := postObserve(t, observeStubFn(nil, "", false), "nope", observeBody(t, "p", "legal-moves:\nm"))
		if status != http.StatusNotFound {
			mbFatalf(t, "status = %d, want 404", status)
		}
		rec := one(find("wui:observer_reject"))
		if rec == nil {
			mbFatalf(t, "no wui:observer_reject record for the unknown-game reject")
		}
		if rec["status"] != int64(http.StatusNotFound) || rec["name"] != "nope" {
			mbFatalf(t, "attrs = %v, want status=404 name=nope", rec)
		}
	})
	run("body too large", func(t *testing.T, find func(string) []map[string]any) {
		big := `{"prompt":"p","state":"` + strings.Repeat("a", observeMaxBody) + `"}`
		status, _, _ := postObserve(t, observeStubFn(nil, "", false), "chess", big)
		if status != http.StatusRequestEntityTooLarge {
			mbFatalf(t, "status = %d, want 413", status)
		}
		rec := one(find("wui:observer_reject"))
		if rec == nil || rec["status"] != int64(http.StatusRequestEntityTooLarge) {
			mbFatalf(t, "attrs = %v, want the 413 reject record", rec)
		}
	})
	run("invalid json", func(t *testing.T, find func(string) []map[string]any) {
		status, _, _ := postObserve(t, observeStubFn(nil, "", false), "chess", "{not json")
		if status != http.StatusBadRequest {
			mbFatalf(t, "status = %d, want 400", status)
		}
		rec := one(find("wui:observer_reject"))
		if rec == nil || rec["status"] != int64(http.StatusBadRequest) {
			mbFatalf(t, "attrs = %v, want the 400 reject record", rec)
		}
		if errText, _ := rec["err"].(string); !strings.Contains(errText, "invalid character") {
			mbFatalf(t, "err attr = %v, want the decoder message", rec["err"])
		}
	})
	run("missing prompt", func(t *testing.T, find func(string) []map[string]any) {
		status, _, _ := postObserve(t, observeStubFn(nil, "", false), "chess", observeBody(t, "", "legal-moves:\nm"))
		if status != http.StatusBadRequest {
			mbFatalf(t, "status = %d, want 400", status)
		}
		rec := one(find("wui:observer_reject"))
		if rec == nil || rec["err"] != "missing prompt" {
			mbFatalf(t, "attrs = %v, want err=missing prompt", rec)
		}
	})
	run("bad state", func(t *testing.T, find func(string) []map[string]any) {
		status, _, _ := postObserve(t, observeStubFn(nil, "", false), "chess", observeBody(t, "p", "马8进7"))
		if status != http.StatusBadRequest {
			mbFatalf(t, "status = %d, want 400", status)
		}
		rec := one(find("wui:observer_reject"))
		if rec == nil || rec["err"] != "no legal-moves: marker in state" {
			mbFatalf(t, "attrs = %v, want the marker error", rec)
		}
	})
	run("no provider", func(t *testing.T, find func(string) []map[string]any) {
		status, _, _ := postObserve(t, observeStubFn(nil, "", false), "chess", observeBody(t, "p", "legal-moves:\nm"))
		if status != http.StatusServiceUnavailable {
			mbFatalf(t, "status = %d, want 503", status)
		}
		rec := one(find("wui:observer_reject"))
		if rec == nil || rec["status"] != int64(http.StatusServiceUnavailable) {
			mbFatalf(t, "attrs = %v, want the 503 reject record", rec)
		}
	})
	run("request summary", func(t *testing.T, find func(string) []map[string]any) {
		state := "受威胁 中卒\nlegal-moves:\n马8进7\n车9平8"
		sp := &stubProvider{script: []stubScript{{text: "马8进7\n"}}}
		status, _, _ := postObserve(t, observeStubFn(sp, "m1", true), "chess", observeBody(t, "你是黑方棋手", state))
		if status != http.StatusOK {
			mbFatalf(t, "status = %d, want 200", status)
		}
		rec := one(find("wui:observer_request"))
		if rec == nil {
			mbFatalf(t, "no wui:observer_request record")
		}
		want := map[string]any{
			"name": "chess", "model": "m1", "legal": int64(2),
			"state_bytes": int64(len(state)), "eval": "受威胁 中卒",
		}
		for k, v := range want {
			if rec[k] != v {
				mbFatalf(t, "observer_request attr %q = %v, want %v (all: %v)", k, rec[k], v, rec)
			}
		}
	})
	run("first call error", func(t *testing.T, find func(string) []map[string]any) {
		sp := &stubProvider{script: []stubScript{{err: errors.New("boom1")}}}
		postObserve(t, observeStubFn(sp, "m1", true), "chess", observeBody(t, "p", observeTestState))
		rec := one(find("wui:observer_error"))
		if rec == nil || rec["attempt"] != int64(1) || rec["err"] != "boom1" {
			mbFatalf(t, "attrs = %v, want attempt=1 err=boom1", rec)
		}
	})
	run("resign final", func(t *testing.T, find func(string) []map[string]any) {
		sp := &stubProvider{script: []stubScript{{text: "认输\n这局你赢了"}}}
		postObserve(t, observeStubFn(sp, "m1", true), "chess", observeBody(t, "p", observeTestState))
		rec := one(find("wui:observer"))
		if rec == nil || rec["attempt"] != int64(1) || rec["move"] != "认输" {
			mbFatalf(t, "attrs = %v, want attempt=1 move=认输", rec)
		}
	})
	run("legal final", func(t *testing.T, find func(string) []map[string]any) {
		sp := &stubProvider{script: []stubScript{{text: "车9平8"}}}
		postObserve(t, observeStubFn(sp, "m1", true), "chess", observeBody(t, "p", observeTestState))
		rec := one(find("wui:observer"))
		if rec == nil || rec["attempt"] != int64(1) || rec["move"] != "车9平8" || rec["legal"] != int64(2) {
			mbFatalf(t, "attrs = %v, want attempt=1 move=车9平8 legal=2", rec)
		}
	})
	run("illegal head trimmed to five", func(t *testing.T, find func(string) []map[string]any) {
		state := "legal-moves:\nm1\nm2\nm3\nm4\nm5\nm6"
		sp := &stubProvider{script: []stubScript{{text: "错一"}, {text: "错二"}}}
		postObserve(t, observeStubFn(sp, "m1", true), "chess", observeBody(t, "p", state))
		recs := find("wui:observer_illegal")
		if len(recs) != 2 {
			mbFatalf(t, "illegal records = %d, want one per attempt", len(recs))
		}
		first, second := recs[0], recs[1]
		if first["attempt"] != int64(1) || first["candidate"] != "错一" ||
			first["legal"] != int64(6) || first["legal_head"] != "m1 m2 m3 m4 m5" {
			mbFatalf(t, "attempt-1 attrs = %v, want candidate=错一 legal=6 head=m1..m5", first)
		}
		if second["attempt"] != int64(2) || second["candidate"] != "错二" || second["legal"] != int64(6) {
			mbFatalf(t, "attempt-2 attrs = %v, want candidate=错二 legal=6", second)
		}
	})
	run("retry error", func(t *testing.T, find func(string) []map[string]any) {
		sp := &stubProvider{script: []stubScript{{text: "错一"}, {err: errors.New("boom2")}}}
		postObserve(t, observeStubFn(sp, "m1", true), "chess", observeBody(t, "p", observeTestState))
		rec := one(find("wui:observer_error"))
		if rec == nil || rec["attempt"] != int64(2) || rec["err"] != "boom2" {
			mbFatalf(t, "attrs = %v, want attempt=2 err=boom2", rec)
		}
	})
	run("retry final", func(t *testing.T, find func(string) []map[string]any) {
		sp := &stubProvider{script: []stubScript{{text: "错一"}, {text: "车9平8\n好"}}}
		postObserve(t, observeStubFn(sp, "m1", true), "chess", observeBody(t, "p", observeTestState))
		recs := find("wui:observer")
		if len(recs) != 1 || recs[0]["attempt"] != int64(2) || recs[0]["move"] != "车9平8" {
			mbFatalf(t, "attrs = %v, want attempt=2 move=车9平8", recs)
		}
	})
	run("text and thinking deltas", func(t *testing.T, find func(string) []map[string]any) {
		sp := &stubProvider{script: []stubScript{{think: "先看看", text: "马8进7\n"}}}
		postObserve(t, observeStubFn(sp, "m1", true), "chess", observeBody(t, "p", observeTestState))
		txts := find("wui:observer_text")
		if len(txts) != 2 || txts[0]["text"] != "马8进7\n" || txts[0]["name"] != "chess" {
			mbFatalf(t, "text attrs = %v, want the first delta to be logged verbatim", txts)
		}
		th := one(find("wui:observer_thinking"))
		if th == nil || th["text"] != "先看看" {
			mbFatalf(t, "thinking attrs = %v, want text=先看看", th)
		}
	})
	run("done with usage", func(t *testing.T, find func(string) []map[string]any) {
		p := &mbEventProvider{script: []mbEventScript{{events: []llm.StreamEvent{
			{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}},
			{Usage: &types.Usage{OutputTokens: 42}},
			mbTextEv("马8进7\n"),
		}}}}
		status, _, _ := postObserve(t, observeStubFn(p, "m1", true), "chess", observeBody(t, "p", observeTestState))
		if status != http.StatusOK {
			mbFatalf(t, "status = %d, want 200", status)
		}
		rec := one(find("wui:observer_done"))
		if rec == nil {
			mbFatalf(t, "no wui:observer_done record")
		}
		if rec["stop_reason"] != "end_turn" || rec["output_tokens"] != int64(42) || rec["text_chars"] != int64(len("马8进7\n")) {
			mbFatalf(t, "attrs = %v, want stop_reason=end_turn output_tokens=42", rec)
		}
	})
	run("done without usage", func(t *testing.T, find func(string) []map[string]any) {
		p := &mbEventProvider{script: []mbEventScript{{events: []llm.StreamEvent{mbTextEv("马8进7\n")}}}}
		postObserve(t, observeStubFn(p, "m1", true), "chess", observeBody(t, "p", observeTestState))
		rec := one(find("wui:observer_done"))
		if rec == nil || rec["output_tokens"] != int64(-1) {
			mbFatalf(t, "attrs = %v, want output_tokens=-1 (nil usage sentinel)", rec)
		}
	})
}

func TestMutB_GamesStreamExactLines(t *testing.T) {
	t.Run("resign", func(t *testing.T) {
		sp := &stubProvider{script: []stubScript{{text: "认输\n这局你赢了，学到了。"}}}
		status, _, body := postObserve(t, observeStubFn(sp, "m1", true), "chess", observeBody(t, "p", observeTestState))
		if status != http.StatusOK {
			mbFatalf(t, "status = %d, want 200", status)
		}
		lines := mbLines(t, body)
		if n := len(lines); n != 3 {
			mbFatalf(t, "lines = %d (%v), want 2 note + 1 final", n, lines)
		}
		final := mbLine(lines, "final")
		if final == nil || final["move"] != "认输" || final["note"] != "这局你赢了，学到了。" || final["done"] != true {
			mbFatalf(t, "final line = %v, want resign with done", final)
		}
		if mbLine(lines, "retry") != nil {
			mbFatalf(t, "resignation must not enter the corrective retry")
		}
		if sp.calls != 1 {
			mbFatalf(t, "provider calls = %d, want 1", sp.calls)
		}
	})
	t.Run("legal with streamed note", func(t *testing.T) {
		sp := &stubProvider{script: []stubScript{{think: "先看看", text: "马8进7\n稳一手"}}}
		_, _, body := postObserve(t, observeStubFn(sp, "m1", true), "chess", observeBody(t, "p", observeTestState))
		lines := mbLines(t, body)
		th := mbLine(lines, "thinking")
		if th == nil || th["text"] != "先看看" {
			mbFatalf(t, "thinking line = %v, want exact text", th)
		}
		var notes []any
		for _, l := range lines {
			if l["type"] == "note" {
				notes = append(notes, l["text"])
			}
		}
		if len(notes) != 2 || notes[0] != "马8进7\n" || notes[1] != "稳一手\n" {
			mbFatalf(t, "note lines = %v, want the two exact deltas", notes)
		}
		final := mbLine(lines, "final")
		if final == nil || final["move"] != "马8进7" || final["note"] != "稳一手" {
			mbFatalf(t, "final line = %v, want move=马8进7 note=稳一手", final)
		}
	})
	t.Run("corrective retry line", func(t *testing.T) {
		sp := &stubProvider{script: []stubScript{{text: "炮五进二"}, {text: "车9平8\n换成出车"}}}
		_, _, body := postObserve(t, observeStubFn(sp, "m1", true), "chess", observeBody(t, "p", observeTestState))
		lines := mbLines(t, body)
		retry := mbLine(lines, "retry")
		if retry == nil || retry["candidate"] != "炮五进二" || len(retry) != 2 {
			mbFatalf(t, "retry line = %v, want {type:retry candidate:炮五进二} only", retry)
		}
		final := mbLine(lines, "final")
		if final == nil || final["move"] != "车9平8" || final["note"] != "换成出车" {
			mbFatalf(t, "final line = %v, want the corrected move", final)
		}
		if sp.calls != 2 {
			mbFatalf(t, "provider calls = %d, want 2", sp.calls)
		}
	})
	t.Run("retry call error", func(t *testing.T) {
		sp := &stubProvider{script: []stubScript{{text: "错一"}, {err: errors.New("boom2")}}}
		_, _, body := postObserve(t, observeStubFn(sp, "m1", true), "chess", observeBody(t, "p", observeTestState))
		lines := mbLines(t, body)
		errLine := mbLine(lines, "error")
		if errLine == nil || errLine["message"] != "LLM call failed: boom2" {
			mbFatalf(t, "error line = %v, want the retry-failure message", errLine)
		}
		if mbLine(lines, "final") != nil {
			mbFatalf(t, "no final may follow a failed retry")
		}
	})
	t.Run("both illegal", func(t *testing.T) {
		sp := &stubProvider{script: []stubScript{{text: "错一"}, {text: "错二"}}}
		_, _, body := postObserve(t, observeStubFn(sp, "m1", true), "chess", observeBody(t, "p", observeTestState))
		lines := mbLines(t, body)
		errLine := mbLine(lines, "error")
		if errLine == nil || errLine["message"] != "model failed to produce a legal move in two attempts" ||
			errLine["candidate"] != "错二" || errLine["legal"] != "马8进7,车9平8" {
			mbFatalf(t, "error line = %v, want the two-attempt failure with candidate and legal list", errLine)
		}
	})
	t.Run("first call error", func(t *testing.T) {
		sp := &stubProvider{script: []stubScript{{err: errors.New("boom1")}}}
		_, _, body := postObserve(t, observeStubFn(sp, "m1", true), "chess", observeBody(t, "p", observeTestState))
		lines := mbLines(t, body)
		errLine := mbLine(lines, "error")
		if errLine == nil || errLine["message"] != "LLM call failed: boom1" {
			mbFatalf(t, "error line = %v, want the first-call failure message", errLine)
		}
		if mbLine(lines, "retry") != nil {
			mbFatalf(t, "a first-call failure must not emit a retry line")
		}
		if sp.calls != 1 {
			mbFatalf(t, "provider calls = %d, want 1", sp.calls)
		}
	})
}

func TestMutB_GamesProviderGuard(t *testing.T) {
	t.Run("nil provider with ok", func(t *testing.T) {
		status, _, body := postObserve(t, observeStubFn(nil, "m1", true), "chess", observeBody(t, "p", observeTestState))
		if status != http.StatusServiceUnavailable {
			mbFatalf(t, "status = %d (body %s), want 503 for a nil provider", status, string(body))
		}
	})
	t.Run("provider with not ok", func(t *testing.T) {
		sp := &stubProvider{script: []stubScript{{text: "马8进7\n"}}}
		status, _, _ := postObserve(t, observeStubFn(sp, "m1", false), "chess", observeBody(t, "p", observeTestState))
		if status != http.StatusServiceUnavailable {
			mbFatalf(t, "status = %d, want 503 when the provider lookup reports not-ok", status)
		}
	})
}

func TestMutB_GamesContextPropagation(t *testing.T) {
	p := &mbEventProvider{script: []mbEventScript{
		{events: []llm.StreamEvent{mbTextEv("错一\n")}},
		{events: []llm.StreamEvent{mbTextEv("车9平8\n")}},
	}}
	status, body := mbPostCtx(t, observeStubFn(p, "m1", true), observeBody(t, "p", observeTestState))
	if status != http.StatusOK {
		mbFatalf(t, "status = %d (body %s), want 200", status, string(body))
	}
	resp := decodeObserveResp(t, body)
	if resp.Move != "车9平8" {
		mbFatalf(t, "move = %q, want the retry's corrected move", resp.Move)
	}
	ctxs := p.recordedCtxs()
	if len(ctxs) != 2 {
		mbFatalf(t, "provider calls = %d, want 2 (first illegal + corrective retry)", len(ctxs))
	}
	for i, ctx := range ctxs {
		if ctx == nil {
			mbFatalf(t, "call %d context = nil, want the request context", i)
		}
		if got := ctx.Value(mbCtxKey{}); got != "sentinel" {
			mbFatalf(t, "call %d context value = %v, want the sentinel (request ctx not propagated)", i, got)
		}
	}
}

func TestMutB_GamesStreamEventFolding(t *testing.T) {
	t.Run("stream error event", func(t *testing.T) {
		p := &mbEventProvider{script: []mbEventScript{{events: []llm.StreamEvent{
			{Error: &llm.APIError{Type: "overloaded_error", Message: "dead"}},
		}}}}
		_, _, body := postObserve(t, observeStubFn(p, "m1", true), "chess", observeBody(t, "p", observeTestState))
		resp := decodeObserveResp(t, body)
		if resp.Error == "" || !strings.Contains(resp.Error, "stream error") {
			mbFatalf(t, "error = %q, want the stream-error wrapper", resp.Error)
		}
	})
	t.Run("non-delta event with payload ignored", func(t *testing.T) {
		p := &mbEventProvider{script: []mbEventScript{{events: []llm.StreamEvent{
			{Type: "message_start", Delta: &llm.StreamDelta{Type: "text_delta", Text: "马8进7\n"}},
		}}, {err: errors.New("done2")}}}
		_, _, body := postObserve(t, observeStubFn(p, "m1", true), "chess", observeBody(t, "p", observeTestState))
		lines := mbLines(t, body)
		if mbLine(lines, "final") != nil {
			mbFatalf(t, "a non-delta event must not contribute text")
		}
		if mbLine(lines, "retry") == nil {
			mbFatalf(t, "empty reply must fall into the corrective retry")
		}
		errLine := mbLine(lines, "error")
		if errLine == nil || errLine["message"] != "LLM call failed: done2" {
			mbFatalf(t, "error line = %v, want the second-call failure", errLine)
		}
	})
	t.Run("nil-delta event tolerated", func(t *testing.T) {
		p := &mbEventProvider{script: []mbEventScript{{events: []llm.StreamEvent{
			{Type: "message_start"},
			{Type: "content_block_delta", Delta: nil},
			mbTextEv("马8进7\n"),
		}}}}
		status, _, body := postObserve(t, observeStubFn(p, "m1", true), "chess", observeBody(t, "p", observeTestState))
		if status != http.StatusOK {
			mbFatalf(t, "status = %d, want 200", status)
		}
		resp := decodeObserveResp(t, body)
		if resp.Move != "马8进7" {
			mbFatalf(t, "move = %q, want 马8进7 after tolerating the nil-delta events", resp.Move)
		}
	})
}

func TestMutB_ObserveEvalDigest(t *testing.T) {
	t.Run("every digest prefix kept", func(t *testing.T) {
		state := "对局\n受威胁 中卒\n- 攻\n步法评估 高\n局面要素 好\n要点情报 多\n你当前的计划 出车\nlegal-moves:\nmv"
		want := "受威胁 中卒 | - 攻 | 步法评估 高 | 局面要素 好 | 要点情报 多 | 你当前的计划 出车"
		if got := observeEvalDigest(state); got != want {
			mbFatalf(t, "digest = %q, want %q", got, want)
		}
	})
	t.Run("no truncation at exactly 1200", func(t *testing.T) {
		line := "- " + strings.Repeat("a", 1198)
		if len(line) != 1200 {
			mbFatalf(t, "fixture length %d, want 1200", len(line))
		}
		got := observeEvalDigest(line)
		if got != line {
			mbFatalf(t, "digest = %q (len %d), want verbatim (len 1200)", got, len(got))
		}
		if strings.HasSuffix(got, "…") {
			mbFatalf(t, "boundary-length digest must not gain an ellipsis")
		}
	})
	t.Run("ascii cut at 1201", func(t *testing.T) {
		line := "- " + strings.Repeat("a", 1199)
		if len(line) != 1201 {
			mbFatalf(t, "fixture length %d, want 1201", len(line))
		}
		want := "- " + strings.Repeat("a", 1198) + "…"
		if got := observeEvalDigest(line); got != want {
			mbFatalf(t, "digest = %q (len %d), want %q (len %d)", got, len(got), want, len(want))
		}
	})
	t.Run("multibyte cut walks to rune boundary", func(t *testing.T) {
		head := "- " + strings.Repeat("a", 1190)
		state := head + "\n" + strings.Repeat("受威胁", 50)
		// digest joins the two lines with " | ": head(1192) + 3 + runes from
		// 1195 in 3-byte steps. Byte 1200 falls inside the second rune, so
		// the cut must walk back to the rune start at 1198.
		want := head + " | " + "受" + "…"
		if got := observeEvalDigest(state); got != want {
			mbFatalf(t, "digest len = %d (tail %q), want len %d ending on a rune boundary",
				len(got), got[len(got)-12:], len(want))
		}
	})
}

func mbTextResp(texts ...string) *llm.Response {
	var blocks []types.ContentBlock
	for _, s := range texts {
		blocks = append(blocks, types.NewTextBlock(s))
	}
	return &llm.Response{Content: blocks}
}

func TestMutB_SplitCandidateNote(t *testing.T) {
	t.Run("first non-empty text block wins", func(t *testing.T) {
		resp := &llm.Response{Content: []types.ContentBlock{
			types.NewTextBlock(""),
			{Type: types.ContentTypeImage, Text: "干扰"},
			types.NewTextBlock("马8进7"),
		}}
		cand, note := splitCandidateNote(resp, []string{"马8进7", "车9平8"})
		if cand != "马8进7" || note != "" {
			mbFatalf(t, "candidate=%q note=%q, want 马8进7 with no note", cand, note)
		}
	})
	t.Run("empty text block skipped", func(t *testing.T) {
		cand, _ := splitCandidateNote(mbTextResp("", "车9平8"), []string{"马8进7", "车9平8"})
		if cand != "车9平8" {
			mbFatalf(t, "candidate = %q, want 车9平8 from the second block", cand)
		}
	})
	t.Run("stops at first text block", func(t *testing.T) {
		cand, _ := splitCandidateNote(mbTextResp("马8进7", "车9平8"), []string{"马8进7", "车9平8"})
		if cand != "马8进7" {
			mbFatalf(t, "candidate = %q, want 马8进7 (first block, not last)", cand)
		}
	})
	t.Run("no match keeps first non-empty line", func(t *testing.T) {
		cand, note := splitCandidateNote(mbTextResp("  \n全乱了"), []string{"马8进7"})
		if cand != "全乱了" || note != "" {
			mbFatalf(t, "candidate=%q note=%q, want the first non-empty line and no note", cand, note)
		}
	})
	t.Run("exact match beats suffix rescue", func(t *testing.T) {
		cand, _ := splitCandidateNote(mbTextResp("马8进7"), []string{"马8进7", "7"})
		if cand != "马8进7" {
			mbFatalf(t, "candidate = %q, want the exact whole-line match 马8进7", cand)
		}
	})
	t.Run("resign keeps its note", func(t *testing.T) {
		cand, note := splitCandidateNote(mbTextResp("认输\n这局你赢了"), []string{"马8进7"})
		if cand != "认输" || note != "这局你赢了" {
			mbFatalf(t, "candidate=%q note=%q, want 认输 with its note", cand, note)
		}
	})
	t.Run("move followed by punctuation contributes no note", func(t *testing.T) {
		// "马8进7！" trims to the exact move, but the untrimmed line differs,
		// so the chosen line must add nothing (LastIndex == 0 is not a suffix
		// rescue with commentary).
		cand, note := splitCandidateNote(mbTextResp("马8进7！\n解说二"), []string{"马8进7"})
		if cand != "马8进7" || note != "解说二" {
			mbFatalf(t, "candidate=%q note=%q, want 马8进7 with only the later line as note", cand, note)
		}
	})
	t.Run("single-char prefix before token stays in note", func(t *testing.T) {
		cand, note := splitCandidateNote(mbTextResp("!马8进7"), []string{"马8进7"})
		if cand != "马8进7" || note != "!" {
			mbFatalf(t, "candidate=%q note=%q, want 马8进7 with note=!", cand, note)
		}
	})
	t.Run("last legal line wins across blanks", func(t *testing.T) {
		// The scan must run over every line (continue, not break, on both the
		// empty-line skip and the exact-match hit) so the LAST legal token is
		// the move and earlier ones degrade into the note.
		cand, note := splitCandidateNote(mbTextResp("马8进7\n\n车9平8"), []string{"马8进7", "车9平8"})
		if cand != "车9平8" || note != "马8进7" {
			mbFatalf(t, "candidate=%q note=%q, want the last legal line 车9平8 with 马8进7 as note", cand, note)
		}
	})
}

func TestMutB_ParseLegalMovesStartSentinel(t *testing.T) {
	_, err := parseLegalMoves("马8进7\n车9平8")
	if err == nil {
		mbFatalf(t, "parseLegalMoves without a marker must fail")
	}
	if err.Error() != "no legal-moves: marker in state" {
		mbFatalf(t, "err = %q, want the marker error", err.Error())
	}
}

func TestMutB_UsageField(t *testing.T) {
	if got := usageField(nil, func(u *types.Usage) int { return u.OutputTokens }); got != -1 {
		mbFatalf(t, "usageField(nil) = %d, want -1", got)
	}
	if got := usageField(&types.Usage{OutputTokens: 7}, func(u *types.Usage) int { return u.OutputTokens }); got != 7 {
		mbFatalf(t, "usageField(usage) = %d, want 7", got)
	}
}

// ---------------------------------------------------------------------------
// settings.go
// ---------------------------------------------------------------------------

func TestMutB_SettingsTimeoutDefault(t *testing.T) {
	if settingsHTTPTimeout != 10*time.Second {
		mbFatalf(t, "settingsHTTPTimeout = %v, want 10s (probe budget is a UX contract)", settingsHTTPTimeout)
	}
}

func TestMutB_GetProvidersBrokenFile(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, "{\n")

	var got settingsProviders
	resp := getJSON(t, srv.URL+"/api/settings/providers", &got)
	if resp.StatusCode != http.StatusOK {
		mbFatalf(t, "status = %d, want 200 — an unreadable file is cold start, not an error", resp.StatusCode)
	}
	if got.Providers == nil || len(got.Providers) != 0 {
		mbFatalf(t, "providers = %v, want [] (never null)", got.Providers)
	}
	if got.Default.Provider != "" || got.Default.Model != "" {
		mbFatalf(t, "default = %+v, want empty", got.Default)
	}
}

func TestMutB_GetProvidersNoStoreHeader(t *testing.T) {
	srv := newSettingsServer(t)
	seedSettings(t, settingsFixture)

	var got settingsProviders
	resp := getJSON(t, srv.URL+"/api/settings/providers", &got)
	if resp.StatusCode != http.StatusOK {
		mbFatalf(t, "status = %d, want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		mbFatalf(t, "Cache-Control = %q, want no-store (a save must never be shadowed by a cached read)", cc)
	}
}

func TestMutB_PutProvidersDecodeError(t *testing.T) {
	srv := newSettingsServer(t)
	path, old := seedSettings(t, settingsFixture)

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/providers", strings.NewReader(`[{"name":`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		mbFatalf(t, "status = %d, want 400", resp.StatusCode)
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		mbFatalf(t, "body not JSON: %v", err)
	}
	if e.Error != "unexpected EOF" {
		mbFatalf(t, "error = %q, want the decode message, not a validation message", e.Error)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(old) {
		mbFatalf(t, "rejected PUT must leave the file byte-identical")
	}
}

func TestMutB_PutProvidersSaveFails(t *testing.T) {
	srv := newSettingsServer(t)
	path, old := seedSettings(t, settingsFixture)
	// A directory where the .bak must land makes the backup write fail, so
	// SaveProviders aborts before touching settings.json.
	if err := os.Mkdir(path+".bak", 0755); err != nil {
		t.Fatal(err)
	}

	body := `[{"name":"solo","url":"https://s","keys":["k"],"models":{"m":{}}}]`
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/providers", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		mbFatalf(t, "body not JSON: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		mbFatalf(t, "status = %d (error %q), want 500 when the save fails", resp.StatusCode, e.Error)
	}
	if e.Error == "" {
		mbFatalf(t, "error envelope must carry the save failure")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(old) {
		mbFatalf(t, "failed save must leave settings.json unchanged")
	}
}

func TestMutB_ValidateKnownTypes(t *testing.T) {
	for _, typ := range []string{"", "auto", "openai", "anthropic", "responses"} {
		body := `[{"name":"p","url":"https://a","keys":["k"],"models":{"m":{}}}]`
		if typ != "" {
			body = `[{"name":"p","type":"` + typ + `","url":"https://a","keys":["k"],"models":{"m":{}}}]`
		}
		var providers []config.Provider
		if err := json.Unmarshal([]byte(body), &providers); err != nil {
			t.Fatalf("type %q: unmarshal: %v", typ, err)
		}
		if msg := validateProviders(providers); msg != "" {
			t.Errorf("type %q rejected: %q", typ, msg)
		}
	}
}

func TestMutB_TestProviderDecodeError(t *testing.T) {
	srv := newSettingsServer(t)

	var e struct {
		Error string `json:"error"`
	}
	resp := postSettingsJSON(t, srv.URL+"/api/settings/test", `{"name":`, &e)
	if resp.StatusCode != http.StatusBadRequest {
		mbFatalf(t, "status = %d, want 400", resp.StatusCode)
	}
	if e.Error != "unexpected EOF" {
		mbFatalf(t, "error = %q, want the decode message", e.Error)
	}
}

func TestMutB_ProbeAnthropicWire(t *testing.T) {
	srv := newSettingsServer(t)
	up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"msg_1","content":[]}`))
	})

	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/test", `{
		"name": "a", "type": "anthropic", "url": "`+up.URL+`",
		"keys": ["ak"], "models": {"m1": {}, "m2": {}}
	}`, &got)
	if !got.OK || got.Error != "" {
		mbFatalf(t, "ok=%v error=%q, want a successful probe", got.OK, got.Error)
	}
	if len(*calls) != 1 {
		mbFatalf(t, "upstream hits = %d, want 1", len(*calls))
	}
	call := (*calls)[0]
	if call.Method != http.MethodPost || call.Path != "/v1/messages" {
		mbFatalf(t, "request = %s %s, want POST /v1/messages", call.Method, call.Path)
	}
	if call.Header.Get("x-api-key") != "ak" {
		mbFatalf(t, "x-api-key = %q, want ak", call.Header.Get("x-api-key"))
	}
	if call.Header.Get("anthropic-version") != "2023-06-01" {
		mbFatalf(t, "anthropic-version = %q, want 2023-06-01", call.Header.Get("anthropic-version"))
	}
	if ct := call.Header.Get("Content-Type"); ct != "application/json" {
		mbFatalf(t, "Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(call.Body, &body); err != nil {
		mbFatalf(t, "ping body not JSON: %v (%s)", err, call.Body)
	}
	if body.Model != "m1" || body.MaxTokens != 1 || len(body.Messages) != 1 ||
		body.Messages[0].Role != "user" || body.Messages[0].Content != "ping" {
		mbFatalf(t, "ping body = %+v, want model=m1 max_tokens=1 one user ping message", body)
	}
}

func TestMutB_ProbeResponsesWire(t *testing.T) {
	srv := newSettingsServer(t)
	up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})

	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/test", `{
		"name": "r", "type": "responses", "url": "`+up.URL+`",
		"keys": ["rk"], "models": {"m1": {}, "m2": {}}
	}`, &got)
	if !got.OK || got.Error != "" {
		mbFatalf(t, "ok=%v error=%q, want a successful probe", got.OK, got.Error)
	}
	if len(*calls) != 1 {
		mbFatalf(t, "upstream hits = %d, want 1", len(*calls))
	}
	call := (*calls)[0]
	if call.Method != http.MethodPost || call.Path != "/responses" {
		mbFatalf(t, "request = %s %s, want POST against the protocol endpoint /responses", call.Method, call.Path)
	}
	if auth := call.Header.Get("Authorization"); auth != "Bearer rk" {
		mbFatalf(t, "Authorization = %q, want Bearer rk", auth)
	}
	if ct := call.Header.Get("Content-Type"); ct != "application/json" {
		mbFatalf(t, "Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Model           string `json:"model"`
		Input           string `json:"input"`
		MaxOutputTokens int    `json:"max_output_tokens"`
	}
	if err := json.Unmarshal(call.Body, &body); err != nil {
		mbFatalf(t, "probe body not JSON: %v (%s)", err, call.Body)
	}
	if body.Model != "m1" || body.Input != "ping" || body.MaxOutputTokens != 1 {
		mbFatalf(t, "probe body = %+v, want model=m1 input=ping max_output_tokens=1", body)
	}
}

func TestMutB_ProbeResponsesNoModels(t *testing.T) {
	srv := newSettingsServer(t)
	up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the responses probe needs a model name; no call is expected without one")
	})

	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/test", `{
		"name": "r", "type": "responses", "url": "`+up.URL+`", "keys": ["k"], "models": {}
	}`, &got)
	if got.OK || got.Error != "no models configured" {
		mbFatalf(t, "ok=%v error=%q, want the no-models error", got.OK, got.Error)
	}
	if len(*calls) != 0 {
		mbFatalf(t, "upstream hits = %d, want 0", len(*calls))
	}
}

func TestMutB_ProbeBadURLGuards(t *testing.T) {
	srv := newSettingsServer(t)
	_, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an unparseable URL must fail before any request goes out")
	})
	for _, typ := range []string{"anthropic", "responses", "openai"} {
		var got struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/test", `{
			"name": "p", "type": "`+typ+`", "url": "http://a b/",
			"keys": ["k"], "models": {"m": {}}
		}`, &got)
		if got.OK {
			mbFatalf(t, "type %s: ok=true on an unparseable URL", typ)
		}
		if !strings.Contains(got.Error, "invalid character") {
			mbFatalf(t, "type %s: error = %q, want the URL parse failure", typ, got.Error)
		}
	}
	if len(*calls) != 0 {
		mbFatalf(t, "upstream hits = %d, want 0", len(*calls))
	}
}

func TestMutB_ProbeStatusClassification(t *testing.T) {
	srv := newSettingsServer(t)

	t.Run("299 is success", func(t *testing.T) {
		up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(299)
			_, _ = w.Write([]byte(`{}`))
		})
		var got struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/test", `{
			"name": "p", "url": "`+up.URL+`", "keys": ["k"], "models": {"m": {}}
		}`, &got)
		if !got.OK || got.Error != "" {
			mbFatalf(t, "ok=%v error=%q, want success — 299 is a 2xx", got.OK, got.Error)
		}
	})
	t.Run("401 error names the status", func(t *testing.T) {
		up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		var got struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/test", `{
			"name": "p", "url": "`+up.URL+`", "keys": ["k"], "models": {"m": {}}
		}`, &got)
		if got.OK || got.Error != "HTTP 401" {
			mbFatalf(t, "ok=%v error=%q, want exactly HTTP 401", got.OK, got.Error)
		}
	})
}

func TestMutB_ProbeLatencyMeasured(t *testing.T) {
	srv := newSettingsServer(t)
	// The upstream stalls on a channel until the test releases it, so the
	// probe's measured window provably spans the release delay — a constant
	// or zero latencyMs cannot pass.
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{}`))
	})

	type probeReply struct {
		OK        bool   `json:"ok"`
		LatencyMs int    `json:"latencyMs"`
		Error     string `json:"error"`
	}
	reply := make(chan probeReply, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/api/settings/test", "application/json",
			strings.NewReader(`{
				"name": "p", "url": "`+up.URL+`", "keys": ["k"], "models": {"m": {}}
			}`))
		if err != nil {
			reply <- probeReply{Error: err.Error()}
			return
		}
		defer resp.Body.Close()
		var got probeReply
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			reply <- probeReply{Error: "decode: " + err.Error()}
			return
		}
		reply <- got
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never saw the probe request")
	}
	// Hold the probe in flight for 50ms of real time so the measured
	// latency has a guaranteed floor.
	select {
	case <-time.After(50 * time.Millisecond):
	}
	close(release)

	select {
	case got := <-reply:
		if !got.OK {
			mbFatalf(t, "ok=false error=%q, want success", got.Error)
		}
		if got.LatencyMs < 25 {
			mbFatalf(t, "latencyMs = %d, want >= 25 (the upstream stalled 50ms)", got.LatencyMs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("probe request never returned after release")
	}
}

func TestMutB_ProbeOutboundContext(t *testing.T) {
	srv := newSettingsServer(t)
	started := make(chan struct{}, 1)
	sawCancel := make(chan struct{}, 1)
	up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
		sawCancel <- struct{}{}
	})
	old := settingsHTTPTimeout
	settingsHTTPTimeout = 800 * time.Millisecond
	t.Cleanup(func() { settingsHTTPTimeout = old })

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/settings/test",
		strings.NewReader(`{"name":"r","type":"responses","url":"`+up.URL+`","keys":["k"],"models":{"m":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		resp, err := srv.Client().Do(req)
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()
	// Cancel only once the probe is provably in flight at the upstream —
	// channel handshake instead of a sleep racing the request dispatch.
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			mbFatalf(t, "client error = %v, want the cancellation to surface", err)
		}
	case <-time.After(3 * time.Second):
		mbFatalf(t, "client request never returned after cancel")
	}
	// The cancellation must reach the upstream long before settingsHTTPTimeout
	// could fire: the outbound probe rides the request context.
	select {
	case <-sawCancel:
	case <-time.After(400 * time.Millisecond):
		mbFatalf(t, "upstream did not observe the cancellation within 400ms — the outbound probe must ride the request context")
	}
}

func TestMutB_FetchModelsFree(t *testing.T) {
	srv := newSettingsServer(t)
	up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "max_price=0&sort=most-popular" {
			t.Errorf("query = %q, want the free-models filter", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"f1","name":"F1","context_length":1,
			 "pricing":{"prompt":"0","completion":"0"},"supported_parameters":["tools"]},
			{"id":"f2","context_length":131072,
			 "pricing":{"prompt":"0","completion":"0"},"supported_parameters":["tools"]}
		]}`))
	})

	var got struct {
		Mode   string `json:"mode"`
		Models []struct {
			ID      string `json:"id"`
			Context string `json:"context"`
		} `json:"models"`
		Error string `json:"error"`
	}
	resp := postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`","free":true}`, &got)
	if resp.StatusCode != http.StatusOK {
		mbFatalf(t, "status = %d", resp.StatusCode)
	}
	if got.Mode != "fetched" || got.Error != "" {
		mbFatalf(t, "mode=%q error=%q, want fetched", got.Mode, got.Error)
	}
	if len(got.Models) != 2 {
		mbFatalf(t, "models = %+v, want 2", got.Models)
	}
	if got.Models[0].ID != "f1" || got.Models[0].Context != "1" {
		mbFatalf(t, "f1 = %+v, want context 1 (small values stay plain digits)", got.Models[0])
	}
	if got.Models[1].ID != "f2" || got.Models[1].Context != "128k" {
		mbFatalf(t, "f2 = %+v, want context 128k (exact k multiple in human form)", got.Models[1])
	}
}

func TestMutB_FetchModelsInputGuards(t *testing.T) {
	srv := newSettingsServer(t)

	t.Run("decode error", func(t *testing.T) {
		resp, err := http.Post(srv.URL+"/api/settings/models", "application/json", strings.NewReader(`{"url":`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			mbFatalf(t, "status = %d, want 400", resp.StatusCode)
		}
		var e struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
			mbFatalf(t, "body not JSON: %v", err)
		}
		if e.Error != "unexpected EOF" {
			mbFatalf(t, "error = %q, want the decode message", e.Error)
		}
	})
	t.Run("missing url", func(t *testing.T) {
		resp, err := http.Post(srv.URL+"/api/settings/models", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			mbFatalf(t, "status = %d, want 400", resp.StatusCode)
		}
		var e struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
			mbFatalf(t, "body not JSON: %v", err)
		}
		if e.Error != "url is required" {
			mbFatalf(t, "error = %q, want exactly 'url is required'", e.Error)
		}
	})
	t.Run("anthropic is manual", func(t *testing.T) {
		up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			t.Error("anthropic has no model list endpoint; no fetch is expected")
		})
		var got struct {
			Mode string `json:"mode"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`","type":"anthropic"}`, &got)
		if got.Mode != "manual" {
			mbFatalf(t, "mode = %q, want manual", got.Mode)
		}
		if len(*calls) != 0 {
			mbFatalf(t, "upstream hits = %d, want 0", len(*calls))
		}
	})
	t.Run("anthropic inferred from url path is manual", func(t *testing.T) {
		up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			t.Error("an /anthropic base URL means manual mode; no fetch is expected")
		})
		var got struct {
			Mode string `json:"mode"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`/anthropic"}`, &got)
		if got.Mode != "manual" {
			mbFatalf(t, "mode = %q, want manual for an untyped /anthropic base", got.Mode)
		}
		if len(*calls) != 0 {
			mbFatalf(t, "upstream hits = %d, want 0", len(*calls))
		}
	})
	t.Run("unparseable url", func(t *testing.T) {
		var got struct {
			Mode  string `json:"mode"`
			Error string `json:"error"`
		}
		resp := postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"http://a b/","type":"openai"}`, &got)
		if resp.StatusCode != http.StatusOK {
			mbFatalf(t, "status = %d", resp.StatusCode)
		}
		if got.Mode != "error" {
			mbFatalf(t, "mode = %q, want error", got.Mode)
		}
		if !strings.Contains(got.Error, "invalid character") {
			mbFatalf(t, "error = %q, want the URL parse failure", got.Error)
		}
	})
}

func TestMutB_FetchModelsAuthHeader(t *testing.T) {
	srv := newSettingsServer(t)
	up, calls := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})

	var withKey struct {
		Mode string `json:"mode"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`","key":"kk","type":"openai"}`, &withKey)
	if withKey.Mode != "fetched" {
		mbFatalf(t, "mode = %q, want fetched", withKey.Mode)
	}
	if auth := (*calls)[0].Header.Get("Authorization"); auth != "Bearer kk" {
		mbFatalf(t, "Authorization = %q, want Bearer kk", auth)
	}

	calls2Up, calls2 := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	var noKey struct {
		Mode string `json:"mode"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+calls2Up.URL+`","type":"openai"}`, &noKey)
	if noKey.Mode != "fetched" {
		mbFatalf(t, "mode = %q, want fetched without a key", noKey.Mode)
	}
	if auth := (*calls2)[0].Header.Get("Authorization"); auth != "" {
		mbFatalf(t, "Authorization = %q, want absent when no key was given", auth)
	}
}

func TestMutB_FetchModelsUpstreamFailures(t *testing.T) {
	srv := newSettingsServer(t)

	t.Run("unreachable upstream", func(t *testing.T) {
		closed := httptest.NewServer(http.NotFoundHandler())
		url := closed.URL
		closed.Close()

		var got struct {
			Mode  string `json:"mode"`
			Error string `json:"error"`
		}
		resp := postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+url+`","type":"openai"}`, &got)
		if resp.StatusCode != http.StatusOK {
			mbFatalf(t, "status = %d — transport failures are data", resp.StatusCode)
		}
		if got.Mode != "error" || got.Error == "" {
			mbFatalf(t, "mode=%q error=%q, want error mode with the transport text", got.Mode, got.Error)
		}
	})
	t.Run("non-2xx with valid body", func(t *testing.T) {
		up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
		})
		var got struct {
			Mode  string `json:"mode"`
			Error string `json:"error"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`","type":"openai"}`, &got)
		if got.Mode != "error" || got.Error != "HTTP 500" {
			mbFatalf(t, "mode=%q error=%q, want error mode naming the status", got.Mode, got.Error)
		}
	})
	t.Run("299 with valid body", func(t *testing.T) {
		up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(299)
			_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
		})
		var got struct {
			Mode   string `json:"mode"`
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`","type":"openai"}`, &got)
		if got.Mode != "fetched" || len(got.Models) != 1 || got.Models[0].ID != "m1" {
			mbFatalf(t, "mode=%q models=%+v, want fetched [m1] — 299 is a 2xx", got.Mode, got.Models)
		}
	})
	t.Run("undecodable body", func(t *testing.T) {
		up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`not-json`))
		})
		var got struct {
			Mode  string `json:"mode"`
			Error string `json:"error"`
		}
		postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`","type":"openai"}`, &got)
		if got.Mode != "error" {
			mbFatalf(t, "mode = %q, want error", got.Mode)
		}
		if !strings.Contains(got.Error, "invalid character") {
			mbFatalf(t, "error = %q, want the decode failure", got.Error)
		}
	})
}

func TestMutB_FetchModelsMergeDedupe(t *testing.T) {
	srv := newSettingsServer(t)
	up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"data": [{"id":"m1"}],
			"models": [
				{"slug":"m2","context_window":1024,"input_modalities":["text","image"]},
				{"slug":"m1"},
				{"slug":"","display_name":"m3"},
				{"slug":"","display_name":""},
				{"slug":"m0","context_window":0},
				{"slug":"m1c","context_window":1}
			]
		}`))
	})

	var got struct {
		Mode   string `json:"mode"`
		Models []struct {
			ID      string   `json:"id"`
			Context string   `json:"context"`
			Input   []string `json:"input"`
		} `json:"models"`
	}
	postSettingsJSON(t, srv.URL+"/api/settings/models", `{"url":"`+up.URL+`","type":"openai"}`, &got)
	if got.Mode != "fetched" {
		mbFatalf(t, "mode = %q, want fetched", got.Mode)
	}
	wantIDs := []string{"m2", "m1", "m3", "m0", "m1c"}
	if len(got.Models) != len(wantIDs) {
		mbFatalf(t, "models = %+v, want exactly %v (deduped, no empty ids)", got.Models, wantIDs)
	}
	for i, id := range wantIDs {
		if got.Models[i].ID != id {
			mbFatalf(t, "models[%d] = %+v, want id %q", i, got.Models[i], id)
		}
	}
	m2 := got.Models[0]
	if m2.Context != "1k" || len(m2.Input) != 2 || m2.Input[0] != "text" || m2.Input[1] != "image" {
		mbFatalf(t, "m2 = %+v, want context 1k and the input modalities passed through", m2)
	}
	if got.Models[1].Context != "" || got.Models[2].Context != "" || got.Models[3].Context != "" {
		mbFatalf(t, "zero context_window must stay unset, got %+v", got.Models[1:4])
	}
	if got.Models[4].Context != "1" {
		mbFatalf(t, "m1c context = %q, want 1", got.Models[4].Context)
	}
}

func TestMutB_FetchModelsClientTimeout(t *testing.T) {
	srv := newSettingsServer(t)
	sawTimeout := make(chan struct{}, 1)
	up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		sawTimeout <- struct{}{}
	})
	old := settingsHTTPTimeout
	settingsHTTPTimeout = 300 * time.Millisecond
	t.Cleanup(func() { settingsHTTPTimeout = old })

	type fetchReply struct {
		Mode  string `json:"mode"`
		Error string `json:"error"`
	}
	reply := make(chan fetchReply, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/api/settings/models", "application/json",
			strings.NewReader(`{"url":"`+up.URL+`","type":"openai"}`))
		if err != nil {
			reply <- fetchReply{Error: err.Error()}
			return
		}
		defer resp.Body.Close()
		var got fetchReply
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			reply <- fetchReply{Error: "decode: " + err.Error()}
			return
		}
		reply <- got
	}()

	// The fetch must end within a bound around the client timeout, not hang.
	var got fetchReply
	select {
	case got = <-reply:
	case <-time.After(2 * time.Second):
		mbFatalf(t, "fetch still in flight after 2s — the %v client timeout must bound it", settingsHTTPTimeout)
	}
	if got.Mode != "error" || got.Error == "" {
		mbFatalf(t, "mode=%q error=%q, want the timeout as data", got.Mode, got.Error)
	}
	// The bound is the probe's own timeout: the upstream observes its
	// request context cancel when the client gives up.
	select {
	case <-sawTimeout:
	case <-time.After(3 * time.Second):
		mbFatalf(t, "upstream request never cancelled — the client timeout did not reach the outbound fetch")
	}
}

func TestMutB_DefaultPut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".gbot"), 0755); err != nil {
		t.Fatal(err)
	}
	seed := `{"providers":[{"name":"zhipu","url":"https://z","type":"openai","keys":["k"],"models":{"glm-5.3":{}}}]}`
	if err := os.WriteFile(filepath.Join(home, ".gbot", "settings.json"), []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterSettingsRoutes(mux)
	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/settings/default", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	rec := put(`{"provider":`)
	if rec.Code != http.StatusBadRequest {
		mbFatalf(t, "decode error: status = %d, want 400", rec.Code)
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		mbFatalf(t, "body not JSON: %s", rec.Body.String())
	}
	if e.Error != "invalid body: unexpected EOF" {
		mbFatalf(t, "error = %q, want the invalid-body wrapper", e.Error)
	}

	rec = put(`{"provider":"nope","model":"x"}`)
	if rec.Code != http.StatusBadRequest {
		mbFatalf(t, "unknown provider: status = %d, want 400", rec.Code)
	}
	{
		var e2 struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &e2); err != nil || e2.Error != "unknown provider" {
			mbFatalf(t, "unknown provider body = %s, want the sentinel text", rec.Body.String())
		}
	}

	rec = put(`{"provider":"zhipu","model":"nope"}`)
	if rec.Code != http.StatusBadRequest {
		mbFatalf(t, "unknown model: status = %d, want 400", rec.Code)
	}
	var e3 struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e3); err != nil || e3.Error != "unknown model for provider" {
		mbFatalf(t, "unknown model body = %s, want the sentinel text", rec.Body.String())
	}

	rec = put(`{"provider":"zhipu","model":"glm-5.3"}`)
	if rec.Code != http.StatusOK {
		mbFatalf(t, "valid put: status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"ok":true}` {
		mbFatalf(t, "body = %s, want exactly {\"ok\":true}", rec.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(home, ".gbot", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Model map[string]string `json:"model"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		mbFatalf(t, "file invalid after put: %v", err)
	}
	if file.Model["default"] != "zhipu/glm-5.3" {
		mbFatalf(t, "default = %q, want the written tier", file.Model["default"])
	}
}
