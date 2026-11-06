package llm

import (
	"testing"
	"time"
)

// Status 1 is the smallest status that must render with the "API Error N:"
// prefix; status 0 must not.
func TestAPIErrorErrorStatusBoundary(t *testing.T) {
	t.Parallel()
	if got := (&APIError{Status: 1, Message: "m"}).Error(); got != "API Error 1: m" {
		t.Errorf("Status 1 renders %q, want %q", got, "API Error 1: m")
	}
	if got := (&APIError{Message: "m"}).Error(); got != "m" {
		t.Errorf("Status 0 renders %q, want %q", got, "m")
	}
}

func TestIsServerErrorBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status int
		want   bool
	}{
		{499, false},
		{500, true},
		{501, true},
		{599, true},
		{600, false},
		{601, false},
	}
	for _, tc := range cases {
		if got := IsServerError(&APIError{Status: tc.status}); got != tc.want {
			t.Errorf("IsServerError(%d) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestIsRetryableStatusTable(t *testing.T) {
	t.Parallel()
	for _, s := range []int{529, 500, 502, 503, 504} {
		if !IsRetryableStatus(s) {
			t.Errorf("IsRetryableStatus(%d) = false, want true", s)
		}
	}
	for _, s := range []int{200, 400, 401, 429, 501, 505} {
		if IsRetryableStatus(s) {
			t.Errorf("IsRetryableStatus(%d) = true, want false", s)
		}
	}
}

func TestNewLLMHTTPClientKeepsTransport(t *testing.T) {
	t.Parallel()
	c := newLLMHTTPClient(DefaultHTTPTimeout)
	if c.Timeout != DefaultHTTPTimeout {
		t.Errorf("client.Timeout = %v, want %v", c.Timeout, DefaultHTTPTimeout)
	}
	if c.Transport == nil {
		t.Error("client.Transport = nil, want the shared LLM transport")
	}
}

// The shared transport tuning is frozen product behavior: sub-agent bursts
// need the raised per-host pool, and the timeouts bound stalls without
// churning healthy idle connections.
func TestNewLLMTransportConfig(t *testing.T) {
	t.Parallel()
	tr := newLLMTransport()
	if tr.MaxIdleConnsPerHost != 100 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 100", tr.MaxIdleConnsPerHost)
	}
	if tr.IdleConnTimeout != 5*time.Minute {
		t.Errorf("IdleConnTimeout = %v, want 5m", tr.IdleConnTimeout)
	}
	if tr.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want 10s", tr.TLSHandshakeTimeout)
	}
}

// Backoff grows as 2^attempt within [0.5,1.5]×jitter and never exceeds
// MaxBackoff. attempt=5 with 100ms base spans [1.6s,4.8s]; a mutated exponent
// lands past the 10s cap instead.
func TestCalculateBackoffExponent(t *testing.T) {
	t.Parallel()
	cfg := &RetryConfig{MaxRetries: 10, BaseBackoff: 100 * time.Millisecond, MaxBackoff: 10 * time.Second}
	for range 20 {
		d := CalculateBackoff(5, cfg)
		if d >= 10*time.Second {
			t.Fatalf("CalculateBackoff(5) = %v, want strictly below the 10s cap", d)
		}
	}
}

// The ±50% jitter must stay centered on the exponential: every sample lands
// in [0.5,1.5]×base and the upper half is reachable.
func TestCalculateBackoffJitterBounds(t *testing.T) {
	t.Parallel()
	cfg := &RetryConfig{MaxRetries: 10, BaseBackoff: time.Hour, MaxBackoff: 100 * time.Hour}
	sawAboveBase := false
	for range 50 {
		d := CalculateBackoff(0, cfg)
		if d < 30*time.Minute || d > 90*time.Minute {
			t.Fatalf("CalculateBackoff(0) = %v, want within [30m,90m] (base 1h ±50%%)", d)
		}
		if d > time.Hour {
			sawAboveBase = true
		}
	}
	if !sawAboveBase {
		t.Error("50 samples never exceeded the 1h base — jitter is not centered")
	}
}

// retryAfter=0 must defer to the jittered backoff, a dominating retryAfter
// must win verbatim.
func TestCalculateBackoffWithRetryAfter(t *testing.T) {
	t.Parallel()
	cfg := &RetryConfig{MaxRetries: 10, BaseBackoff: 100 * time.Millisecond, MaxBackoff: 10 * time.Second}
	for range 20 {
		d := CalculateBackoffWithRetryAfter(0, cfg, 0)
		if d < 50*time.Millisecond || d > 150*time.Millisecond {
			t.Fatalf("retryAfter=0 gave %v, want the 100ms±50%% backoff", d)
		}
	}
	if d := CalculateBackoffWithRetryAfter(0, cfg, 2*time.Hour); d != 2*time.Hour {
		t.Errorf("retryAfter=2h gave %v, want 2h verbatim", d)
	}
	// A tiny positive retryAfter defers to the (larger) jittered backoff.
	if d := CalculateBackoffWithRetryAfter(0, cfg, time.Nanosecond); d < 50*time.Millisecond || d > 150*time.Millisecond {
		t.Errorf("retryAfter=1ns gave %v, want the 100ms±50%% backoff", d)
	}
}

func TestDefaultRetryConfigValues(t *testing.T) {
	t.Parallel()
	cfg := DefaultRetryConfig()
	if cfg.MaxRetries != 10 || cfg.BaseBackoff != 500*time.Millisecond || cfg.MaxBackoff != 32*time.Second {
		t.Errorf("DefaultRetryConfig = %+v, want {10, 500ms, 32s}", cfg)
	}
	if DefaultHTTPTimeout != 45*time.Minute {
		t.Errorf("DefaultHTTPTimeout = %v, want 45m", DefaultHTTPTimeout)
	}
	if DefaultSSETimeout != 90*time.Second {
		t.Errorf("DefaultSSETimeout = %v, want 90s", DefaultSSETimeout)
	}
}
