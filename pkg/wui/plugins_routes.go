package wui

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/liuy/gbot/pkg/plugins"
	"github.com/liuy/gbot/pkg/transport"
)

// pluginCard describes one plugin that provides a live pipe — the payload
// of GET /api/plugins and the wui sidebar's opt-in section.
type pluginCard struct {
	Name    string `json:"name"`
	UI      string `json:"ui"`
	Running bool   `json:"running"`
}

// runningState is the seam route tests use to fake transport presence.
var runningState = func(hub *transport.Hub, plugin string) bool {
	return hub != nil && hub.Connected(plugin)
}

// discoverPluginsFn is the seam route tests use to point discovery at a
// fixture tree without touching the global plugins dir.
var discoverPluginsFn = plugins.DiscoverPlugins

// RegisterPluginRoutes mounts the plugin live-card endpoints:
//
//	GET /api/plugins          → [{name, ui, running}]
//	GET /plugins/{name}/ui    → the plugin's declared UI entry module (same-origin)
//
// Both re-scan the plugin dir per request so a manual plugin reload needs no
// daemon restart. Only plugins declaring service processes are listed; the
// running bit comes from the transport hub (process up AND wired).
func RegisterPluginRoutes(mux *http.ServeMux, hub *transport.Hub) {
	mux.HandleFunc("GET /api/plugins", servePluginList(hub))
	mux.HandleFunc("GET /plugins/{name}/ui", servePluginUI)
}

func servePluginList(hub *transport.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cards, err := discoverPluginCards(hub)
		if err != nil {
			http.Error(w, "plugin scan failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(cards); err != nil {
			slog.Warn("wui: plugin list encode", "error", err)
		}
	}
}

// uiRoutePath is the stable URL the wui host imports for every plugin; the
// manifest's relative "ui" path never leaks into URLs.
const uiRoutePath = "/ui"

func servePluginUI(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// {name} is a single path segment by mux pattern, and the resolved
	// plugin must exist — together that closes path traversal on the URL
	// side. The manifest's ui path is additionally confined to the plugin
	// root below.
	plugin, ok := findPluginByName(name)
	if !ok || plugin.Manifest == nil || plugin.Manifest.UI == "" {
		http.NotFound(w, r)
		return
	}
	file := resolveUIFile(plugin.RootPath, plugin.Manifest.UI)
	if file == "" {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if _, err := w.Write(data); err != nil {
		slog.Warn("wui: plugin ui write", "plugin", name, "error", err)
	}
}

// resolveUIFile maps the manifest's declared ui path to a file inside the
// plugin root; anything escaping the root (absolute or ../) is rejected.
func resolveUIFile(root, ui string) string {
	if filepath.IsAbs(ui) {
		return ""
	}
	file := filepath.Clean(filepath.Join(root, ui))
	rel, err := filepath.Rel(root, file)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ""
	}
	return file
}

func discoverPluginCards(hub *transport.Hub) ([]pluginCard, error) {
	list, err := discoverPluginsFn()
	if err != nil {
		return nil, err
	}
	var cards []pluginCard
	for _, p := range list {
		if p.Manifest == nil || p.Manifest.Protocol == "" {
			continue
		}
		cards = append(cards, pluginCard{
			Name:    p.Name,
			UI:      "/plugins/" + p.Name + uiRoutePath,
			Running: runningState(hub, p.Name),
		})
	}
	return cards, nil
}

func findPluginByName(name string) (*plugins.ResolvedPlugin, bool) {
	if name == "" || name != filepath.Base(name) {
		return nil, false
	}
	list, err := discoverPluginsFn()
	if err != nil {
		return nil, false
	}
	for _, p := range list {
		if p.Name == name {
			return p, true
		}
	}
	return nil, false
}
