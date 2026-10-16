package wui

// wui_mutkill_d_test.go kills the batch-D escaped mutants (context_breakdown,
// remotedesktop, jobs_panel, attachment, admin, reload). Each test names the
// mutations it kills in a comment so the mapping stays auditable. Mutations
// that are provably unobservable are argued in the batch report instead.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/reload"
	"github.com/liuy/gbot/pkg/tool/job"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// slog capture helper — several mutants delete a log statement whose output
// is the only observable effect.
// ---------------------------------------------------------------------------

type mutDLog struct {
	mu      sync.Mutex
	records []slog.Record
}

func (r *mutDLog) add(rec slog.Record) {
	r.mu.Lock()
	r.records = append(r.records, rec)
	r.mu.Unlock()
}

func (r *mutDLog) count(level slog.Level, substr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rec := range r.records {
		if rec.Level == level && strings.Contains(rec.Message, substr) {
			n++
		}
	}
	return n
}

func (r *mutDLog) waitFor(t *testing.T, level slog.Level, substr string, timeout time.Duration) {
	t.Helper()
	if !waitFor(timeout, func() bool { return r.count(level, substr) > 0 }) {
		t.Fatalf("no %v log containing %q within %v", level, substr, timeout)
	}
}

// assertNoneWithin fails if the given record appears within grace. Used for
// "must NOT log on the success path" assertions where the errant record would
// be emitted microseconds after the trigger we synchronize on.
func (r *mutDLog) assertNoneWithin(t *testing.T, level slog.Level, substr string, grace time.Duration) {
	t.Helper()
	deadline := time.Now().Add(grace) // REAL-TIME
	for time.Now().Before(deadline) { // REAL-TIME
		if n := r.count(level, substr); n > 0 {
			t.Fatalf("unexpected %v log containing %q (count %d)", level, substr, n)
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME
	}
}

type mutDLogHandler struct{ rec *mutDLog }

func (h mutDLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h mutDLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.rec.add(r)
	return nil
}
func (h mutDLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h mutDLogHandler) WithGroup(string) slog.Handler      { return h }

func mutDCaptureSlog(t *testing.T) *mutDLog {
	t.Helper()
	rec := &mutDLog{}
	old := slog.Default()
	slog.SetDefault(slog.New(mutDLogHandler{rec: rec}))
	t.Cleanup(func() { slog.SetDefault(old) })
	return rec
}

// mutDSlot builds a slot whose jobs maps exist, for direct calls into the
// merge/capture helpers without a running watcher.
func mutDSlot() *engineSlot {
	return &engineSlot{
		jobView:            make(map[string]*jobViewEntry),
		jobFeeds:           make(map[string]*jobFeedState),
		innerToolToFeed:    make(map[string]string),
		pendingAgentEvents: make(map[string]*pendingAgentEvents),
	}
}

// ---------------------------------------------------------------------------
// admin.go
// ---------------------------------------------------------------------------

// Kills admin.go:56 (concurrency/goroutine-remove): a synchronous upgrade
// call would block RequestRestart for the upgrade's full duration.
func TestMutD_RequestRestart_UpgradeMustNotBlockCaller(t *testing.T) {
	upgradeReturned := make(chan struct{})
	start := time.Now() // REAL-TIME
	outcome, _ := RequestRestart(
		func() BusyReport { return BusyReport{} },
		func() error { time.Sleep(300 * time.Millisecond); close(upgradeReturned); return nil }, // REAL-TIME
	)
	elapsed := time.Since(start) // REAL-TIME
	if outcome != RestartStarted {
		t.Fatalf("outcome = %v, want RestartStarted", outcome)
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("RequestRestart blocked %v on the upgrade call; tableflip blocks until the child is ready, so the launch must be a goroutine", elapsed)
	}
	<-upgradeReturned
}

// Kills admin.go:57 (expression/error-guard), admin.go:57 (conditional/negated,
// together with the success-path test below), admin.go:58 (branch/if) and
// admin.go:58 (statement/remove): a failed upgrade must produce the warn.
func TestMutD_RequestRestart_UpgradeFailureWarns(t *testing.T) {
	rec := mutDCaptureSlog(t)
	outcome, _ := RequestRestart(
		func() BusyReport { return BusyReport{} },
		func() error { return errors.New("tableflip: exit status 1") },
	)
	if outcome != RestartStarted {
		t.Fatalf("outcome = %v, want RestartStarted", outcome)
	}
	rec.waitFor(t, slog.LevelWarn, "upgrade: failed", 2*time.Second)
}

// Kills the conditional/negated flavor of admin.go:57 (err == nil would log).
func TestMutD_RequestRestart_UpgradeSuccessSilent(t *testing.T) {
	rec := mutDCaptureSlog(t)
	upgradeDone := make(chan struct{})
	outcome, _ := RequestRestart(
		func() BusyReport { return BusyReport{} },
		func() error { close(upgradeDone); return nil },
	)
	if outcome != RestartStarted {
		t.Fatalf("outcome = %v, want RestartStarted", outcome)
	}
	<-upgradeDone
	rec.assertNoneWithin(t, slog.LevelWarn, "upgrade: failed", 150*time.Millisecond)
}

// Kills admin.go:54 (statement/return): the unsupported outcome must still
// hand the caller the busy report (409/501 bodies reuse it).
func TestMutD_RequestRestart_UnsupportedCarriesReport(t *testing.T) {
	outcome, report := RequestRestart(
		func() BusyReport {
			return BusyReport{Busy: false, Items: []BusyItem{{Kind: "job", EngineID: "main", Detail: "make test"}}}
		},
		nil,
	)
	if outcome != RestartUnsupported {
		t.Fatalf("outcome = %v, want RestartUnsupported", outcome)
	}
	if report.Busy {
		t.Error("report.Busy = true, want false")
	}
	if len(report.Items) != 1 {
		t.Fatalf("report.Items len = %d, want 1 (the report must be passed through, not zeroed)", len(report.Items))
	}
	if report.Items[0].Detail != "make test" {
		t.Errorf("report.Items[0].Detail = %q, want %q", report.Items[0].Detail, "make test")
	}
}

// Kills admin.go:61 (statement/return, both entries): the started outcome must
// return the probe's report, not a zero BusyReport.
func TestMutD_RequestRestart_StartedCarriesReport(t *testing.T) {
	outcome, report := RequestRestart(
		func() BusyReport {
			return BusyReport{Busy: false, Items: []BusyItem{{Kind: "query", EngineID: "main", Detail: "scan"}}}
		},
		func() error { return nil },
	)
	if outcome != RestartStarted {
		t.Fatalf("outcome = %v, want RestartStarted", outcome)
	}
	if len(report.Items) != 1 {
		t.Fatalf("report.Items len = %d, want 1 (the report must be passed through, not zeroed)", len(report.Items))
	}
	if report.Items[0].Detail != "scan" {
		t.Errorf("report.Items[0].Detail = %q, want %q", report.Items[0].Detail, "scan")
	}
}

// Kills admin.go:113 (statement/remove: Cache-Control header) and
// admin.go:115 (composite/field-clear: embedded BusyReport zeroed).
func TestMutD_AdminGET_NoStoreHeaderAndBusyState(t *testing.T) {
	srv := newAdminTestServerDeps(t, newTestConnector(t), AdminDeps{
		Probe: func() BusyReport {
			return BusyReport{Busy: true, Items: []BusyItem{{Kind: "query", EngineID: "main", Detail: "scanning files"}}}
		},
	})
	resp, err := http.Get(srv.URL + "/api/admin/restart")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (state must not be cached across a restart)", cc)
	}
	var body adminStatePayloadResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Busy {
		t.Error("busy = false, want true (probe result must reach the payload)")
	}
	if len(body.Items) != 1 || body.Items[0].Detail != "scanning files" {
		t.Errorf("items = %+v, want one entry with detail %q", body.Items, "scanning files")
	}
}

