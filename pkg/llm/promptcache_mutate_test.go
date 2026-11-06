package llm

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fmt"
	"github.com/liuy/gbot/pkg/types"
	"slices"
)

// NOT parallel anywhere in this file: the prompt-cache state store and the
// default slog handler are process-global.

// syncedLogBuffer serializes writes from slog handlers: lingering goroutines
// from earlier stream tests may still log through a swapped default logger.
type syncedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncedLogBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

func pcSystem(text string) []map[string]any {
	return []map[string]any{{"type": "text", "text": text}}
}

func pcTool(name, desc string) []map[string]any {
	return []map[string]any{{"name": name, "description": desc, "input_schema": map[string]any{"type": "object"}}}
}

// captureBreakLog runs fn with a capturing default logger and returns what
// was logged at WARN+.
func captureBreakLog(t *testing.T, fn func()) string {
	t.Helper()
	buf := &syncedLogBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(old)
	fn()
	return buf.String()
}

// breakLogFor records the phase-1 and phase-2 states and runs the post-call
// check with a >5%/>2000-token drop, returning the logged break output.
func breakLogFor(t *testing.T, record func(key PromptStateKey, phase int)) string {
	t.Helper()
	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	record(key, 1)
	record(key, 2)
	CheckResponseForCacheBreak(key, 10000, 0, nil) // prime the read baseline
	return captureBreakLog(t, func() {
		CheckResponseForCacheBreak(key, 7999, 0, nil)
	})
}

func TestPromptCacheConstants(t *testing.T) {
	if maxTrackedSources != 10 {
		t.Errorf("maxTrackedSources = %d, want 10", maxTrackedSources)
	}
	if minCacheMissTokens != 2000 {
		t.Errorf("minCacheMissTokens = %d, want 2000", minCacheMissTokens)
	}
	if cacheTTL5MinMS != 5*60*1000 {
		t.Errorf("cacheTTL5MinMS = %d, want 300000", cacheTTL5MinMS)
	}
	if cacheTTL1HourMS != 60*60*1000 {
		t.Errorf("cacheTTL1HourMS = %d, want 3600000", cacheTTL1HourMS)
	}
}

// djb2 is an exact port: seed 5381, hash*33+c per byte.
func TestDjb2HashExactValues(t *testing.T) {
	if djb2Hash("") != 5381 {
		t.Errorf("djb2Hash(\"\") = %d, want 5381", djb2Hash(""))
	}
	if djb2Hash("a") != 5381*33+97 {
		t.Errorf("djb2Hash(\"a\") = %d, want %d", djb2Hash("a"), 5381*33+97)
	}
	if djb2Hash("ab") != (5381*33+97)*33+98 {
		t.Errorf("djb2Hash(\"ab\") = %d", djb2Hash("ab"))
	}
}

func TestSanitizeToolName(t *testing.T) {
	if got := sanitizeToolName("mcp__github__tool"); got != "mcp" {
		t.Errorf("sanitizeToolName mcp = %q, want mcp", got)
	}
	if got := sanitizeToolName("Bash"); got != "Bash" {
		t.Errorf("sanitizeToolName plain = %q, want passthrough", got)
	}
}

func TestGetTrackingKey(t *testing.T) {
	if got := getTrackingKey("compact", ""); got != "repl_main_thread" {
		t.Errorf("compact = %q, want repl_main_thread", got)
	}
	if got := getTrackingKey("repl_main_thread", ""); got != "repl_main_thread" {
		t.Errorf("repl_main_thread = %q", got)
	}
	if got := getTrackingKey("agent:custom", "agent-7"); got != "agent-7" {
		t.Errorf("agent with id = %q, want agent-7", got)
	}
	if got := getTrackingKey("agent:custom", ""); got != "agent:custom" {
		t.Errorf("agent without id = %q, want source", got)
	}
	if got := getTrackingKey("untracked", ""); got != "" {
		t.Errorf("untracked = %q, want empty", got)
	}
}

var flagFor = map[string]string{
	"model changed (m1 → m2)":                                   "modelChanged=true",
	"system prompt changed (+2 chars)":                          "systemChanged=true",
	"system prompt changed (-2 chars)":                          "systemChanged=true",
	"tools changed (tool prompt/schema changed, same tool set)": "toolsChanged=true",
	"tools changed (+1/-0 tools)":                               "toolsChanged=true",
	"tools changed (+0/-1 tools)":                               "toolsChanged=true",
	"cache_control changed (scope or TTL)":                      "cacheControlChanged=true",
	"betas changed (+b)":                                        "betasChanged=true",
	"betas changed (-b)":                                        "betasChanged=true",
	"fast mode toggled":                                         "fastModeChanged=true",
	"auto mode toggled":                                         "autoModeChanged=true",
	"overage state changed (TTL latched, no flip)":              "overageChanged=true",
	"cached microcompact toggled":                               "cachedMCChanged=true",
	"effort changed (low → high)":                               "effortChanged=true",
	"extra body params changed":                                 "extraBodyChanged=true",
}

// Each changed dimension surfaces its exact reason phrase in the break log.
func TestCheckResponseForCacheBreak_ReasonPhrases(t *testing.T) {
	cases := []struct {
		name   string
		record func(key PromptStateKey, phase int)
		want   string
	}{
		{"model", func(k PromptStateKey, p int) {
			m := "m1"
			if p == 2 {
				m = "m2"
			}
			RecordPromptState(nil, nil, k, m, nil, "", false, false, false, false, "", 0)
		}, "model changed (m1 → m2)"},
		{"system", func(k PromptStateKey, p int) {
			s := "aaaa"
			if p == 2 {
				s = "bbbbbb"
			}
			RecordPromptState(pcSystem(s), nil, k, "m", nil, "", false, false, false, false, "", 0)
		}, "system prompt changed (+2 chars)"},
		{"system shrink", func(k PromptStateKey, p int) {
			s := "bbbbbb"
			if p == 2 {
				s = "aaaa"
			}
			RecordPromptState(pcSystem(s), nil, k, "m", nil, "", false, false, false, false, "", 0)
		}, "system prompt changed (-2 chars)"},
		{"tool schema", func(k PromptStateKey, p int) {
			d := "d1"
			if p == 2 {
				d = "d2"
			}
			RecordPromptState(nil, pcTool("W", d), k, "m", nil, "", false, false, false, false, "", 0)
		}, "tools changed (tool prompt/schema changed, same tool set)"},
		{"tool added", func(k PromptStateKey, p int) {
			ts := pcTool("A", "d")
			if p == 2 {
				ts = append(ts, pcTool("B", "d")[0])
			}
			RecordPromptState(nil, ts, k, "m", nil, "", false, false, false, false, "", 0)
		}, "tools changed (+1/-0 tools)"},
		{"tool removed", func(k PromptStateKey, p int) {
			ts := append(pcTool("A", "d"), pcTool("B", "d")[0])
			if p == 2 {
				ts = pcTool("A", "d")
			}
			RecordPromptState(nil, ts, k, "m", nil, "", false, false, false, false, "", 0)
		}, "tools changed (+0/-1 tools)"},
		{"cache control", func(k PromptStateKey, p int) {
			sys := []map[string]any{{"type": "text", "text": "same"}}
			if p == 2 {
				sys[0]["cache_control"] = map[string]any{"type": "ephemeral"}
			}
			RecordPromptState(sys, nil, k, "m", nil, "", false, false, false, false, "", 0)
		}, "cache_control changed (scope or TTL)"},
		{"betas", func(k PromptStateKey, p int) {
			b := []string{"a"}
			if p == 2 {
				b = []string{"a", "b"}
			}
			RecordPromptState(nil, nil, k, "m", b, "", false, false, false, false, "", 0)
		}, "betas changed (+b)"},
		{"betas removed", func(k PromptStateKey, p int) {
			b := []string{"a", "b"}
			if p == 2 {
				b = []string{"a"}
			}
			RecordPromptState(nil, nil, k, "m", b, "", false, false, false, false, "", 0)
		}, "betas changed (-b)"},
		{"strategy", func(k PromptStateKey, p int) {
			s := ""
			if p == 2 {
				s = "aggressive"
			}
			RecordPromptState(nil, nil, k, "m", nil, s, false, false, false, false, "", 0)
		}, "global cache strategy changed ( → aggressive)"},
		{"fast mode", func(k PromptStateKey, p int) {
			RecordPromptState(nil, nil, k, "m", nil, "", p == 2, false, false, false, "", 0)
		}, "fast mode toggled"},
		{"auto mode", func(k PromptStateKey, p int) {
			RecordPromptState(nil, nil, k, "m", nil, "", false, p == 2, false, false, "", 0)
		}, "auto mode toggled"},
		{"overage", func(k PromptStateKey, p int) {
			RecordPromptState(nil, nil, k, "m", nil, "", false, false, p == 2, false, "", 0)
		}, "overage state changed (TTL latched, no flip)"},
		{"cached mc", func(k PromptStateKey, p int) {
			RecordPromptState(nil, nil, k, "m", nil, "", false, false, false, p == 2, "", 0)
		}, "cached microcompact toggled"},
		{"effort", func(k PromptStateKey, p int) {
			e := "low"
			if p == 2 {
				e = "high"
			}
			RecordPromptState(nil, nil, k, "m", nil, "", false, false, false, false, e, 0)
		}, "effort changed (low → high)"},
		{"extra body", func(k PromptStateKey, p int) {
			h := uint32(0)
			if p == 2 {
				h = 7
			}
			RecordPromptState(nil, nil, k, "m", nil, "", false, false, false, false, "", h)
		}, "extra body params changed"},
	}
	for _, tc := range cases {
		logged := breakLogFor(t, tc.record)
		if !strings.Contains(logged, tc.want) {
			t.Errorf("%s: reason %q missing from break log: %.400q", tc.name, tc.want, logged)
		}
		if flag, ok := flagFor[tc.want]; ok && !strings.Contains(logged, flag) {
			t.Errorf("%s: flag %q missing from break log: %.400q", tc.name, flag, logged)
		}
	}
}

