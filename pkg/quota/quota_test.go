package quota

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
}

func TestMinimaxFetcher_Fetch_OK(t *testing.T) {
	t.Parallel()
	const endMs = 1800000000000 // 2027-01-15T08:00:00Z
	body := mustJSON(t, map[string]any{
		"base_resp": map[string]any{
			"status_code": 0,
			"status_msg":  "",
		},
		"model_name":                         "abab",
		"start_time":                         1799989200000,
		"end_time":                           endMs,
		"current_interval_remaining_percent": 85,
		"current_interval_status":            1,
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/token_plan/remains" {
			t.Errorf("path = %q, want /v1/token_plan/remains", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer mmxkey" {
			t.Errorf("Authorization = %q, want 'Bearer mmxkey'", got)
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
		"end_time":  1800000000000,
		// 105% remaining is impossible — Used should clamp to 0.
		"current_interval_remaining_percent": 105,
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

func TestDetect_ZhipuByName(t *testing.T) {
	t.Parallel()
	p := &config.Provider{Name: "zhipu", URL: "https://open.bigmodel.cn/api/coding/paas/v4", Keys: []string{"k"}}
	f := Detect(p)
	if _, ok := f.(*ZhipuFetcher); !ok {
		t.Errorf("Detect(zhipu) = %T, want *ZhipuFetcher", f)
	}
	if zf, ok := f.(*ZhipuFetcher); ok {
		if want := "https://open.bigmodel.cn"; zf.BaseURL != want {
			t.Errorf("BaseURL = %q, want %q (host only, no path)", zf.BaseURL, want)
		}
	}
}

func TestDetect_ZhipuByZaiURL(t *testing.T) {
	t.Parallel()
	p := &config.Provider{Name: "intl", URL: "https://api.z.ai/v1/messages", Keys: []string{"k"}}
	f := Detect(p)
	if _, ok := f.(*ZhipuFetcher); !ok {
		t.Errorf("Detect(z.ai url) = %T, want *ZhipuFetcher", f)
	}
}

func TestDetect_ZhipuByGLMName(t *testing.T) {
	t.Parallel()
	p := &config.Provider{Name: "glm-official", URL: "https://x.com", Keys: []string{"k"}}
	f := Detect(p)
	if _, ok := f.(*ZhipuFetcher); !ok {
		t.Errorf("Detect(glm name) = %T, want *ZhipuFetcher", f)
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

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