// Kills admin.go:125 (composite/field-clear: 501 error field) and
// admin.go:134 (composite/field-clear: stepped_down status field).
func TestMutD_AdminStepdown_Bodies(t *testing.T) {
	c := newTestConnector(t)

	nilSrv := newAdminTestServerDeps(t, c, AdminDeps{Probe: c.BusyReport})
	resp, err := http.Post(nilSrv.URL+"/api/admin/stepdown", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("nil Stepdown status = %d, want 501", resp.StatusCode)
	}
	var refused struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&refused); err != nil {
		t.Fatal(err)
	}
	if refused.Error != "stepdown not supported: no supervisor owns this daemon" {
		t.Errorf("501 error = %q, want the supervisor sentence", refused.Error)
	}

	idleSrv := newAdminTestServerDeps(t, c, AdminDeps{
		Probe:    c.BusyReport,
		Stepdown: func() (bool, BusyReport) { return false, BusyReport{} },
	})
	resp2, err := http.Post(idleSrv.URL+"/api/admin/stepdown", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("idle Stepdown status = %d, want 200", resp2.StatusCode)
	}
	var ok struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&ok); err != nil {
		t.Fatal(err)
	}
	if ok.Status != "stepped_down" {
		t.Errorf("200 status field = %q, want stepped_down", ok.Status)
	}
}

// Kills admin.go:144 (branch/if) and admin.go:144 (statement/remove): an empty
// UnsupportedMessage must fall back to the platform sentence.
func TestMutD_AdminRestartPOST_UnsupportedDefaultMessage(t *testing.T) {
	srv := newAdminTestServerDeps(t, newTestConnector(t), AdminDeps{Probe: newTestConnector(t).BusyReport})
	resp, err := http.Post(srv.URL+"/api/admin/restart", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "restart not supported on this platform" {
		t.Errorf("error = %q, want the default platform sentence", body.Error)
	}
}

// ---------------------------------------------------------------------------
// reload.go
// ---------------------------------------------------------------------------

// Kills reload.go:13 (arithmetic/base, numbers/decrementer, numbers/
// incrementer): the request context deadline must be the full 120s window
// (the MCP drain alone can legally take tens of seconds).
func TestMutD_ReloadRoute_TimeoutWindow(t *testing.T) {
	mux := http.NewServeMux()
	var remain time.Duration
	RegisterReloadRoute(mux, func(ctx context.Context) *reload.Report {
		dl, ok := ctx.Deadline()
		if !ok {
			t.Error("reload fn received a context without deadline")
			return fixtureReport(true)
		}
		remain = time.Until(dl)
		return fixtureReport(true)
	})
	rec := postReload(t, mux.ServeHTTP)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// The handler's own work between WithTimeout and the fn call costs
	// microseconds, so a correct 120s window lands just under 120s and every
	// off-by-one or zeroed constant lands outside [119s, 120s].
	if remain < 119*time.Second || remain > 120*time.Second {
		t.Fatalf("deadline remaining = %v, want within [119s, 120s]", remain)
	}
}

// Kills reload.go:28 (statement/defer-remove): cancel becomes an immediate
// call, so the reload fn observes an already-canceled context.
func TestMutD_ReloadRoute_ContextLiveInsideFn(t *testing.T) {
	mux := http.NewServeMux()
	var innerErr error
	RegisterReloadRoute(mux, func(ctx context.Context) *reload.Report {
		innerErr = ctx.Err()
		return fixtureReport(true)
	})
	rec := postReload(t, mux.ServeHTTP)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if innerErr != nil {
		t.Fatalf("ctx.Err() inside reload fn = %v, want nil (cancel must be deferred)", innerErr)
	}
}

// Kills reload.go:30 (statement/remove: Cache-Control header).
func TestMutD_ReloadRoute_NoStoreHeader(t *testing.T) {
	mux := http.NewServeMux()
	RegisterReloadRoute(mux, func(ctx context.Context) *reload.Report { return fixtureReport(true) })
	rec := postReload(t, mux.ServeHTTP)
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (reload results are per-request)", cc)
	}
}

// ---------------------------------------------------------------------------
// attachment.go
// ---------------------------------------------------------------------------

// Kills attachment.go:15 (numbers x4): the cap must be exactly 50 MiB —
// literal sizes, not the constant symbol, so a mutated constant cannot
// self-fulfill the comparison.
func TestMutD_AttachmentCapExactly50MiB(t *testing.T) {
	acc := &attachmentAccumulator{}
	if errMsg := acc.handleStart(attachmentStartMsg{ID: "edge", Name: "x.bin", Size: int64(50) << 20}); errMsg != "" {
		t.Fatalf("size exactly 50 MiB must be accepted, got %q", errMsg)
	}
	over := &attachmentAccumulator{}
	errMsg := over.handleStart(attachmentStartMsg{ID: "over", Name: "x.bin", Size: int64(51)<<20 - 1})
	if errMsg == "" {
		t.Fatal("size 51 MiB - 1 must be rejected")
	}
	if !strings.Contains(errMsg, "too large") {
		t.Errorf("errMsg = %q, want substring 'too large'", errMsg)
	}
}

// Kills attachment.go:130 (branch/if via nil-buf panic, statement/return via
// the exact error string).
func TestMutD_HandleEnd_NoActiveExactError(t *testing.T) {
	acc := &attachmentAccumulator{}
	if got := acc.handleEnd("ghost", nil); got != "no active attachment" {
		t.Fatalf("handleEnd with no active upload = %q, want the exact 'no active attachment' message", got)
	}
}

