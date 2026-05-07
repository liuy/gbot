// Package engine implements the core agentic loop for gbot.
//
// This file: ToolSearch state management and filtering logic.
// Source: utils/toolSearch.ts — extractDiscoveredToolNames, shouldEnableToolSearch
// Source: tools/ToolSearchTool/prompt.ts — isDeferredTool, formatDeferredToolLine
package engine

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ToolSearchToolName is the name of the ToolSearch tool.
// Source: tools/ToolSearchTool/constants.ts
const ToolSearchToolName = "ToolSearch"

// ToolSearch is activated whenever any deferred tools exist.

// toolSearchState tracks which deferred tools have been discovered via ToolSearch.
// When ToolSearch is active, only non-deferred tools + discovered deferred tools
// are sent to the API. Undiscovered deferred tools are listed by name in a
// synthetic user message prefix.
type toolSearchState struct {
	mu         sync.RWMutex
	discovered map[string]bool
}

// newToolSearchState creates a fresh tool search state with no discoveries.
func newToolSearchState() *toolSearchState {
	return &toolSearchState{
		discovered: make(map[string]bool),
	}
}

// DiscoverTools marks the named tools as discovered, making them available
// for inclusion in subsequent API requests.
func (s *toolSearchState) DiscoverTools(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		s.discovered[name] = true
	}
}

// IsDiscovered returns whether a deferred tool has been discovered via ToolSearch.
func (s *toolSearchState) IsDiscovered(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.discovered[name]
}

// DiscoveredNames returns all discovered tool names in sorted order.
func (s *toolSearchState) DiscoveredNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.discovered))
	for name := range s.discovered {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// FilterToolsForRequest partitions tools into active and deferred sets.
// Source: utils/toolSearch.ts — isToolSearchEnabled + extractDiscoveredToolNames
//
// Returns:
//   - activeTools: tools to include in the API request
//   - deferredNames: names of deferred tools not yet discovered (for announcement)
//   - activated: true if ToolSearch filtering is active (deferred >= threshold)
//
// Logic:
//  1. Count deferred tools using tool.IsDeferred(t)
//  2. If count == 0, return all tools (no filtering)
//  3. Otherwise, partition:
//     non-deferred + discovered deferred = active tools
//     undiscovered deferred = deferred names
//  4. The ToolSearch tool itself is always included in active tools
func FilterToolsForRequest(
	tools map[string]tool.Tool,
	state *toolSearchState,
	toolOrder []string,
) (activeTools []tool.Tool, deferredTools []tool.Tool, activated bool) {
	// Step 1: Count deferred tools.
	deferredCount := 0
	for _, t := range tools {
		if tool.IsDeferred(t) {
			deferredCount++
		}
	}

	// Step 2: No deferred tools, return all enabled tools.
	if deferredCount == 0 {
		for _, name := range toolOrder {
			t, ok := tools[name]
			if !ok || !t.IsEnabled() {
				continue
			}
			activeTools = append(activeTools, t)
		}
		return activeTools, nil, false
	}

	// Step 3: Partition into active and deferred.
	for _, name := range toolOrder {
		t, ok := tools[name]
		if !ok || !t.IsEnabled() {
			continue
		}

		if !tool.IsDeferred(t) {
			// Non-deferred tools are always active.
			activeTools = append(activeTools, t)
		} else if state.IsDiscovered(name) {
			// Discovered deferred tools are active.
			activeTools = append(activeTools, t)
		} else if name == ToolSearchToolName {
			// ToolSearch itself is always active (even though it may be in the map).
			activeTools = append(activeTools, t)
		} else {
			// Undiscovered deferred tools — include full tool for announcement with hints.
			deferredTools = append(deferredTools, t)
		}
	}

	return activeTools, deferredTools, true
}

// DeferredToolsAnnouncement generates a synthetic user message prefix
// listing deferred tools with their search hints. This informs the model which tools are
// available but need to be loaded via ToolSearch.
// Source: tools/ToolSearchTool/prompt.ts — formatDeferredToolLine
//
// Format:
//
//	<available-deferred-tools>
//	tool_name_1: short description
//	tool_name_2: short description
//	...
//	</available-deferred-tools>
func DeferredToolsAnnouncement(deferred []tool.Tool) string {
	if len(deferred) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<available-deferred-tools>\n")
	for _, t := range deferred {
		hint := tool.SearchHint(t)
		if hint == "" {
			// Fallback to Description when no SearchHint is available.
			if desc, err := t.Description(nil); err == nil && desc != "" {
				hint = desc
			}
		}
		if hint != "" {
			fmt.Fprintf(&sb, "%s: %s\n", t.Name(), hint)
		} else {
			sb.WriteString(t.Name())
			sb.WriteByte('\n')
		}
	}
	sb.WriteString("</available-deferred-tools>")
	return sb.String()
}

// ExtractDiscoveredToolNamesFromResult extracts tool names from a
// ToolSearch tool result. The result data is expected to be a JSON
// string containing tool definitions in a <functions> block, or a
// structured result with a Tools field.
//
// Source: utils/toolSearch.ts — extractDiscoveredToolNames
// Scans for tool_reference blocks and extracts tool_name fields.
func ExtractDiscoveredToolNamesFromResult(data any) []string {
	if data == nil {
		return nil
	}

	// Try structured extraction: data may be a string containing JSON
	// with a "tools" array or a "discovered" array.
	switch v := data.(type) {
	case string:
		return extractToolNamesFromString(v)
	case []byte:
		return extractToolNamesFromString(string(v))
	case map[string]any:
		return extractToolNamesFromMap(v)
	}

	// Try JSON marshal + re-parse as last resort.
	b, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) == nil {
		return extractToolNamesFromMap(m)
	}
	return extractToolNamesFromString(string(b))
}

