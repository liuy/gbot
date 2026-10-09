package reload

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/plugins"
)

// ---------------------------------------------------------------------------
// settings.json JSON tree diff
// ---------------------------------------------------------------------------

// diffSettingsJSON produces "dotted.path: <old> → <new>" leaves for every
// changed/added/removed value, sorted by path, capped at 5 leaves plus a
// "+N more" marker. Values render as compact JSON (strings quoted, numbers
// bare); absent sides render as <none>.
func diffSettingsJSON(oldRaw, newRaw []byte) []string {
	var oldV, newV any
	if len(oldRaw) > 0 {
		if err := json.Unmarshal(oldRaw, &oldV); err != nil {
			oldV = nil
		}
	}
	if len(newRaw) > 0 {
		if err := json.Unmarshal(newRaw, &newV); err != nil {
			newV = nil
		}
	}
	var leaves []string
	walkJSON("", oldV, newV, &leaves)
	sort.Strings(leaves)
	const cap = 5
	if len(leaves) > cap {
		extra := len(leaves) - cap
		return append(leaves[:cap], fmt.Sprintf("+%d more", extra))
	}
	return leaves
}

func walkJSON(path string, oldV, newV any, leaves *[]string) {
	if jsonLeafEqual(oldV, newV) {
		return
	}
	oldMap, oldIsMap := oldV.(map[string]any)
	newMap, newIsMap := newV.(map[string]any)
	if oldIsMap || newIsMap {
		keys := mapKeys(oldMap)
		keys = append(keys, mapKeys(newMap)...)
		sort.Strings(keys)
		seen := map[string]bool{}
		for _, k := range keys {
			if seen[k] {
				continue
			}
			seen[k] = true
			child := k
			if path != "" {
				child = path + "." + k
			}
			walkJSON(child, oldMap[k], newMap[k], leaves)
		}
		return
	}
	oldArr, oldIsArr := oldV.([]any)
	newArr, newIsArr := newV.([]any)
	if oldIsArr || newIsArr {
		n := max(len(oldArr), len(newArr))
		for i := range n {
			var ov, nv any
			if i < len(oldArr) {
				ov = oldArr[i]
			}
			if i < len(newArr) {
				nv = newArr[i]
			}
			walkJSON(fmt.Sprintf("%s[%d]", path, i), ov, nv, leaves)
		}
		return
	}
	*leaves = append(*leaves, fmt.Sprintf("%s: %s → %s", path, leafJSON(oldV), leafJSON(newV)))
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func jsonLeafEqual(a, b any) bool {
	return leafJSON(a) == leafJSON(b) && a != nil == (b != nil)
}

func leafJSON(v any) string {
	if v == nil {
		return "<none>"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Plugin surface diffs
// ---------------------------------------------------------------------------

// diffMcpServers counts added/removed/changed servers between two pieces'
// server maps. old may be nil (every server counts as added).
func diffMcpServers(old, new *plugins.PluginPieces) (added, removed, changed int) {
	var oldServers map[string]mcp.ScopedMcpServerConfig
	if old != nil {
		oldServers = old.McpServers
	}
	newServers := map[string]mcp.ScopedMcpServerConfig{}
	if new != nil {
		newServers = new.McpServers
	}
	for name := range newServers {
		ov, existed := oldServers[name]
		if !existed {
			added++
		} else if !mcp.AreMcpConfigsEqual(ov, newServers[name]) {
			changed++
		}
	}
	for name := range oldServers {
		if _, still := newServers[name]; !still {
			removed++
		}
	}
	return added, removed, changed
}

// fingerprinters hash a plugin surface into a comparable string; "new" wins
// whenever its fingerprint differs from the baseline's.
type pieceFingerprint func(p *plugins.PluginPieces) string

func skillsFingerprint(p *plugins.PluginPieces) string {
	if p == nil {
		return ""
	}
	parts := make([]string, 0, len(p.Skills))
	for _, s := range p.Skills {
		parts = append(parts, s.Name+"\x00"+s.Description+"\x00"+fmt.Sprintf("%x", sha256.Sum256([]byte(s.Content))))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x01")
}

func agentsFingerprint(p *plugins.PluginPieces) string {
	if p == nil {
		return ""
	}
	parts := make([]string, 0, len(p.Agents))
	for _, a := range p.Agents {
		parts = append(parts, a.AgentType+"\x00"+a.SystemPrompt()+"\x00"+a.Model)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x01")
}

func replsFingerprint(p *plugins.PluginPieces) string {
	if p == nil {
		return ""
	}
	parts := make([]string, 0, len(p.ReplScripts))
	for _, rs := range p.ReplScripts {
		parts = append(parts, rs.Name+"\x00"+fmt.Sprintf("%x", sha256.Sum256([]byte(rs.Source))))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x01")
}

// fingerprintStatus compares one surface's fingerprint against the baseline
// piece. A nil baseline piece (new plugin) reports reloaded.
func fingerprintStatus(fp pieceFingerprint, old, new *plugins.PluginPieces) FileStatus {
	if old == nil || fp(old) != fp(new) {
		return StatusReloaded
	}
	return StatusUnchanged
}

// ---------------------------------------------------------------------------
// Project .mcp.json validation mirroring pkg/mcp's serverDroppingErrors
// (unexported there): errors that caused servers to be DROPPED from the
// parsed set abort the reload; missing-env errors keep their servers and
// must not.
// ---------------------------------------------------------------------------

func droppedServerErrors(errs []mcp.ValidationError) []mcp.ValidationError {
	var dropped []mcp.ValidationError
	for _, e := range errs {
		if !strings.HasPrefix(e.Message, "Missing environment variables") {
			dropped = append(dropped, e)
		}
	}
	return dropped
}
