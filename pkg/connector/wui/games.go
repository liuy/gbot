package wui

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// chessTemplate backs the GET fallback for names in the builtin game
// registry: when no artifact file exists on disk, the bundled page is served
// instead, so the game works on a fresh projectspace with no setup.
//
//go:embed games/chess.html
var chessTemplate []byte

// builtinGames gates both the GET fallback and the POST handler — a same-named
// file on disk must not conjure an observable game out of nowhere.
var builtinGames = map[string]struct{}{"chess": {}}

const observeMaxBody = 1 << 20

// ObserveProviderFn resolves the active LLM at request time (engines can be
// switched or absent); ok=false means no provider is usable right now.
type ObserveProviderFn func() (provider llm.Provider, model string, ok bool)

// observeRequest is the POST body the bundled game pages send.
type observeRequest struct {
	Prompt string `json:"prompt"`
	State  string `json:"state"`
}

// observerHandler turns a game observation into one LLM call and validates the
// reply against the observation's own legal-move list. The body's arbitrary
// system prompt carries the same trust level as the existing unauthenticated
// WS chat (the wui network boundary IS the trust boundary), so this adds no
// new attack surface.
func observerHandler(observe ObserveProviderFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if _, known := builtinGames[name]; !known {
			slog.Warn("wui:observer_reject", "name", name, "status", http.StatusNotFound)
			writeObserveError(w, http.StatusNotFound, "unknown game")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, observeMaxBody)
		var body observeRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				slog.Warn("wui:observer_reject", "name", name, "status", http.StatusRequestEntityTooLarge)
				writeObserveError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			slog.Warn("wui:observer_reject", "name", name, "status", http.StatusBadRequest, "err", err.Error())
			writeObserveError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if body.Prompt == "" {
			slog.Warn("wui:observer_reject", "name", name, "status", http.StatusBadRequest, "err", "missing prompt")
			writeObserveError(w, http.StatusBadRequest, "missing prompt")
			return
		}
		legal, err := parseLegalMoves(body.State)
		if err != nil {
			slog.Warn("wui:observer_reject", "name", name, "status", http.StatusBadRequest, "err", err.Error())
			writeObserveError(w, http.StatusBadRequest, err.Error())
			return
		}
		provider, model, ok := observe()
		if !ok || provider == nil {
			slog.Warn("wui:observer_reject", "name", name, "status", http.StatusServiceUnavailable)
			writeObserveError(w, http.StatusServiceUnavailable, "no LLM provider available")
			return
		}
		slog.Info("wui:observer_request", "name", name, "model", model, "legal", len(legal), "state_bytes", len(body.State))

		// From here the reply streams: NDJSON lines with per-event flush, so
		// the board can render thinking and the note as they are produced.
		// Errors past this point travel as {"type":"error"} lines, not codes.
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		emit := func(payload map[string]any) {
			b, mErr := json.Marshal(payload)
			if mErr != nil {
				return
			}
			_, _ = w.Write(append(b, '\n'))
			_ = rc.Flush()
		}

		resp, err := observeComplete(r.Context(), provider, name, model, body.Prompt, []types.Message{observeUserMsg(body.State)}, emit)
		if err != nil {
			slog.Warn("wui:observer_error", "name", name, "model", model, "attempt", 1, "err", err.Error())
			emit(map[string]any{"type": "error", "message": "LLM call failed: " + err.Error()})
			return
		}
		candidate, note := splitCandidateNote(resp)
		// Resignation is not a board move — it never appears in the legal
		// list, so it must be accepted before list validation.
		if candidate == "认输" {
			slog.Info("wui:observer", "name", name, "model", model, "attempt", 1, "move", candidate)
			emit(map[string]any{"type": "final", "move": candidate, "note": note, "done": true})
			return
		}
		if containsMove(legal, candidate) {
			slog.Info("wui:observer", "name", name, "model", model, "attempt", 1, "move", candidate, "legal", len(legal))
			emit(map[string]any{"type": "final", "move": candidate, "note": note})
			return
		}
		slog.Warn("wui:observer_illegal", "name", name, "model", model, "attempt", 1,
			"candidate", candidate, "legal", len(legal), "legal_head", strings.Join(legal[:min(5, len(legal))], " "))

		// One corrective retry. The correction is merged into a single user
		// message because Anthropic requires alternating roles — two
		// consecutive user turns would be rejected with a 400.
		emit(map[string]any{"type": "retry", "candidate": candidate})
		retryText := body.State + "\n\n" + fmt.Sprintf(
			"Your reply \"%s\" is not in the legal-move list. Legal moves:\n%s\nReply with exactly one entry from the list as the first line.",
			candidate, strings.Join(legal, "\n"))
		resp, err = observeComplete(r.Context(), provider, name, model, body.Prompt, []types.Message{observeUserMsg(retryText)}, emit)
		if err != nil {
			slog.Warn("wui:observer_error", "name", name, "model", model, "attempt", 2, "err", err.Error())
			emit(map[string]any{"type": "error", "message": "LLM call failed: " + err.Error()})
			return
		}
		candidate, note = splitCandidateNote(resp)
		if containsMove(legal, candidate) {
			slog.Info("wui:observer", "name", name, "model", model, "attempt", 2, "move", candidate, "legal", len(legal))
			emit(map[string]any{"type": "final", "move": candidate, "note": note})
			return
		}
		slog.Warn("wui:observer_illegal", "name", name, "model", model, "attempt", 2,
			"candidate", candidate, "legal", len(legal))
		emit(map[string]any{"type": "error", "message": "model failed to produce a legal move in two attempts", "candidate": candidate, "legal": strings.Join(legal, ",")})
	}
}

