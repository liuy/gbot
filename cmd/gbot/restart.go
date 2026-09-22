package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// restartBaseURL resolves the daemon endpoint: GBOT_WS_ADDR (the daemon's
// own override) wins, else 127.0.0.1:<port>.
func restartBaseURL(port string) string {
	if addr := os.Getenv("GBOT_WS_ADDR"); addr != "" {
		if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
			return addr
		}
		return "http://" + addr
	}
	return "http://127.0.0.1:" + port
}

type restartBusyItem struct {
	Kind     string `json:"kind"`
	Engine   string `json:"engine"`
	EngineID string `json:"engineId"`
	Session  string `json:"session"`
	Detail   string `json:"detail"`
}

// runRestart POSTs /api/admin/restart and prints a human summary.
// Exit codes: 0 = upgrading, 2 = busy (409), 1 = unreachable/other error.
func runRestart(baseURL string, out io.Writer) int {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(baseURL+"/api/admin/restart", "application/json", nil)
	if err != nil {
		_, _ = fmt.Fprintf(out, "cannot reach gbot daemon at %s: %v\n", baseURL, err)
		return 1
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted:
		_, _ = fmt.Fprintln(out, "Upgrading: gbot is restarting into the new binary; the web UI reconnects automatically.")
		return 0
	case http.StatusConflict:
		var body struct {
			Items []restartBusyItem `json:"items"`
		}
		if derr := json.NewDecoder(resp.Body).Decode(&body); derr != nil {
			_, _ = fmt.Fprintf(out, "unexpected 409 body: %v\n", derr)
			return 1
		}
		_, _ = fmt.Fprintf(out, "Restart deferred — %d active:\n", len(body.Items))
		for _, it := range body.Items {
			if it.Kind == "job" {
				_, _ = fmt.Fprintf(out, "- [job] %s: %s\n", it.Engine, it.Detail)
				continue
			}
			if it.Session != "" {
				_, _ = fmt.Fprintf(out, "- [%s] %s (%s): %s\n", it.Kind, it.Engine, it.Session, it.Detail)
			} else {
				_, _ = fmt.Fprintf(out, "- [%s] %s: %s\n", it.Kind, it.Engine, it.Detail)
			}
		}
		return 2
	case http.StatusNotImplemented:
		// Prefer the server's refusal sentence (TUI gate, future codes);
		// fall back to the platform default only when the body is mute.
		var body struct {
			Error string `json:"error"`
		}
		msg := "restart not supported on this platform"
		if derr := json.NewDecoder(resp.Body).Decode(&body); derr == nil && body.Error != "" {
			msg = body.Error
		}
		_, _ = fmt.Fprintln(out, msg)
		return 1
	default:
		_, _ = fmt.Fprintf(out, "unexpected status %d\n", resp.StatusCode)
		return 1
	}
}
