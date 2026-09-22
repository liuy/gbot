package wui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool/job"
	"github.com/liuy/gbot/pkg/types"
)

// newAdminTestServer mounts the admin routes on a real HTTP server with a
// connector-backed Probe (mockEngine) and the given Upgrade fake, so the
// REAL RequestRestart gating runs behind the full handler path.
func newAdminTestServer(t *testing.T, c *WUIConnector, upgrade func() error) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	RegisterAdminRoutes(mux, AdminDeps{
		Probe:   c.BusyReport,
		Upgrade: upgrade,
		Version: AdminVersion{Build: "test-v · test-c"},
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func postAdminRestart(t *testing.T, url string) (int, adminStatePayloadResponse) {
	t.Helper()
	resp, err := http.Post(url+"/api/admin/restart", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/admin/restart: %v", err)
	}
	defer resp.Body.Close()
	var body adminStatePayloadResponse
	if resp.StatusCode != http.StatusAccepted {
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode %d body: %v", resp.StatusCode, err)
		}
	}
	return resp.StatusCode, body
}

// adminStatePayloadResponse decodes both the GET state shape and the 409
// BusyReport body (a subset).
type adminStatePayloadResponse struct {
	Busy       bool          `json:"busy"`
	Items      []busyItemRaw `json:"items"`
	Upgradable bool          `json:"upgradable"`
	Reason     string        `json:"reason"`
	Build      string        `json:"build"`
}

type busyItemRaw struct {
	Kind     string `json:"kind"`
	Engine   string `json:"engine"`
	EngineID string `json:"engineId"`
	Session  string `json:"session"`
	Detail   string `json:"detail"`
}

func getAdminRestart(t *testing.T, url string) adminStatePayloadResponse {
	t.Helper()
	resp, err := http.Get(url + "/api/admin/restart")
	if err != nil {
		t.Fatalf("GET /api/admin/restart: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", resp.StatusCode)
	}
	var body adminStatePayloadResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode GET body: %v", err)
	}
	return body
}

// setSlotName labels the connector's mock slot with a display name.
func setSlotName(t *testing.T, c *WUIConnector, name string) {
	t.Helper()
	c.slotsMu.Lock()
	defer c.slotsMu.Unlock()
	s := c.slots["main"]
	if s == nil {
		t.Fatal("no main slot")
	}
	s.name = name
}

func TestAdminRestartPOST_BusyQuery409(t *testing.T) {
	c := newTestConnector(t)
	setSlotName(t, c, "Main")
	c.mock().isBusyFn = func() bool { return true }
	c.mock().SetMessagesFn(func() []types.Message {
		return []types.Message{{
			ID: "u1", Role: types.RoleUser,
			Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "fix the scanner bug"}},
		}}
	})
	c.mock().queryStartMsgIdxFn = func() int { return 0 }
	c.mock().sessionIDFn = func() string { return "sess-1" }
	c.mock().listSessionsFn = func(int) ([]*short.Session, error) {
		return []*short.Session{{SessionID: "sess-1", Title: "debugging"}}, nil
	}

	upgradeCalled := false
	srv := newAdminTestServer(t, c, func() error { upgradeCalled = true; return nil })

	status, body := postAdminRestart(t, srv.URL)
	if status != http.StatusConflict {
		t.Fatalf("POST status = %d, want 409", status)
	}
	if !body.Busy {
		t.Error("body.busy = false, want true")
	}
	if len(body.Items) != 1 {
		t.Fatalf("items len = %d, want exactly 1 (query only)", len(body.Items))
	}
	item := body.Items[0]
	if item.Kind != "query" {
		t.Errorf("item.kind = %q, want \"query\"", item.Kind)
	}
	if item.EngineID != "main" {
		t.Errorf("item.engineId = %q, want \"main\"", item.EngineID)
	}
	if item.Engine != "Main" {
		t.Errorf("item.engine = %q, want \"Main\" (display name)", item.Engine)
	}
	if item.Session != "debugging" {
		t.Errorf("item.session = %q, want \"debugging\"", item.Session)
	}
	if item.Detail != "fix the scanner bug" {
		t.Errorf("item.detail = %q, want \"fix the scanner bug\"", item.Detail)
	}
	if upgradeCalled {
		t.Error("Upgrade fake was called despite busy report — the 409 path must not start an upgrade")
	}
}

