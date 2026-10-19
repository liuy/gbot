// Plugin protocol host: each plugin's protocol.js runs in the daemon inside
// its own goja event loop (same assembly as pkg/tool/repl's sessions). The
// host exposes a small API — dial, channel, log — and bridges everything
// between the JS module and the transport hub. Timers (setTimeout and
// friends) come from the event loop, not from this package.
//
// Threading rule: the goja runtime is single-threaded. Every callback that
// originates on a Go goroutine (websocket reads, hub frames) must enter the
// loop via loop.RunOnLoop — calling the runtime directly races the VM.
package plugins

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	_ "github.com/dop251/goja_nodejs/buffer"
	"github.com/dop251/goja_nodejs/eventloop"
	_ "github.com/dop251/goja_nodejs/process"
	"github.com/dop251/goja_nodejs/require"
	_ "github.com/dop251/goja_nodejs/url"
	_ "github.com/dop251/goja_nodejs/util"
	"github.com/gorilla/websocket"

	"github.com/liuy/gbot/pkg/transport"
)

// ProtocolRunner is one loaded protocol module: its event loop, its hub
// registration and every connection it dialed (tracked for shutdown).
type ProtocolRunner struct {
	Plugin string
	loop   *eventloop.EventLoop
	vm     *goja.Runtime
	hub    *transport.Hub
	handle *transport.InprocHandle

	// frameCB is the module's channel inbound callback. Written from inside
	// the loop (channel().onMessage), invoked from the hub's onFrame via
	// RunOnLoop — loop-serialized on both ends.
	frameCB func(string)

	mu    sync.Mutex
	conns map[*hostConn]struct{}
	dead  bool
}

// LoadProtocol reads and runs one protocol module against the hub. The
// module's top-level code executes immediately (it registers channel
// callbacks, may dial). A module error fails the load; the caller skips the
// plugin and the daemon runs on.
func LoadProtocol(mod ProtocolModule, hub *transport.Hub) (*ProtocolRunner, error) {
	src, err := readFileOrEmpty(mod.Path)
	if err != nil {
		return nil, fmt.Errorf("protocol %s: %w", mod.Plugin, err)
	}
	r := &ProtocolRunner{Plugin: mod.Plugin, hub: hub, conns: make(map[*hostConn]struct{})}

	var loadErr error
	r.loop = eventloop.NewEventLoop(eventloop.EnableConsole(false))
	// Run() executes the callback, drains immediate jobs, then STOPS the
	// loop — globals setup AND module evaluation both belong inside it
	// (RunOnLoop on a stopped loop silently drops the job).
	r.loop.Run(func(vm *goja.Runtime) {
		r.vm = vm
		require.NewRegistry().Enable(vm)
		loadErr = r.registerGlobals(vm)
		if loadErr == nil {
			_, loadErr = vm.RunString(src)
		}
	})
	if loadErr != nil {
		return nil, fmt.Errorf("protocol %s: %w", mod.Plugin, loadErr)
	}

	// The channel exists before the module runs so its top-level code can
	// grab it. Client frames arrive as JS calls via RunOnLoop.
	r.handle = hub.RegisterInproc(mod.Plugin, func(payload []byte) {
		r.loop.RunOnLoop(func(vm *goja.Runtime) {
			if r.frameCB != nil {
				r.frameCB(string(payload))
			}
		})
	}, func() {
		// Replaced or hub-closed: stop timers and conns so the loop can
		// wind down. StopNoWait because this fires on hub goroutines that
		// may hold the hub mutex — a blocking Stop could deadlock against
		// a queued job calling back into the hub.
		r.loop.StopNoWait()
		r.closeConns()
	})

	// Start() re-launches the loop in the background, running until Stop —
	// the module's callbacks and timers live here for the daemon's lifetime.
	r.loop.Start()
	slog.Info("plugins: protocol module loaded", "plugin", mod.Plugin)
	return r, nil
}