// Kills attachment.go:156 (expression/error-guard), :157 (branch/if),
// :158 (statement/remove), :159 (statement/return): a failing Save must
// return the save-failed error, clear the active upload, and not record a
// saved entry. The failure is forced by parking a directory at the exact
// destination path, which makes the atomic Rename fail — no permission tricks.
func TestMutD_HandleEnd_SaveFailurePath(t *testing.T) {
	store := newAccStore(t)
	acc := &attachmentAccumulator{}
	data := []byte("pretend document bytes")
	if errMsg := acc.handleStart(attachmentStartMsg{ID: "d1", Name: "report.pdf", Size: int64(len(data))}); errMsg != "" {
		t.Fatalf("handleStart: %q", errMsg)
	}
	if errMsg := acc.handleBinary(data); errMsg != "" {
		t.Fatalf("handleBinary: %q", errMsg)
	}
	dst := store.Path(media.CategoryDocument, data, ".pdf")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	errMsg := acc.handleEnd("d1", store)
	if errMsg == "" || !strings.Contains(errMsg, "save failed") {
		t.Fatalf("errMsg = %q, want a 'save failed' error", errMsg)
	}
	if acc.activeID != "" {
		t.Errorf("activeID = %q, want cleared after save failure", acc.activeID)
	}
	if _, ok := acc.saved["d1"]; ok {
		t.Error("saved must not gain an entry when the store write failed")
	}
}

// Kills attachment.go:175 (statement/return) and :185 (statement/return):
// document classification must return real mime/ext/kind values.
func TestMutD_ClassifyAttachmentDocumentValues(t *testing.T) {
	mime, ext, kind := classifyAttachment([]byte("not an image"), "report.pdf", "text/plain")
	if mime != "application/pdf" {
		t.Errorf("mime = %q, want application/pdf", mime)
	}
	if ext != ".pdf" {
		t.Errorf("ext = %q, want .pdf (filename extension preserved)", ext)
	}
	if kind != "document" {
		t.Errorf("kind = %q, want document", kind)
	}
	if got := preserveDocExt("archive"); got != ".bin" {
		t.Errorf("preserveDocExt(no extension) = %q, want .bin", got)
	}
}

// Kills attachment.go:200 (loop/break: continue flipped to break): the missing
// scan must continue past the first missing id — later ids still report.
func TestMutD_BuildContents_MissingScanContinues(t *testing.T) {
	acc := &attachmentAccumulator{}
	acc.bindSaved("present", savedAttachment{path: "/cache/x.pdf", mime: "application/pdf", ext: ".pdf", kind: "document"})
	out, missing := acc.buildContents([]inboundAttachment{
		{ID: "miss1", Name: "a.pdf", Size: 1},
		{ID: "present", Name: "b.pdf", Size: 1},
		{ID: "miss2", Name: "c.pdf", Size: 1},
	})
	if out != nil {
		t.Fatalf("out = %+v, want nil when ids are missing", out)
	}
	if missing != "miss1, miss2" {
		t.Fatalf("missing = %q, want %q (every missing id, comma-joined)", missing, "miss1, miss2")
	}
}

// Kills attachment.go:209 (composite/field-clear: Source.Type zeroed).
func TestMutD_BuildContents_SourceTypeFile(t *testing.T) {
	acc := &attachmentAccumulator{}
	acc.bindSaved("img1", savedAttachment{path: "/cache/i.png", mime: "image/png", ext: ".png", kind: "image"})
	out, missing := acc.buildContents([]inboundAttachment{{ID: "img1", Name: "photo.png", Mime: "image/png", Size: 5}})
	if missing != "" {
		t.Fatalf("missing = %q, want empty", missing)
	}
	if len(out) != 1 {
		t.Fatalf("out len = %d, want 1", len(out))
	}
	src := out[0].Source
	if src.Type != "file" {
		t.Errorf("Source.Type = %q, want file", src.Type)
	}
	if out[0].Type != "image" {
		t.Errorf("Type = %q, want image", out[0].Type)
	}
	if src.Path != "/cache/i.png" || src.Mime != "image/png" || src.Name != "photo.png" || src.Size != 5 {
		t.Errorf("source = %+v, want the saved/upload metadata verbatim", src)
	}
}

// Kills attachment.go:237 (statement/remove: activeID not cleared): after a
// reset, a fresh upload must be accepted, not rejected as "in progress".
func TestMutD_Reset_AllowsFreshUpload(t *testing.T) {
	acc := &attachmentAccumulator{}
	if errMsg := acc.handleStart(attachmentStartMsg{ID: "r1", Name: "x.png", Size: 4}); errMsg != "" {
		t.Fatalf("first handleStart: %q", errMsg)
	}
	acc.reset()
	if errMsg := acc.handleStart(attachmentStartMsg{ID: "r2", Name: "y.png", Size: 4}); errMsg != "" {
		t.Fatalf("handleStart after reset = %q, want accepted (activeID must be cleared)", errMsg)
	}
	if acc.activeID != "r2" {
		t.Errorf("activeID = %q, want r2", acc.activeID)
	}
}

// ---------------------------------------------------------------------------
// context_breakdown.go
// ---------------------------------------------------------------------------