func TestAdminRestartPOST_BusyJob409(t *testing.T) {
	c := newTestConnector(t)
	c.mock().jobsFn = func() []*job.JobInfo {
		return []*job.JobInfo{
			{ID: "j1", Status: "running", Command: "npm run build"},
			{ID: "j2", Status: "completed", Command: "make test"},
		}
	}

	srv := newAdminTestServer(t, c, func() error { return nil })

	status, body := postAdminRestart(t, srv.URL)
	if status != http.StatusConflict {
		t.Fatalf("POST status = %d, want 409", status)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items len = %d, want exactly 1 (running job only, completed excluded)", len(body.Items))
	}
	item := body.Items[0]
	if item.Kind != "job" {
		t.Errorf("item.kind = %q, want \"job\"", item.Kind)
	}
	if item.Detail != "npm run build" {
		t.Errorf("item.detail = %q, want \"npm run build\"", item.Detail)
	}
}

func TestAdminRestartPOST_LongPromptTruncated(t *testing.T) {
	c := newTestConnector(t)
	c.mock().isBusyFn = func() bool { return true }
	// 120 ASCII + 80 CJK runes = 200 runes; a rune-safe cut keeps the
	// first 80 ASCII chars and appends a single ellipsis rune.
	var sb strings.Builder
	for range 120 {
		sb.WriteByte('a')
	}
	for range 80 {
		sb.WriteRune('中')
	}
	long := sb.String()
	c.mock().SetMessagesFn(func() []types.Message {
		return []types.Message{{
			ID: "u1", Role: types.RoleUser,
			Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: long}},
		}}
	})
	c.mock().queryStartMsgIdxFn = func() int { return 0 }

	srv := newAdminTestServer(t, c, func() error { return nil })

	status, body := postAdminRestart(t, srv.URL)
	if status != http.StatusConflict {
		t.Fatalf("POST status = %d, want 409", status)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items len = %d, want 1", len(body.Items))
	}
	detail := body.Items[0].Detail
	n := utf8.RuneCountInString(detail)
	if n > 81 {
		t.Errorf("detail rune count = %d, want <= 81 (80 + ellipsis)", n)
	}
	if !strings.HasSuffix(detail, "…") {
		t.Errorf("detail must end with ellipsis, got %q", detail)
	}
	if !strings.HasPrefix(detail, strings.Repeat("a", 80)) {
		t.Errorf("detail must start with the first 80 ASCII runes, got %q", detail)
	}
}

func TestAdminRestartPOST_SystemEngineIgnored(t *testing.T) {
	c := newTestConnector(t)
	c.mock().isBusyFn = func() bool { return true }
	c.slotsMu.Lock()
	if s := c.slots["main"]; s != nil {
		s.system = true
	}
	c.slotsMu.Unlock()

	srv := newAdminTestServer(t, c, func() error { return nil })

	body := getAdminRestart(t, srv.URL)
	if body.Busy {
		t.Error("GET busy = true, want false (system engine must not block restart)")
	}
	if len(body.Items) != 0 {
		t.Errorf("items len = %d, want 0", len(body.Items))
	}
}

func TestAdminRestartPOST_Idle202TriggersUpgrade(t *testing.T) {
	c := newTestConnector(t)
	upgraded := make(chan struct{}, 1)
	srv := newAdminTestServer(t, c, func() error {
		upgraded <- struct{}{}
		return nil
	})

	resp, err := http.Post(srv.URL+"/api/admin/restart", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST status = %d, want 202", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode 202 body: %v", err)
	}
	if body.Status != "upgrading" {
		t.Errorf("body.status = %q, want \"upgrading\"", body.Status)
	}
	select {
	case <-upgraded:
	case <-time.After(2 * time.Second): // REAL-TIME — async launch assertion
		t.Fatal("Upgrade fake never fired within 2s after 202")
	}
}

func TestAdminRestartPOST_Unsupported501(t *testing.T) {
	c := newTestConnector(t)
	srv := newAdminTestServer(t, c, nil)

	resp, err := http.Post(srv.URL+"/api/admin/restart", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("POST status = %d, want 501", resp.StatusCode)
	}
	body := getAdminRestart(t, srv.URL)
	if body.Upgradable {
		t.Error("GET upgradable = true with nil Upgrade, want false")
	}
	if body.Build != "test-v · test-c" {
		t.Errorf("GET build = %q, want %q", body.Build, "test-v · test-c")
	}

	// Wired case: non-nil Upgrade must report upgradable.
	srv2 := newAdminTestServer(t, newTestConnector(t), func() error { return nil })
	if body := getAdminRestart(t, srv2.URL); !body.Upgradable {
		t.Error("GET upgradable = false with non-nil Upgrade, want true")
	}
}

