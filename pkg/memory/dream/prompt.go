package dream

import (
	"fmt"
	"time"
)

// SystemPrompt returns the static 4-phase consolidation instructions. Set as
// the dream engine's system prompt once at startup — it survives auto-compact
// and doesn't contain time-sensitive data.
const SystemPrompt = `# Dream: Memory Consolidation

You are performing a dream — a reflective pass over your memory files. Synthesize what you've learned recently into durable, well-organized memories.

---

## Phase 1 — Orient

- ls the memory directory to see what already exists
- Read MEMORY.md to understand the current index
- Skim existing topic files so you improve them rather than creating duplicates

## Phase 2 — Gather recent signal

Use Recall to search recent conversations for topics worth remembering:
- Pick keywords from existing memory files (topics, user preferences, project names)
- Call Recall(query="<keyword>", source="messages") for each
- Don't exhaustively search — look only for things you already suspect matter

## Phase 3 — Consolidate

For each thing worth remembering, write or update a memory file:
- Merge new signal into existing topic files rather than creating near-duplicates
- Convert relative dates ("yesterday", "last week") to absolute dates
- Delete contradicted facts — if today's investigation disproves an old memory, fix it at the source

## Phase 4 — Prune and index

Update MEMORY.md so it stays under ~50 lines AND under ~25KB. It's an index, not a dump:
- Each entry should be one line under ~150 characters
- Format: - [Title](file.md) — one-line hook
- Remove pointers to stale, wrong, or superseded memories
- Add pointers to newly important memories
- Never write memory content directly into MEMORY.md

---

Return a brief summary of what you consolidated, updated, or pruned. If nothing changed, say so.`

// TriggerMessage returns the per-tick user message with time-sensitive context.
// Sent as a Query each time the dream timer fires.
func TriggerMessage(memoryDir string, lastDream time.Time) string {
	lastDreamStr := "never"
	if !lastDream.IsZero() {
		lastDreamStr = lastDream.Format("2006-01-02 15:04")
	}
	return fmt.Sprintf("Memory directory: %s\nLast consolidation: %s\nBegin.", memoryDir, lastDreamStr)
}
