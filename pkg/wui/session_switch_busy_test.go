package wui

import (
	"encoding/json"
	"strings"
	"testing"
)

// drainErrorText pops the outbound queue and returns the first error
// frame's message text ("" if the queue is empty or no error frame).
func drainErrorText(t *testing.T, c *WUIConnector) string {
	t.Helper()
	frames := c.outQ.Load().take()
	if len(frames) == 0 {
		return ""
	}
	var m struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(frames[0].data, &m) != nil {
		return ""
	}
	if m.Type != "error" {
		return ""
	}
	return m.Message
}

// busyConnector builds a connector whose active engine reports busy.
func busyConnector(t *testing.T) (*WUIConnector, *mockEngine) {
	t.Helper()
	mock := &mockEngine{
		isBusyFn: func() bool { return true },
	}
	c := &WUIConnector{
		slots:    make(map[string]*engineSlot),
		done:     make(chan struct{}),
		testMock: mock,
	}
	c.outQ.Store(newOutQueue(16))
	mainID := "main"
	c.active.Store(&mainID)
	c.slots["main"] = &engineSlot{engine: mock}
	return c, mock
}

func TestHandleSessionSwitch_BusyRejected(t *testing.T) {
	c, mock := busyConnector(t)

	c.handleSessionSwitch("other-session")

	if calls := mock.switchSessionCalls; len(calls) != 0 {
		t.Errorf("SwitchSession called with %v during busy engine, want no switch", calls)
	}
	if got := drainErrorText(t, c); !strings.Contains(got, errBusySessionOp.Error()) {
		t.Errorf("error frame = %q, want %q", got, errBusySessionOp.Error())
	}
}

func TestHandleSessionNew_BusyRejected(t *testing.T) {
	c, mock := busyConnector(t)

	c.handleSessionNew()

	if mock.newSessionCalls != 0 {
		t.Errorf("NewSession called %d times during busy engine, want 0", mock.newSessionCalls)
	}
	if got := drainErrorText(t, c); !strings.Contains(got, errBusySessionOp.Error()) {
		t.Errorf("error frame = %q, want %q", got, errBusySessionOp.Error())
	}
}

func TestHandleSessionSwitch_IdleStillSwitches(t *testing.T) {
	mock := &mockEngine{isBusyFn: func() bool { return false }}
	c := &WUIConnector{
		slots:    make(map[string]*engineSlot),
		done:     make(chan struct{}),
		testMock: mock,
	}
	c.outQ.Store(newOutQueue(16))
	mainID := "main"
	c.active.Store(&mainID)
	c.slots["main"] = &engineSlot{engine: mock}

	c.handleSessionSwitch("other-session")
	if calls := mock.switchSessionCalls; len(calls) != 1 || calls[0] != "other-session" {
		t.Errorf("SwitchSession calls = %v, want exactly [other-session] (idle engines must not be blocked)", calls)
	}
}
