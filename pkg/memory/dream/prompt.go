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
3. For topics worth remembering, Recall with keywords for full context.
   The dialogue preview is 200 chars — when it hints at more beneath the
   surface, Recall BEFORE writing the memory:
   - preview shows a decision or lesson but not the reasoning →
     Recall(query="oom filehistory snapshot")
   - preview references an incident by shorthand ("the leak", "那个
     bug") → Recall that shorthand to pull the full thread
   - an old memory file conflicts with what you just read → Recall the
     original discussion to confirm before overwriting
   Recall returns snippets; when a snippet matters, Recall(uuid="...")
   pulls the full message. Hand-written SQL is for time windows — for
   keyword digging, Recall is the right tool.

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
		// Format renders in the time's OWN zone — time.Local is a
		// hardcoded UTC stub on Android (no initLocal TZ support), so
		// .Local() would silently flatten every zone to UTC there.
		lastDreamStr = lastDream.Format("2006-01-02 15:04 MST")
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

Read("%s")   — overview: sessions active since the cutoff
Read("%s")   — dialogue previews since the cutoff

This run, per the phases in your system prompt:
Phase 2 (gather) — run both queries above; while the dialogue query still
returns rows, re-run it with AND seq > <last returned seq> until the
window is exhausted. When a preview hints at reasoning or incidents
beneath the surface, Recall with keywords before moving on.
Phase 3 (consolidate) — before overwriting a memory that conflicts with
what you read, Recall the original discussion to confirm.
Phase 4 (prune) — before deleting a memory as stale, Recall to confirm
it has been superseded.
Begin.`, memoryDir, dbPath, lastDreamStr, cutoff, newMsgCount, overview, dialogue)
}
