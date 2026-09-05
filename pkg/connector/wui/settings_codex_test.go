package wui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchModels_CodexShape(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"slug":"glm-5.3","context_window":1048576,"input_modalities":["text","image"]},{"slug":"glm-5.3-flash","context_window":1048576,"input_modalities":["text"]}]}`))
	}))
	defer upstream.Close()

	mux := http.NewServeMux()
	RegisterSettingsRoutes(mux)
	req := httptest.NewRequest("POST", "/api/settings/models",
		strings.NewReader(`{"url":"`+upstream.URL+`","key":"k","type":"responses"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	got := rec.Body.String()
	for _, want := range []string{`"id":"glm-5.3"`, `"context":"1M"`, `"input":["text","image"]`, `"mode":"fetched"`} {
		if !strings.Contains(got, want) {
			t.Errorf("response missing %s: %s", want, got)
		}
	}
}
