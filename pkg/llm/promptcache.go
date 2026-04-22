// Package llm provides prompt cache break detection for Anthropic API.
//
// Source: services/api/promptCacheBreakDetection.ts (728 lines)
// Two-phase detection:
// 1. RecordPromptState (pre-call): compute hashes, detect changes
// 2. CheckResponseForCacheBreak (post-call): check cache token drops, log breaks
package llm

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"cmp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sergi/go-diff/diffmatchpatch"
)

const (
	// maxTrackedSources caps the number of tracked sources to prevent unbounded memory growth.
	// Source: promptCacheBreakDetection.ts:107
	maxTrackedSources = 10

	// minCacheMissTokens is the minimum absolute token drop required to trigger a cache break warning.
	// Source: promptCacheBreakDetection.ts:120
	minCacheMissTokens = 2000

	// cacheTTL5MinMS is the 5-minute TTL threshold in milliseconds.
	// Source: promptCacheBreakDetection.ts:125
	cacheTTL5MinMS = 5 * 60 * 1000

	// cacheTTL1HourMS is the 1-hour TTL threshold in milliseconds.
	// Source: promptCacheBreakDetection.ts:126
	cacheTTL1HourMS = 60 * 60 * 1000
)

// trackedSourcePrefixes lists query sources that are tracked for break detection.
// Source: promptCacheBreakDetection.ts:109-115
var trackedSourcePrefixes = []string{
	"repl_main_thread",
	"sdk",
	"agent:custom",
	"agent:default",
	"agent:builtin",
}

// Global state store (plain map + RWMutex for simplicity).
var (
	stateStore     map[string]*promptStateInternal
	muState        sync.RWMutex
	insertionOrder []string // deterministic insertion-order for eviction
)

func init() {
	stateStore = make(map[string]*promptStateInternal)
}

// ResetPromptCacheBreakDetection clears all tracking state.
// Source: promptCacheBreakDetection.ts:704-706
func ResetPromptCacheBreakDetection() {
	muState.Lock()
	defer muState.Unlock()
	stateStore = make(map[string]*promptStateInternal)
	insertionOrder = nil
}

// ResetMainThreadCacheBreakDetection clears only the main thread tracking state,
// preserving sub-agent state so concurrent agents aren't affected.
func ResetMainThreadCacheBreakDetection() {
	muState.Lock()
	defer muState.Unlock()
	delete(stateStore, "repl_main_thread")
	// Remove from insertionOrder
	filtered := insertionOrder[:0]
	for _, k := range insertionOrder {
		if k != "repl_main_thread" {
			filtered = append(filtered, k)
		}
	}
	insertionOrder = filtered
}

// djb2Hash computes a djb2 hash of the input string.
// Source: utils/hash.ts:7-13 — exact port.
func djb2Hash(s string) uint32 {
	var hash uint32 = 5381
	for i := 0; i < len(s); i++ {
		hash = ((hash << 5) + hash) + uint32(s[i]) // hash * 33 + c
	}
	return hash
}

// computeHash serializes data to JSON and returns its djb2 hash.
// Source: promptCacheBreakDetection.ts:170-179
// TS uses Bun.hash primary with djb2 fallback; Go only needs djb2.
func computeHash(data any) uint32 {
	b, err := json.Marshal(data)
	if err != nil {
		return 0
	}
	return djb2Hash(string(b))
}

// stripCacheControl removes cache_control keys from each block.
// Source: promptCacheBreakDetection.ts:160-168
func stripCacheControl(blocks []map[string]any) []map[string]any {
	result := make([]map[string]any, len(blocks))
	for i, block := range blocks {
		stripped := make(map[string]any, len(block))
		for k, v := range block {
			if k != "cache_control" {
				stripped[k] = v
			}
		}
		result[i] = stripped
	}
	return result
}

// sanitizeToolName collapses MCP tool names to 'mcp' to prevent path leakage.
// Source: promptCacheBreakDetection.ts:183-185
func sanitizeToolName(name string) string {
	if strings.HasPrefix(name, "mcp__") {
		return "mcp"
	}
	return name
}

// isExcludedModel skips break detection for haiku models.
// Source: promptCacheBreakDetection.ts:129-131
func isExcludedModel(model string) bool {
	return strings.Contains(model, "haiku")
}

