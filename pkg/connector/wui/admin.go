package wui

import (
	"log/slog"
	"net/http"
)

// AdminVersion is the build identity reported by the admin restart endpoint:
// one "version · commit" string — nothing downstream (tableflip re-execs
// whatever binary is on disk) consumes the parts separately, so the wire
// stays as single-field as the display.
type AdminVersion struct {
	Build string `json:"build"`
}

// BusyItem is one unit of activity a restart would interrupt.
type BusyItem struct {
	Kind     string `json:"kind"`   // "query" | "job"
	Engine   string `json:"engine"` // display name
	EngineID string `json:"engineId"`
	Session  string `json:"session,omitempty"` // active session title
	Detail   string `json:"detail,omitempty"`  // in-flight prompt excerpt or job command
}

// BusyReport is the restart gate's answer: what is running right now.
type BusyReport struct {
	Busy  bool       `json:"busy"`
	Items []BusyItem `json:"items"`
}

// RestartOutcome is what RequestRestart decided for a restart trigger.
type RestartOutcome int

const (
	RestartStarted     RestartOutcome = iota // upgrade kicked off
	RestartBusy                              // refused: activity in flight
	RestartUnsupported                       // no upgrader (Windows)
)

// RequestRestart is the single busy-gated entry for triggering a binary
// upgrade, shared by POST /api/admin/restart and the daemon's SIGUSR2
// handler — the signal must not be an ungated bypass around the busy
// check. Busy → the report is returned for the caller to surface (409
// body / log line) and no upgrade starts; idle → upgrade() is launched
// in a goroutine (tableflip's Upgrade blocks until the child is ready
// or fails, so callers must not wait on it; its error is logged here);
// upgrade == nil → RestartUnsupported.
func RequestRestart(busy func() BusyReport, upgrade func() error) (RestartOutcome, BusyReport) {
	report := busy()
	if report.Busy {
		return RestartBusy, report
	}
	if upgrade == nil {
		return RestartUnsupported, report
	}
	go func() {
		if err := upgrade(); err != nil {
			slog.Warn("upgrade: failed", "error", err)
		}
	}()
	return RestartStarted, report
}

// AdminDeps wires the admin restart endpoints without importing pkg/app.
type AdminDeps struct {
	Probe   func() BusyReport // never nil; GET display
	Upgrade func() error      // raw tableflip trigger; nil = unsupported
	// UnsupportedCode is a stable CODE naming why Upgrade is nil;
	// UnsupportedMessage is the human sentence for CLI consumers. The GET
	// payload and 501 body carry the code for frontend i18n mapping.
	// Empty code = plain platform gap.
	UnsupportedCode    string
	UnsupportedMessage string
	Version            AdminVersion
}

// Refusal codes for AdminDeps.UnsupportedCode — contract with the wui
// frontend's i18n mapping (web/ui/src/upgrade_mode.ts).
const RefusalTUIMode = "tui_mode"

// adminStatePayload is the GET /api/admin/restart body: busy state +
// activity list + whether this platform can upgrade + build identity.
type adminStatePayload struct {
	BusyReport
	Upgradable bool   `json:"upgradable"`
	Reason     string `json:"reason,omitempty"`
	AdminVersion
}

// RegisterAdminRoutes mounts:
//
//	GET  /api/admin/restart — current busy state + activity list + build identity
//	POST /api/admin/restart — maps RequestRestart outcomes: Busy → 409 +
//	     BusyReport, Unsupported → 501, Started → 202
//
// The gating decision lives in RequestRestart alone — the handler (like
// the signal path) only maps outcomes to transport.
func RegisterAdminRoutes(mux *http.ServeMux, deps AdminDeps) {
	mux.HandleFunc("GET /api/admin/restart", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, adminStatePayload{
			BusyReport:   deps.Probe(),
			Upgradable:   deps.Upgrade != nil,
			Reason:       deps.UnsupportedCode,
			AdminVersion: deps.Version,
		})
	})
	mux.HandleFunc("POST /api/admin/restart", func(w http.ResponseWriter, r *http.Request) {
		outcome, report := RequestRestart(deps.Probe, deps.Upgrade)
		switch outcome {
		case RestartBusy:
			writeJSON(w, http.StatusConflict, report)
		case RestartUnsupported:
			msg := deps.UnsupportedMessage
			if msg == "" {
				msg = "restart not supported on this platform"
			}
			// errorJSON's envelope is {"error": msg}; the code rides a
			// dedicated field so the wui can localize known refusals.
			writeJSON(w, http.StatusNotImplemented, map[string]string{
				"error":  msg,
				"reason": deps.UnsupportedCode,
			})
		case RestartStarted:
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "upgrading"})
		}
	})
}
