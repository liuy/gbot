package app

import (
	"log/slog"

	"github.com/liuy/gbot/pkg/plugins"
	"github.com/liuy/gbot/pkg/transport"
)

// wireTransport loads every plugin's protocol module into the daemon's
// embedded JS host and registers it with the transport hub. The hub is cheap
// when no plugin declares a protocol, so it always exists. Failures degrade
// per-plugin (logged, skipped) — the daemon and its regular tools run on.
func wireTransport(loaded *plugins.LoadedPlugins) (*transport.Hub, []*plugins.ProtocolRunner) {
	hub := transport.New()
	if loaded == nil {
		return hub, nil
	}
	var runners []*plugins.ProtocolRunner
	for _, mod := range loaded.Protocols {
		runner, err := plugins.LoadProtocol(mod, hub)
		if err != nil {
			slog.Warn("main: protocol module failed, plugin live view disabled", "error", err)
			continue
		}
		runners = append(runners, runner)
	}
	return hub, runners
}