// Kills the composite/field-clear mutants on every converter line (110, 112,
// 121, 129, 137, 145 x2, 153 x2, 165, 168, 181, 183, 188, 193, 194, 195,
// 207, 208, 209): each converter must copy every field.
func TestMutD_ConvertersCopyEveryField(t *testing.T) {
	mcp := convertMCPTools([]engine.MCPToolDetail{{Name: "search", ServerName: "brave", Tokens: 800, IsLoaded: true}})
	if len(mcp) != 1 {
		t.Fatalf("mcp len = %d, want 1", len(mcp))
	}
	if mcp[0].ServerName != "brave" {
		t.Errorf("mcp ServerName = %q, want brave", mcp[0].ServerName)
	}
	if mcp[0].IsLoaded != true {
		t.Errorf("mcp IsLoaded = %v, want true", mcp[0].IsLoaded)
	}
	if mcp[0].Tokens != 800 || mcp[0].Name != "search" {
		t.Errorf("mcp = %+v, want the input detail verbatim", mcp[0])
	}

	sys := convertSystemTools([]engine.SystemToolDetail{{Name: "Bash", Tokens: 1000}})
	if len(sys) != 1 || sys[0].Name != "Bash" || sys[0].Tokens != 1000 {
		t.Errorf("systemTools = %+v, want {Bash 1000}", sys)
	}

	sec := convertPromptSections([]engine.SystemPromptSectionDetail{{Name: "Base prompt", ID: "base_prompt", Tokens: 2000}})
	if len(sec) != 1 || sec[0].Name != "Base prompt" || sec[0].ID != "base_prompt" || sec[0].Tokens != 2000 {
		t.Errorf("promptSections = %+v, want all three fields", sec)
	}

	mem := convertMemoryFiles([]engine.MemoryFileDetail{{Path: "CLAUDE.md", Tokens: 1500}})
	if len(mem) != 1 || mem[0].Path != "CLAUDE.md" || mem[0].Tokens != 1500 {
		t.Errorf("memoryFiles = %+v, want both fields", mem)
	}

	ag := convertAgents([]engine.AgentDetail{{AgentType: "coder", Source: "built-in", Tokens: 400}})
	if len(ag) != 1 || ag[0].AgentType != "coder" || ag[0].Source != "built-in" || ag[0].Tokens != 400 {
		t.Errorf("agents = %+v, want all three fields", ag)
	}

	sk := convertSkills([]engine.SkillDetail{{Name: "commit", Source: "plugin", Tokens: 300}})
	if len(sk) != 1 || sk[0].Name != "commit" || sk[0].Source != "plugin" || sk[0].Tokens != 300 {
		t.Errorf("skills = %+v, want all three fields", sk)
	}

	cat := convertCategories([]engine.ContextCategory{{Name: "Messages", ID: "messages", Tokens: 30000, Percentage: 15.5, Color: "255", IsFree: false, IsReserved: true}})
	if len(cat) != 1 {
		t.Fatalf("categories len = %d, want 1", len(cat))
	}
	if cat[0].Percentage != 15.5 {
		t.Errorf("category Percentage = %v, want 15.5", cat[0].Percentage)
	}
	if cat[0].IsReserved != true {
		t.Errorf("category IsReserved = %v, want true", cat[0].IsReserved)
	}
	if cat[0].Name != "Messages" || cat[0].ID != "messages" || cat[0].Tokens != 30000 || cat[0].Color != "255" || cat[0].IsFree != false {
		t.Errorf("category = %+v, want every field copied", cat[0])
	}

	mb := convertMessageBreakdown(&engine.MessageBreakdown{
		ToolCallTokens:      3000,
		ToolResultTokens:    6000,
		AttachmentTokens:    500,
		AssistantTextTokens: 2000,
		UserTextTokens:      1000,
		ToolCallsByType:     []engine.ToolCallByType{{Name: "Bash", CallTokens: 2000, ResultTokens: 4000}},
		AttachmentsByType:   []engine.AttachmentByType{{Name: "file", Tokens: 500}},
	})
	if mb == nil {
		t.Fatal("messageBreakdown = nil, want populated")
	}
	if mb.AttachmentTokens != 500 || mb.AssistantTextTokens != 2000 || mb.UserTextTokens != 1000 {
		t.Errorf("mb token fields = %d/%d/%d, want 500/2000/1000", mb.AttachmentTokens, mb.AssistantTextTokens, mb.UserTextTokens)
	}
	if len(mb.ToolCallsByType) != 1 || mb.ToolCallsByType[0].Name != "Bash" || mb.ToolCallsByType[0].ResultTokens != 4000 {
		t.Errorf("toolCallsByType = %+v, want name and result tokens", mb.ToolCallsByType)
	}
	if len(mb.AttachmentsByType) != 1 || mb.AttachmentsByType[0].Name != "file" || mb.AttachmentsByType[0].Tokens != 500 {
		t.Errorf("attachmentsByType = %+v, want name and tokens", mb.AttachmentsByType)
	}

	api := convertAPIUsage(&engine.APIUsageSnapshot{
		InputTokens: 70000, OutputTokens: 2000,
		CacheCreationInputTokens: 5000, CacheReadInputTokens: 60000,
	})
	if api == nil {
		t.Fatal("apiUsage = nil, want populated")
	}
	if api.OutputTokens != 2000 || api.CacheCreationInputTokens != 5000 || api.CacheReadInputTokens != 60000 {
		t.Errorf("apiUsage = %+v, want output/cache fields copied", api)
	}
}

// Kills context_breakdown.go:176 (branch/if): nil must map to nil without a
// panic (a non-nil breakdown with nil MessageBreakdown is a legal state).
func TestMutD_ConvertMessageBreakdown_NilIsNil(t *testing.T) {
	if got := convertMessageBreakdown(nil); got != nil {
		t.Fatalf("convertMessageBreakdown(nil) = %+v, want nil", got)
	}
}

// Kills context_breakdown.go:214 (expression/remove x2, numbers x2): nil and
// zero-token breakdowns must serialize as the minimal early-return shape, so
// the wire never carries an empty full frame.
func TestMutD_BuildContextBreakdown_ZeroShapes(t *testing.T) {
	want := `{"type":"context_breakdown","totalTokens":0}`
	if got := string(buildContextBreakdown(nil)); got != want {
		t.Errorf("nil breakdown = %s, want %s", got, want)
	}
	bd := &engine.ContextBreakdown{Model: "m", TotalTokens: 0, Percentage: 12.5}
	if got := string(buildContextBreakdown(bd)); got != want {
		t.Errorf("zero-token breakdown = %s, want %s (zero tokens take the minimal path)", got, want)
	}
}

// Kills context_breakdown.go:225 (composite/field-clear: ContextWindow) and
// :228 (composite/field-clear: IsAutoCompact).
func TestMutD_BuildContextBreakdown_TopLevelFields(t *testing.T) {
	bd := &engine.ContextBreakdown{
		Model:         "glm-5.2",
		ContextWindow: 131072,
		TotalTokens:   7,
		Percentage:    2.5,
		IsAutoCompact: true,
	}
	payload := buildContextBreakdown(bd)
	if payload == nil {
		t.Fatal("payload = nil, want JSON")
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatal(err)
	}
	if m["contextWindow"] != float64(131072) {
		t.Errorf("contextWindow = %v, want 131072", m["contextWindow"])
	}
	if m["isAutoCompact"] != true {
		t.Errorf("isAutoCompact = %v, want true", m["isAutoCompact"])
	}
	if m["model"] != "glm-5.2" || m["percentage"] != 2.5 {
		t.Errorf("model/percentage = %v/%v, want glm-5.2/2.5", m["model"], m["percentage"])
	}
}

// Kills context_breakdown.go:243 (expression/error-guard) and :244
// (statement/remove): a NaN percentage is the one input json.Marshal rejects;
// the failure must be logged.
func TestMutD_BuildContextBreakdown_MarshalFailureWarns(t *testing.T) {
	rec := mutDCaptureSlog(t)
	bd := &engine.ContextBreakdown{TotalTokens: 10, Percentage: math.NaN()}
	if payload := buildContextBreakdown(bd); payload != nil {
		t.Fatalf("payload = %s, want nil when marshal fails", payload)
	}
	if n := rec.count(slog.LevelWarn, "wui: marshal context_breakdown failed"); n != 1 {
		t.Fatalf("marshal-failure warn count = %d, want exactly 1", n)
	}
}

