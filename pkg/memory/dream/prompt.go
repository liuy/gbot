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

The trigger message ships ready-to-run Read queries against the transcript DB
(excluded: this dream session, tool noise). Work them in order:
1. Overview query — which sessions were active since the cutoff
2. Dialogue query — user asks and assistant conclusions, interleaved; the
   densest signal of what was requested and what actually happened
3. For topics worth remembering, Recall with keywords for full context

The cutoff timestamp in the queries is UTC and pre-computed for you — use the
queries verbatim (adjust LIMIT as needed), do not construct time literals
yourself.

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
//
// The trigger embeds ready-to-run Read queries scoped to messages since the
// last consolidation: the LLM copies them verbatim instead of guessing time
// literals (DB stores UTC; the cutoff is pre-computed by the caller) or
// blind keyword searches. dreamSessionID excludes the dream's own transcript
// so it never consolidates its own previous dreams.
func TriggerMessage(memoryDir, dbPath, dreamSessionID string, lastDream time.Time, newMsgCount int) string {
	lastDreamStr := "never"
	cutoff := "1970-01-01 00:00:00"
	if !lastDream.IsZero() {
		lastDreamStr = lastDream.Local().Format("2006-01-02 15:04 MST")
		cutoff = lastDream.UTC().Format("2006-01-02 15:04:05")
	}
	exclude := " AND session_id != '" + dreamSessionID + "'"
	overview := dbPath + "?q=SELECT session_id, COUNT(*) AS n, MAX(created_at) AS last " +
		"FROM messages WHERE created_at > '" + cutoff + "' AND is_sidechain = 0" + exclude +
		" GROUP BY session_id"
	dialogue := dbPath + "?q=SELECT seq, type, substr(COALESCE(" +
		"json_extract(content,'$[0].text'), json_extract(content,'$[1].text'), " +
		"json_extract(content,'$[2].text'), '(no text)'), 1, 200) AS preview " +
		"FROM messages WHERE ((type = 'user' AND content NOT LIKE '%tool_result%') OR " +
		"(type = 'assistant' AND content LIKE '%\"type\":\"text\"%'))" +
		" AND is_sidechain = 0" + exclude +
		" AND created_at > '" + cutoff + "' ORDER BY seq LIMIT 80"
	return fmt.Sprintf(`Memory directory: %s
Transcript DB: %s
Last consolidation: %s (query cutoff: '%s' UTC)
New main-thread messages since cutoff: %d

Step 1 — overview, which sessions were active:
Read("%s")

Step 2 — dialogue since cutoff (user asks + assistant conclusions, interleaved):
Read("%s")

Adjust LIMIT freely. For deeper context on a topic, Recall with keywords.
Begin.`, memoryDir, dbPath, lastDreamStr, cutoff, newMsgCount, overview, dialogue)
}