func readFileOrEmpty(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Close tears the runner down: hub registration first (clients see the pipe
// drop), then connections, then the loop and its timers.
func (r *ProtocolRunner) Close() {
	r.mu.Lock()
	r.dead = true
	r.mu.Unlock()
	if r.handle != nil {
		r.handle.Close()
	}
	r.closeConns()
	r.loop.Terminate()
}

func (r *ProtocolRunner) closeConns() {
	r.mu.Lock()
	set := r.conns
	r.conns = make(map[*hostConn]struct{})
	r.mu.Unlock()
	for c := range set {
		c.close()
	}
}

// registerGlobals builds the host API on the VM.
func (r *ProtocolRunner) registerGlobals(vm *goja.Runtime) error {
	hostObj := vm.NewObject()

	// log(...args)
	if err := hostObj.Set("log", func(call goja.FunctionCall) goja.Value {
		parts := make([]string, len(call.Arguments))
		for i, a := range call.Arguments {
			parts[i] = a.String()
		}
		slog.Info("protocol: "+strings.Join(parts, " "), "plugin", r.Plugin)
		return goja.Undefined()
	}); err != nil {
		return err
	}

	// dial(url) → Promise<Conn>. ws:// wss:// dial a WebSocket; tcp:// dials
	// raw TCP (frames are newline-delimited JSON strings).
	if err := hostObj.Set("dial", func(call goja.FunctionCall) goja.Value {
		url := call.Argument(0).String()
		promise, resolve, reject := vm.NewPromise()
		go func() {
			conn, err := r.dialGo(url)
			r.loop.RunOnLoop(func(vm *goja.Runtime) {
				if err != nil {
					_ = reject(vm.NewTypeError(err.Error()))
					return
				}
				_ = resolve(r.connObject(conn))
			})
		}()
		return vm.ToValue(promise)
	}); err != nil {
		return err
	}

	// fetch(url) → Promise<{status, body}>. Plain GET — protocol modules
	// need it for endpoint discovery (e.g. CDP's /json/version before the
	// WS URL is known).
	if err := hostObj.Set("fetch", func(call goja.FunctionCall) goja.Value {
		url := call.Argument(0).String()
		promise, resolve, reject := vm.NewPromise()
		go func() {
			resp, err := http.Get(url)
			if err != nil {
				r.loop.RunOnLoop(func(vm *goja.Runtime) {
					_ = reject(vm.NewTypeError(err.Error()))
				})
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if err != nil {
				r.loop.RunOnLoop(func(vm *goja.Runtime) {
					_ = reject(vm.NewTypeError(err.Error()))
				})
				return
			}
			status := resp.StatusCode
			text := string(body)
			r.loop.RunOnLoop(func(vm *goja.Runtime) {
				obj := vm.NewObject()
				_ = obj.Set("status", status)
				_ = obj.Set("body", text)
				_ = resolve(obj)
			})
		}()
		return vm.ToValue(promise)
	}); err != nil {
		return err
	}

	// channel(name?) → pipe object. The channel IS the plugin's single pipe
	// (name is a label only — the client opens it as openChannel('<plugin>'),
	// which must equal the plugin's manifest name).
	if err := hostObj.Set("channel", func(call goja.FunctionCall) goja.Value {
		name := r.Plugin
		if len(call.Arguments) > 0 && !goja.IsUndefined(call.Argument(0)) {
			name = call.Argument(0).String()
		}
		obj := vm.NewObject()
		_ = obj.Set("name", name)
		_ = obj.Set("send", func(call goja.FunctionCall) goja.Value {
			payload := call.Argument(0).String()
			r.handle.Deliver([]byte(payload))
			return goja.Undefined()
		})
		_ = obj.Set("onMessage", func(call goja.FunctionCall) goja.Value {
			if fn, ok := goja.AssertFunction(call.Argument(0)); ok {
				r.frameCB = func(s string) { _, _ = fn(goja.Undefined(), r.vm.ToValue(s)) }
			}
			return goja.Undefined()
		})
		return obj
	}); err != nil {
		return err
	}

	if err := vm.Set("host", hostObj); err != nil {
		return err
	}
	return nil
}

// dialGo performs the network dial off-loop.
func (r *ProtocolRunner) dialGo(url string) (*hostConn, error) {
	switch {
	case strings.HasPrefix(url, "ws://"), strings.HasPrefix(url, "wss://"):
		d := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
		ws, resp, err := d.Dial(url, nil)
		if err != nil {
			if resp != nil {
				return nil, fmt.Errorf("dial: %w (status %s)", err, resp.Status)
			}
			return nil, fmt.Errorf("dial: %w", err)
		}
		c := &hostConn{runner: r, ws: ws}
		r.trackConn(c)
		go c.pumpWS()
		return c, nil
	case strings.HasPrefix(url, "tcp://"):
		addr := strings.TrimPrefix(url, "tcp://")
		nc, err := net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			return nil, fmt.Errorf("dial: %w", err)
		}
		c := &hostConn{runner: r, tcp: nc}
		r.trackConn(c)
		go c.pumpTCP()
		return c, nil
	default:
		return nil, fmt.Errorf("dial: unsupported scheme in %q", url)
	}
}

func (r *ProtocolRunner) trackConn(c *hostConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead {
		// Runner closed between dial start and now — drop it right away.
		go c.close()
		return
	}
	r.conns[c] = struct{}{}
}

func (r *ProtocolRunner) untrackConn(c *hostConn) {
	r.mu.Lock()
	delete(r.conns, c)
	r.mu.Unlock()
}

// connObject builds the JS-side Conn: {send(text), close(), onMessage(cb), onClose(cb)}.
func (r *ProtocolRunner) connObject(c *hostConn) *goja.Object {
	obj := r.vm.NewObject()
	_ = obj.Set("send", func(call goja.FunctionCall) goja.Value {
		text := call.Argument(0).String()
		if err := c.sendText(text); err != nil {
			panic(r.vm.NewTypeError(err.Error()))
		}
		return goja.Undefined()
	})
	_ = obj.Set("close", func(call goja.FunctionCall) goja.Value {
		c.close()
		return goja.Undefined()
	})
	_ = obj.Set("onMessage", func(call goja.FunctionCall) goja.Value {
		if fn, ok := goja.AssertFunction(call.Argument(0)); ok {
			c.mu.Lock()
			c.onMessage = fn
			c.mu.Unlock()
		}
		return goja.Undefined()
	})
	_ = obj.Set("onClose", func(call goja.FunctionCall) goja.Value {
		if fn, ok := goja.AssertFunction(call.Argument(0)); ok {
			c.mu.Lock()
			alreadyClosed := c.closed
			if !alreadyClosed {
				c.onClose = fn
			}
			c.mu.Unlock()
			// The connection may have died between dial resolve and this
			// call (server drops right after handshake) — deliver the
			// event the module would have missed. We are on the loop, so
			// calling directly is safe.
			if alreadyClosed {
				_, _ = fn(goja.Undefined())
			}
		}
		return goja.Undefined()
	})
	return obj
}

// hostConn is one dialed connection. Frames from the network enter the loop
// via RunOnLoop; sends serialize under wmu.
type hostConn struct {
	runner *ProtocolRunner
	ws     *websocket.Conn
	tcp    net.Conn

	wmu  sync.Mutex
	once sync.Once

	// mu guards the JS callbacks: setters run on the loop, pumps read on
	// network goroutines.
	mu        sync.Mutex
	closed    bool
	onMessage goja.Callable
	onClose   goja.Callable
}

func (c *hostConn) sendText(text string) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.ws != nil {
		return c.ws.WriteMessage(websocket.TextMessage, []byte(text))
	}
	if c.tcp != nil {
		_, err := io.WriteString(c.tcp, text+"\n")
		return err
	}
	return fmt.Errorf("connection closed")
}

func (c *hostConn) close() {
	c.once.Do(func() {
		if c.ws != nil {
			_ = c.ws.Close()
		}
		if c.tcp != nil {
			_ = c.tcp.Close()
		}
		c.runner.untrackConn(c)
		c.mu.Lock()
		c.closed = true
		cb := c.onClose
		c.mu.Unlock()
		lo := c.runner.loop
		if lo != nil && cb != nil {
			lo.RunOnLoop(func(vm *goja.Runtime) { _, _ = cb(goja.Undefined()) })
		}
	})
}

// pumpWS reads websocket text frames into the loop. Binary frames are not
// spoken by any supported protocol (and CDP explicitly needs text) — they
// close the connection with an error.
func (c *hostConn) pumpWS() {
	for {
		mt, data, err := c.ws.ReadMessage()
		if err != nil {
			c.close()
			return
		}
		if mt != websocket.TextMessage {
			c.close()
			return
		}
		payload := string(data)
		c.mu.Lock()
		cb := c.onMessage
		c.mu.Unlock()
		c.runner.loop.RunOnLoop(func(vm *goja.Runtime) {
			if cb != nil {
				_, _ = cb(goja.Undefined(), vm.ToValue(payload))
			}
		})
	}
}

// pumpTCP reads newline-delimited lines.
func (c *hostConn) pumpTCP() {
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 2048)
	for {
		n, err := c.tcp.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			for {
				idx := indexOfByte(buf, '\n')
				if idx < 0 {
					break
				}
				line := string(buf[:idx])
				buf = buf[idx+1:]
				c.mu.Lock()
				cb := c.onMessage
				c.mu.Unlock()
				c.runner.loop.RunOnLoop(func(vm *goja.Runtime) {
					if cb != nil {
						_, _ = cb(goja.Undefined(), vm.ToValue(line))
					}
				})
			}
		}
		if err != nil {
			c.close()
			return
		}
	}
}

func indexOfByte(b []byte, v byte) int {
	for i, x := range b {
		if x == v {
			return i
		}
	}
	return -1
}

// frameCB lives on the runner (single field — one channel per plugin in v1).
// It is written from inside the loop (channel().onMessage) and read from the
// hub's onFrame goroutine via RunOnLoop, so plain access is loop-serialized.
