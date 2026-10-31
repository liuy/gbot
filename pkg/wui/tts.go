// /api/tts: one sentence in (text/plain), raw PCM out (s16le@24k mono,
// chunked). Voice/model/seed resolve server-side via config.ResolveTTS, so
// the client carries no backend knowledge. The 200 header is deferred to
// the first PCM chunk: config/resolve/synthesis-startup failures must
// surface as HTTP error codes, not as a half-open 200 stream.
package wui

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/liuy/gbot/pkg/audio/tts"
	"github.com/liuy/gbot/pkg/config"
)

const ttsBodyLimit = 1 << 20 // one sentence is tiny; this is an abuse bound

func RegisterTTSRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/tts", handleTTS)
	mux.HandleFunc("GET /api/tts/voices", handleTTSVoices)
}

// handleTTSVoices relays the engine's speaker catalog for the configured
// model (audio.cpp GET /audio/voices?model=). The names are backend
// knowledge: this only feeds a settings picker. Unconfigured TTS is a
// normal state and answers an empty list, not an error.
//
// Per-request config.Load is deliberate (both handlers): settings edits —
// tts.voice above all — must land without a daemon restart; do not hoist
// this to a startup-cached config like the STT side.
func handleTTSVoices(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load()
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "tts: config load failed: "+err.Error())
		return
	}
	p, model, err := cfg.ResolveTTS()
	if err != nil {
		writeJSON(w, http.StatusOK, struct {
			Voices []string `json:"voices"`
		}{})
		return
	}
	voices, err := (&tts.Client{}).Voices(r.Context(), tts.Request{
		BaseURL: strings.TrimRight(p.URL, "/"),
		Model:   model,
		APIKey:  p.ResolveKey(),
	})
	if err != nil {
		errorJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	if voices == nil {
		voices = []string{}
	}
	writeJSON(w, http.StatusOK, struct {
		Voices []string `json:"voices"`
	}{Voices: voices})
}

func handleTTS(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, ttsBodyLimit)
	text, err := io.ReadAll(r.Body)
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &mbe):
		errorJSON(w, http.StatusRequestEntityTooLarge, "tts: body too large")
	case err != nil:
		errorJSON(w, http.StatusBadRequest, "tts: read body: "+err.Error())
	case len(text) == 0:
		errorJSON(w, http.StatusBadRequest, "tts: empty body")
	default:
		speakTTS(w, r, string(text))
	}
}

func speakTTS(w http.ResponseWriter, r *http.Request, text string) {
	cfg, err := config.Load()
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "tts: config load failed: "+err.Error())
		return
	}
	p, model, err := cfg.ResolveTTS()
	if err != nil {
		errorJSON(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	wrote := false
	streamPCM := func(pcm []byte) {
		if !wrote {
			w.Header().Set("Content-Type", "audio/L16;rate=24000;channels=1")
			w.WriteHeader(http.StatusOK)
			wrote = true
		}
		_, _ = w.Write(pcm)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	// Client abort cancels r.Context(), which cancels the upstream synthesis.
	err = (&tts.Client{}).Stream(r.Context(), tts.Request{
		BaseURL: strings.TrimRight(p.URL, "/"),
		Model:   model,
		APIKey:  p.ResolveKey(),
		Text:    text,
		Voice:   cfg.TTS.Voice,
	}, streamPCM)
	if err != nil && !wrote {
		// Failed before any audio: the client still gets a real status.
		errorJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	// err != nil && wrote: truncated stream ends here. err == nil && !wrote:
	// a zero-PCM synthesis is a valid (empty) audio response.
	if err == nil && !wrote {
		w.Header().Set("Content-Type", "audio/L16;rate=24000;channels=1")
		w.WriteHeader(http.StatusOK)
	}
}
