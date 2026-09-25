//go:build windows

package app

import "net"

// SO_REUSEPORT does not exist on Windows; supervised mode is an Android
// concept, so the plain bind is the correct (and only) behavior here.
func listenTCPReuseport(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