// getTrackingKey returns the tracking key for a querySource, or "" if untracked.
// Source: promptCacheBreakDetection.ts:149-158
// "compact" shares the same server-side cache as "repl_main_thread".
func getTrackingKey(querySource, agentID string) string {
	if querySource == "compact" {
		return "repl_main_thread"
	}
	for _, prefix := range trackedSourcePrefixes {
		if strings.HasPrefix(querySource, prefix) {
			if agentID != "" {
				return agentID
			}
			return querySource
		}
	}
	return ""
}

// slicesEqual compares two string slices for equality (order-sensitive).
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sortCopy returns a sorted copy of a string slice.
func sortCopy(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	out := make([]string, len(ss))
	copy(out, ss)
	slices.Sort(out)
	return out
}

// computePerToolHashes computes per-tool schema hashes.
// Source: promptCacheBreakDetection.ts:187-196
func computePerToolHashes(strippedTools []map[string]any, names []string) map[string]uint32 {
	hashes := make(map[string]uint32, len(strippedTools))
	for i := range strippedTools {
		name := names[i]
		if name == "" {
			name = fmt.Sprintf("__idx_%d", i)
		}
		hashes[name] = computeHash(strippedTools[i])
	}
	return hashes
}

// buildDiffableContent builds a diffable string from system and tools.
// Source: promptCacheBreakDetection.ts:206-222
func buildDiffableContent(system []map[string]any, tools []map[string]any, model string) string {
	// Extract system text
	var systemTexts []string
	for _, block := range system {
		if text, ok := block["text"].(string); ok {
			systemTexts = append(systemTexts, text)
		}
	}
	systemText := strings.Join(systemTexts, "\n\n")

	// Build tool details sorted by name
	type toolDetail struct {
		name        string
		description string
		schema      string
	}
	var details []toolDetail
	for _, t := range tools {
		name, _ := t["name"].(string)
		if name == "" {
			name = "unknown"
		}
		desc, _ := t["description"].(string)
		var schema string
		if s, ok := t["input_schema"]; ok {
			b, _ := json.Marshal(s)
			schema = string(b)
		}
		details = append(details, toolDetail{name: name, description: desc, schema: schema})
	}
	slices.SortFunc(details, func(a, b toolDetail) int { return cmp.Compare(a.name, b.name) })

	var toolLines []string
	for _, d := range details {
		toolLines = append(toolLines, fmt.Sprintf("%s\n  description: %s\n  input_schema: %s", d.name, d.description, d.schema))
	}

	return fmt.Sprintf("Model: %s\n\n=== System Prompt ===\n\n%s\n\n=== Tools (%d) ===\n\n%s\n",
		model, systemText, len(tools), strings.Join(toolLines, "\n\n"))
}

// getCacheBreakDir returns the directory for cache break diff files.
func getCacheBreakDir() (string, error) {
	dir := filepath.Join(os.TempDir(), "gbot-cache-break")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// getCacheBreakDiffPath generates a random diff file path.
// Source: promptCacheBreakDetection.ts:19-26
func getCacheBreakDiffPath() (string, error) {
	dir, err := getCacheBreakDir()
	if err != nil {
		return "", err
	}
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random suffix: %w", err)
	}
	for i := range b {
		b[i] = chars[b[i]%byte(len(chars))]
	}
	return filepath.Join(dir, "cache-break-"+string(b)+".diff"), nil
}

// writeCacheBreakDiff writes a diff file and returns its path.
// Source: promptCacheBreakDetection.ts:708-727
func writeCacheBreakDiff(prevContent, newContent string) string {
	// Clean up old diff files before writing new one.
	cleanupOldDiffs()

	diffPath, err := getCacheBreakDiffPath()
	if err != nil {
		return ""
	}
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(prevContent, newContent, true)
	patch := dmp.PatchToText(dmp.PatchMake(prevContent, diffs))
	if err := os.WriteFile(diffPath, []byte(patch), 0600); err != nil {
		return ""
	}
	return diffPath
}

