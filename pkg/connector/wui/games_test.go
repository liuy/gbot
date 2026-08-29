package wui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
				if req.Thinking == nil || req.Thinking.Type != "disabled" {
					t.Errorf("Thinking = %v, want disabled (game moves need no reasoning)", req.Thinking)
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
