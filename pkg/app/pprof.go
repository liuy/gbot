package app

import (
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
)

// StartPprofServer starts a localhost-bound pprof HTTP server.
// Priority: GBOT_PPROF_ADDR env > cfgAddr > "localhost:6060" default.
// "off" or "-" disables the server. Logs the listen address (or skip notice).
// upg non-nil routes the bind through tableflip's fd handover so an upgraded
// child reuses the parent's listening socket instead of racing the draining
// parent into EADDRINUSE (2026-11-16); nil (supervised, unsupported platform,
// tableflip construction failure) keeps the plain bind.
func StartPprofServer(cfgAddr string, upg Upgrader) {
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
	var ln net.Listener
	var err error
	if upg != nil {
		ln, err = upg.Listen("tcp", addr)
	} else {
		ln, err = net.Listen("tcp", addr)
	}
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