// cleanupOldDiffs removes diff files older than 24 hours.
func cleanupOldDiffs() {
	dir, err := getCacheBreakDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		info, _ := entry.Info()
		if info != nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// getSystemCharCount returns total character count of system block texts.
// Source: promptCacheBreakDetection.ts:198-204
func getSystemCharCount(system []map[string]any) int {
	total := 0
	for _, block := range system {
		if text, ok := block["text"].(string); ok {
			total += len(text)
		}
	}
	return total
}

// extractCacheControlHash computes hash of cache_control fields only from system blocks.
// Source: promptCacheBreakDetection.ts:279-281
func extractCacheControlHash(system []map[string]any) uint32 {
	var controls []any
	for _, block := range system {
		if cc, ok := block["cache_control"]; ok {
			controls = append(controls, cc)
		} else {
			controls = append(controls, nil)
		}
	}
	return computeHash(controls)
}

// RecordPromptState records the current prompt/tool state and detects changes.
// Phase 1 (pre-call). Does NOT fire events — stores pending changes for phase 2.
// Source: promptCacheBreakDetection.ts:247-430
func RecordPromptState(system []map[string]any, tools []map[string]any, key PromptStateKey, model string, betas []string, globalCacheStrategy string, fastMode bool, autoModeActive bool, isUsingOverage bool, cachedMCEnabled bool, effortValue string, extraBodyHash uint32) {

	trackKey := getTrackingKey(key.QuerySource, key.AgentID)
	if trackKey == "" {
		return
	}

	strippedSystem := stripCacheControl(system)
	strippedTools := stripCacheControl(tools)

	systemHash := computeHash(strippedSystem)
	toolsHash := computeHash(strippedTools)
	cacheControlHash := extractCacheControlHash(system)

	toolNames := make([]string, len(tools))
	for i, t := range tools {
		name, _ := t["name"].(string)
		if name == "" {
			name = "unknown"
		}
		toolNames[i] = name
	}

	systemCharCount := getSystemCharCount(system)
	sortedBetas := sortCopy(betas)

	// Defensive copy: closure captures slices that caller may mutate.
	systemCopy := make([]map[string]any, len(system))
	for i, m := range system {
		cp := make(map[string]any, len(m))
		maps.Copy(cp, m)
		systemCopy[i] = cp
	}
	toolsCopy := make([]map[string]any, len(tools))
	for i, m := range tools {
		cp := make(map[string]any, len(m))
		maps.Copy(cp, m)
		toolsCopy[i] = cp
	}
	lazyDiffableContent := func() string {
		return buildDiffableContent(systemCopy, toolsCopy, model)
	}

	// Compute per-tool hashes lazily
	computeToolHashes := func() map[string]uint32 {
		return computePerToolHashes(strippedTools, toolNames)
	}

	muState.Lock()
	defer muState.Unlock()

	prevVal, loaded := stateStore[trackKey]
	if !loaded {
		// Evict oldest entries if at capacity (before inserting new)
		// Source: TS lines 298-303
		for len(stateStore) >= maxTrackedSources {
			if len(insertionOrder) > 0 {
				oldest := insertionOrder[0]
				insertionOrder = insertionOrder[1:]
				delete(stateStore, oldest)

			} else {
				break
			}
		}

		stateStore[trackKey] = &promptStateInternal{
			SystemHash:           systemHash,
			ToolsHash:            toolsHash,
			CacheControlHash:     cacheControlHash,
			ToolNames:            toolNames,
			PerToolHashes:        computeToolHashes(),
			SystemCharCount:      systemCharCount,
			Model:                model,
			GlobalCacheStrategy:  globalCacheStrategy,
			Betas:                sortedBetas,
			FastMode:             fastMode,
			AutoModeActive:       autoModeActive,
			IsUsingOverage:       isUsingOverage,
			CachedMCEnabled:      cachedMCEnabled,
			EffortValue:          effortValue,
			ExtraBodyHash:        extraBodyHash,
			CallCount:            1,
			PrevCacheRead:        0, // 0 = null (first call)
			BuildDiffableContent: lazyDiffableContent,
		}
		insertionOrder = append(insertionOrder, trackKey)
		return
	}

	prev := prevVal
	prev.CallCount++

	// Compute change flags
	// Source: TS lines 332-346
	systemPromptChanged := systemHash != prev.SystemHash
	toolSchemasChanged := toolsHash != prev.ToolsHash
	modelChanged := model != prev.Model
	fastModeChanged := fastMode != prev.FastMode
	cacheControlChanged := cacheControlHash != prev.CacheControlHash
	globalCacheStrategyChanged := globalCacheStrategy != prev.GlobalCacheStrategy
	betasChanged := !slicesEqual(sortedBetas, prev.Betas)
	autoModeChanged := autoModeActive != prev.AutoModeActive
	overageChanged := isUsingOverage != prev.IsUsingOverage
	cachedMCChanged := cachedMCEnabled != prev.CachedMCEnabled
	effortChanged := effortValue != prev.EffortValue
	extraBodyChanged := extraBodyHash != prev.ExtraBodyHash

	if systemPromptChanged || toolSchemasChanged || modelChanged ||
		fastModeChanged || cacheControlChanged || globalCacheStrategyChanged ||
		betasChanged || autoModeChanged || overageChanged ||
		cachedMCChanged || effortChanged || extraBodyChanged {
		// Set-based derivation for tools
		// Source: TS lines 362-378
		prevToolSet := make(map[string]bool, len(prev.ToolNames))
		for _, n := range prev.ToolNames {
			prevToolSet[n] = true
		}
		newToolSet := make(map[string]bool, len(toolNames))
		for _, n := range toolNames {
			newToolSet[n] = true
		}

		var addedTools []string
		for _, n := range toolNames {
			if !prevToolSet[n] {
				addedTools = append(addedTools, n)
			}
		}
		var removedTools []string
		for _, n := range prev.ToolNames {
			if !newToolSet[n] {
				removedTools = append(removedTools, n)
			}
		}

		// Per-tool schema diff
		var changedToolSchemas []string
		if toolSchemasChanged {
			newHashes := computeToolHashes()
			for _, name := range toolNames {
				if !prevToolSet[name] {
					continue
				}
				if newHashes[name] != prev.PerToolHashes[name] {
					changedToolSchemas = append(changedToolSchemas, name)
				}
			}
			prev.PerToolHashes = newHashes
		}

		// Set-based derivation for betas
		// Source: TS lines 364-365, 402-403
		prevBetaSet := make(map[string]bool, len(prev.Betas))
		for _, b := range prev.Betas {
			prevBetaSet[b] = true
		}
		newBetaSet := make(map[string]bool, len(sortedBetas))
		for _, b := range sortedBetas {
			newBetaSet[b] = true
		}
		var addedBetas []string
		for _, b := range sortedBetas {
			if !prevBetaSet[b] {
				addedBetas = append(addedBetas, b)
			}
		}
		var removedBetas []string
		for _, b := range prev.Betas {
			if !newBetaSet[b] {
				removedBetas = append(removedBetas, b)
			}
		}

		// Capture OLD diffable content before replacing with new (TS:412-426)
		oldDiffableContent := prev.BuildDiffableContent
		prev.BuildDiffableContent = lazyDiffableContent
		prev.PendingChanges = &PendingChanges{
			SystemPromptChanged:        systemPromptChanged,
			ToolSchemasChanged:         toolSchemasChanged,
			ModelChanged:               modelChanged,
			CacheControlChanged:        cacheControlChanged,
			GlobalCacheStrategyChanged: globalCacheStrategyChanged,
			BetasChanged:               betasChanged,
			AutoModeActiveChanged:      autoModeChanged,
			OverageChanged:             overageChanged,
			CachedMCEnabledChanged:     cachedMCChanged,
			EffortChanged:              effortChanged,
			ExtraBodyChanged:           extraBodyChanged,
			FastModeChanged:            fastModeChanged,
			AddedToolCount:             len(addedTools),
			RemovedToolCount:           len(removedTools),
			AddedTools:                 addedTools,
			RemovedTools:               removedTools,
			ChangedToolSchemas:         changedToolSchemas,
			SystemCharDelta:            systemCharCount - prev.SystemCharCount,
			PreviousModel:              prev.Model,
			NewModel:                   model,
			PrevGlobalCacheStrategy:    prev.GlobalCacheStrategy,
			NewGlobalCacheStrategy:     globalCacheStrategy,
			PrevEffortValue:            prev.EffortValue,
			NewEffortValue:             effortValue,
			AddedBetas:                 addedBetas,
			RemovedBetas:               removedBetas,
			BuildPrevDiffableContent:   oldDiffableContent,
		}
	} else {
		prev.PendingChanges = nil
	}

	// Update prev state
	// Source: TS lines 412-426
	prev.SystemHash = systemHash
	prev.ToolsHash = toolsHash
	prev.CacheControlHash = cacheControlHash
	prev.ToolNames = toolNames
	prev.SystemCharCount = systemCharCount
	prev.Model = model
	prev.GlobalCacheStrategy = globalCacheStrategy
	prev.Betas = sortedBetas
	prev.FastMode = fastMode
	prev.AutoModeActive = autoModeActive
	prev.IsUsingOverage = isUsingOverage
	prev.CachedMCEnabled = cachedMCEnabled
	prev.EffortValue = effortValue
	prev.ExtraBodyHash = extraBodyHash
	prev.BuildDiffableContent = lazyDiffableContent
}

// CheckResponseForCacheBreak checks API response cache tokens for a break.
// Phase 2 (post-call).
// Source: promptCacheBreakDetection.ts:437-666
//
// Locking: fine-grained lock/unlock cycles are intentional. The function
// reads prev state under lock, does expensive computation (diff building,
// reason construction) outside the lock, then acquires briefly for updates.
// defer is NOT used because the lock must be released before computation
// and re-acquired for writes — a single deferred unlock would hold the
// lock through the entire computation, serializing all cache checks.
// Concurrency safety: RecordPromptState and CheckResponseForCacheBreak
// run sequentially per-request (pre-call / post-call), so concurrent
// mutation of the same prev struct is not expected.
func CheckResponseForCacheBreak(key PromptStateKey, cacheReadTokens, cacheCreationTokens int, messages []messageWithTimestamp) {

	trackKey := getTrackingKey(key.QuerySource, key.AgentID)
	if trackKey == "" {
		return
	}

	muState.Lock()
	prevVal, loaded := stateStore[trackKey]
	if !loaded {
		muState.Unlock()
		return
	}
	prev := prevVal

	// Skip excluded models
	// Source: TS lines 452-453
	if isExcludedModel(prev.Model) {
		muState.Unlock()
		return
	}

	prevCacheRead := prev.PrevCacheRead
	prev.PrevCacheRead = cacheReadTokens
	muState.Unlock()

	// Find last assistant message timestamp for TTL detection
	// Source: TS lines 460-463
	var timeSinceLastAssistantMsg int64 = -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].role == "assistant" {
			timeSinceLastAssistantMsg = time.Since(messages[i].timestamp).Milliseconds()
			break
		}
	}

	// Skip first call — no previous value to compare against
	// Source: TS line 466
	if prevCacheRead == 0 {
		return
	}

	changes := prev.PendingChanges

	// Cache deletions via cached microcompact are expected
	// Source: TS lines 473-481
	muState.Lock()
	if prev.CacheDeletionsPending {
		prev.CacheDeletionsPending = false
		prev.PendingChanges = nil
		muState.Unlock()
		slog.Info("prompt_cache:deletion_applied",
			"cacheReadPrev", prevCacheRead,
			"cacheReadCurr", cacheReadTokens,
		)
		return
	}
	muState.Unlock()

	// Detect cache break: cache read dropped >5% AND absolute drop exceeds minimum
	// Source: TS lines 485-492
	tokenDrop := prevCacheRead - cacheReadTokens
	if cacheReadTokens >= int(float64(prevCacheRead)*0.95) || tokenDrop < minCacheMissTokens {
		muState.Lock()
		prev.PendingChanges = nil
		muState.Unlock()
		return
	}

	// Build explanation from pending changes
	// Source: TS lines 494-563
	var parts []string
	if changes != nil {
		if changes.ModelChanged {
			parts = append(parts, fmt.Sprintf("model changed (%s → %s)", changes.PreviousModel, changes.NewModel))
		}
		if changes.SystemPromptChanged {
			charDelta := changes.SystemCharDelta
			var charInfo string
			if charDelta > 0 {
				charInfo = fmt.Sprintf(" (+%d chars)", charDelta)
			} else if charDelta < 0 {
				charInfo = fmt.Sprintf(" (%d chars)", charDelta)
			}
			parts = append(parts, "system prompt changed"+charInfo)
		}
		if changes.ToolSchemasChanged {
			var toolDiff string
			if changes.AddedToolCount > 0 || changes.RemovedToolCount > 0 {
				toolDiff = fmt.Sprintf(" (+%d/-%d tools)", changes.AddedToolCount, changes.RemovedToolCount)
			} else {
				toolDiff = " (tool prompt/schema changed, same tool set)"
			}
			parts = append(parts, "tools changed"+toolDiff)
		}
		if changes.CacheControlChanged && !changes.GlobalCacheStrategyChanged && !changes.SystemPromptChanged {
			parts = append(parts, "cache_control changed (scope or TTL)")
		}
		if changes.BetasChanged {
			var diffParts []string
			if len(changes.AddedBetas) > 0 {
				diffParts = append(diffParts, "+"+strings.Join(changes.AddedBetas, ","))
			}
			if len(changes.RemovedBetas) > 0 {
				diffParts = append(diffParts, "-"+strings.Join(changes.RemovedBetas, ","))
			}
			diff := strings.Join(diffParts, " ")
			suffix := ""
			if diff != "" {
				suffix = " (" + diff + ")"
			}
			parts = append(parts, "betas changed"+suffix)
		}
		if changes.GlobalCacheStrategyChanged {
			parts = append(parts, fmt.Sprintf("global cache strategy changed (%s → %s)",
				changes.PrevGlobalCacheStrategy, changes.NewGlobalCacheStrategy))
		}
		// Source: TS lines 519-562
		if changes.FastModeChanged {
			parts = append(parts, "fast mode toggled")
		}
		if changes.AutoModeActiveChanged {
			parts = append(parts, "auto mode toggled")
		}
		if changes.OverageChanged {
			parts = append(parts, "overage state changed (TTL latched, no flip)")
		}
		if changes.CachedMCEnabledChanged {
			parts = append(parts, "cached microcompact toggled")
		}
		if changes.EffortChanged {
			parts = append(parts, fmt.Sprintf("effort changed (%s → %s)",
				changes.PrevEffortValue, changes.NewEffortValue))
		}
		if changes.ExtraBodyChanged {
			parts = append(parts, "extra body params changed")
		}
	}

	// Check TTL expiration
	// Source: TS lines 565-588
	lastOver5min := timeSinceLastAssistantMsg > cacheTTL5MinMS
	lastOver1h := timeSinceLastAssistantMsg > cacheTTL1HourMS

	var reason string
	if len(parts) > 0 {
		reason = strings.Join(parts, ", ")
	} else if lastOver1h {
		reason = "possible 1h TTL expiry (prompt unchanged)"
	} else if lastOver5min {
		reason = "possible 5min TTL expiry (prompt unchanged)"
	} else if timeSinceLastAssistantMsg >= 0 {
		reason = "likely server-side (prompt unchanged, <5min gap)"
	} else {
		reason = "unknown cause"
	}

	// Write diff file
	// Source: TS lines 649-655
	var diffPath string
	if changes != nil && changes.BuildPrevDiffableContent != nil {
		prevContent := changes.BuildPrevDiffableContent()
		muState.Lock()
		currContent := prev.BuildDiffableContent()
		muState.Unlock()
		diffPath = writeCacheBreakDiff(prevContent, currContent)
	}

	// Structured slog log
	// Source: TS lines 590-644 (analytics) + 658-660 (debug log)
	slog.Warn("prompt_cache:break",
		"source", key.String(),
		"callCount", prev.CallCount,
		"reason", reason,
		"cacheReadPrev", prevCacheRead,
		"cacheReadCurr", cacheReadTokens,
		"tokenDrop", tokenDrop,
		"cacheCreation", cacheCreationTokens,
		"ttlExpiry", ttlExpiryLabel(timeSinceLastAssistantMsg),
		"systemChanged", changes != nil && changes.SystemPromptChanged,
		"toolsChanged", changes != nil && changes.ToolSchemasChanged,
		"modelChanged", changes != nil && changes.ModelChanged,
		"cacheControlChanged", changes != nil && changes.CacheControlChanged,
		"betasChanged", changes != nil && changes.BetasChanged,
		"fastModeChanged", changes != nil && changes.FastModeChanged,
		"autoModeChanged", changes != nil && changes.AutoModeActiveChanged,
		"overageChanged", changes != nil && changes.OverageChanged,
		"cachedMCChanged", changes != nil && changes.CachedMCEnabledChanged,
		"effortChanged", changes != nil && changes.EffortChanged,
		"extraBodyChanged", changes != nil && changes.ExtraBodyChanged,
		"diff", diffPath,
	)

	muState.Lock()
	prev.PendingChanges = nil
	muState.Unlock()
}

