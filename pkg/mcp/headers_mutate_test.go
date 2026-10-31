package mcp

// Mutation-escape kill tests for headers.go (mutate batch m2).
// Each test pins a behavior an escaped mutant breaks; verdicts and evidence are
// in /home/yliu/mutate-mcp2/ledger-mcp2.txt.

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// headers.go:25 numbers/decrementer (10s → 9s) and numbers/incrementer
// (10s → 11s): the helper budget is exactly the TS value
// (headersHelper.ts:61 timeout:10000). A helper that finishes inside the real
// budget but outside a shortened one (or vice versa) is the only thing that
// distinguishes these, so the constant itself is pinned.
func TestHeaderHelperTimeout_MatchesTSValue(t *testing.T) {
	if headerHelperTimeout != 10*time.Second {
		t.Errorf("headerHelperTimeout = %v, want 10s (headersHelper.ts:61 timeout:10000)", headerHelperTimeout)
	}
}

// headers.go:76 statement/return (httpHeaders.getURL → "") and :80
// statement/return (wsHeaders.getURL → ""): the helper must receive the
// configured URL, not the zero value, for HTTP and WS configs too.
func TestGetDynamicHeaders_HelperSeesConfigURL(t *testing.T) {
	const helper = `printf '{"url":"%s"}' "$CLAUDE_CODE_MCP_SERVER_URL"`
	cases := []struct {
		name string
		cfg  McpServerConfig
		want string
	}{
		{"http", &HTTPConfig{URL: "https://http.example/mcp", HeadersHelper: helper}, "https://http.example/mcp"},
		{"ws", &WSConfig{URL: "wss://ws.example/mcp", HeadersHelper: helper}, "wss://ws.example/mcp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GetDynamicHeaders(context.Background(), "srv", tc.cfg, ScopeUser, true)
			if got == nil {
				t.Fatalf("headers = nil, want url=%q", tc.want)
			}
			if got["url"] != tc.want {
				t.Errorf("CLAUDE_CODE_MCP_SERVER_URL = %q, want %q", got["url"], tc.want)
			}
		})
	}
}

// headers.go:125 numbers/decrementer (cmd.WaitDelay 2s → 1s): a helper that
// exits promptly but leaves a background child holding the stdout pipe must
// still succeed — the 2s grace covers the 1.2s child. With a 1s WaitDelay
// cmd.Output() fails with ErrWaitDelay and the headers are dropped.
func TestGetDynamicHeaders_LingeringChildWithinWaitDelay(t *testing.T) {
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: `sleep 1.2 & echo '{"k":"v"}'`, // REAL-TIME: child sits in the 1s mutant / 2s real WaitDelay gap with 0.8s margin on the pass side
	}
	got := GetDynamicHeaders(context.Background(), "srv", cfg, ScopeUser, true)
	if got == nil {
		t.Fatal("headers = nil, want k=v (helper exited cleanly within the 2s WaitDelay)")
	}
	if got["k"] != "v" {
		t.Errorf("headers[k] = %q, want %q", got["k"], "v")
	}
}

// headers.go:134 expression/error-guard, :135 branch/if and :135
// statement/remove: a helper that exits non-zero must yield no headers even
// when it wrote a complete JSON object to stdout — cmd.Output() hands back that
// stdout along with the *ExitError, so dropping the guard turns a failed
// helper into usable credentials.
func TestGetDynamicHeaders_NonZeroExitDiscardsStdout(t *testing.T) {
	cfg := &SSEConfig{
		URL:           "http://example.com",
		HeadersHelper: `echo '{"k":"v"}'; exit 1`,
	}
	if got := GetDynamicHeaders(context.Background(), "srv", cfg, ScopeUser, true); got != nil {
		t.Errorf("headers = %v, want nil (helper exited 1)", got)
	}
}

type headerLogRecord struct {
	msg   string
	attrs map[string]string
}

type headerLogRecorder struct {
	mu      sync.Mutex
	records []headerLogRecord
}

func (h *headerLogRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (h *headerLogRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *headerLogRecorder) WithGroup(string) slog.Handler { return h }

func (h *headerLogRecorder) Handle(_ context.Context, r slog.Record) error {
	rec := headerLogRecord{msg: r.Message, attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, rec)
	h.mu.Unlock()
	return nil
}

func (h *headerLogRecorder) logged() []headerLogRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]headerLogRecord(nil), h.records...)
}

// headers.go:142 branch/if (empty-output guard body removed): an empty helper
// response is an expected outcome and must return nil without going through the
// JSON parser — the parse failure would log "headers helper returned invalid
// JSON" for a run that returned nothing at all.
func TestGetDynamicHeaders_EmptyOutputIsNotAParseFailure(t *testing.T) {
	rec := &headerLogRecorder{}
	old := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(old) })

	cfg := &SSEConfig{URL: "http://example.com", HeadersHelper: "true"}
	if got := GetDynamicHeaders(context.Background(), "srv", cfg, ScopeUser, true); got != nil {
		t.Errorf("headers = %v, want nil", got)
	}
	if recs := rec.logged(); len(recs) != 0 {
		t.Errorf("log records = %v, want none (empty output is not an invalid-JSON failure)", recs)
	}
}

// headers.go:135 statement/remove (the warning is dropped, the nil return
// stays): a failed helper returns nil headers just like an unconfigured one, so
// the warning is the only signal that the helper ran and broke.
func TestGetDynamicHeaders_HelperFailureIsLogged(t *testing.T) {
	rec := &headerLogRecorder{}
	old := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(old) })

	cfg := &SSEConfig{URL: "http://example.com", HeadersHelper: "exit 3"}
	if got := GetDynamicHeaders(context.Background(), "srv", cfg, ScopeUser, true); got != nil {
		t.Errorf("headers = %v, want nil", got)
	}
	recs := rec.logged()
	if len(recs) != 1 {
		t.Fatalf("log records = %v, want exactly one helper-failure record", recs)
	}
	if recs[0].msg != "mcp: headers helper failed" {
		t.Errorf("log message = %q, want %q", recs[0].msg, "mcp: headers helper failed")
	}
	if recs[0].attrs["server"] != "srv" {
		t.Errorf("log server attr = %q, want %q", recs[0].attrs["server"], "srv")
	}
	if recs[0].attrs["error"] != "exit status 3" {
		t.Errorf("log error attr = %q, want %q", recs[0].attrs["error"], "exit status 3")
	}
}
