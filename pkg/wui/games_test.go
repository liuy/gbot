package wui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

func TestParseLegalMoves(t *testing.T) {
	tests := []struct {
		name    string
		state   string
		want    []string
		wantErr bool
	}{
		{
			name:  "normal",
			state: "some board text\nlegal-moves:\n马8进7\n车9平8",
			want:  []string{"马8进7", "车9平8"},
		},
		{
			name:  "prose before marker",
			state: "对局记录:\n炮二平五\n──────────\nlegal-moves:\n马8进7",
			want:  []string{"马8进7"},
		},
		{
			name:  "duplicate moves preserved",
			state: "legal-moves:\n车9平8\n车9平8",
			want:  []string{"车9平8", "车9平8"},
		},
		{
			name:  "CRLF line endings",
			state: "board\r\nlegal-moves:\r\n马8进7\r\n车9平8\r\n",
			want:  []string{"马8进7", "车9平8"},
		},
		{
			name:  "trailing spaces on entries",
			state: "legal-moves:\n  马8进7  \n\t车9平8\t",
			want:  []string{"马8进7", "车9平8"},
		},
		{
			name:  "blank lines between entries",
			state: "legal-moves:\n\n马8进7\n\n车9平8",
			want:  []string{"马8进7", "车9平8"},
		},
		{
			name:    "missing marker",
			state:   "马8进7\n车9平8",
			wantErr: true,
		},
		{
			name:    "marker with no entries",
			state:   "legal-moves:\n",
			wantErr: true,
		},
		{
			name:    "empty state",
			state:   "",
			wantErr: true,
		},
		{
			name:  "last marker wins",
			state: "legal-moves:\n车9平8\nlegal-moves:\n马8进7",
			want:  []string{"马8进7"},
		},
		{
			name:    "marker substring with leading junk is not a marker",
			state:   "xlegal-moves:\n马8进7",
			wantErr: true,
		},
		{
			name:  "marker with surrounding whitespace still counts",
			state: "board\n  legal-moves:  \n马8进7",
			want:  []string{"马8进7"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLegalMoves(tt.state)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseLegalMoves(%q) err = nil, want error", tt.state)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLegalMoves(%q) err = %v, want nil", tt.state, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d (%q), want %d (%q)", len(got), got, len(tt.want), tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestBundledChessTemplate pins the observable surface of the embedded game
// template: the zh-chess lib must be inlined (UMD global + class access via
// .default), the game script must expose the ChessGame namespace tests eval,
// and no vendor-stage placeholder may survive — chess.html is the single
// authoring source, so a placeholder here means the shipped page is broken.
func TestBundledChessTemplate(t *testing.T) {
	s := string(chessTemplate)
	for _, want := range []string{
		"var ZhChess",
		"ZhChess.default",
		"generateLegalMoves",
		`<script id="zh-chess-lib">`,
		`<script id="game-code">`,
		"window.ChessGame",
		"OBSERVE_PROMPT",
		"legal-moves:",
		"github.com/kongyijilafumi/zh-chess",
		"v3.2.1",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("bundled chess.html missing %q", want)
		}
	}
	if strings.Contains(s, "//__ZH_CHESS_LIB__") {
		t.Error(`bundled chess.html contains unfilled placeholder "//__ZH_CHESS_LIB__"`)
	}
	if !strings.Contains(s, "MIT License") {
		t.Error("bundled chess.html missing the zh-chess MIT license header")
	}
}

const observeTestPrompt = "你是黑方棋手"
const observeTestState = "legal-moves:\n马8进7\n车9平8"

// observeResp mirrors every key the handler can emit, so scenarios can assert
// the full payload shape precisely (unset keys decode to zero values).
type observeResp struct {
	Move        string   `json:"move"`
	Note        string   `json:"note"`
	Error       string   `json:"error"`
	Candidate   string   `json:"candidate"`
	Legal       []string `json:"legal"`
	Done        bool     `json:"done"`
	SawThinking int
	SawNote     int
}

// stubProvider is the llm.Provider stand-in for the observe handler tests. It
// records every *llm.Request it receives (the public wire shape — not internal
// calls) and plays back a per-call script.
type stubProvider struct {
	mu     sync.Mutex
	reqs   []*llm.Request
	script []stubScript
	calls  int
}

type stubScript struct {
	text  string
	think string
	err   error
}

func (p *stubProvider) Name() string { return "stub" }

func (p *stubProvider) Complete(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	return nil, errors.New("stubProvider: observe tests drive Stream")
}

func (p *stubProvider) Stream(ctx context.Context, req *llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	i := p.calls
	p.calls++
	p.reqs = append(p.reqs, req)
	script := p.script
	p.mu.Unlock()
	if i >= len(script) {
		return nil, fmt.Errorf("stubProvider: no script entry for call %d", i)
	}
	s := script[i]
	if s.err != nil {
		return nil, s.err
	}
	ch := make(chan llm.StreamEvent, 8)
	go func() {
		defer close(ch)
		if s.think != "" {
			ch <- llm.StreamEvent{
				Type:  "content_block_delta",
				Delta: &llm.StreamDelta{Type: "thinking_delta", Thinking: s.think},
			}
		}
		for _, line := range strings.Split(s.text, "\n") {
			ch <- llm.StreamEvent{
				Type:  "content_block_delta",
				Delta: &llm.StreamDelta{Type: "text_delta", Text: line + "\n"},
			}
		}
	}()
	return ch, nil
}

func (p *stubProvider) recorded() []*llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reqs
}

func observeStubFn(p llm.Provider, model string, ok bool) ObserveProviderFn {
	return func() (llm.Provider, string, bool) { return p, model, ok }
}

// postObserve routes one POST through a mux carrying the observe pattern, so
// r.PathValue resolves exactly as it does on the daemon mux.
func postObserve(t *testing.T, observe ObserveProviderFn, name, body string) (int, http.Header, []byte) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("POST /artifacts/{name...}", observerHandler(observe))
	req, err := http.NewRequest(http.MethodPost, "/artifacts/"+name, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Header(), rec.Body.Bytes()
}

func observeBody(t *testing.T, prompt, state string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"prompt": prompt, "state": state})
	if err != nil {
		t.Fatalf("marshal observe body: %v", err)
	}
	return string(b)
}