// The recorded state after a change carries the NEW values.
func TestRecordPromptState_UpdatesState(t *testing.T) {
	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(pcSystem("aaaa"), pcTool("W", "d"), key, "m1", []string{"b"}, "s1", true, true, true, true, "low", 1)
	RecordPromptState(pcSystem("bb"), pcTool("X", "d"), key, "m2", []string{"c"}, "s2", false, false, false, false, "high", 2)

	muState.Lock()
	st := stateStore["repl_main_thread"]
	muState.Unlock()
	if st.Model != "m2" || st.SystemCharCount != 2 || st.GlobalCacheStrategy != "s2" || st.Betas[0] != "c" ||
		st.FastMode || st.AutoModeActive || st.IsUsingOverage || st.CachedMCEnabled || st.EffortValue != "high" || st.ExtraBodyHash != 2 {
		t.Errorf("updated state = %+v", st)
	}
	if len(st.ToolNames) != 1 || st.ToolNames[0] != "X" {
		t.Errorf("tool names = %v, want [X]", st.ToolNames)
	}
	if st.SystemHash != computeHash(stripCacheControl(pcSystem("bb"))) {
		t.Errorf("system hash not refreshed to the new system")
	}
	if st.ToolsHash != computeHash(stripCacheControl(pcTool("X", "d"))) {
		t.Errorf("tools hash not refreshed to the new tools")
	}
	if st.CacheControlHash != extractCacheControlHash(pcSystem("bb")) {
		t.Errorf("cache-control hash not refreshed")
	}
	if content := st.BuildDiffableContent(); !strings.Contains(content, "bb") || !strings.Contains(content, "X") {
		t.Errorf("diffable content = %.100q, want the new system and tool", content)
	}
	if st.CallCount != 2 {
		t.Errorf("call count = %d, want 2", st.CallCount)
	}
	pc := st.PendingChanges
	if pc == nil {
		t.Fatal("no pending changes recorded")
	}
	if !pc.SystemPromptChanged || !pc.ToolSchemasChanged || !pc.ModelChanged || !pc.BetasChanged ||
		!pc.GlobalCacheStrategyChanged || !pc.FastModeChanged || !pc.AutoModeActiveChanged ||
		!pc.OverageChanged || !pc.CachedMCEnabledChanged || !pc.EffortChanged || !pc.ExtraBodyChanged {
		t.Errorf("pending flags = %+v", pc)
	}
	if len(pc.AddedTools) != 1 || pc.AddedTools[0] != "X" || len(pc.RemovedTools) != 1 || pc.RemovedTools[0] != "W" {
		t.Errorf("tool diff = %+v / %+v", pc.AddedTools, pc.RemovedTools)
	}
	if len(pc.AddedBetas) != 1 || pc.AddedBetas[0] != "c" || len(pc.RemovedBetas) != 1 || pc.RemovedBetas[0] != "b" {
		t.Errorf("beta diff = %+v / %+v", pc.AddedBetas, pc.RemovedBetas)
	}
	if pc.SystemCharDelta != -2 {
		t.Errorf("char delta = %d, want -2", pc.SystemCharDelta)
	}
	if pc.PreviousModel != "m1" || pc.NewModel != "m2" {
		t.Errorf("model transition = %q → %q", pc.PreviousModel, pc.NewModel)
	}
}

// An unchanged second call clears pending changes.
func TestRecordPromptState_UnchangedClearsPending(t *testing.T) {
	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	rec := func() { RecordPromptState(pcSystem("s"), nil, key, "m", nil, "", false, false, false, false, "", 0) }
	rec()
	rec()
	muState.Lock()
	pc := stateStore["repl_main_thread"].PendingChanges
	muState.Unlock()
	if pc != nil {
		t.Errorf("pending changes = %+v, want nil after unchanged record", pc)
	}
}