// extractToolNamesFromString parses tool names from a result string.
// The ToolSearch tool returns a <functions> block with <function> elements,
// each containing a JSON object with a "name" field.
func extractToolNamesFromString(s string) []string {
	var names []string

	// Try JSON parse first (may be a JSON string wrapper).
	var raw string
	if json.Unmarshal([]byte(s), &raw) == nil {
		s = raw
	}

	// Try structured JSON first.
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) == nil {
		if found := extractToolNamesFromMap(m); len(found) > 0 {
			return found
		}
	}

	// Parse <function>{"name": "..."}</function> blocks.
	// Source: ToolSearchTool returns results in this format.
	for {
		start := strings.Index(s, `<function>`)
		if start < 0 {
			break
		}
		s = s[start+len("<function>"):]
		end := strings.Index(s, `</function>`)
		if end < 0 {
			break
		}
		jsonFragment := s[:end]
		s = s[end+len("</function>"):]

		var fn struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(jsonFragment), &fn) == nil && fn.Name != "" {
			names = append(names, fn.Name)
		}
	}

	return names
}

// extractToolNamesFromMap extracts tool names from a structured map result.
func extractToolNamesFromMap(m map[string]any) []string {
	var names []string

	// Check for "tools", "discovered", "names", or "matches" arrays.
	// "matches" is the field returned by ToolSearch's structured Output.
	for _, key := range []string{"tools", "discovered", "names", "matches"} {
		arr, ok := m[key].([]any)
		if !ok {
			continue
		}
		for _, item := range arr {
			switch v := item.(type) {
			case string:
				names = append(names, v)
			case map[string]any:
				if name, ok := v["name"].(string); ok && name != "" {
					names = append(names, name)
				}
			}
		}
		if len(names) > 0 {
			return names
		}
	}

	return nil
}

// RestoreToolSearchState restores the discovered state from message history.
// Scans compact boundary messages for preCompactDiscoveredTools and
// tool_result blocks for tool_reference entries.
//
// Source: utils/toolSearch.ts — extractDiscoveredToolNames
//
// This is called on session resume to rebuild the tool search state
// so previously discovered tools remain active without re-searching.
func RestoreToolSearchState(messages []types.Message, state *toolSearchState) {
	for _, msg := range messages {
		// Compact boundary carries pre-compact discovered set.
		// TS: msg.type === 'system' && msg.subtype === 'compact_boundary'
		// In our types.Message, compact boundaries are stored differently —
		// check for system messages with compact metadata in content.
		restoreFromCompactBoundary(msg, state)

		// Only user messages contain tool_result blocks.
		if msg.Role != types.RoleUser {
			continue
		}

		for _, block := range msg.Content {
			restoreFromToolResult(block, state)
		}
	}
}

// restoreFromCompactBoundary extracts discovered tools from compact boundary
// system messages. The compact boundary stores preCompactDiscoveredTools
// as a JSON field in the content.
func restoreFromCompactBoundary(msg types.Message, state *toolSearchState) {
	// Look for system messages that contain compactMetadata.
	// In gbot, compact boundaries are stored as system messages with
	// a JSON content block containing compactMetadata.
	if msg.Role != types.RoleSystem {
		return
	}
	for _, block := range msg.Content {
		if block.Type != types.ContentTypeText {
			continue
		}
		var content struct {
			Subtype         string   `json:"subtype"`
			DiscoveredTools []string `json:"preCompactDiscoveredTools"`
		}
		if json.Unmarshal([]byte(block.Text), &content) == nil &&
			content.Subtype == "compact_boundary" &&
			len(content.DiscoveredTools) > 0 {
			state.DiscoverTools(content.DiscoveredTools)
		}
	}
}

// restoreFromToolResult extracts discovered tool names from tool_result
// content blocks. ToolSearch results contain tool_reference entries
// with tool_name fields.
func restoreFromToolResult(block types.ContentBlock, state *toolSearchState) {
	if block.Type != types.ContentTypeToolResult {
		return
	}

	// Tool result content may be JSON string containing tool references.
	if len(block.Content) == 0 {
		return
	}

	// The content is typically a JSON-encoded string.
	var contentStr string
	if json.Unmarshal(block.Content, &contentStr) != nil {
		contentStr = string(block.Content)
	}

	// Parse <function> blocks from ToolSearch results.
	names := extractToolNamesFromString(contentStr)
	if len(names) > 0 {
		state.DiscoverTools(names)
	}
}

// ToolSearchActivationError is returned when the model tries to call
// a deferred tool that has not been discovered via ToolSearch.
type ToolSearchActivationError struct {
	ToolName string
}

func (e *ToolSearchActivationError) Error() string {
	return fmt.Sprintf(
		"Tool %s is deferred. Use %s first to discover its schema.",
		e.ToolName, ToolSearchToolName,
	)
}