// observeComplete runs the observe turn over the streaming API and folds the
// events back into a single Response. Streaming is required (not Complete):
// the driver's SSE idle timeout (timeoutReader) only guards streamed bodies,
// and it is what turns a stalled upstream into an error event — no wall-clock
// cap on a non-streaming call can do that.
func observeComplete(ctx context.Context, provider llm.Provider, name, model, prompt string, msgs []types.Message, emit func(map[string]any)) (*llm.Response, error) {
	sys, err := json.Marshal(prompt)
	if err != nil {
		return nil, err
	}
	events, err := provider.Stream(ctx, &llm.Request{
		Model:  model,
		System: sys,
		// Reasoning models default thinking ON and a game move does not need
		// it — glm-5.3 burned 26K tokens per turn thinking. Budget params are
		// silently ignored by these providers; only the on/off toggle works.
		Thinking: &llm.ThinkingConfig{Type: "disabled"},
		// Thinking tokens count toward max_tokens, and omitting the field
		// makes the provider apply its own small default — either way deep
		// thinking starves into an empty reply (observed with glm-5.3).
		// 32768 matches the global DefaultCapabilities budget.
		MaxTokens: 32768,
		Messages:  msgs,
		Stream:    true,
	})
	if err != nil {
		return nil, err
	}
	var text strings.Builder
	var stopReason string
	var usage *types.Usage
	for ev := range events {
		if ev.Error != nil {
			return nil, fmt.Errorf("stream error: %v", ev.Error)
		}
		if ev.DeltaMsg != nil {
			stopReason = ev.DeltaMsg.StopReason
		}
		if ev.Usage != nil {
			usage = ev.Usage
		}
		if ev.Type == "content_block_delta" && ev.Delta != nil {
			switch ev.Delta.Type {
			case "text_delta":
				text.WriteString(ev.Delta.Text)
				slog.Info("wui:observer_text", "name", name, "text", ev.Delta.Text)
				emit(map[string]any{"type": "note", "text": ev.Delta.Text})
			case "thinking_delta":
				slog.Info("wui:observer_thinking", "name", name, "text", ev.Delta.Thinking)
				emit(map[string]any{"type": "thinking", "text": ev.Delta.Thinking})
			}
		}
	}
	// An empty reply needs its stop_reason recorded: "length" means the
	// budget vanished into reasoning, anything else means the model chose
	// to answer nothing.
	slog.Info("wui:observer_done", "name", name, "stop_reason", stopReason, "text_chars", text.Len(),
		"output_tokens", usageField(usage, func(u *types.Usage) int { return u.OutputTokens }))
	return &llm.Response{Content: []types.ContentBlock{types.NewTextBlock(text.String())}}, nil
}

func usageField(u *types.Usage, get func(*types.Usage) int) int {
	if u == nil {
		return -1
	}
	return get(u)
}

func observeUserMsg(text string) types.Message {
	return types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock(text)},
	}
}

// splitCandidateNote takes the first non-empty text block; the first non-empty
// line is the move candidate (models often emit leading blank lines), the
// remaining lines joined by \n are the player's note.
func splitCandidateNote(resp *llm.Response) (candidate, note string) {
	var text string
	for _, block := range resp.Content {
		if block.Type == types.ContentTypeText && block.Text != "" {
			text = block.Text
			break
		}
	}
	lines := strings.Split(text, "\n")
	idx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", ""
	}
	candidate = strings.TrimSpace(lines[idx])
	note = strings.TrimSpace(strings.Join(lines[idx+1:], "\n"))
	return candidate, note
}

func containsMove(legal []string, candidate string) bool {
	for _, m := range legal {
		if m == candidate {
			return true
		}
	}
	return false
}

func writeObserveJSON(w http.ResponseWriter, status int, payload any) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payloadBytes)
}

func writeObserveError(w http.ResponseWriter, status int, msg string) {
	writeObserveJSON(w, status, map[string]string{"error": msg})
}

// parseLegalMoves extracts the legal-move list from a game observation: the
// entries are the non-empty trimmed lines after the LAST line that is exactly
// the "legal-moves:" marker (trim-tolerant). The last marker wins because the
// observation is append-oriented and a stale marker from earlier turns must
// not shadow the current list.
func parseLegalMoves(state string) ([]string, error) {
	const marker = "legal-moves:"
	lines := strings.Split(state, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == marker {
			start = i
		}
	}
	if start < 0 {
		return nil, errors.New("no legal-moves: marker in state")
	}
	var moves []string
	for _, line := range lines[start+1:] {
		if t := strings.TrimSpace(line); t != "" {
			moves = append(moves, t)
		}
	}
	if len(moves) == 0 {
		return nil, errors.New("no legal moves after legal-moves: marker")
	}
	return moves, nil
}