// The break condition needs BOTH >5% drop and >=2000 absolute tokens.
func TestCheckResponseForCacheBreak_DropBoundaries(t *testing.T) {
	cases := []struct {
		curr   int
		breaks bool
	}{
		{8000, true},  // drop exactly 2000 ≥ 2000, curr < 95% of 10000
		{7999, true},  // drop 2001
		{8001, false}, // drop 1999 < 2000
		{9500, false}, // 9500 ≥ 95% of 10000 (0.95 boundary inclusive)
		{9499, false}, // below 95% but drop 501 < 2000
	}
	// Exactly 95% of a large baseline: no break (>= is inclusive).
	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(pcSystem("s"), nil, key, "m", nil, "", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(key, 100000, 0, nil)
	logged := captureBreakLog(t, func() {
		CheckResponseForCacheBreak(key, 95000, 0, nil)
	})
	if strings.Contains(logged, "prompt_cache:break") {
		t.Errorf("exactly-95%% read logged a break: %q", logged)
	}

	for _, tc := range cases {
		ResetPromptCacheBreakDetection()
		key := PromptStateKey{QuerySource: "repl_main_thread"}
		RecordPromptState(pcSystem("s"), nil, key, "m", nil, "", false, false, false, false, "", 0)
		CheckResponseForCacheBreak(key, 10000, 0, nil) // prime PrevCacheRead
		logged := captureBreakLog(t, func() {
			CheckResponseForCacheBreak(key, tc.curr, 0, nil)
		})
		if strings.Contains(logged, "prompt_cache:break") != tc.breaks {
			t.Errorf("curr=%d: break logged = %v, want %v (%.200q)", tc.curr, strings.Contains(logged, "prompt_cache:break"), tc.breaks, logged)
		}
	}
}

// The first call for a key has no baseline and must not log a break.
func TestCheckResponseForCacheBreak_FirstCallSilent(t *testing.T) {
	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(nil, nil, key, "m", nil, "", false, false, false, false, "", 0)
	logged := captureBreakLog(t, func() {
		CheckResponseForCacheBreak(key, 5000, 0, nil)
	})
	if strings.Contains(logged, "prompt_cache:break") {
		t.Errorf("first call logged a break: %q", logged)
	}
	muState.Lock()
	prev := stateStore["repl_main_thread"].PrevCacheRead
	muState.Unlock()
	if prev != 5000 {
		t.Errorf("PrevCacheRead = %d, want 5000 (recorded for next call)", prev)
	}
}

// TTL reasons when nothing changed: the gap to the LAST assistant message
// picks the bucket.
func TestCheckResponseForCacheBreak_TTLReasons(t *testing.T) {
	cases := []struct {
		name string
		msgs []messageWithTimestamp
		want string
	}{
		{"1h", []messageWithTimestamp{
			{role: "assistant", timestamp: time.Now().Add(-2 * time.Hour)}, // REAL-TIME: TTL buckets compare wall clock
			{role: "user", timestamp: time.Now()},                          // REAL-TIME: TTL buckets compare wall clock
		}, "possible 1h TTL expiry (prompt unchanged)"},
		{"5min", []messageWithTimestamp{
			{role: "assistant", timestamp: time.Now().Add(-10 * time.Minute)}, // REAL-TIME: TTL buckets compare wall clock
		}, "possible 5min TTL expiry (prompt unchanged)"},
		{"server-side", []messageWithTimestamp{
			{role: "assistant", timestamp: time.Now().Add(-1 * time.Minute)}, // REAL-TIME: TTL buckets compare wall clock
		}, "likely server-side (prompt unchanged, <5min gap)"},
		{"unknown", []messageWithTimestamp{
			{role: "user", timestamp: time.Now()}, // REAL-TIME: TTL buckets compare wall clock
		}, "unknown cause"},
		{"last assistant wins", []messageWithTimestamp{
			{role: "assistant", timestamp: time.Now().Add(-2 * time.Hour)},   // REAL-TIME: TTL buckets compare wall clock
			{role: "assistant", timestamp: time.Now().Add(-1 * time.Minute)}, // REAL-TIME: TTL buckets compare wall clock
		}, "likely server-side (prompt unchanged, <5min gap)"},
	}
	for _, tc := range cases {
		ResetPromptCacheBreakDetection()
		key := PromptStateKey{QuerySource: "repl_main_thread"}
		rec := func() { RecordPromptState(nil, nil, key, "m", nil, "", false, false, false, false, "", 0) }
		rec()
		rec()
		CheckResponseForCacheBreak(key, 10000, 0, nil) // prime the read baseline
		logged := captureBreakLog(t, func() {
			CheckResponseForCacheBreak(key, 7999, 0, tc.msgs)
		})
		if !strings.Contains(logged, tc.want) {
			t.Errorf("%s: reason %q missing: %.300q", tc.name, tc.want, logged)
		}
		// With nothing changed every dimension flag must read false.
		for _, flag := range []string{
			"systemChanged=false", "toolsChanged=false", "modelChanged=false", "cacheControlChanged=false",
			"betasChanged=false", "fastModeChanged=false", "autoModeChanged=false", "overageChanged=false",
			"cachedMCChanged=false", "effortChanged=false", "extraBodyChanged=false",
		} {
			if !strings.Contains(logged, flag) {
				t.Errorf("%s: flag %q missing from unchanged break log: %.300q", tc.name, flag, logged)
			}
		}
	}
}

// The break log carries the exact counters.
func TestCheckResponseForCacheBreak_LogFields(t *testing.T) {
	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	rec := func() { RecordPromptState(nil, nil, key, "m", nil, "", false, false, false, false, "", 0) }
	rec()
	rec()
	CheckResponseForCacheBreak(key, 10000, 0, nil)                                                 // prime the read baseline
	msgs := []messageWithTimestamp{{role: "assistant", timestamp: time.Now().Add(-2 * time.Hour)}} // REAL-TIME: TTL buckets compare wall clock
	logged := captureBreakLog(t, func() {
		CheckResponseForCacheBreak(key, 7999, 4321, msgs)
	})
	for _, field := range []string{
		"cacheReadPrev=10000", "cacheReadCurr=7999", "tokenDrop=2001", "cacheCreation=4321",
		"callCount=2", "ttlExpiry=1h", "source=repl_main_thread",
	} {
		if !strings.Contains(logged, field) {
			t.Errorf("field %q missing from break log: %.400q", field, logged)
		}
	}
}

// A notified deletion swallows the next drop as expected.
func TestNotifyCacheDeletion_SuppressesBreak(t *testing.T) {
	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	rec := func() { RecordPromptState(nil, nil, key, "m", nil, "", false, false, false, false, "", 0) }
	rec()
	rec()
	CheckResponseForCacheBreak(key, 10000, 0, nil)
	NotifyCacheDeletion(key)
	logged := captureBreakLog(t, func() {
		CheckResponseForCacheBreak(key, 100, 0, nil)
	})
	if !strings.Contains(logged, "prompt_cache:deletion_applied") {
		t.Errorf("deletion_applied missing: %q", logged)
	}
	if strings.Contains(logged, "prompt_cache:break") {
		t.Errorf("expected deletion logged as break: %q", logged)
	}
}

// Compaction resets the read baseline: no break on the next call.
func TestNotifyCompaction_ResetsBaseline(t *testing.T) {
	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	rec := func() { RecordPromptState(nil, nil, key, "m", nil, "", false, false, false, false, "", 0) }
	rec()
	rec()
	CheckResponseForCacheBreak(key, 10000, 0, nil)
	NotifyCompaction(key)
	logged := captureBreakLog(t, func() {
		CheckResponseForCacheBreak(key, 100, 0, nil)
	})
	if strings.Contains(logged, "prompt_cache:break") {
		t.Errorf("post-compaction drop logged as break: %q", logged)
	}
}

// Capacity eviction drops the oldest tracked key in insertion order.
func TestRecordPromptState_EvictionAtCapacity(t *testing.T) {
	ResetPromptCacheBreakDetection()
	for i := range maxTrackedSources + 1 {
		key := PromptStateKey{QuerySource: "agent:custom", AgentID: string(rune('a' + i))}
		RecordPromptState(nil, nil, key, "m", nil, "", false, false, false, false, "", 0)
	}
	muState.Lock()
	defer muState.Unlock()
	if len(stateStore) != maxTrackedSources {
		t.Errorf("tracked = %d, want %d", len(stateStore), maxTrackedSources)
	}
	if _, still := stateStore["a"]; still {
		t.Error("oldest key a was not evicted")
	}
	if _, ok := stateStore[string(rune('a'+maxTrackedSources))]; !ok {
		t.Error("newest key missing")
	}
}

// Main-thread reset preserves sub-agent tracking.
func TestResetMainThreadCacheBreakDetection(t *testing.T) {
	ResetPromptCacheBreakDetection()
	main := PromptStateKey{QuerySource: "repl_main_thread"}
	agent := PromptStateKey{QuerySource: "agent:custom", AgentID: "agent-1"}
	RecordPromptState(nil, nil, main, "m", nil, "", false, false, false, false, "", 0)
	RecordPromptState(nil, nil, agent, "m", nil, "", false, false, false, false, "", 0)
	ResetMainThreadCacheBreakDetection()
	muState.Lock()
	_, mainGone := stateStore["repl_main_thread"]
	_, agentKept := stateStore["agent-1"]
	order := len(insertionOrder)
	muState.Unlock()
	if mainGone {
		t.Error("main thread state not cleared")
	}
	if !agentKept {
		t.Error("agent state cleared by main-thread reset")
	}
	if order != 1 {
		t.Errorf("insertionOrder length = %d, want 1", order)
	}
}

func TestCleanupAgentTrackingRemovesOnlyAgent(t *testing.T) {
	ResetPromptCacheBreakDetection()
	agent := PromptStateKey{QuerySource: "agent:custom", AgentID: "agent-9"}
	other := PromptStateKey{QuerySource: "agent:custom", AgentID: "agent-8"}
	RecordPromptState(nil, nil, agent, "m", nil, "", false, false, false, false, "", 0)
	RecordPromptState(nil, nil, other, "m", nil, "", false, false, false, false, "", 0)
	CleanupAgentTracking("agent-9")
	muState.Lock()
	_, gone := stateStore["agent-9"]
	_, kept := stateStore["agent-8"]
	muState.Unlock()
	if gone || !kept {
		t.Errorf("cleanup removed agent-9=%v kept agent-8=%v", gone, kept)
	}
}

// Untracked sources are ignored end to end.
func TestCheckResponseForCacheBreak_UntrackedKey(t *testing.T) {
	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "somewhere_else"}
	logged := captureBreakLog(t, func() {
		RecordPromptState(nil, nil, key, "m", nil, "", false, false, false, false, "", 0)
		CheckResponseForCacheBreak(key, 7999, 0, nil)
		NotifyCacheDeletion(key)
		NotifyCompaction(key)
	})
	if logged != "" {
		t.Errorf("untracked source produced logs: %q", logged)
	}
	muState.Lock()
	n := len(stateStore)
	muState.Unlock()
	if n != 0 {
		t.Errorf("untracked source recorded state: %d entries", n)
	}
}

