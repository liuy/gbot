package app

import (
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
)

// startPprofServer starts a localhost-bound pprof HTTP server.
// Priority: GBOT_PPROF_ADDR env > cfg.PprofAddr > "localhost:6060" default.
// "off" or "-" disables the server. Logs the listen address (or skip notice).
func startPprofServer(cfgAddr string) {
	addr := cfgAddr
	if env := os.Getenv("GBOT_PPROF_ADDR"); env != "" {
		addr = env
	}
	if addr == "" {
		addr = "localhost:6060"
	}
	if addr == "off" || addr == "-" {
		slog.Info("pprof:disabled")
		return
	}
	// net/http/pprof registers handlers on DefaultServeMux at import time.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Warn("pprof:listen_failed", "addr", addr, "error", err)
		return
	}
	go func() {
		slog.Info("pprof:listen", "addr", ln.Addr().String())
		if err := http.Serve(ln, http.DefaultServeMux); err != nil {
			slog.Warn("pprof:server_failed", "error", err)
		}
	}()
}