// decodeObserveResp decodes the response into the full handler payload shape;
// unset fields decode to zero values, so every scenario asserts all relevant
// keys exactly.
// decodeObserveResp parses the NDJSON reply: the final line carries the
// outcome; note/thinking lines preceding it are ignored by assertions.
func decodeObserveResp(t *testing.T, body []byte) observeResp {
	t.Helper()
	var r observeResp
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev struct {
			Type      string `json:"type"`
			Move      string `json:"move"`
			Note      string `json:"note"`
			Done      bool   `json:"done"`
			Message   string `json:"message"`
			Error     string `json:"error"`
			Candidate string `json:"candidate"`
			Legal     string `json:"legal"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal ndjson line %q: %v", line, err)
		}
		switch ev.Type {
		case "thinking":
			r.SawThinking++
		case "note":
			r.SawNote++
		case "final":
			r.Move, r.Note = ev.Move, ev.Note
			r.Done = ev.Done
		case "error":
			r.Error = firstNonEmpty(ev.Message, ev.Error)
			r.Candidate = ev.Candidate
			if ev.Legal != "" {
				r.Legal = strings.Split(ev.Legal, ",")
			}
		case "":
			// Early-reject bodies are a single JSON object with no type tag.
			if ev.Error != "" {
				r.Error = ev.Error
			}
		}
	}
	if r.Move == "" && r.Error == "" {
		t.Fatalf("no final or error line in response %q", string(body))
	}
	return r
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func TestObserveHandler(t *testing.T) {
	correctionFor := func(candidate string) string {
		return observeTestState + "\n\n" +
			"Your reply \"" + candidate + "\" is not in the legal-move list. Legal moves:\n马8进7\n车9平8\nReply with exactly one entry from the list as the first line."
	}

	tests := []struct {
		name       string
		observe    ObserveProviderFn
		script     []stubScript
		gameName   string
		body       string
		wantStatus int
		check      func(t *testing.T, sp *stubProvider, resp observeResp)
	}{
		{
			name:       "success with note",
			observe:    observeStubFn(&stubProvider{}, "glm-5.2", true),
			script:     []stubScript{{think: "先看看中路", text: "马8进7\n稳一手"}},
			body:       observeBody(t, observeTestPrompt, observeTestState),
			wantStatus: http.StatusOK,
			check: func(t *testing.T, sp *stubProvider, resp observeResp) {
				if resp.SawThinking == 0 || resp.SawNote == 0 {
					t.Errorf("streamed lines: thinking=%d note=%d, want both > 0", resp.SawThinking, resp.SawNote)
				}
				if resp.Move != "马8进7" || resp.Note != "稳一手" || resp.Error != "" || resp.Legal != nil {
					t.Fatalf("resp = %+v, want move=马8进7 note=稳一手 no error/legal", resp)
				}
				reqs := sp.recorded()
				if len(reqs) != 1 {
					t.Fatalf("Complete calls = %d, want 1", len(reqs))
				}
				req := reqs[0]
				if req.Model != "glm-5.2" {
					t.Errorf("Model = %q, want glm-5.2", req.Model)
				}
				wantSys, err := json.Marshal(observeTestPrompt)
				if err != nil {
					t.Fatalf("marshal prompt: %v", err)
				}
				if string(req.System) != string(wantSys) {
					t.Errorf("System = %s, want %s", req.System, wantSys)
				}
				if req.MaxTokens != 32768 {
					t.Errorf("MaxTokens = %d, want 32768 (thinking headroom)", req.MaxTokens)
				}
				if req.Thinking != llm.EffortNone {
					t.Errorf("Thinking = %q, want none (game moves need no reasoning)", req.Thinking)
				}
				if !req.Stream {
					t.Error("Stream = false, want true (observe runs over the streaming API)")
				}
				if len(req.Messages) != 1 {
					t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
				}
				msg := req.Messages[0]
				if msg.Role != types.RoleUser {
					t.Errorf("Role = %q, want user", msg.Role)
				}
				if len(msg.Content) != 1 || msg.Content[0].Type != types.ContentTypeText || msg.Content[0].Text != observeTestState {
					t.Fatalf("Content = %+v, want single text block %q", msg.Content, observeTestState)
				}
			},
		},
		{
			name:       "reasoning-then-move: analysis lines precede the move token",
			observe:    observeStubFn(&stubProvider{}, "glm-5.2", true),
			script:     []stubScript{{think: "红方当头炮直攻中路", text: "红方当头炮急攻中路,我当屏风马固守。\n计划:跳马护中卒,兼通车路。\n马8进7"}},
			body:       observeBody(t, observeTestPrompt, observeTestState),
			wantStatus: http.StatusOK,
			check: func(t *testing.T, sp *stubProvider, resp observeResp) {
				if resp.Move != "马8进7" {
					t.Fatalf("Move = %q, want 马8进7 (last legal-matching line)", resp.Move)
				}
				if !strings.Contains(resp.Note, "屏风马") || !strings.Contains(resp.Note, "跳马护中卒") {
					t.Errorf("Note = %q, want the analysis lines preserved", resp.Note)
				}
				if strings.Contains(resp.Note, "马8进7") {
					t.Errorf("Note must not repeat the bare move line: %q", resp.Note)
				}
			},
		},
		{
			name:       "prose-embedded move: trailing-token rescue",
			observe:    observeStubFn(&stubProvider{}, "glm-5.2", true),
			script:     []stubScript{{text: "红车炮叠在中路强攻,只能支士垫将,先解燃眉之急,再图后计。马8进7"}},
			body:       observeBody(t, observeTestPrompt, observeTestState),
			wantStatus: http.StatusOK,
			check: func(t *testing.T, sp *stubProvider, resp observeResp) {
				if resp.Move != "马8进7" {
					t.Fatalf("Move = %q, want 马8进7 rescued from the trailing prose", resp.Move)
				}
				if resp.Error != "" {
					t.Errorf("Error = %q, want none", resp.Error)
				}
				if !strings.Contains(resp.Note, "红车炮叠") {
					t.Errorf("Note = %q, want the prose kept as the note", resp.Note)
				}
			},
		},
		{
			name:       "resignation streams final with done",
			observe:    observeStubFn(&stubProvider{}, "glm-5.2", true),
			script:     []stubScript{{text: "认输\n这局你赢了，学到了。"}},
			body:       observeBody(t, observeTestPrompt, observeTestState),
			wantStatus: http.StatusOK,
			check: func(t *testing.T, sp *stubProvider, resp observeResp) {
				if resp.Move != "认输" {
					t.Fatalf("Move = %q, want 认输", resp.Move)
				}
				if resp.Note != "这局你赢了，学到了。" {
					t.Errorf("Note = %q", resp.Note)
				}
				if !resp.Done {
					t.Error("Done = false, want true")
				}
			},
		},
		{
			name:       "success with empty note",
			observe:    observeStubFn(&stubProvider{}, "glm-5.2", true),
			script:     []stubScript{{text: "车9平8"}},
			body:       observeBody(t, observeTestPrompt, observeTestState),
			wantStatus: http.StatusOK,
			check: func(t *testing.T, sp *stubProvider, resp observeResp) {
				if resp.Move != "车9平8" {
					t.Fatalf("Move = %q, want 车9平8", resp.Move)
				}
				if resp.Note != "" {
					t.Errorf("Note = %q, want empty when the model outputs only the move", resp.Note)
				}
			},
		},
		{
			name:       "first candidate illegal then corrected",
			observe:    observeStubFn(&stubProvider{}, "glm-5.2", true),
			script:     []stubScript{{text: "炮五进二"}, {text: "车9平8\n换成出车"}},
			body:       observeBody(t, observeTestPrompt, observeTestState),
			wantStatus: http.StatusOK,
			check: func(t *testing.T, sp *stubProvider, resp observeResp) {
				if resp.Move != "车9平8" || resp.Note != "换成出车" {
					t.Fatalf("resp = %+v, want move=车9平8 note=换成出车", resp)
				}
				reqs := sp.recorded()
				if len(reqs) != 2 {
					t.Fatalf("Complete calls = %d, want 2", len(reqs))
				}
				retry := reqs[1]
				if len(retry.Messages) != 1 {
					t.Fatalf("retry len(Messages) = %d, want 1 (merged single user turn)", len(retry.Messages))
				}
				msg := retry.Messages[0]
				if msg.Role != types.RoleUser {
					t.Errorf("retry Role = %q, want user", msg.Role)
				}
				wantText := correctionFor("炮五进二")
				if len(msg.Content) != 1 || msg.Content[0].Text != wantText {
					t.Fatalf("retry Content = %+v, want single text block %q", msg.Content, wantText)
				}
			},
		},
		{
			name:       "both candidates illegal 422",
			observe:    observeStubFn(&stubProvider{}, "glm-5.2", true),
			script:     []stubScript{{text: "错一"}, {text: "错二"}},
			body:       observeBody(t, observeTestPrompt, observeTestState),
			wantStatus: http.StatusOK, // streamed error line
			check: func(t *testing.T, sp *stubProvider, resp observeResp) {
				if resp.Error != "model failed to produce a legal move in two attempts" {
					t.Fatalf("Error = %q, want model-failed message", resp.Error)
				}
				if resp.Candidate != "错二" {
					t.Errorf("Candidate = %q, want 错二", resp.Candidate)
				}
				if len(resp.Legal) != 2 || resp.Legal[0] != "马8进7" || resp.Legal[1] != "车9平8" {
					t.Errorf("Legal = %q, want [马8进7 车9平8]", resp.Legal)
				}
			},
		},
		{
			name:       "invalid JSON body 400",
			observe:    observeStubFn(&stubProvider{}, "glm-5.2", true),
			body:       "{not json",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing prompt 400",
			observe:    observeStubFn(&stubProvider{}, "glm-5.2", true),
			body:       observeBody(t, "", observeTestState),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "state without marker 400",
			observe:    observeStubFn(&stubProvider{}, "glm-5.2", true),
			body:       observeBody(t, observeTestPrompt, "马8进7\n车9平8"),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "body over 1MB 413",
			observe:    observeStubFn(&stubProvider{}, "glm-5.2", true),
			body:       `{"prompt":"p","state":"` + strings.Repeat("a", observeMaxBody) + `"}`,
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:       "unknown game name 404",
			observe:    observeStubFn(&stubProvider{}, "glm-5.2", true),
			gameName:   "nope",
			body:       observeBody(t, observeTestPrompt, observeTestState),
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "no provider available 503",
			observe:    observeStubFn(nil, "", false),
			body:       observeBody(t, observeTestPrompt, observeTestState),
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "provider error streams error line",
			observe:    observeStubFn(&stubProvider{}, "glm-5.2", true),
			script:     []stubScript{{err: errors.New("boom")}},
			body:       observeBody(t, observeTestPrompt, observeTestState),
			wantStatus: http.StatusOK, // streamed error line
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sp *stubProvider
			observe := tt.observe
			if tt.script != nil {
				sp = &stubProvider{script: tt.script}
				observe = observeStubFn(sp, "glm-5.2", true)
			}
			gameName := tt.gameName
			if gameName == "" {
				gameName = "chess"
			}
			status, header, body := postObserve(t, observe, gameName, tt.body)
			if status != tt.wantStatus {
				t.Fatalf("status = %d (body %s), want %d", status, string(body), tt.wantStatus)
			}
			wantCT := "application/json"
			if tt.wantStatus == http.StatusOK {
				wantCT = "application/x-ndjson"
			}
			if got := header.Get("Content-Type"); got != wantCT {
				t.Errorf("Content-Type = %q, want %s", got, wantCT)
			}
			resp := decodeObserveResp(t, body)
			if tt.check != nil {
				tt.check(t, sp, resp)
			}
		})
	}
}

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

func (h *mbLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *mbLogHandler) WithGroup(string) slog.Handler { return h }

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
	status, _, _ = postObserve(t, observeStubFn(&stubProvider{}, "m1", true), "chess", big)
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