func TestAdminRestartPOST_CodedRefusalCarriesCodeAndMessage(t *testing.T) {
	c := newTestConnector(t)
	mux := http.NewServeMux()
	RegisterAdminRoutes(mux, AdminDeps{
		Probe:              c.BusyReport,
		Upgrade:            nil,
		UnsupportedCode:    RefusalTUIMode,
		UnsupportedMessage: "TUI mode cannot hot-restart",
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/admin/restart", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("POST status = %d, want 501", resp.StatusCode)
	}
	var refused struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&refused); err != nil {
		t.Fatalf("decode 501 body: %v", err)
	}
	if refused.Reason != RefusalTUIMode {
		t.Errorf("501 reason = %q, want code %q (frontend i18n key)", refused.Reason, RefusalTUIMode)
	}
	if refused.Error != "TUI mode cannot hot-restart" {
		t.Errorf("501 error = %q, want the human message for CLI", refused.Error)
	}
	body := getAdminRestart(t, srv.URL)
	if body.Reason != RefusalTUIMode {
		t.Errorf("GET reason = %q, want code %q", body.Reason, RefusalTUIMode)
	}
}

// TestRefusalTUIMode_MatchesBundledFrontend pins the Go↔TS contract: the
// embedded SPA must know the "tui_mode" code (REFUSAL_TUI_MODE in
// web/ui/src/upgrade_mode.ts). The constants are duplicated across
// languages by necessity; this catches silent drift at build time — if the
// frontend renames the code, every TUI refusal degrades to the fallback
// sentence and this test forces the reconciliation.
func TestRefusalTUIMode_MatchesBundledFrontend(t *testing.T) {
	bundle, err := os.ReadFile("assets/index.html")
	if err != nil {
		t.Fatalf("read bundled SPA: %v", err)
	}
	if !bytes.Contains(bundle, []byte(`"tui_mode"`)) {
		t.Error(`bundled frontend does not recognize "tui_mode" — REFUSAL_TUI_MODE drift between Go and TS`)
	}
}

func TestCloseForUpgrade_Sends1012(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)

	c.CloseForUpgrade()

	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second)) // REAL-TIME
	_, _, err := ws.ReadMessage()
	closeErr, ok := err.(*websocket.CloseError)
	if !ok {
		t.Fatalf("read after CloseForUpgrade: %v, want CloseError", err)
	}
	if closeErr.Code != 1012 {
		t.Errorf("close code = %d, want 1012 (Service Restart)", closeErr.Code)
	}
}

// ---------------------------------------------------------------------------
// RequestRestart unit tests (plain fakes — no pkg/app dependency).
// ---------------------------------------------------------------------------

func TestRequestRestart_BusyRefusesWithoutUpgrade(t *testing.T) {
	upgradeCalled := false
	outcome, report := RequestRestart(
		func() BusyReport {
			return BusyReport{Busy: true, Items: []BusyItem{{Kind: "query", Engine: "Main", EngineID: "main", Session: "s", Detail: "long job"}}}
		},
		func() error { upgradeCalled = true; return nil },
	)
	if outcome != RestartBusy {
		t.Fatalf("outcome = %v, want RestartBusy", outcome)
	}
	if !report.Busy {
		t.Error("report.Busy = false, want true")
	}
	if len(report.Items) != 1 {
		t.Fatalf("report.Items len = %d, want 1", len(report.Items))
	}
	if report.Items[0].Detail != "long job" {
		t.Errorf("report.Items[0].Detail = %q, want \"long job\"", report.Items[0].Detail)
	}
	if upgradeCalled {
		t.Error("upgrade fake called on busy path — the busy path never launches the goroutine")
	}
}

func TestRequestRestart_IdleStartsUpgrade(t *testing.T) {
	upgraded := make(chan struct{}, 1)
	outcome, report := RequestRestart(
		func() BusyReport { return BusyReport{Busy: false, Items: []BusyItem{}} },
		func() error { upgraded <- struct{}{}; return nil },
	)
	if outcome != RestartStarted {
		t.Fatalf("outcome = %v, want RestartStarted", outcome)
	}
	if report.Busy {
		t.Error("report.Busy = true, want false")
	}
	select {
	case <-upgraded:
	case <-time.After(2 * time.Second): // REAL-TIME — async launch assertion
		t.Fatal("upgrade fake never fired within 2s (must be launched asynchronously)")
	}
}

func TestRequestRestart_NilUpgradeUnsupported(t *testing.T) {
	outcome, _ := RequestRestart(
		func() BusyReport { return BusyReport{Busy: false, Items: []BusyItem{}} },
		nil,
	)
	if outcome != RestartUnsupported {
		t.Fatalf("outcome = %v, want RestartUnsupported", outcome)
	}
}

// TestBusyReport_ItemsNeverNil pins the JSON wire shape: an idle connector
// must serialize items as [], not null (the frontend iterates it).
func TestBusyReport_ItemsNeverNil(t *testing.T) {
	c := newTestConnector(t)
	report := c.BusyReport()
	if report.Busy {
		t.Error("busy = true on idle connector, want false")
	}
	if report.Items == nil {
		t.Fatal("Items = nil, want empty slice (JSON must be [] not null)")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"items":[]`)) {
		t.Errorf("wire shape = %s, want items:[]", raw)
	}
}
