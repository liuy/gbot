package wui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Audio providers serve the standard OpenAI /models list (audio.cpp adds a
// mode field); fetch must list their entries like any OpenAI provider, not
// report manual.
func TestFetchModels_AudioListsModels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"stt-stream","mode":"streaming"},{"id":"stt","mode":"offline"}]}`))
	}))
	defer upstream.Close()

	mux := http.NewServeMux()
	RegisterSettingsRoutes(mux)
	req := httptest.NewRequest("POST", "/api/settings/models",
		strings.NewReader(`{"url":"`+upstream.URL+`","type":"audio"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	got := rec.Body.String()
	for _, want := range []string{`"id":"stt-stream"`, `"id":"stt"`, `"mode":"fetched"`} {
		if !strings.Contains(got, want) {
			t.Errorf("response missing %s: %s", want, got)
		}
	}
}

// audio.cpp's /models entries carry a task field ("asr"|"tts") alongside id;
// the proxy must pass it through verbatim so the UI can badge and split the
// STT/TTS cards. Entries without a task field must stay task-free.
func TestFetchModels_AudioPassesTaskField(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"asr-m","task":"asr"},{"id":"tts-m","task":"tts"},{"id":"plain-m"}]}`))
	}))
	defer upstream.Close()

	mux := http.NewServeMux()
	RegisterSettingsRoutes(mux)
	req := httptest.NewRequest("POST", "/api/settings/models",
		strings.NewReader(`{"url":"`+upstream.URL+`","type":"audio"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	got := rec.Body.String()
	for _, want := range []string{
		`"id":"asr-m","task":"asr"`,
		`"id":"tts-m","task":"tts"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("response missing %s: %s", want, got)
		}
	}
	if !strings.Contains(got, `"id":"plain-m"}`) {
		t.Errorf("taskless entry must marshal without a task field: %s", got)
	}
}