// Kills context_breakdown.go:253 (branch/if) and :265 (branch/if): a missing
// active engine must be a silent no-op, not a nil-interface panic.
func TestMutD_ContextHandlers_NilEngineNoPanic(t *testing.T) {
	c := newTestConnector(t)
	unknown := "no-such-engine"
	c.active.Store(&unknown)
	c.handleContextRequest()
	c.handleCompactRequest()
}

// Kills context_breakdown.go:268 (expression/error-guard), :268
// (conditional/negated, with the success test below) and :269
// (statement/remove): compact failures must warn exactly once.
func TestMutD_HandleCompactRequest_FailureWarns(t *testing.T) {
	c := newTestConnector(t)
	c.mock().manualCompactFn = func(context.Context, types.Message, string) (*short.CompactResult, error) {
		return nil, errors.New("compact: engine busy")
	}
	rec := mutDCaptureSlog(t)
	c.handleCompactRequest()
	if n := rec.count(slog.LevelWarn, "wui: manual compact failed"); n != 1 {
		t.Fatalf("compact-failure warn count = %d, want exactly 1", n)
	}
}

// Kills the conditional/negated flavor of context_breakdown.go:268.
func TestMutD_HandleCompactRequest_SuccessSilent(t *testing.T) {
	c := newTestConnector(t)
	c.mock().manualCompactFn = func(context.Context, types.Message, string) (*short.CompactResult, error) {
		return nil, nil
	}
	rec := mutDCaptureSlog(t)
	c.handleCompactRequest()
	rec.assertNoneWithin(t, slog.LevelWarn, "wui: manual compact failed", 100*time.Millisecond)
}

// ---------------------------------------------------------------------------
// jobs_panel.go
// ---------------------------------------------------------------------------

// Kills jobs_panel.go:142 (expression/remove x2, expression/string-literal
// x2): all three terminal statuses must be recognized.
func TestMutD_IsTerminalJobStatus_AllThree(t *testing.T) {
	for _, s := range []string{"completed", "failed", "killed"} {
		if !isTerminalJobStatus(s) {
			t.Errorf("isTerminalJobStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"running", ""} {
		if isTerminalJobStatus(s) {
			t.Errorf("isTerminalJobStatus(%q) = true, want false", s)
		}
	}
}

// Kills jobs_panel.go:236 (statement/remove: vanish-drop log).
func TestMutD_MergeLogs_RunningJobVanishDrop(t *testing.T) {
	rec := mutDCaptureSlog(t)
	slot := mutDSlot()
	slot.jobView["gone"] = &jobViewEntry{item: jobWireItem{ID: "gone", Kind: "bash", Status: "running", StartedMs: 1}}
	c := &WUIConnector{}
	c.mergeJobSummariesLocked(slot, nil)
	if _, ok := slot.jobView["gone"]; ok {
		t.Error("running job missing from the registry must be dropped from the view")
	}
	if rec.count(slog.LevelInfo, "wui:jobs: running job missing from registry, dropping") != 1 {
		t.Fatal("vanish drop must log the wui:jobs drop line exactly once")
	}
}

// Kills jobs_panel.go:254 (statement/remove: cap-eviction log).
func TestMutD_MergeLogs_CapEviction(t *testing.T) {
	rec := mutDCaptureSlog(t)
	slot := mutDSlot()
	summaries := make([]*job.JobInfo, 0, 201)
	for i := 1; i <= 201; i++ {
		summaries = append(summaries, bgBashJob(mutDCapID(i), "completed", "cmd", int64(1000+i)))
	}
	c := &WUIConnector{}
	c.mergeJobSummariesLocked(slot, summaries)
	if len(slot.jobView) != jobsViewCap {
		t.Fatalf("view len = %d, want %d", len(slot.jobView), jobsViewCap)
	}
	if rec.count(slog.LevelInfo, "wui:jobs: view over cap, evicting oldest terminal") != 1 {
		t.Fatal("cap eviction must log the wui:jobs eviction line exactly once")
	}
}

func mutDCapID(i int) string {
	return "cap-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
}

// Kills jobs_panel.go:262 (statement/remove: TTL sweep log).
func TestMutD_MergeLogs_StalePendingSweep(t *testing.T) {
	rec := mutDCaptureSlog(t)
	slot := mutDSlot()
	slot.pendingAgentEvents["toolu_stale"] = &pendingAgentEvents{
		firstSeen: time.Now().Add(-jobsPendingTTL() - 5*time.Second), // REAL-TIME
		events:    []types.QueryEvent{{Type: types.EventTextDelta, Text: "stale"}},
	}
	c := &WUIConnector{}
	c.mergeJobSummariesLocked(slot, nil)
	if len(slot.pendingAgentEvents) != 0 {
		t.Fatalf("pendingAgentEvents len = %d, want 0 after the TTL sweep", len(slot.pendingAgentEvents))
	}
	if rec.count(slog.LevelInfo, "wui:jobs: sweeping stale pending agent events") != 1 {
		t.Fatal("TTL sweep must log the wui:jobs sweep line exactly once")
	}
}

// Kills jobs_panel.go:269 (expression/remove): nil entries must not panic and
// foreground jobs must not be tailed — only backgrounded bash jobs.
func TestMutD_MergeTailIDs_FiltersNilForegroundAndFeeds(t *testing.T) {
	slot := mutDSlot()
	slot.jobView["f1"] = &jobViewEntry{item: jobWireItem{ID: "f1", Kind: "bash", Status: "completed"}}
	slot.jobView["b1"] = &jobViewEntry{item: jobWireItem{ID: "b1", Kind: "bash", Status: "running"}}
	summaries := []*job.JobInfo{
		nil,
		{ID: "f1", Type: "bash", Status: "completed", Backgrounded: false},
		{ID: "b1", Type: "bash", Status: "running", Backgrounded: true},
	}
	c := &WUIConnector{}
	got := c.mergeJobSummariesLocked(slot, summaries)
	if len(got) != 1 || got[0] != "b1" {
		t.Fatalf("tailIDs = %v, want exactly [b1] (backgrounded bash only)", got)
	}
}

// Kills jobs_panel.go:297 (statement/return) and :380 (statement/return):
// every job in a round shares one StartedMs, so ONLY the ID tiebreak orders
// the group — a zeroed tiebreak return leaves the group in map-iteration
// order, which Go randomizes per range as a rotation of insertion order.
// Each round uses freshly built maps, so a surviving mutant must draw the
// identity rotation in both paths across every round.
func TestMutD_JobSort_TiebreakOrdersBothOutputs(t *testing.T) {
	c := &WUIConnector{done: make(chan struct{})}
	c.outQ.Store(newOutQueue(outQueueCapacity))
	c.activeWS.Store(&websocket.Conn{})

	ids := []string{
		"k-01", "k-02", "k-03", "k-04", "k-05",
		"k-06", "k-07", "k-08", "k-09", "k-10",
	}
	want := ids

	for round := 0; round < 6; round++ {
		slot := mutDSlot()
		slot.active.Store(true)
		for _, id := range ids {
			slot.jobView[id] = &jobViewEntry{item: jobWireItem{ID: id, Kind: "bash", Status: "running", StartedMs: 7777}}
		}

		frames := c.collectJobFramesLocked(slot)
		if len(frames) != 1 {
			t.Fatalf("round %d: frames = %d, want exactly one job_list push", round, len(frames))
		}
		var list jobListOutbound
		if err := json.Unmarshal([]byte(frames[0]), &list); err != nil {
			t.Fatal(err)
		}
		if got := jobIDs(list.Jobs); !mutDStringSlicesEqual(got, want) {
			t.Fatalf("round %d: job_list tie order = %v, want %v", round, got, want)
		}

		raw := c.buildJobsMetadata(slot)
		if raw == nil {
			t.Fatalf("round %d: metadata jobs = nil, want snapshot", round)
		}
		var env jobsMetadataOutbound
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if got := hydratedIDs(env.Jobs); !mutDStringSlicesEqual(got, want) {
			t.Fatalf("round %d: metadata tie order = %v, want %v", round, got, want)
		}
	}
}

func mutDStringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Kills jobs_panel.go:346 (statement/remove: Unlock dropped): the forced
// snapshot must release jobsMu or every later watcher beat deadlocks.
func TestMutD_PushJobsSnapshot_ReleasesLock(t *testing.T) {
	c := &WUIConnector{}
	slot := mutDSlot()
	c.pushJobsSnapshot(slot)
	if !waitFor(time.Second, func() bool { return slot.jobsMu.TryLock() }) {
		t.Fatal("pushJobsSnapshot must release jobsMu before returning")
	}
	slot.jobsMu.Unlock()
}

// Kills jobs_panel.go:359 (statement/defer-remove): with the unlock made
// immediate, buildJobsMetadata iterates jobView without the lock while a
// locked writer mutates the same map — the Go runtime aborts the process on
// concurrent map iteration and map write.
func TestMutD_BuildJobsMetadata_HoldsLockDuringRead(t *testing.T) {
	slot := mutDSlot()
	slot.jobView["k"] = &jobViewEntry{item: jobWireItem{ID: "k", Kind: "bash", Status: "running", StartedMs: 1}}
	c := &WUIConnector{}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			slot.jobsMu.Lock()
			for i := 0; i < 64; i++ {
				slot.jobView["k"] = &jobViewEntry{item: jobWireItem{ID: "k", Kind: "bash", Status: "running", StartedMs: int64(i)}}
			}
			slot.jobsMu.Unlock()
		}
	}()
	var last json.RawMessage
	for range 2000 {
		if raw := c.buildJobsMetadata(slot); raw != nil {
			last = raw
		}
	}
	close(stop)
	wg.Wait()
	if last == nil {
		t.Fatal("buildJobsMetadata never produced a snapshot")
	}
}

