package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/config"
)

func TestInfo_Remaining(t *testing.T) {
	t.Parallel()
	cases := []struct {
		used, want int
	}{
		{0, 100},
		{15, 85},
		{100, 0},
	}
	for _, c := range cases {
		i := Info{Used: c.used}
		if got := i.Remaining(); got != c.want {
			t.Errorf("Used=%d: Remaining()=%d, want %d", c.used, got, c.want)
		}
	}
}

func TestZhipuFetcher_Fetch_OK(t *testing.T) {
	t.Parallel()
	// Fixed timestamp avoids flaky time-dependent assertions.
	const resetMs = 1800000000000 // 2027-01-15T08:00:00Z
	body := mustJSON(t, map[string]any{
		"code":    200,
		"success": true,
		"data": map[string]any{
			"limits": []map[string]any{
				{
					"type":          "TOKENS_LIMIT",
					"unit":          3,
					"number":        5,
					"percentage":    15,
					"nextResetTime": resetMs,
				},
				{
					"type":       "TOKENS_LIMIT",
					"unit":       6,
					"number":     7,
					"percentage": 40,
				},
				{
					"type":       "TIME_LIMIT",
					"unit":       5,
					"number":     1,
					"percentage": 45,
				},
			},
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/monitor/usage/quota/limit" {
			t.Errorf("path = %q, want /api/monitor/usage/quota/limit", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer testkey" {
			t.Errorf("Authorization = %q, want 'Bearer testkey'", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want application/json", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	f := NewZhipuFetcher(srv.URL, "testkey")
	info, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Used != 15 {
		t.Errorf("Used = %d, want 15", info.Used)
	}
	if info.Remaining() != 85 {
		t.Errorf("Remaining = %d, want 85", info.Remaining())
	}
	wantReset := time.UnixMilli(resetMs)
	if !info.ResetAt.Equal(wantReset) {
		t.Errorf("ResetAt = %v, want %v", info.ResetAt, wantReset)
	}
}

func TestZhipuFetcher_Fetch_OK_WeeklyFallback(t *testing.T) {
	t.Parallel()
	// No 5h entry (unit=3,number=5); fallback to first TOKENS_LIMIT (weekly).
	body := mustJSON(t, map[string]any{
		"code":    200,
		"success": true,
		"data": map[string]any{
			"limits": []map[string]any{
				{
					"type":       "TOKENS_LIMIT",
					"unit":       6,
					"number":     7,
					"percentage": 70,
				},
			},
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	f := NewZhipuFetcher(srv.URL, "k")
	info, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Used != 70 {
		t.Errorf("Used = %d, want 70 (weekly fallback)", info.Used)
	}
}

func TestZhipuFetcher_Fetch_NoTokensLimit(t *testing.T) {
	t.Parallel()
	body := mustJSON(t, map[string]any{
		"code":    200,
		"success": true,
		"data": map[string]any{
			"limits": []map[string]any{
				{"type": "TIME_LIMIT", "unit": 5, "number": 1},
			},
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	_, err := NewZhipuFetcher(srv.URL, "k").Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no TOKENS_LIMIT") {
		t.Fatalf("want 'no TOKENS_LIMIT' error, got %v", err)
	}
}

func TestZhipuFetcher_Fetch_HTTPError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"msg":"bad key"}`))
	}))
	defer srv.Close()

	_, err := NewZhipuFetcher(srv.URL, "k").Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("want HTTP 401 error, got %v", err)
	}
}

func TestZhipuFetcher_Fetch_APINotSuccess(t *testing.T) {
	t.Parallel()
	body := mustJSON(t, map[string]any{
		"code":    500,
		"success": false,
		"msg":     "internal error",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	_, err := NewZhipuFetcher(srv.URL, "k").Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "code=500") {
		t.Fatalf("want code=500 error, got %v", err)
	}
}

func TestZhipuFetcher_NormalizesBaseURL(t *testing.T) {
	t.Parallel()
	f := NewZhipuFetcher("https://open.bigmodel.cn/", "k")
	if f.BaseURL != "https://open.bigmodel.cn" {
		t.Errorf("BaseURL = %q, want no trailing slash", f.BaseURL)
	}
}

func TestZhipuFetcher_DefaultBaseURL(t *testing.T) {
	t.Parallel()
	f := NewZhipuFetcher("", "k")
	if f.BaseURL != "https://open.bigmodel.cn" {
		t.Errorf("BaseURL = %q, want default", f.BaseURL)
	}
	// The quota call runs on the TUI's async path: an unbounded or shifted
	// client timeout stalls the status bar.
	if f.Client == nil {
		t.Fatal("Client is nil")
	}
	if f.Client.Timeout != 10*time.Second {
		t.Errorf("Client.Timeout = %v, want 10s", f.Client.Timeout)
	}
}

func TestMinimaxFetcher_Fetch_OK(t *testing.T) {
	t.Parallel()
	const endMs = 1800000000000 // 2027-01-15T08:00:00Z
	body := mustJSON(t, map[string]any{
		"base_resp": map[string]any{
			"status_code": 0,
			"status_msg":  "",
		},
		"model_remains": []map[string]any{
			{
				"model_name":                         "general",
				"start_time":                         1799989200000,
				"end_time":                           endMs,
				"current_interval_remaining_percent": 85,
				"current_interval_status":            1,
			},
			{
				"model_name":                         "video",
				"end_time":                           endMs,
				"current_interval_remaining_percent": 100,
			},
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/token_plan/remains" {
			t.Errorf("path = %q, want /v1/token_plan/remains", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer mmxkey" {
			t.Errorf("Authorization = %q, want 'Bearer mmxkey'", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	f := NewMinimaxFetcher(srv.URL, "mmxkey")
	info, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Used != 15 {
		t.Errorf("Used = %d, want 15 (100-85)", info.Used)
	}
	if info.Remaining() != 85 {
		t.Errorf("Remaining = %d, want 85", info.Remaining())
	}
	wantReset := time.UnixMilli(endMs)
	if !info.ResetAt.Equal(wantReset) {
		t.Errorf("ResetAt = %v, want %v", info.ResetAt, wantReset)
	}
}

func TestMinimaxFetcher_Fetch_ClampsNegative(t *testing.T) {
	t.Parallel()
	body := mustJSON(t, map[string]any{
		"base_resp": map[string]any{"status_code": 0},
		"model_remains": []map[string]any{
			{
				"model_name":                         "general",
				"end_time":                           1800000000000,
				"current_interval_remaining_percent": 105,
			},
		},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	info, err := NewMinimaxFetcher(srv.URL, "k").Fetch(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Used != 0 {
		t.Errorf("Used = %d, want 0 (clamped from negative)", info.Used)
	}
}

func TestMinimaxFetcher_Fetch_EmptyRemains(t *testing.T) {
	t.Parallel()
	body := mustJSON(t, map[string]any{
		"base_resp":     map[string]any{"status_code": 0},
		"model_remains": []map[string]any{},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	_, err := NewMinimaxFetcher(srv.URL, "k").Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "empty model_remains") {
		t.Fatalf("want 'empty model_remains' error, got %v", err)
	}
}

func TestMinimaxFetcher_Fetch_FallbackToFirstEntry(t *testing.T) {
	t.Parallel()
	const endMs = 1800000000000
	body := mustJSON(t, map[string]any{
		"base_resp": map[string]any{"status_code": 0},
		"model_remains": []map[string]any{
			// No "general" entry — should fall back to first.
			{
				"model_name":                         "audio",
				"end_time":                           endMs,
				"current_interval_remaining_percent": 50,
			},
		},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	info, err := NewMinimaxFetcher(srv.URL, "k").Fetch(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Used != 50 {
		t.Errorf("Used = %d, want 50 (100-50, fallback to first entry)", info.Used)
	}
}

func TestMinimaxFetcher_PickBucket(t *testing.T) {
	t.Parallel()
	entries := []minimaxModelRemains{
		{ModelName: "video", CurrentIntervalRemainingPercent: 100},
		{ModelName: "general", CurrentIntervalRemainingPercent: 85},
		{ModelName: "audio", CurrentIntervalRemainingPercent: 100},
	}
	p := pickMinimaxBucket(entries)
	if p == nil || p.ModelName != "general" {
		t.Errorf("pickMinimaxBucket = %+v, want 'general'", p)
	}
}

func TestMinimaxFetcher_PickBucket_Empty(t *testing.T) {
	t.Parallel()
	if p := pickMinimaxBucket(nil); p != nil {
		t.Errorf("pickMinimaxBucket(nil) = %+v, want nil", p)
	}
}

func TestMinimaxFetcher_PickBucket_NoGeneral(t *testing.T) {
	t.Parallel()
	entries := []minimaxModelRemains{
		{ModelName: "video", CurrentIntervalRemainingPercent: 100},
	}
	p := pickMinimaxBucket(entries)
	if p == nil || p.ModelName != "video" {
		t.Errorf("pickMinimaxBucket = %+v, want first entry 'video'", p)
	}
}

func TestMinimaxFetcher_Fetch_StatusError(t *testing.T) {
	t.Parallel()
	body := mustJSON(t, map[string]any{
		"base_resp": map[string]any{
			"status_code": 2056,
			"status_msg":  "usage limit exceeded",
		},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	_, err := NewMinimaxFetcher(srv.URL, "k").Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status=2056") {
		t.Fatalf("want status=2056 error, got %v", err)
	}
}

func TestMinimaxFetcher_NormalizesBaseURL(t *testing.T) {
	t.Parallel()
	f := NewMinimaxFetcher("https://api.minimax.io/", "k")
	if f.BaseURL != "https://api.minimax.io" {
		t.Errorf("BaseURL = %q, want no trailing slash", f.BaseURL)
	}
}

func TestMinimaxFetcher_Fetch_RealResponseShape(t *testing.T) {
	t.Parallel()
	// This test uses the exact response shape observed from the live API.
	// The "general" entry has current_interval_remaining_percent=63, which
	// means 63% remaining → 37% used (100-63).
	const endMs = 1781697600000
	body := mustJSON(t, map[string]any{
		"base_resp": map[string]any{
			"status_code": 0,
			"status_msg":  "success",
		},
		"model_remains": []map[string]any{
			{
				"model_name":                         "general",
				"start_time":                         1781679600000,
				"end_time":                           endMs,
				"remains_time":                       16862587,
				"current_interval_total_count":       0,
				"current_interval_usage_count":       0,
				"current_weekly_total_count":         0,
				"current_weekly_usage_count":         0,
				"weekly_start_time":                  1781452800000,
				"weekly_end_time":                    1782057600000,
				"weekly_remains_time":                376862587,
				"current_interval_status":            1,
				"current_interval_remaining_percent": 63,
				"current_weekly_status":              3,
				"current_weekly_remaining_percent":   100,
			},
			{
				"model_name":                         "video",
				"start_time":                         1781625600000,
				"end_time":                           1781712000000,
				"remains_time":                       31262587,
				"current_interval_total_count":       0,
				"current_interval_usage_count":       0,
				"current_weekly_total_count":         0,
				"current_weekly_usage_count":         0,
				"weekly_start_time":                  1781452800000,
				"weekly_end_time":                    1782057600000,
				"weekly_remains_time":                376862587,
				"current_interval_status":            3,
				"current_interval_remaining_percent": 100,
				"current_weekly_status":              3,
				"current_weekly_remaining_percent":   100,
			},
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/token_plan/remains" {
			t.Errorf("path = %q, want /v1/token_plan/remains", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer mmxkey" {
			t.Errorf("Authorization = %q, want 'Bearer mmxkey'", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	f := NewMinimaxFetcher(srv.URL, "mmxkey")
	info, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// API returns current_interval_remaining_percent=63 (remaining)
	// Our code: used = 100 - 63 = 37
	if info.Used != 37 {
		t.Errorf("Used = %d, want 37 (100-63)", info.Used)
	}
	if info.Remaining() != 63 {
		t.Errorf("Remaining = %d, want 63", info.Remaining())
	}
	// Verify formatter output would show "63%/4h40m"-ish
	if info.ResetAt.UnixMilli() != endMs {
		t.Errorf("ResetAt = %d, want %d", info.ResetAt.UnixMilli(), endMs)
	}
}

func TestPickMinimaxBucket_GeneralFirst(t *testing.T) {
	t.Parallel()
	// The real API returns "general" as the first entry — verify it's picked.
	entries := []minimaxModelRemains{
		{ModelName: "general", CurrentIntervalRemainingPercent: 63},
		{ModelName: "video", CurrentIntervalRemainingPercent: 100},
	}
	p := pickMinimaxBucket(entries)
	if p == nil || p.ModelName != "general" {
		t.Errorf("pickMinimaxBucket = %+v, want 'general'", p)
	}
	if p.CurrentIntervalRemainingPercent != 63 {
		t.Errorf("percent = %d, want 63", p.CurrentIntervalRemainingPercent)
	}
}

func TestDetect_ZhipuByName(t *testing.T) {
	t.Parallel()
	p := &config.Provider{Name: "zhipu", URL: "https://open.bigmodel.cn/api/coding/paas/v4", Keys: []string{"k"}}
	f := Detect(p)
	if f == nil {
		t.Error("Detect(zhipu) = nil, want Fetcher")
	}
	if _, ok := f.(*ZhipuFetcher); !ok {
		t.Errorf("Detect(zhipu) = %T, want *ZhipuFetcher", f)
	}
}

func TestDetect_ZhipuByZaiURL(t *testing.T) {
	t.Parallel()
	p := &config.Provider{Name: "intl", URL: "https://api.z.ai/v1/messages", Keys: []string{"k"}}
	f := Detect(p)
	if f == nil {
		t.Error("Detect(z.ai url) = nil, want Fetcher")
	}
	if _, ok := f.(*ZhipuFetcher); !ok {
		t.Errorf("Detect(z.ai url) = %T, want *ZhipuFetcher", f)
	}
}

func TestDetect_ZhipuByGLMName(t *testing.T) {
	t.Parallel()
	p := &config.Provider{Name: "glm-official", URL: "https://x.com", Keys: []string{"k"}}
	f := Detect(p)
	if f != nil {
		t.Errorf("Detect(glm name) = %T, want nil (disabled)", f)
	}
}

func TestDetect_MinimaxByName(t *testing.T) {
	t.Parallel()
	p := &config.Provider{Name: "MiniMax", URL: "https://api.minimaxi.com/anthropic", Keys: []string{"k"}}
	f := Detect(p)
	if _, ok := f.(*MinimaxFetcher); !ok {
		t.Errorf("Detect(MiniMax) = %T, want *MinimaxFetcher", f)
	}
	if mf, ok := f.(*MinimaxFetcher); ok {
		if want := "https://api.minimaxi.com"; mf.BaseURL != want {
			t.Errorf("BaseURL = %q, want %q (host only, no /anthropic)", mf.BaseURL, want)
		}
	}
}

func TestDetect_MinimaxByURL(t *testing.T) {
	t.Parallel()
	p := &config.Provider{Name: "x", URL: "https://api.minimax.io", Keys: []string{"k"}}
	f := Detect(p)
	if _, ok := f.(*MinimaxFetcher); !ok {
		t.Errorf("Detect(minimax url) = %T, want *MinimaxFetcher", f)
	}
}

func TestHostOnly(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"https://open.bigmodel.cn/api/coding/paas/v4", "https://open.bigmodel.cn"},
		{"https://api.minimaxi.com/anthropic", "https://api.minimaxi.com"},
		{"https://api.z.ai", "https://api.z.ai"},
		{"not-a-url", "not-a-url"}, // parse succeeds but host empty → fallback
	}
	for _, c := range cases {
		if got := hostOnly(c.in); got != c.want {
			t.Errorf("hostOnly(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDetect_UnknownProvider(t *testing.T) {
	t.Parallel()
	p := &config.Provider{Name: "openai", URL: "https://api.openai.com", Keys: []string{"k"}}
	if f := Detect(p); f != nil {
		t.Errorf("Detect(openai) = %T, want nil", f)
	}
}

func TestDetect_NoKey(t *testing.T) {
	t.Parallel()
	p := &config.Provider{Name: "zhipu", URL: "https://open.bigmodel.cn", Keys: []string{}}
	if f := Detect(p); f != nil {
		t.Errorf("Detect(no key) = %T, want nil", f)
	}
}

func TestDetect_NilProvider(t *testing.T) {
	t.Parallel()
	if f := Detect(nil); f != nil {
		t.Errorf("Detect(nil) = %T, want nil", f)
	}
}

func TestClamp100(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input, want int
	}{
		{-1, 0}, {0, 0}, {50, 50}, {100, 100}, {101, 100}, {200, 100},
	}
	for _, tc := range tests {
		if got := clamp100(tc.input); got != tc.want {
			t.Errorf("clamp100(%d) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	if got := truncate([]byte("short"), 10); got != "short" {
		t.Errorf("truncate short = %q, want %q", got, "short")
	}
	got := truncate([]byte("abcdefghij"), 5)
	if got != "abcde..." {
		t.Errorf("truncate long = %q, want %q", got, "abcde...")
	}
}

func TestNewMinimaxFetcher_DefaultsAndTrims(t *testing.T) {
	t.Parallel()
	f := NewMinimaxFetcher("", "key")
	if f.BaseURL != "https://api.minimax.io" {
		t.Errorf("BaseURL = %q, want default", f.BaseURL)
	}
	if f.Client == nil {
		t.Error("Client is nil")
	}

	f2 := NewMinimaxFetcher("https://api.minimax.io///", "key")
	if f2.BaseURL != "https://api.minimax.io" {
		t.Errorf("BaseURL = %q, want trailing slashes trimmed", f2.BaseURL)
	}
	if f.Client.Timeout != 10*time.Second {
		t.Errorf("Client.Timeout = %v, want 10s", f.Client.Timeout)
	}
}

func TestMinimaxFetcher_Fetch_HTTPError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	f := &MinimaxFetcher{BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
	_, err := f.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("Fetch HTTP error = %v, want HTTP 401", err)
	}
}

func TestMinimaxFetcher_Fetch_InvalidJSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	f := &MinimaxFetcher{BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
	_, err := f.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "parse JSON") {
		t.Fatalf("Fetch invalid JSON error = %v, want parse JSON", err)
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("Fetch invalid JSON error = %v, want it to wrap *json.SyntaxError", err)
	}
}

func TestMinimaxFetcher_Fetch_APIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"base_resp":{"status_code":1001,"status_msg":"invalid key"}}`))
	}))
	defer srv.Close()

	f := &MinimaxFetcher{BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
	_, err := f.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status=1001") {
		t.Fatalf("Fetch API error = %v, want status=1001", err)
	}
}

func TestMinimaxFetcher_Fetch_RequestError(t *testing.T) {
	t.Parallel()
	f := &MinimaxFetcher{BaseURL: "http://[::1", APIKey: "k", Client: &http.Client{}}
	_, err := f.Fetch(context.Background())
	if err == nil {
		t.Fatalf("Fetch malformed URL: got nil error, want request error")
	}
	if !strings.HasPrefix(err.Error(), "minimax quota: build request: ") {
		t.Errorf("Fetch malformed URL error = %q, want the \"minimax quota: build request: \" prefix", err.Error())
	}
	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Errorf("Fetch malformed URL error = %v, want it to wrap *url.Error", err)
	}
}

func TestZhipuFetcher_Fetch_InvalidJSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{bad json`))
	}))
	defer srv.Close()

	f := &ZhipuFetcher{BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
	_, err := f.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "parse JSON") {
		t.Fatalf("Fetch invalid JSON = %v, want parse JSON", err)
	}
	// The cause must stay unwrappable, not merely be printed into the text.
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Errorf("Fetch invalid JSON error = %v, want it to wrap *json.SyntaxError", err)
	}
}

// The read-body failure must be reported for minimax too: a truncated
// response would otherwise fall through as an empty quota record.
func TestMinimaxFetcher_Fetch_ReadBodyError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	f := &MinimaxFetcher{BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
	_, err := f.Fetch(context.Background())
	if err == nil {
		t.Fatal("Fetch with a short body: got nil error, want a read-body failure")
	}
	if !strings.HasPrefix(err.Error(), "minimax quota: read body: ") {
		t.Errorf("Fetch read-body error = %q, want the \"minimax quota: read body: \" prefix", err.Error())
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Fetch read-body error = %v, want it to wrap io.ErrUnexpectedEOF", err)
	}
}

func TestZhipuFetcher_Fetch_RequestError(t *testing.T) {
	t.Parallel()
	f := &ZhipuFetcher{BaseURL: "http://[::1", APIKey: "k", Client: &http.Client{}}
	_, err := f.Fetch(context.Background())
	if err == nil {
		t.Fatalf("Fetch malformed URL: got nil error, want request error")
	}
	if !strings.HasPrefix(err.Error(), "zhipu quota: build request: ") {
		t.Errorf("Fetch malformed URL error = %q, want the \"zhipu quota: build request: \" prefix", err.Error())
	}
	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Errorf("Fetch malformed URL error = %v, want it to wrap *url.Error", err)
	}
}

// A cancelled context must abort the in-flight request, not the request build.
func TestZhipuFetcher_Fetch_CancelledContext(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"success":true}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := &ZhipuFetcher{BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
	_, err := f.Fetch(ctx)
	if err == nil {
		t.Fatal("Fetch with a cancelled context: got nil error")
	}
	if !strings.HasPrefix(err.Error(), "zhipu quota: request: ") {
		t.Errorf("Fetch cancelled error = %q, want the \"zhipu quota: request: \" prefix", err.Error())
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Fetch cancelled error = %v, want it to wrap context.Canceled", err)
	}
}

func TestMinimaxFetcher_Fetch_CancelledContext(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"base_resp":{"status_code":0}}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := &MinimaxFetcher{BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
	_, err := f.Fetch(ctx)
	if err == nil {
		t.Fatal("Fetch with a cancelled context: got nil error")
	}
	if !strings.HasPrefix(err.Error(), "minimax quota: request: ") {
		t.Errorf("Fetch cancelled error = %q, want the \"minimax quota: request: \" prefix", err.Error())
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Fetch cancelled error = %v, want it to wrap context.Canceled", err)
	}
}

// The error body is truncated at 200 bytes so a huge HTML error page cannot
// flood the status bar; the boundary is exact.
func TestZhipuFetcher_Fetch_HTTPErrorBodyTruncation(t *testing.T) {
	t.Parallel()
	body200 := strings.Repeat("x", 200)

	cases := []struct {
		name string
		body string
		want string
	}{
		{"exactly 200", body200, "zhipu quota: HTTP 500: " + body200},
		{"201 truncated", body200 + "y", "zhipu quota: HTTP 500: " + body200 + "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			f := &ZhipuFetcher{BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
			_, err := f.Fetch(context.Background())
			if err == nil {
				t.Fatal("Fetch HTTP 500: got nil error")
			}
			if err.Error() != tc.want {
				t.Errorf("error = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

func TestMinimaxFetcher_Fetch_HTTPErrorBodyTruncation(t *testing.T) {
	t.Parallel()
	body200 := strings.Repeat("z", 200)

	cases := []struct {
		name string
		body string
		want string
	}{
		{"exactly 200", body200, "minimax quota: HTTP 500: " + body200},
		{"201 truncated", body200 + "y", "minimax quota: HTTP 500: " + body200 + "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			f := &MinimaxFetcher{BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
			_, err := f.Fetch(context.Background())
			if err == nil {
				t.Fatal("Fetch HTTP 500: got nil error")
			}
			if err.Error() != tc.want {
				t.Errorf("error = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

// The API-level guard rejects either failure shape on its own.
func TestZhipuFetcher_Fetch_APIFailureShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want string
	}{
		{"success false", `{"code":200,"success":false,"msg":"nope"}`, `zhipu quota: API returned code=200 success=false msg="nope"`},
		{"code not 200", `{"code":500,"success":true,"msg":"boom"}`, `zhipu quota: API returned code=500 success=true msg="boom"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			f := &ZhipuFetcher{BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
			_, err := f.Fetch(context.Background())
			if err == nil {
				t.Fatal("got nil error, want the API failure")
			}
			if err.Error() != tc.want {
				t.Errorf("error = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

// The 5h window is identified by all three discriminators together: every
// wrong combination appears in the response before the exact match, so any
// dropped or shifted condition selects a different entry.
func TestZhipuFetcher_Fetch_ExactWindowMatch(t *testing.T) {
	t.Parallel()
	const resetMs = 1800000000000
	body := mustJSON(t, map[string]any{
		"code":    200,
		"success": true,
		"data": map[string]any{
			"limits": []map[string]any{
				{"type": "TOKENS_LIMIT", "unit": 6, "number": 5, "percentage": 40},
				{"type": "TIME_LIMIT", "unit": 3, "number": 5, "percentage": 45},
				{"type": "TOKENS_LIMIT", "unit": 3, "number": 7, "percentage": 50},
				{"type": "TOKENS_LIMIT", "unit": 3, "number": 5, "percentage": 15, "nextResetTime": resetMs},
			},
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	f := &ZhipuFetcher{BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
	info, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if info.Used != 15 {
		t.Errorf("Used = %d, want 15 (the unit=3 number=5 TOKENS_LIMIT entry)", info.Used)
	}
	if !info.ResetAt.Equal(time.UnixMilli(resetMs)) {
		t.Errorf("ResetAt = %v, want %v", info.ResetAt, time.UnixMilli(resetMs))
	}
}

// If the response repeats the 5h entry, the first one wins: the scan stops at
// the first match rather than at the last.
func TestZhipuFetcher_Fetch_FirstExactMatchWins(t *testing.T) {
	t.Parallel()
	body := mustJSON(t, map[string]any{
		"code":    200,
		"success": true,
		"data": map[string]any{
			"limits": []map[string]any{
				{"type": "TOKENS_LIMIT", "unit": 3, "number": 5, "percentage": 15},
				{"type": "TOKENS_LIMIT", "unit": 3, "number": 5, "percentage": 99},
			},
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	f := &ZhipuFetcher{BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
	info, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if info.Used != 15 {
		t.Errorf("Used = %d, want 15 (first matching entry, not the last)", info.Used)
	}
}

// With no exact 5h entry the fallback takes the FIRST TOKENS_LIMIT entry.
func TestZhipuFetcher_Fetch_FallbackTakesFirstTokensLimit(t *testing.T) {
	t.Parallel()
	body := mustJSON(t, map[string]any{
		"code":    200,
		"success": true,
		"data": map[string]any{
			"limits": []map[string]any{
				{"type": "TOKENS_LIMIT", "unit": 6, "number": 7, "percentage": 70},
				{"type": "TOKENS_LIMIT", "unit": 3, "number": 9, "percentage": 80},
			},
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	f := &ZhipuFetcher{BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
	info, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if info.Used != 70 {
		t.Errorf("Used = %d, want 70 (first TOKENS_LIMIT entry)", info.Used)
	}
}

// Detect matches on provider name OR host; each of the four name/host
// conditions must be able to fire on its own.
func TestDetect_NameOrHostAlone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"MiniMax", "https://example.com", "*quota.MinimaxFetcher"},
		{"x", "https://minimax.example.com", "*quota.MinimaxFetcher"},
		{"zhipu", "https://example.com", "*quota.ZhipuFetcher"},
		{"x", "https://zhipu.example.com", "*quota.ZhipuFetcher"},
		{"z.ai", "https://example.com", "*quota.ZhipuFetcher"},
		{"x", "https://api.z.ai/v1", "*quota.ZhipuFetcher"},
	}
	for _, tc := range cases {
		p := &config.Provider{Name: tc.name, URL: tc.url, Keys: []string{"k"}}
		if got := fmt.Sprintf("%T", Detect(p)); got != tc.want {
			t.Errorf("Detect(name=%q url=%q) = %s, want %s", tc.name, tc.url, got, tc.want)
		}
	}
}

func TestZhipuFetcher_Fetch_ReadBodyError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	f := &ZhipuFetcher{BaseURL: srv.URL, APIKey: "k", Client: srv.Client()}
	_, err := f.Fetch(context.Background())
	if err == nil {
		t.Fatal("Fetch with a short body: got nil error, want a read-body failure")
	}
	if !strings.HasPrefix(err.Error(), "zhipu quota: read body: ") {
		t.Errorf("Fetch read-body error = %q, want the \"zhipu quota: read body: \" prefix", err.Error())
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Fetch read-body error = %v, want it to wrap io.ErrUnexpectedEOF", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
