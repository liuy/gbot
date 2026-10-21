package wui

import (
	"context"
	"net/http"
	"time"

	"github.com/liuy/gbot/pkg/reload"
)

// reloadRouteTimeout caps one reload request: the MCP reconcile may wait out
// the full drain window (10s per server group) plus reconnects.
const reloadRouteTimeout = 120 * time.Second

// RegisterReloadRoute mounts POST /api/settings/reload — triggers the
// daemon-side config reload and answers with the full per-file report.
// Always 200: the report's Applied flag carries the outcome (abort is a
// result, not a transport failure). 120s cap covers the MCP drain window.
func RegisterReloadRoute(mux *http.ServeMux, reloadFn func(ctx context.Context) *reload.Report) {
	mux.HandleFunc("POST /api/settings/reload", func(w http.ResponseWriter, r *http.Request) {
		if reloadFn == nil {
			// Defensive: never nil in practice (Start always wires the
			// orchestrator), but a nil fn would panic below.
			errorJSON(w, http.StatusServiceUnavailable, "reload not available")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), reloadRouteTimeout)
		defer cancel()
		report := reloadFn(ctx)
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, report)
	})
}