// messageWithTimestamp is a minimal message type for TTL detection.
type messageWithTimestamp struct {
	role      string
	timestamp time.Time
}

// ttlExpiryLabel returns a human-readable TTL expiry label.
func ttlExpiryLabel(timeSinceMs int64) string {
	if timeSinceMs < 0 {
		return ""
	}
	if timeSinceMs > cacheTTL1HourMS {
		return "1h"
	}
	if timeSinceMs > cacheTTL5MinMS {
		return "5min"
	}
	return "server-side"
}

// NotifyCacheDeletion marks that cached microcompact sent cache_edits deletions.
// The next API response will have lower cache read tokens — expected, not a break.
// Source: promptCacheBreakDetection.ts:673-682
func NotifyCacheDeletion(key PromptStateKey) {
	trackKey := getTrackingKey(key.QuerySource, key.AgentID)
	if trackKey == "" {
		return
	}
	muState.Lock()
	defer muState.Unlock()
	if val, ok := stateStore[trackKey]; ok {
		val.CacheDeletionsPending = true
	}
}

// NotifyCompaction resets the cache read baseline after compaction.
// Source: promptCacheBreakDetection.ts:689-698
func NotifyCompaction(key PromptStateKey) {
	trackKey := getTrackingKey(key.QuerySource, key.AgentID)
	if trackKey == "" {
		return
	}
	muState.Lock()
	defer muState.Unlock()
	if val, ok := stateStore[trackKey]; ok {
		val.PrevCacheRead = 0
	}
}

