package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeFileSender records SendFile calls for assertion. err, when non-nil,
// is returned by SendFile so tests can exercise error propagation.
type fakeFileSender struct {
	calls    int
	lastPath string
	lastCap  string
	err      error
}

func (f *fakeFileSender) SendFile(_ context.Context, filePath, caption string) error {
	f.calls++
	f.lastPath = filePath
	f.lastCap = caption
	return f.err
}

func TestRegisterFileSender_RoutesBySource(t *testing.T) {
	t.Parallel()
	sWechat := &fakeFileSender{}
	sWui := &fakeFileSender{}
	eng := New(&Params{Model: "test"})
	t.Cleanup(eng.Close)
	eng.RegisterFileSender("wechat", sWechat)
	eng.RegisterFileSender("wui", sWui)

	err := eng.SendFile(WithSource(context.Background(), "wui"), "/tmp/a.png", "cap")
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if sWui.calls != 1 {
		t.Errorf("wui sender calls = %d, want 1", sWui.calls)
	}
	if sWui.lastPath != "/tmp/a.png" {
		t.Errorf("wui sender lastPath = %q, want /tmp/a.png", sWui.lastPath)
	}
	if sWui.lastCap != "cap" {
		t.Errorf("wui sender lastCap = %q, want cap", sWui.lastCap)
	}
	if sWechat.calls != 0 {
		t.Errorf("wechat sender calls = %d, want 0 (wrong source routed)", sWechat.calls)
	}
}

func TestSendFile_NoSource_Error(t *testing.T) {
	t.Parallel()
	s := &fakeFileSender{}
	eng := New(&Params{Model: "test"})
	t.Cleanup(eng.Close)
	eng.RegisterFileSender("wechat", s)

	err := eng.SendFile(context.Background(), "/tmp/a.png", "")
	if err == nil {
		t.Fatal("expected error for missing source, got nil")
	}
	if !strings.Contains(err.Error(), "no FileSender registered for source") {
		t.Errorf("error = %q, want 'no FileSender registered for source'", err.Error())
	}
	if !strings.Contains(err.Error(), `""`) {
		t.Errorf("error = %q, want empty source quoted", err.Error())
	}
	if s.calls != 0 {
		t.Errorf("sender calls = %d, want 0 (no source → no route)", s.calls)
	}
}

func TestSendFile_UnknownSource_Error(t *testing.T) {
	t.Parallel()
	s := &fakeFileSender{}
	eng := New(&Params{Model: "test"})
	t.Cleanup(eng.Close)
	eng.RegisterFileSender("wechat", s)

	err := eng.SendFile(WithSource(context.Background(), "telegram"), "/tmp/a.png", "")
	if err == nil {
		t.Fatal("expected error for unknown source, got nil")
	}
	if !strings.Contains(err.Error(), "telegram") {
		t.Errorf("error = %q, want 'telegram' in message", err.Error())
	}
	if s.calls != 0 {
		t.Errorf("sender calls = %d, want 0 (unknown source → no route)", s.calls)
	}
}

func TestSendFile_SenderErrorPropagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("cdn timeout")
	s := &fakeFileSender{err: sentinel}
	eng := New(&Params{Model: "test"})
	t.Cleanup(eng.Close)
	eng.RegisterFileSender("wechat", s)

	err := eng.SendFile(WithSource(context.Background(), "wechat"), "/tmp/a.png", "")
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel %v", err, sentinel)
	}
	if s.calls != 1 {
		t.Errorf("sender calls = %d, want 1", s.calls)
	}
}

func TestWithSource_SourceFromContext_RoundTrip(t *testing.T) {
	t.Parallel()
	if got := SourceFromContext(WithSource(context.Background(), "wechat")); got != "wechat" {
		t.Errorf("round-trip = %q, want wechat", got)
	}
	if got := SourceFromContext(context.Background()); got != "" {
		t.Errorf("bare ctx = %q, want empty", got)
	}
}
