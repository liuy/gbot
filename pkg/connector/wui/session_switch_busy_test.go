package wui

import (
	"encoding/json"
	"strings"
	"testing"
)

// drainErrorText pops one frame from wsCh and returns its error message text
// ("" if the queue is empty or the frame is not an error frame).
func drainErrorText(t *testing.T, c *WUIConnector) string {
	t.Helper()
	select {
	case f := <-c.wsCh:
		var m struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(f.data, &m) != nil {
			return ""
		}
		if m.Type != "error" {
			return ""
		}
		return m.Message
	default:
		return ""
	}
}

// busyConnector builds a connector whose active engine reports busy.
func busyConnector(t *testing.T) (*WUIConnector, *mockEngine) {
	t.Helper()
	mock := &mockEngine{
		isBusyFn: func() bool { return true },
	}
	c := &WUIConnector{
		slots:    make(map[string]*engineSlot),
		wsCh:     make(chan wsMsg, 16),
		done:     make(chan struct{}),
		testMock: mock,
	}
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
		wsCh:     make(chan wsMsg, 16),
		done:     make(chan struct{}),
		testMock: mock,
	}
	mainID := "main"
	c.active.Store(&mainID)
	c.slots["main"] = &engineSlot{engine: mock}

	c.handleSessionSwitch("other-session")
	if calls := mock.switchSessionCalls; len(calls) != 1 || calls[0] != "other-session" {
		t.Errorf("SwitchSession calls = %v, want exactly [other-session] (idle engines must not be blocked)", calls)
	}
}