// Kills jobs_panel.go:465 (statement/defer-remove): with the unlock made
// immediate, two captureJobEvent callers append to the same pending list
// without the lock and updates are lost. Serialized by jobsMu the count is
// exact.
func TestMutD_CaptureJobEvent_HoldsLockDuringAppend(t *testing.T) {
	slot := mutDSlot()
	c := &WUIConnector{}
	ev := types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{ParentToolUseID: "toolu_ghost"}, Text: "x"}
	const iters = 30000
	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iters {
				c.captureJobEvent(slot, ev)
			}
		}()
	}
	wg.Wait()
	slot.jobsMu.Lock()
	n := len(slot.pendingAgentEvents["toolu_ghost"].events)
	slot.jobsMu.Unlock()
	if n != 2*iters {
		t.Fatalf("pending events = %d, want exactly %d (the append must run under jobsMu)", n, 2*iters)
	}
}

// Kills jobs_panel.go:456 (expression/remove: both operand mutants) and :460
// (branch/if): nil Agent and empty-parent events return early; non-feed event
// types park nothing.
func TestMutD_CaptureJobEvent_Guards(t *testing.T) {
	c := &WUIConnector{}
	// nil Agent: the guard must return before touching event.Agent
	c.captureJobEvent(mutDSlot(), types.QueryEvent{Type: types.EventTextDelta, Text: "x"})

	// Agent present but empty parent: nothing may be parked under ""
	slot := mutDSlot()
	c.captureJobEvent(slot, types.QueryEvent{Type: types.EventTextDelta, Agent: &types.AgentMeta{}, Text: "x"})
	if len(slot.pendingAgentEvents) != 0 {
		t.Errorf("pendingAgentEvents = %+v, want empty for an empty-parent event", slot.pendingAgentEvents)
	}

	// Non-feed event type: usage/lifecycle events contribute no blocks and
	// must not be parked for later replay.
	slot2 := mutDSlot()
	c.captureJobEvent(slot2, types.QueryEvent{Type: types.EventQueryEnd, Agent: &types.AgentMeta{ParentToolUseID: "toolu_ghost"}})
	if len(slot2.pendingAgentEvents) != 0 {
		t.Errorf("pendingAgentEvents = %+v, want empty for a non-feed event type", slot2.pendingAgentEvents)
	}
}

// Kills jobs_panel.go:399 (expression/remove: !jobHasFeed term dropped): a
// bash entry must not reclaim the feed of its (still live) parent agent job.
func TestMutD_DeleteJobEntry_BashKeepsForeignFeed(t *testing.T) {
	slot := mutDSlot()
	slot.jobView["bashjob"] = &jobViewEntry{item: jobWireItem{ID: "bashjob", Kind: "bash", Status: "completed", ParentToolUseID: "toolu_live"}}
	slot.jobFeeds["toolu_live"] = &jobFeedState{}
	slot.innerToolToFeed["inner_t"] = "toolu_live"
	c := &WUIConnector{}
	c.deleteJobEntryLocked(slot, "bashjob")
	if _, ok := slot.jobView["bashjob"]; ok {
		t.Error("the bash entry itself must be deleted")
	}
	if _, ok := slot.jobFeeds["toolu_live"]; !ok {
		t.Error("feed toolu_live must survive: it belongs to a live agent job, not the bash entry")
	}
	if _, ok := slot.innerToolToFeed["inner_t"]; !ok {
		t.Error("route inner_t must survive the bash entry deletion")
	}
}

