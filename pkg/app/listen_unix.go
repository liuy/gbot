//go:build !windows

package app

import (
	"context"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// listenTCPReuseport binds with SO_REUSEPORT so a supervised restart's
// replacement daemon can bind the same address while this process is still
// draining: the kernel load-balances accepts across both listeners during
// the overlap, and the port never goes unlistened. Both sockets must set
// the option — which is why every supervised bind path uses this helper.
func listenTCPReuseport(addr string) (net.Listener, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var sockErr error
			err := c.Control(func(fd uintptr) {
				sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			})
			if err != nil {
				return err
			}
			return sockErr
		},
	}
	return lc.Listen(context.Background(), "tcp", addr)
}
