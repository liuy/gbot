//go:build !windows

package app

import (
	"net"
	"testing"
)

// The supervised restart overlaps two daemons on one port: the second
// bind must succeed while the first listener is still open (SO_REUSEPORT
// on both sockets), and accepts must work on each independently.
func TestListenTCPReuseport_OverlapSucceeds(t *testing.T) {
	first, err := listenTCPReuseport("127.0.0.1:0")
	if err != nil {
		t.Fatalf("first bind: %v", err)
	}
	defer first.Close()
	addr := first.Addr().String()

	second, err := listenTCPReuseport(addr)
	if err != nil {
		t.Fatalf("second bind alongside live listener: %v", err)
	}
	defer second.Close()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial overlapped port: %v", err)
	}
	_ = conn.Close()
}