// Kills jobs_panel.go:507 (expression/remove: both operand mutants):
// ToolStart with a nil ToolUse must not panic, and a non-ToolStart event
// carrying a ToolUse must not register an inner route.
func TestMutD_ApplyAgentEvent_RouteRegistrationGuard(t *testing.T) {
	slot := mutDSlot()
	slot.jobFeeds["toolu_f"] = &jobFeedState{}
	c := &WUIConnector{}
	c.applyAgentEventLocked(slot, "toolu_f", types.QueryEvent{
		Type: types.EventToolStart, Agent: &types.AgentMeta{ParentToolUseID: "toolu_f"},
	})
	if len(slot.innerToolToFeed) != 0 {
		t.Errorf("innerToolToFeed = %v, want empty for ToolStart without a ToolUse payload", slot.innerToolToFeed)
	}
	c.applyAgentEventLocked(slot, "toolu_f", types.QueryEvent{
		Type: types.EventTextDelta, Agent: &types.AgentMeta{ParentToolUseID: "toolu_f"},
		ToolUse: &types.ToolUseEvent{ID: "t9"}, Text: "x",
	})
	if len(slot.innerToolToFeed) != 0 {
		t.Errorf("innerToolToFeed = %v, want empty: only ToolStart registers routes", slot.innerToolToFeed)
	}
}

// ---------------------------------------------------------------------------
// remotedesktop.go
// ---------------------------------------------------------------------------

// Kills remotedesktop.go:49 (statement/remove: Cache-Control header).
func TestMutD_RemoteDesktopGET_NoStoreHeader(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	resp, err := http.Get(srv.URL + "/api/settings/remotedesktop")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (devices may change via PUT at any time)", cc)
	}
}

// Kills remotedesktop.go:170 (numbers x2), :179 (numbers x2) and :181
// (conditional/negated, which panics on the nil resp of a refused dial):
// error paths must report latency 0.
func TestMutD_ProbeVNC_ErrorPathsReportZeroLatency(t *testing.T) {
	latency, err := probeVNC(context.Background(), "")
	if err == nil || err.Error() != "address is required" {
		t.Fatalf("empty addr err = %v, want 'address is required'", err)
	}
	if latency != 0 {
		t.Errorf("latency on normalize error = %d, want 0", latency)
	}

	latency2, err2 := probeVNC(context.Background(), "ws://127.0.0.1:1")
	if err2 == nil {
		t.Fatal("dial to a refused port must fail")
	}
	if latency2 != 0 {
		t.Errorf("latency on dial error = %d, want 0", latency2)
	}
}

// mutDDeviceServer upgrades one connection and records the negotiated
// subprotocol from the device side.
func mutDDeviceServer(t *testing.T, record func(string)) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{Subprotocols: []string{"binary"}, CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		record(ws.Subprotocol())
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Kills remotedesktop.go:173 (composite/field-clear: probe dialer
// subprotocols): the probe must negotiate the binary subprotocol websockify
// and noVNC speak.
func TestMutD_ProbeVNC_NegotiatesBinarySubprotocol(t *testing.T) {
	got := make(chan string, 4)
	dev := mutDDeviceServer(t, func(s string) { got <- s })
	if _, err := probeVNC(context.Background(), dev.URL); err != nil {
		t.Fatalf("probe: %v", err)
	}
	select {
	case sub := <-got:
		if sub != "binary" {
			t.Fatalf("device negotiated subprotocol %q, want binary", sub)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("device never saw the probe dial")
	}
}

// Kills remotedesktop.go:189 (numbers/incrementer): a sub-millisecond dial
// must clamp to exactly 1, the documented floor.
func TestMutD_ProbeVNC_LatencyFloorIsExactlyOne(t *testing.T) {
	dev := mutDDeviceServer(t, func(string) {})
	best := 0
	for i := 0; i < 8; i++ {
		lat, err := probeVNC(context.Background(), dev.URL)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if i == 0 || lat < best {
			best = lat
		}
		if best == 1 {
			break
		}
	}
	if best != 1 {
		t.Fatalf("best loopback latency = %d ms, want the floor of exactly 1 (loopback dials are sub-ms)", best)
	}
}

// mutDHangListener accepts TCP connections and never answers: the handshake
// timeout under test is the only way a dial to it terminates.
func mutDHangListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			if _, err := ln.Accept(); err != nil {
				return
			}
		}
	}()
	return "ws://" + ln.Addr().String()
}

// Kills remotedesktop.go:174 (composite/field-clear, arithmetic/base,
// numbers/decrementer, numbers/incrementer): the probe dialer's handshake
// timeout must fire at ~5s (mutants: 4s, 6s, or none until the 8s ctx cap).
func TestMutD_ProbeVNC_HandshakeTimeout5s(t *testing.T) {
	addr := mutDHangListener(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	start := time.Now() // REAL-TIME
	_, err := probeVNC(ctx, addr)
	elapsed := time.Since(start) // REAL-TIME
	if err == nil {
		t.Fatal("dial to a hanging server must fail")
	}
	if elapsed < 4500*time.Millisecond || elapsed > 5500*time.Millisecond {
		t.Fatalf("handshake timeout fired after %v, want ~5s", elapsed)
	}
}

// Kills remotedesktop.go:245 (composite/field-clear, arithmetic/base,
// numbers/decrementer, numbers/incrementer): the proxy's device dial must
// time out at ~10s and surface a 1011 close to the browser client (mutants:
// 9s, 11s, or none before the 12s read deadline).
func TestMutD_VNCProxy_DeviceDialTimeout10s(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	addr := mutDHangListener(t)
	seedSettings(t, `{"desktops":[{"name":"hang","addr":"`+addr+`"}]}`)
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	start := time.Now() // REAL-TIME
	client, _, err := dialer.Dial(wsProxyURL(srv, "/wui/vnc/hang"), nil)
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(12 * time.Second)) // REAL-TIME
	_, _, rerr := client.ReadMessage()
	elapsed := time.Since(start) // REAL-TIME
	ce, ok := rerr.(*websocket.CloseError)
	if !ok || ce.Code != websocket.CloseInternalServerErr {
		t.Fatalf("client read err = %v, want a 1011 close after the device dial fails", rerr)
	}
	if elapsed < 9300*time.Millisecond || elapsed > 10700*time.Millisecond {
		t.Fatalf("device dial failed after %v, want ~10s handshake timeout", elapsed)
	}
}