// CleanupAgentTracking removes tracking state for a specific agent.
// Source: promptCacheBreakDetection.ts:700-702
func CleanupAgentTracking(agentID string) {
	muState.Lock()
	defer muState.Unlock()
	delete(stateStore, agentID)
	for i, k := range insertionOrder {
		if k == agentID {
			insertionOrder = append(insertionOrder[:i], insertionOrder[i+1:]...)

			break
		}
	}
}

// RequestToSystemMaps converts Request system data to []map[string]any for break detection.
// Handles both SystemBlocks and raw System (json.RawMessage) formats.
func RequestToSystemMaps(req *Request) []map[string]any {
	if len(req.SystemBlocks) > 0 {
		result := make([]map[string]any, len(req.SystemBlocks))
		for i, b := range req.SystemBlocks {
			m := map[string]any{
				"type": b.Type,
				"text": b.Text,
			}
			if b.CacheControl != nil {
				m["cache_control"] = map[string]any{
					"type": b.CacheControl.Type,
				}
			}
			result[i] = m
		}
		return result
	}
	if len(req.System) > 0 {
		// Try as array first
		var blocks []map[string]any
		if err := json.Unmarshal(req.System, &blocks); err == nil {
			return blocks
		}
		// Try as string
		var s string
		if err := json.Unmarshal(req.System, &s); err == nil {
			return []map[string]any{{"type": "text", "text": s}}
		}
	}
	return nil
}

// RequestToToolMaps converts Request tools to []map[string]any for break detection.
// Uses direct struct-to-map conversion instead of JSON round-trip.
func RequestToToolMaps(req *Request) []map[string]any {
	if len(req.Tools) == 0 {
		return nil
	}
	result := make([]map[string]any, 0, len(req.Tools))
	for _, t := range req.Tools {
		m := map[string]any{
			"name":        t.Name,
			"description": t.Description,
		}
		if len(t.InputSchema) > 0 {
			var schema any
			if err := json.Unmarshal(t.InputSchema, &schema); err == nil {
				m["input_schema"] = schema
			}
		}
		result = append(result, m)
	}
	return result
}
