package wui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/reload"
)

func fixtureReport(applied bool) *reload.Report {
	rep := &reload.Report{
		Applied: applied,
		Phase:   4,
		Files: []reload.FileEntry{
			{Path: "~/.gbot/settings.json", Status: reload.StatusReloaded, Note: "max_tokens: 32000 → 16000"},
			{Path: "MEMORY.md · CLAUDE.md", Status: reload.StatusUnchanged},
		},
	}
	rep.Counts.Reloaded, rep.Counts.Unchanged, rep.Counts.Failed = 1, 1, 0
	rep.EnginesRefreshed = 2
	rep.SettingsChanged = true
	rep.LastReloadAt = time.Unix(1700000000, 0).UTC()
	if !applied {
		rep.Phase = 1
		rep.Err = "plugins: invalid mcp.json"
		rep.Files = []reload.FileEntry{
			{Path: "/x/plugins/broken/mcp.json", Status: reload.StatusFailed, ErrLine: "bad json"},
			{Path: "~/.gbot/settings.json", Status: reload.StatusUnchanged},
		}
		rep.Counts.Reloaded, rep.Counts.Unchanged, rep.Counts.Failed = 0, 1, 1
		rep.SettingsChanged = false
	}
	return rep
}

func postReload(t *testing.T, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/reload", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestReloadRoute_ReturnsReport(t *testing.T) {
	mux := http.NewServeMux()
	fixture := fixtureReport(true)
	RegisterReloadRoute(mux, func(ctx context.Context) *reload.Report {
		return fixture
	})

	rec := postReload(t, mux.ServeHTTP)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got reload.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !got.Applied || got.Phase != 4 || got.EnginesRefreshed != 2 || !got.SettingsChanged {
		t.Errorf("report fields mismatch: %+v", got)
	}
	if len(got.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(got.Files))
	}
	if got.Files[0].Path != "~/.gbot/settings.json" || got.Files[0].Status != reload.StatusReloaded || got.Files[0].Note != "max_tokens: 32000 → 16000" {
		t.Errorf("files[0] = %+v, want the fixture row verbatim", got.Files[0])
	}
	if got.Files[1].Status != reload.StatusUnchanged {
		t.Errorf("files[1] = %+v, want unchanged", got.Files[1])
	}
	if got.Counts.Reloaded != 1 || got.Counts.Unchanged != 1 || got.Counts.Failed != 0 {
		t.Errorf("counts = %+v, want 1/1/0", got.Counts)
	}
}

func TestReloadRoute_AbortEnvelope(t *testing.T) {
	mux := http.NewServeMux()
	RegisterReloadRoute(mux, func(ctx context.Context) *reload.Report {
		return fixtureReport(false)
	})

	rec := postReload(t, mux.ServeHTTP)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (abort is a result, not a transport failure)", rec.Code)
	}
	var got reload.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.Applied {
		t.Error("applied must be false")
	}
	if got.Err == "" {
		t.Error("err must be populated on an aborted reload")
	}
	if got.Phase != 1 {
		t.Errorf("phase = %d, want 1", got.Phase)
	}
}

func TestReloadRoute_NilFn503(t *testing.T) {
	mux := http.NewServeMux()
	RegisterReloadRoute(mux, nil)

	rec := postReload(t, mux.ServeHTTP)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] == nil || body["error"] == "" {
		t.Errorf("body must carry an error message, got %v", body)
	}
}

func TestReloadRoute_PassesContext(t *testing.T) {
	mux := http.NewServeMux()
	sawDeadline := false
	RegisterReloadRoute(mux, func(ctx context.Context) *reload.Report {
		_, ok := ctx.Deadline()
		sawDeadline = ok
		return fixtureReport(true)
	})

	rec := postReload(t, mux.ServeHTTP)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !sawDeadline {
		t.Error("handler must pass a context with a deadline to the reload fn")
	}
}