// A break with changes writes a diff file containing the tool inventory.
func TestCheckResponseForCacheBreak_WritesDiff(t *testing.T) {
	dir := t.TempDir()
	cacheBreakDirOverride = dir
	defer func() { cacheBreakDirOverride = filepath.Join(os.TempDir(), "gbot-test-cache-break") }()

	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(nil, pcTool("Old", "d"), key, "m", nil, "", false, false, false, false, "", 0)
	RecordPromptState(nil, pcTool("New", "d"), key, "m", nil, "", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(key, 10000, 0, nil) // prime the read baseline

	var diffPath string
	logged := captureBreakLog(t, func() {
		CheckResponseForCacheBreak(key, 7999, 0, nil)
	})
	if _, after, ok := strings.Cut(logged, "diff="); ok {
		diffPath = strings.Trim(strings.TrimSpace(after), "\"")
	}
	if diffPath == "" {
		t.Fatalf("no diff path in log: %.300q", logged)
	}
	data, err := os.ReadFile(diffPath)
	if err != nil {
		t.Fatalf("read diff: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "Old") || !strings.Contains(text, "New") {
		t.Errorf("diff lacks tool names: %.200q", text)
	}
	if !strings.Contains(text, "=== Tools (1) ===") {
		t.Errorf("diff lacks tools header: %.200q", text)
	}
}

// Old diff files (past 24h) are pruned by the next write; fresh ones stay.
func TestCleanupOldDiffs(t *testing.T) {
	dir := t.TempDir()
	cacheBreakDirOverride = dir
	defer func() { cacheBreakDirOverride = filepath.Join(os.TempDir(), "gbot-test-cache-break") }()

	old := filepath.Join(dir, "cache-break-old.diff")
	fresh := filepath.Join(dir, "cache-break-fresh.diff")
	for _, f := range []string{old, fresh} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	stampOld := time.Now().Add(-25 * time.Hour)   // REAL-TIME: cleanup cutoff compares wall clock
	stampFresh := time.Now().Add(-23 * time.Hour) // REAL-TIME: cleanup cutoff compares wall clock
	if err := os.Chtimes(old, stampOld, stampOld); err != nil {
		t.Fatalf("chtimes old: %v", err)
	}
	if err := os.Chtimes(fresh, stampFresh, stampFresh); err != nil {
		t.Fatalf("chtimes fresh: %v", err)
	}

	cleanupOldDiffs()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("25h-old diff survived: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("23h-old diff pruned: %v", err)
	}
}

// Request conversion covers all three system spellings and both tool forms.
func TestRequestToSystemMaps_Forms(t *testing.T) {
	blocks := RequestToSystemMaps(&Request{SystemBlocks: []SystemBlockParam{
		{Type: "text", Text: "a"},
		{Type: "text", Text: "b", CacheControl: &types.CacheControlConfig{Type: "ephemeral"}},
	}})
	if len(blocks) != 2 || blocks[0]["text"] != "a" || blocks[1]["cache_control"] == nil {
		t.Errorf("SystemBlocks form = %+v", blocks)
	}

	arr := RequestToSystemMaps(&Request{System: json.RawMessage(`[{"type":"text","text":"arr"}]`)})
	if len(arr) != 1 || arr[0]["text"] != "arr" {
		t.Errorf("array form = %+v", arr)
	}

	str := RequestToSystemMaps(&Request{System: json.RawMessage(`"plain"`)})
	if len(str) != 1 || str[0]["text"] != "plain" || str[0]["type"] != "text" {
		t.Errorf("string form = %+v", str)
	}

	if got := RequestToSystemMaps(&Request{}); got != nil {
		t.Errorf("empty form = %+v, want nil", got)
	}
}

func TestRequestToToolMaps_Forms(t *testing.T) {
	if got := RequestToToolMaps(&Request{}); got != nil {
		t.Errorf("empty tools = %+v, want nil", got)
	}
	tools := RequestToToolMaps(&Request{Tools: []ToolDef{
		{Name: "W", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}})
	if len(tools) != 1 || tools[0]["name"] != "W" || tools[0]["description"] != "d" {
		t.Errorf("tools = %+v", tools)
	}
	schema, ok := tools[0]["input_schema"].(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Errorf("input_schema = %+v", tools[0]["input_schema"])
	}
}

// TTL labels follow the documented boundaries.
func TestTTLExpiryLabelBoundaries(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{-1, ""},
		{0, "server-side"},
		{4 * 60 * 1000, "server-side"},
		{5 * 60 * 1000, "server-side"},
		{5*60*1000 + 1, "5min"},
		{time.Hour.Milliseconds(), "5min"},
		{time.Hour.Milliseconds() + 1, "1h"},
	}
	for _, tc := range cases {
		if got := ttlExpiryLabel(tc.ms); got != tc.want {
			t.Errorf("ttlExpiryLabel(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

func TestSlicesEqualAndSortCopy(t *testing.T) {
	if !slicesEqual([]string{"a", "b"}, []string{"a", "b"}) {
		t.Error("equal slices reported unequal")
	}
	if slicesEqual([]string{"a"}, []string{"b"}) || slicesEqual([]string{"a"}, []string{"a", "b"}) {
		t.Error("unequal slices reported equal")
	}
	if got := sortCopy([]string{"b", "a"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("sortCopy = %v", got)
	}
	if got := sortCopy(nil); got != nil {
		t.Errorf("sortCopy(nil) = %v, want nil", got)
	}
}

func TestStripCacheControlAndHashes(t *testing.T) {
	blocks := []map[string]any{
		{"type": "text", "text": "a", "cache_control": map[string]any{"type": "ephemeral"}},
		{"type": "text", "text": "b"},
	}
	stripped := stripCacheControl(blocks)
	if _, has := stripped[0]["cache_control"]; has {
		t.Error("cache_control not stripped")
	}
	if stripped[0]["text"] != "a" {
		t.Error("stripping lost other keys")
	}

	// cache_control hash distinguishes present vs absent positions.
	sysA := extractCacheControlHash([]map[string]any{{"cache_control": map[string]any{"type": "ephemeral"}}})
	sysB := extractCacheControlHash([]map[string]any{{}})
	if sysA == sysB {
		t.Error("cache_control hash does not distinguish marked from unmarked")
	}

	got := computeHash(map[string]any{"a": 1})
	if got != computeHash(map[string]any{"a": 1}) || got == computeHash(map[string]any{"b": 1}) {
		t.Errorf("computeHash = %d, want stable per-value hashes", got)
	}

	// Per-tool hashes key by name with index fallback.
	hashes := computePerToolHashes([]map[string]any{{"x": 1}, {"y": 2}}, []string{"", "Named"})
	if _, ok := hashes["__idx_0"]; !ok {
		t.Errorf("index fallback missing: %+v", hashes)
	}
	if _, ok := hashes["Named"]; !ok {
		t.Errorf("named hash missing: %+v", hashes)
	}

	if got := getSystemCharCount([]map[string]any{{"text": "abc"}, {"text": "de"}, {"other": 1}}); got != 5 {
		t.Errorf("char count = %d, want 5", got)
	}
}

// Same-length system edits carry no char-delta suffix.
func TestCheckResponseForCacheBreak_ZeroCharDelta(t *testing.T) {
	logged := breakLogFor(t, func(k PromptStateKey, p int) {
		s := "aaaa"
		if p == 2 {
			s = "bbbb"
		}
		RecordPromptState(pcSystem(s), nil, k, "m", nil, "", false, false, false, false, "", 0)
	})
	if !strings.Contains(logged, "system prompt changed") {
		t.Fatalf("reason missing: %.300q", logged)
	}
	if strings.Contains(logged, "chars)") {
		t.Errorf("zero delta must not carry a chars suffix: %.300q", logged)
	}

	// ±1 deltas carry their exact suffix.
	plus1 := breakLogFor(t, func(k PromptStateKey, p int) {
		s := "aaaa"
		if p == 2 {
			s = "aaaaa"
		}
		RecordPromptState(pcSystem(s), nil, k, "m", nil, "", false, false, false, false, "", 0)
	})
	if !strings.Contains(plus1, "(+1 chars)") {
		t.Errorf("+1 delta suffix missing: %.300q", plus1)
	}
	minus1 := breakLogFor(t, func(k PromptStateKey, p int) {
		s := "aaaaa"
		if p == 2 {
			s = "aaaa"
		}
		RecordPromptState(pcSystem(s), nil, k, "m", nil, "", false, false, false, false, "", 0)
	})
	if !strings.Contains(minus1, "(-1 chars)") {
		t.Errorf("-1 delta suffix missing: %.300q", minus1)
	}
}

// cache_control changes surface only when neither the strategy nor the
// system prompt changed alongside.
func TestCheckResponseForCacheBreak_CacheControlGating(t *testing.T) {
	// cc + system change: only the system reason.
	logged := breakLogFor(t, func(k PromptStateKey, p int) {
		sys := []map[string]any{{"type": "text", "text": "aaaa"}}
		if p == 2 {
			sys = []map[string]any{{"type": "text", "text": "bbbbbb", "cache_control": map[string]any{"type": "ephemeral"}}}
		}
		RecordPromptState(sys, nil, k, "m", nil, "", false, false, false, false, "", 0)
	})
	if !strings.Contains(logged, "system prompt changed") || strings.Contains(logged, "cache_control changed") {
		t.Errorf("cc+system: %.300q", logged)
	}

	// cc + strategy change: only the strategy reason.
	logged2 := breakLogFor(t, func(k PromptStateKey, p int) {
		sys := []map[string]any{{"type": "text", "text": "same"}}
		strat := ""
		if p == 2 {
			sys[0]["cache_control"] = map[string]any{"type": "ephemeral"}
			strat = "aggressive"
		}
		RecordPromptState(sys, nil, k, "m", nil, strat, false, false, false, false, "", 0)
	})
	if !strings.Contains(logged2, "global cache strategy changed") || strings.Contains(logged2, "cache_control changed") {
		t.Errorf("cc+strategy: %.300q", logged2)
	}
}

// Tool additions do not pollute the changed-schema list; the per-tool hash
// refresh makes a third identical record see no change.
func TestRecordPromptState_ToolDiffBookkeeping(t *testing.T) {
	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(nil, pcTool("A", "d1"), key, "m", nil, "", false, false, false, false, "", 0)
	RecordPromptState(nil, append(pcTool("A", "d1"), pcTool("B", "d2")[0]), key, "m", nil, "", false, false, false, false, "", 0)

	muState.Lock()
	pc := stateStore["repl_main_thread"].PendingChanges
	muState.Unlock()
	if pc == nil {
		t.Fatal("no pending changes after tool addition")
	}
	if len(pc.ChangedToolSchemas) != 0 {
		t.Errorf("added tool listed as changed schema: %+v", pc.ChangedToolSchemas)
	}
	if len(pc.AddedTools) != 1 || pc.AddedTools[0] != "B" {
		t.Errorf("added tools = %+v", pc.AddedTools)
	}

	// A changed schema AFTER a newly added tool still lands in the changed
	// list (the loop continues past new names).
	RecordPromptState(nil, append(pcTool("C", "d9"), pcTool("A", "d3")[0]), key, "m", nil, "", false, false, false, false, "", 0)
	muState.Lock()
	pcLater := stateStore["repl_main_thread"].PendingChanges
	muState.Unlock()
	if pcLater == nil || len(pcLater.ChangedToolSchemas) != 1 || pcLater.ChangedToolSchemas[0] != "A" {
		t.Errorf("schema change behind a new tool missed: %+v", pcLater)
	}

	// Schema change then an identical repeat: the refresh must clear deltas.
	RecordPromptState(nil, pcTool("A", "d2"), key, "m", nil, "", false, false, false, false, "", 0)
	RecordPromptState(nil, pcTool("A", "d2"), key, "m", nil, "", false, false, false, false, "", 0)
	muState.Lock()
	pc2 := stateStore["repl_main_thread"].PendingChanges
	perTool := stateStore["repl_main_thread"].PerToolHashes
	muState.Unlock()
	if pc2 != nil {
		t.Errorf("identical repeat after refresh produced pending changes: %+v", pc2)
	}
	if perTool["A"] != computePerToolHashes(stripCacheControl(pcTool("A", "d2")), []string{"A"})["A"] {
		t.Error("per-tool hash not refreshed to the latest schema")
	}
}

// A no-break check clears the pending changes so the next break cannot
// replay stale reasons.
func TestCheckResponseForCacheBreak_NoBreakClearsPending(t *testing.T) {
	ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}
	RecordPromptState(nil, nil, key, "m1", nil, "", false, false, false, false, "", 0)
	RecordPromptState(nil, nil, key, "m2", nil, "", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(key, 10000, 0, nil)
	// No-break: current read is above the 95% floor.
	captureBreakLog(t, func() { CheckResponseForCacheBreak(key, 9900, 0, nil) })
	muState.Lock()
	pc := stateStore["repl_main_thread"].PendingChanges
	muState.Unlock()
	if pc != nil {
		t.Errorf("pending changes survived a no-break check: %+v", pc)
	}
}

// Agent cleanup also drops the insertion-order entry.
func TestCleanupAgentTrackingOrderEntry(t *testing.T) {
	ResetPromptCacheBreakDetection()
	a := PromptStateKey{QuerySource: "agent:custom", AgentID: "agent-x"}
	b := PromptStateKey{QuerySource: "agent:custom", AgentID: "agent-y"}
	RecordPromptState(nil, nil, a, "m", nil, "", false, false, false, false, "", 0)
	RecordPromptState(nil, nil, b, "m", nil, "", false, false, false, false, "", 0)
	CleanupAgentTracking("agent-x")
	muState.Lock()
	order := append([]string(nil), insertionOrder...)
	muState.Unlock()
	if len(order) != 1 || order[0] != "agent-y" {
		t.Errorf("insertionOrder after cleanup = %v, want [agent-y]", order)
	}
}

// Serialized system blocks keep their type field.
func TestRequestToSystemMaps_KeepsType(t *testing.T) {
	blocks := RequestToSystemMaps(&Request{SystemBlocks: []SystemBlockParam{{Type: "text", Text: "a"}}})
	if blocks[0]["type"] != "text" {
		t.Errorf("type field lost: %+v", blocks[0])
	}
}

// Residual-cleanup round 2. Each test targets the SURVIVED keys listed in
// /home/yliu/mutate-llm/mutver-promptcache-readj.txt; kill lines verified by
// targeted mutver re-runs (see ledger).

// The 24h cutoff is two-sided: 23.5h-old diffs survive, 24.5h/25h go, fresh
// files never collected, and the sweep visits every entry (stale sorts last).
func TestPromptCache_CleanupOldDiffsBoundary(t *testing.T) {
	dir := t.TempDir()
	old := cacheBreakDirOverride
	cacheBreakDirOverride = dir
	defer func() { cacheBreakDirOverride = old }()

	ages := map[string]time.Duration{
		"cache-break-23h.diff":    23*time.Hour + 30*time.Minute,
		"cache-break-24h.diff":    24*time.Hour + 30*time.Minute,
		"cache-break-afresh.diff": time.Hour,
		"cache-break-stale.diff":  25 * time.Hour,
	}
	now := time.Now() // REAL-TIME: cleanup cutoff compares wall clock
	for name, age := range ages {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
	}

	cleanupOldDiffs()

	wantGone := map[string]bool{
		"cache-break-23h.diff":    false,
		"cache-break-24h.diff":    true,
		"cache-break-afresh.diff": false,
		"cache-break-stale.diff":  true,
	}
	for name, gone := range wantGone {
		_, err := os.Stat(filepath.Join(dir, name))
		if os.IsNotExist(err) != gone {
			t.Errorf("%s: removed=%v, want %v", name, os.IsNotExist(err), gone)
		}
	}
}

// writeCacheBreakDiff collects stale diffs, writes mode 0600, and surfaces a
// WriteFile failure as an empty path.
func TestPromptCache_WriteCacheBreakDiffLifecycle(t *testing.T) {
	dir := t.TempDir()
	old := cacheBreakDirOverride
	cacheBreakDirOverride = dir
	defer func() { cacheBreakDirOverride = old }()

	stale := filepath.Join(dir, "cache-break-stale.diff")
	if err := os.WriteFile(stale, nil, 0600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-25 * time.Hour) // REAL-TIME: cleanup cutoff compares wall clock
	if err := os.Chtimes(stale, past, past); err != nil {
		t.Fatal(err)
	}

	path := writeCacheBreakDiff("old body\n", "new body\n")
	if path == "" {
		t.Fatal("empty diff path")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale diff survived writeCacheBreakDiff")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat diff: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("diff perm = %o, want 0600", info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "new body") {
		t.Errorf("diff content missing new side: %q", b)
	}

	// Read-only directory: the failed WriteFile must return "", not a path
	// to a file that was never written.
	ro := t.TempDir()
	cacheBreakDirOverride = ro
	if err := os.Chmod(ro, 0500); err != nil {
		t.Fatal(err)
	}
	if p := writeCacheBreakDiff("a", "b"); p != "" {
		t.Errorf("read-only dir returned %q, want \"\"", p)
	}
	if err := os.Chmod(ro, 0700); err != nil {
		t.Fatal(err)
	}
}

// getCacheBreakDir: HOME fallback path (0755), empty-HOME error, blocked
// MkdirAll error, override precedence.
func TestPromptCache_GetCacheBreakDirPaths(t *testing.T) {
	old := cacheBreakDirOverride
	defer func() { cacheBreakDirOverride = old }()

	home := t.TempDir()
	t.Setenv("HOME", home)
	cacheBreakDirOverride = ""
	dir, err := getCacheBreakDir()
	if err != nil {
		t.Fatalf("getCacheBreakDir: %v", err)
	}
	if want := filepath.Join(home, ".gbot", "cache", "break"); dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0755 {
		t.Errorf("dir perm = %o, want 0755", info.Mode().Perm())
	}

	t.Setenv("HOME", "")
	cacheBreakDirOverride = ""
	if _, err := getCacheBreakDir(); err == nil {
		t.Error("empty HOME: want error")
	}

	t.Setenv("HOME", home)
	cacheBreakDirOverride = ""
	if d, err := getCacheBreakDir(); err != nil || d == "" {
		t.Errorf("home retry: dir=%q err=%v, want success", d, err)
	}

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	cacheBreakDirOverride = filepath.Join(blocker, "sub")
	if _, err := getCacheBreakDir(); err == nil {
		t.Error("blocked path: want MkdirAll error")
	}

	over := filepath.Join(t.TempDir(), "override")
	cacheBreakDirOverride = over
	if d, err := getCacheBreakDir(); err != nil || d != over {
		t.Errorf("override: dir=%q err=%v, want %q", d, err, over)
	}
}

func pcRecord(key PromptStateKey, system string, model string) {
	RecordPromptState(pcSystem(system), nil, key, model, nil, "", false, false, false, false, "", 0)
}

// Eviction is insertion-ordered, bounded at maxTrackedSources, and each new
// untracked source evicts exactly the oldest.
func TestPromptCache_EvictionOrderAndCapacity(t *testing.T) {
	ResetPromptCacheBreakDetection()
	defer ResetPromptCacheBreakDetection()
	for i := range maxTrackedSources {
		pcRecord(PromptStateKey{QuerySource: fmt.Sprintf("agent:custom-%02d", i)}, "s", "m")
	}
	muState.RLock()
	n := len(stateStore)
	muState.RUnlock()
	if n != maxTrackedSources {
		t.Fatalf("store = %d, want %d", n, maxTrackedSources)
	}

	pcRecord(PromptStateKey{QuerySource: "agent:custom-new"}, "s", "m")
	muState.RLock()
	n = len(stateStore)
	_, hasOldest := stateStore["agent:custom-00"]
	_, hasNew := stateStore["agent:custom-new"]
	muState.RUnlock()
	if n != maxTrackedSources {
		t.Errorf("after evict store = %d, want %d", n, maxTrackedSources)
	}
	if hasOldest {
		t.Error("oldest source survived eviction")
	}
	if !hasNew {
		t.Error("new source missing after eviction")
	}

	pcRecord(PromptStateKey{QuerySource: "agent:custom-new2"}, "s", "m")
	muState.RLock()
	_, hasSecond := stateStore["agent:custom-01"]
	muState.RUnlock()
	if hasSecond {
		t.Error("second-oldest survived second eviction")
	}
}

// A full store with an empty insertionOrder must break out, not panic; with
// exactly one order entry the eviction still runs.
func TestPromptCache_EvictionDesyncGuard(t *testing.T) {
	ResetPromptCacheBreakDetection()
	defer ResetPromptCacheBreakDetection()
	muState.Lock()
	for i := range maxTrackedSources {
		stateStore[fmt.Sprintf("x%d", i)] = &promptStateInternal{}
	}
	insertionOrder = nil
	muState.Unlock()

	pcRecord(PromptStateKey{QuerySource: "agent:custom-live"}, "s", "m")
	muState.RLock()
	n := len(stateStore)
	muState.RUnlock()
	if n != maxTrackedSources+1 {
		t.Errorf("desynced store = %d, want %d (break, no eviction)", n, maxTrackedSources+1)
	}

	// Exactly one order entry while full: that entry is the eviction victim.
	ResetPromptCacheBreakDetection()
	muState.Lock()
	for i := range maxTrackedSources - 1 { // 9 entries + "only" = exactly full
		stateStore[fmt.Sprintf("y%d", i)] = &promptStateInternal{}
	}
	insertionOrder = []string{"only"}
	stateStore["only"] = &promptStateInternal{}
	muState.Unlock()
	pcRecord(PromptStateKey{QuerySource: "agent:custom-live"}, "s", "m")
	muState.RLock()
	n = len(stateStore)
	_, onlyPresent := stateStore["only"]
	muState.RUnlock()
	if n != maxTrackedSources {
		t.Errorf("single-order store = %d, want %d (evict 'only', insert new)", n, maxTrackedSources)
	}
	if onlyPresent {
		t.Error("single order entry was not the eviction victim")
	}
}

// Scoped reset keeps sub-agent state and the insertion order consistent;
// full reset empties the store.
func TestPromptCache_ResetScopes(t *testing.T) {
	ResetPromptCacheBreakDetection()
	defer ResetPromptCacheBreakDetection()
	pcRecord(PromptStateKey{QuerySource: "repl_main_thread"}, "a", "m")
	pcRecord(PromptStateKey{QuerySource: "agent:custom-sub"}, "a", "m")

	ResetMainThreadCacheBreakDetection()
	muState.RLock()
	_, mainPresent := stateStore["repl_main_thread"]
	_, subThere := stateStore["agent:custom-sub"]
	ord := slices.Clone(insertionOrder)
	muState.RUnlock()
	if mainPresent {
		t.Error("main thread state survived scoped reset")
	}
	if !subThere {
		t.Error("sub-agent state lost to scoped reset")
	}
	if len(ord) != 1 || ord[0] != "agent:custom-sub" {
		t.Errorf("insertionOrder = %v, want [agent:custom-sub]", ord)
	}

	ResetPromptCacheBreakDetection()
	muState.RLock()
	n := len(stateStore)
	muState.RUnlock()
	if n != 0 {
		t.Errorf("after full reset store = %d, want 0", n)
	}
}

// Break reasons: nil-messages sentinel (unknown cause), cache_control part
// masked by a simultaneous system change, cc-only wording, attr matrix with
// only the model changed, and no-panic on nil changes. Each scenario uses a
// fresh key: a second CheckResponseForCacheBreak on the same key runs the
// no-break path and clears pending changes.
func TestPromptCache_BreakReasonsAndAttrs(t *testing.T) {
	ResetPromptCacheBreakDetection()
	defer ResetPromptCacheBreakDetection()
	freshKey := func(tag string) PromptStateKey {
		return PromptStateKey{QuerySource: "agent:custom-" + tag}
	}

	// nil messages keep the -1 sentinel: "unknown cause".
	k := freshKey("nilmsg")
	pcRecord(k, "m1", "modelA")
	pcRecord(k, "m1", "modelA")
	CheckResponseForCacheBreak(k, 10000, 0, nil)
	log := captureBreakLog(t, func() { CheckResponseForCacheBreak(k, 7999, 0, nil) })
	if !strings.Contains(log, `reason="unknown cause"`) {
		t.Errorf("nil-messages reason: %s", log)
	}

	sysCC := func(text, ttl string) []map[string]any {
		return []map[string]any{{"type": "text", "text": text, "cache_control": map[string]any{"type": "ephemeral", "ttl": ttl}}}
	}

	// cache_control + system both changed: the cache_control part is masked.
	k = freshKey("ccsys")
	RecordPromptState(sysCC("sys one", "5m"), nil, k, "modelA", nil, "", false, false, false, false, "", 0)
	RecordPromptState(sysCC("sys two", "1h"), nil, k, "modelA", nil, "", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(k, 10000, 0, nil)
	log = captureBreakLog(t, func() { CheckResponseForCacheBreak(k, 7999, 0, nil) })
	if strings.Contains(log, "cache_control changed") {
		t.Errorf("system change must mask cache_control part: %s", log)
	}
	if !strings.Contains(log, "system prompt changed") {
		t.Errorf("missing system part: %s", log)
	}

	// cache_control-only change keeps its dedicated wording.
	k = freshKey("cconly")
	RecordPromptState(sysCC("s", "5m"), nil, k, "modelA", nil, "", false, false, false, false, "", 0)
	RecordPromptState(sysCC("s", "1h"), nil, k, "modelA", nil, "", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(k, 10000, 0, nil)
	log = captureBreakLog(t, func() { CheckResponseForCacheBreak(k, 7999, 0, nil) })
	if !strings.Contains(log, "cache_control changed (scope or TTL)") {
		t.Errorf("cc-only wording missing: %s", log)
	}
	if strings.Contains(log, "modelChanged=true") {
		t.Errorf("cc-only change must not set modelChanged: %s", log)
	}

	// Only the model changed: every other attr must log false.
	k = freshKey("attrs")
	pcRecord(k, "stable", "modelA")
	pcRecord(k, "stable", "modelB")
	CheckResponseForCacheBreak(k, 10000, 0, nil)
	log = captureBreakLog(t, func() { CheckResponseForCacheBreak(k, 7999, 0, nil) })
	for _, attr := range []string{"systemChanged", "toolsChanged", "cacheControlChanged", "betasChanged", "fastModeChanged", "autoModeChanged", "overageChanged", "cachedMCChanged", "effortChanged", "extraBodyChanged"} {
		if !strings.Contains(log, attr+"=false") {
			t.Errorf("attr %s not false in: %s", attr, log)
		}
	}
	if !strings.Contains(log, "modelChanged=true") {
		t.Errorf("modelChanged not true: %s", log)
	}
	if strings.Contains(log, "cache_control changed") {
		t.Errorf("model-only change must not carry the cache_control part: %s", log)
	}
	if !strings.Contains(log, "model changed (modelA → modelB)") {
		t.Errorf("model reason part missing: %s", log)
	}

	// Tools changed: the reason carries the add/remove counts.
	k = freshKey("tools")
	RecordPromptState(nil, pcTool("bash", "a"), k, "modelA", nil, "", false, false, false, false, "", 0)
	RecordPromptState(nil, pcTool("bash", "a changed"), k, "modelA", nil, "", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(k, 10000, 0, nil)
	log = captureBreakLog(t, func() { CheckResponseForCacheBreak(k, 7999, 0, nil) })
	if !strings.Contains(log, "tools changed") {
		t.Errorf("tools reason part missing: %s", log)
	}
	k = freshKey("tools2")
	RecordPromptState(nil, pcTool("bash", "a"), k, "modelA", nil, "", false, false, false, false, "", 0)
	RecordPromptState(nil, []map[string]any{{"name": "bash", "description": "a changed", "input_schema": map[string]any{"type": "object"}}, {"name": "read", "description": "r", "input_schema": map[string]any{"type": "object"}}}, k, "modelA", nil, "", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(k, 10000, 0, nil)
	log = captureBreakLog(t, func() { CheckResponseForCacheBreak(k, 7999, 0, nil) })
	if !strings.Contains(log, "tools changed (+1/-0 tools)") {
		t.Errorf("tool add count missing: %s", log)
	}

	// Identical prompts: nil pending changes must not panic on the diff path.
	k = freshKey("nilch")
	pcRecord(k, "stable", "modelB")
	pcRecord(k, "stable", "modelB")
	CheckResponseForCacheBreak(k, 10000, 0, nil)
	log = captureBreakLog(t, func() { CheckResponseForCacheBreak(k, 7999, 0, nil) })
	if !strings.Contains(log, "prompt_cache:break") {
		t.Errorf("nil-changes break missing: %s", log)
	}
}

// Global strategy transitions carry prev/new values in the reason; state
// fields survive the refresh; identical re-records clear pending changes.
func TestPromptCache_StateRefreshFields(t *testing.T) {
	ResetPromptCacheBreakDetection()
	defer ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	RecordPromptState(pcSystem("sys1"), pcTool("bash", "run things"), key, "modelA", nil, "strategyA", false, false, false, false, "", 0)
	muState.RLock()
	st := stateStore["repl_main_thread"]
	gcs := st.GlobalCacheStrategy
	names := slices.Clone(st.ToolNames)
	muState.RUnlock()
	if gcs != "strategyA" {
		t.Errorf("GlobalCacheStrategy = %q, want strategyA", gcs)
	}
	if len(names) != 1 || names[0] != "bash" {
		t.Errorf("ToolNames = %v, want [bash]", names)
	}

	RecordPromptState(pcSystem("sys1"), pcTool("bash", "run things"), key, "modelA", nil, "strategyB", false, false, false, false, "", 0)
	CheckResponseForCacheBreak(key, 10000, 0, nil)
	log := captureBreakLog(t, func() { CheckResponseForCacheBreak(key, 7999, 0, nil) })
	if !strings.Contains(log, "global cache strategy changed (strategyA → strategyB)") {
		t.Errorf("strategy transition missing: %s", log)
	}

	// Same cache_control twice: the second identical record has no changes.
	ccSys := func(cc any) []map[string]any {
		return []map[string]any{{"type": "text", "text": "s", "cache_control": cc}}
	}
	RecordPromptState(ccSys(map[string]any{"type": "ephemeral", "ttl": "1"}), nil, key, "modelA", nil, "strategyB", false, false, false, false, "", 0)
	RecordPromptState(ccSys(map[string]any{"type": "ephemeral", "ttl": "2"}), nil, key, "modelA", nil, "strategyB", false, false, false, false, "", 0)
	RecordPromptState(ccSys(map[string]any{"type": "ephemeral", "ttl": "2"}), nil, key, "modelA", nil, "strategyB", false, false, false, false, "", 0)
	pendingNil := false
	muState.RLock()
	st = stateStore["repl_main_thread"]
	pendingNil = st.PendingChanges == nil
	muState.RUnlock()
	if !pendingNil {
		t.Error("identical re-record left pending changes behind")
	}

	// Deletion-applied branch clears pending changes.
	RecordPromptState(pcSystem("sys2"), nil, key, "modelA", nil, "strategyB", false, false, false, false, "", 0)
	NotifyCacheDeletion(key)
	CheckResponseForCacheBreak(key, 10000, 0, nil)
	muState.RLock()
	st = stateStore["repl_main_thread"]
	pendingNil = st.PendingChanges == nil
	muState.RUnlock()
	if !pendingNil {
		t.Error("deletion-applied branch left pending changes behind")
	}
}

// The break diff renders prev-vs-current content: after an unchanged
// re-record, the prev side must be the re-recorded content, not the first.
func TestPromptCache_BreakDiffUsesLatestContent(t *testing.T) {
	ResetPromptCacheBreakDetection()
	defer ResetPromptCacheBreakDetection()
	key := PromptStateKey{QuerySource: "repl_main_thread"}

	pcRecord(key, "alpha prompt", "m")
	pcRecord(key, "beta prompt", "m")  // changed: prev side = alpha
	pcRecord(key, "beta prompt", "m")  // unchanged: latest content must stick
	pcRecord(key, "gamma prompt", "m") // changed: prev side = beta

	CheckResponseForCacheBreak(key, 10000, 0, nil)
	dir := t.TempDir()
	oldOverride := cacheBreakDirOverride
	cacheBreakDirOverride = dir
	defer func() { cacheBreakDirOverride = oldOverride }()
	log := captureBreakLog(t, func() { CheckResponseForCacheBreak(key, 7999, 0, nil) })

	_, after, ok := strings.Cut(log, "diff=")
	if !ok {
		t.Fatalf("no diff path in log: %s", log)
	}
	path := strings.TrimSpace(strings.SplitN(after, " ", 2)[0])
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read diff: %v", err)
	}
	diff := string(b)
	if !strings.Contains(diff, "gamma prompt") {
		t.Errorf("diff missing current side: %q", diff)
	}
	if !strings.Contains(diff, "beta prompt") {
		t.Errorf("diff prev side must be the re-recorded beta prompt: %q", diff)
	}
	if strings.Contains(diff, "alpha prompt") {
		t.Errorf("diff prev side is stale alpha prompt: %q", diff)
	}
}

// buildDiffableContent renders the marshaled input_schema and the unknown
// fallback for nameless tools; extractCacheControlHash keeps nil padding for
// blocks without cache_control.
func TestPromptCache_DiffableContentAndControlHash(t *testing.T) {
	out := buildDiffableContent(nil, []map[string]any{
		{"name": "t", "description": "d", "input_schema": map[string]any{"type": "object"}},
	}, "m")
	if !strings.Contains(out, `input_schema: {"type":"object"}`) {
		t.Errorf("schema not rendered: %q", out)
	}

	out = buildDiffableContent(nil, []map[string]any{{"description": "d"}}, "m")
	if !strings.Contains(out, "\nunknown\n") {
		t.Errorf("nameless tool not rendered as unknown: %q", out)
	}

	mixed := []map[string]any{
		{"type": "text", "text": "a"},
		{"type": "text", "text": "b", "cache_control": map[string]any{"type": "ephemeral"}},
		{"type": "text", "text": "c"},
	}
	want := computeHash([]any{nil, map[string]any{"type": "ephemeral"}, nil})
	if got := extractCacheControlHash(mixed); got != want {
		t.Errorf("extractCacheControlHash = %d, want %d (nil-padded alignment)", got, want)
	}

	// A 1-byte valid schema JSON survives the len>0 gate.
	req := &Request{Tools: []ToolDef{{Name: "t", InputSchema: json.RawMessage("5")}}}
	ms := RequestToToolMaps(req)
	if len(ms) != 1 {
		t.Fatalf("tool maps = %d, want 1", len(ms))
	}
	if _, ok := ms[0]["input_schema"]; !ok {
		t.Errorf("1-byte input_schema dropped: %v", ms[0])
	}
}

// Hammer for the defer→inline Unlock class: sequential runs pass, -race
// replants die on the data race. Bounded like the reasoning_fallback hammer.
func TestPromptCacheStateConcurrentHammers(t *testing.T) {
	for range 4 {
		ResetPromptCacheBreakDetection()
		var wg sync.WaitGroup
		for g := range 200 {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				key := PromptStateKey{QuerySource: fmt.Sprintf("agent:custom-%d", g%12)}
				RecordPromptState(pcSystem("s"), nil, key, "m", nil, "", false, false, false, false, "", 0)
				NotifyCacheDeletion(key)
				NotifyCompaction(key)
				CheckResponseForCacheBreak(key, 100, 0, nil)
			}(g)
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				ResetMainThreadCacheBreakDetection()
				ResetPromptCacheBreakDetection()
				CleanupAgentTracking(fmt.Sprintf("agent:custom-%d", g%12))
			}(g)
		}
		wg.Wait()
	}
	ResetPromptCacheBreakDetection()
}
