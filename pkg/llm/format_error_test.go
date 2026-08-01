package llm

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
)

func TestFormatLLMError_APIError(t *testing.T) {
	apiErr := &APIError{Status: 429, Message: "Rate limit exceeded"}
	got := FormatLLMError(apiErr)
	want := "API Error 429: Rate limit exceeded"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatLLMError_NetworkTimeout(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	_ = ln.Close()
	_, err := net.Dial("tcp", ln.Addr().String())
	err = fmt.Errorf("send request: Post \"https://api.example.com/v1/chat\": %w", err)
	got := FormatLLMError(err)
	if got == err.Error() {
		t.Errorf("expected formatted error, got raw: %q", got)
	}
	if !strings.HasPrefix(got, "Network Error:") {
		t.Errorf("expected 'Network Error:' prefix, got %q", got)
	}
}

func TestFormatLLMError_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := fmt.Errorf("send request: %w", ctx.Err())
	got := FormatLLMError(err)
	if got != "Request canceled" {
		t.Errorf("got %q, want %q", got, "Request canceled")
	}
}

func TestFormatLLMError_ConnectionAborted(t *testing.T) {
	err := fmt.Errorf("send request: Post \"https://open.bigmodel.cn/api/coding/paas/v4/chat/completions\": read tcp [::1]:49210->[::1]:443: read: software caused connection abort")
	got := FormatLLMError(err)
	want := "Network Error: Connection aborted"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatLLMError_NilError(t *testing.T) {
	got := FormatLLMError(nil)
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestFormatLLMError_NoSuchHost(t *testing.T) {
	err := fmt.Errorf("dial tcp: lookup open.bigmodel.cn: no such host")
	got := FormatLLMError(err)
	want := "Network Error: DNS resolution failed"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatLLMError_ConnectionReset(t *testing.T) {
	err := fmt.Errorf("read tcp 10.0.0.1:12345->10.0.0.2:443: read: connection reset by peer")
	got := FormatLLMError(err)
	want := "Network Error: Connection reset"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatLLMError_GenericError(t *testing.T) {
	err := fmt.Errorf("some unknown error")
	got := FormatLLMError(err)
	if got != "some unknown error" {
		t.Errorf("got %q, want raw passthrough %q", got, "some unknown error")
	}
}