// Kills remotedesktop.go:199 (composite/field-clear: vncUpgrader
// subprotocols): the browser-facing upgrade must negotiate binary.
func TestMutD_VNCProxy_ClientNegotiatesBinary(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	dev := mutDDeviceServer(t, func(string) {})
	seedSettings(t, `{"desktops":[{"name":"echo","addr":"`+dev.URL+`"}]}`)
	dialer := websocket.Dialer{Subprotocols: []string{"binary"}}
	client, _, err := dialer.Dial(wsProxyURL(srv, "/wui/vnc/echo"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if sub := client.Subprotocol(); sub != "binary" {
		t.Fatalf("client negotiated subprotocol %q, want binary", sub)
	}
}

// Kills remotedesktop.go:200 (composite/field-clear: CheckOrigin dropped,
// gorilla then rejects cross-origin upgrades by default): the proxy must
// accept browsers from any origin — it is same-origin by construction.
func TestMutD_VNCProxy_CrossOriginAccepted(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	dev := mutDDeviceServer(t, func(string) {})
	seedSettings(t, `{"desktops":[{"name":"echo","addr":"`+dev.URL+`"}]}`)
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	header := http.Header{"Origin": []string{"http://elsewhere.example:8080"}}
	client, _, err := dialer.Dial(wsProxyURL(srv, "/wui/vnc/echo"), header)
	if err != nil {
		t.Fatalf("cross-origin dial must be accepted: %v", err)
	}
	_ = client.Close()
}

// Kills remotedesktop.go:244 (composite/field-clear: proxy dialer
// subprotocols): the proxy must dial the device with the binary subprotocol.
func TestMutD_VNCProxy_DeviceSeesBinarySubprotocol(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	subCh := make(chan string, 1)
	dev := mutDDeviceServer(t, func(s string) { subCh <- s })
	seedSettings(t, `{"desktops":[{"name":"echo","addr":"`+dev.URL+`"}]}`)
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	client, _, err := dialer.Dial(wsProxyURL(srv, "/wui/vnc/echo"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	select {
	case sub := <-subCh:
		if sub != "binary" {
			t.Fatalf("device saw subprotocol %q, want binary", sub)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("proxy never dialed the device")
	}
}

// Kills remotedesktop.go:223 (loop/break: break flipped to continue): with
// duplicate device names only settings.json can produce, the FIRST match must
// win; flipping to continue lets the last match hijack the lookup.
func TestMutD_VNCProxy_DuplicateNamesFirstMatchWins(t *testing.T) {
	srv := newRemoteDesktopServer(t)
	dev := mutDDeviceServer(t, func(string) {})
	// First "dup" points at a refused port; second at the live echo device.
	// The loop's break must select the first, so the relay dies with 1011.
	seedSettings(t, `{"desktops":[
		{"name":"dup","addr":"ws://127.0.0.1:1"},
		{"name":"dup","addr":"`+dev.URL+`"}]}`)
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	client, _, err := dialer.Dial(wsProxyURL(srv, "/wui/vnc/dup"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second)) // REAL-TIME
	_, _, rerr := client.ReadMessage()
	ce, ok := rerr.(*websocket.CloseError)
	if !ok || ce.Code != websocket.CloseInternalServerErr {
		t.Fatalf("client read = %v, want 1011 close from the first (refused) device", rerr)
	}
	if !strings.Contains(ce.Text, "device dial failed") {
		t.Errorf("close text = %q, want it to name the device dial failure", ce.Text)
	}
}

// Kills remotedesktop.go:272 (statement/remove: trailing stop dropped): when
// the main pump exits, relayWS itself must close both conns — the surviving
// reverse pump would otherwise block on its read forever.
func TestMutD_RelayWS_ClosesBothConnsWhenMainPumpExits(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mkPair := func() (*websocket.Conn, *websocket.Conn, *httptest.Server) {
		srvConn := make(chan *websocket.Conn, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ws, err := up.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			srvConn <- ws
		}))
		client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			t.Fatalf("pair dial: %v", err)
		}
		return client, <-srvConn, srv
	}
	aC, aS, srvA := mkPair()
	defer srvA.Close()
	bC, bS, srvB := mkPair()
	defer srvB.Close()

	relayDone := make(chan struct{})
	go func() {
		relayWS(aS, bS)
		close(relayDone)
	}()
	// Kill the b side: the MAIN pump (b→a) reads b and exits first, while the
	// goroutine pump stays parked reading aS.
	_ = bC.Close()
	select {
	case <-relayDone:
	case <-time.After(2 * time.Second):
		t.Fatal("relayWS did not return after the main pump exited")
	}
	_ = aC.SetReadDeadline(time.Now().Add(2 * time.Second)) // REAL-TIME
	_, _, rerr := aC.ReadMessage()
	if rerr == nil {
		t.Fatal("a side must be closed after relay teardown")
	}
	if ne, ok := rerr.(net.Error); ok && ne.Timeout() {
		t.Fatal("a peer still open after 2s — relayWS must close both conns so the parked pump unblocks")
	}
	_ = aC.Close()
}

// Kills remotedesktop.go:282 (branch/if): when ReadMessage fails MID-MESSAGE
// it returns a valid message type plus partial data; the mutated pump falls
// through and forwards those partial bytes, the original must not. The
// mid-message error is produced deterministically with a read limit hit on
// the second frame of a fragmented message.
func TestMutD_PumpWS_ReadErrorForwardsNothing(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	// src side: the server pushes a fragmented binary message (2x64 bytes);
	// the client under a 64-byte read limit errors mid-message after the
	// first frame.
	srcSrvConn := make(chan *websocket.Conn, 1)
	srcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		srcSrvConn <- ws
		frame1 := append([]byte{0x02, 0x40}, bytes.Repeat([]byte{0xA1}, 64)...) // binary, no FIN
		frame2 := append([]byte{0x80, 0x40}, bytes.Repeat([]byte{0xB2}, 64)...) // continuation, FIN
		if _, err := ws.UnderlyingConn().Write(append(frame1, frame2...)); err != nil {
			return
		}
	}))
	t.Cleanup(srcSrv.Close)

	dstSrvConn := make(chan *websocket.Conn, 1)
	dstSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		dstSrvConn <- ws
	}))
	t.Cleanup(dstSrv.Close)

	src, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srcSrv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("src dial: %v", err)
	}
	defer src.Close()
	src.SetReadLimit(64)
	dst, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(dstSrv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dst dial: %v", err)
	}
	defer dst.Close()
	dstS := <-dstSrvConn

	pumped := make(chan struct{})
	go func() {
		pumpWS(src, dstS)
		close(pumped)
	}()

	_ = dst.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // REAL-TIME
	_, data, derr := dst.ReadMessage()
	if derr == nil {
		t.Fatalf("pump forwarded %d bytes after a failed read; the error path must write no frame", len(data))
	}
	if ne, ok := derr.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("dst read err = %v, want the 500ms deadline (silence, not a frame)", derr)
	}
	select {
	case <-pumped:
	case <-time.After(time.Second):
		t.Fatal("pumpWS did not return after the read error")
	}
}
