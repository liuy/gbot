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
