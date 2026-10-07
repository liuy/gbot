package dream

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// entrypointFileName is the memory index the archive phase works against.
const entrypointFileName = "MEMORY.md"

// archiveWarnLines is where the trigger's index-size report turns into a
// warning. MaxEntrypointLines (200) is a hard context cut; the warning sits
// below it so archive/merge pressure is visible before the wall.
const archiveWarnLines = 150

// SystemPrompt returns the static 5-phase consolidation instructions. Set as
// the dream engine's system prompt once at startup — it survives auto-compact
// and doesn't contain time-sensitive data.
const SystemPrompt = `# Dream: Memory Consolidation

You are performing a dream — a reflective pass over your memory files. Synthesize what you've learned recently into durable, well-organized memories.

All five phases below run on EVERY pass, in order — none is optional. When the
trigger reports the index is near its truncation wall, treat Phase 5 as the
most urgent item of the pass.

---

## Phase 1 — Orient

- ls the memory directory to see what already exists
- Read MEMORY.md to understand the current index
- Grep-then-write: before creating any file, Grep the memory directory for
  same-topic files and read them — index lines alone can't reveal topical
  overlap (two lines can each look legitimate while covering one topic)

## Phase 2 — Gather recent signal

The trigger message ships ready-to-run Read queries against the transcript DB
(excluded: this dream session, tool noise). Work them in order:
1. Overview query — which sessions were active since the cutoff
2. Dialogue query — user asks and assistant conclusions, interleaved; the
   densest signal of what was requested and what actually happened.
   While it still returns rows, re-run it with AND seq > <last returned
   seq> until the window is exhausted
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
- Same topic in multiple files = merge into one (keep the fresher frontmatter,
  fold the content together), delete the redundant files
- Convert relative dates ("yesterday", "last week") to absolute dates
- Delete contradicted facts — if today's investigation disproves an old memory, fix it at the source

## Phase 4 — Prune and index

Update MEMORY.md so it stays well under 25KB. It's an index, not a dump:
- Lines past 200 are hard-truncated from every conversation's context — the
  tail entries silently vanish. Keep the index well clear of that wall
- Each entry should be one line under ~150 characters
- Format: - [Title](file.md) — one-line hook
- Remove pointers to stale, wrong, superseded, or duplicated memories — the
  bar is "every line earns its keep", not a line count
- If the index is already past the wall: ls the memory directory and rebuild
  the index from the files on disk (files survive; only the index lines were lost)
- Never write memory content directly into MEMORY.md

## Phase 5 — Archive closed-out projects (MUST run every pass)

memory/archive/ is the cold layer: closed-out case records, never loaded
into context, reachable only via Recall. Move finished projects into it:
- Scan MEMORY.md entries; open the pointed-to files and check type. Only
  type=project candidates qualify
- Archive criterion: the memory carries an explicit closure marker (shipped,
  fixed with verification passed, merged — or an equivalent unambiguous
  completion) AND you are confident. When unsure, leave it — a wrongly
  archived memory becomes unreachable; a stale index line is merely noise
- Never archive feedback/user/reference/personal memories — they are
  evergreen
- Action per archived memory: mv memory/xxx.md memory/archive/xxx.md,
  prepend "> Archived from MEMORY.md @ YYYY-MM-DD" as the first line of the
  moved file, and delete the entry's line from MEMORY.md — confirm the moved
  file exists in memory/archive/ BEFORE deleting the line (a failed mv plus a
  deleted line loses the memory on both layers)
- No dedicated log — the archive/ directory plus the removed MEMORY.md lines
  are the record

---

Return a brief summary of what you consolidated, updated, or pruned. If nothing changed, say so.`

// TriggerMessage returns the per-tick user message with time-sensitive context.
// Sent as a Query each time the dream timer fires.
//
// The trigger embeds ready-to-run Read queries scoped to messages since the
// last consolidation: the LLM copies them verbatim instead of guessing time
// literals (DB stores UTC; the cutoff is pre-computed by the caller) or
// blind keyword searches. dreamSessionID excludes the dream's own transcript
// so it never consolidates its own previous dreams. indexLines is the
// pre-computed MEMORY.md line count, reported as context — near the
// truncation wall it becomes a warning, not a gate: the archive phase runs
// every pass regardless.
func TriggerMessage(memoryDir, dbPath, dreamSessionID string, lastDream time.Time, newMsgCount, indexLines int) string {
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
	indexLine := fmt.Sprintf("%s: %d lines", entrypointFileName, indexLines)
	if indexLines > archiveWarnLines {
		indexLine += " — near the 200-line truncation wall: treat Phase 5 (archive) and merging as this pass's most urgent items"
	}
	return fmt.Sprintf(`Memory directory: %s
Transcript DB: %s
Last consolidation: %s (query cutoff: '%s' UTC)
New main-thread messages since cutoff: %d
%s

Read("%s")   — overview: sessions active since the cutoff
Read("%s")   — dialogue previews since the cutoff

This run, execute every phase below in order — none is optional:
Phase 1 (orient) — orient before writing: the grep-then-write rule applies
from the first file you touch.
Phase 2 (gather) — run both queries above; while the dialogue query still
returns rows, re-run it with AND seq > <last returned seq> until the
window is exhausted. When a preview hints at reasoning or incidents
beneath the surface, use the Recall tool with keywords before moving on.
Phase 3 (consolidate) — before overwriting a memory that conflicts with
what you read, use the Recall tool on the original discussion to confirm.
Phase 4 (prune) — before deleting a memory as stale or duplicated,
use the Recall tool to confirm.
Phase 5 (archive, MUST run every pass) — move closed-out type=project memories into
memory/archive/ (explicit closure marker + confident; when unsure, leave
the entry — never feedback/user/reference/personal).
Begin.`, memoryDir, dbPath, lastDreamStr, cutoff, newMsgCount, indexLine, overview, dialogue)
}

// IndexLineCount returns the line count of MEMORY.md in memoryDir. A missing
// or empty index is 0.
func IndexLineCount(memoryDir string) int {
	data, err := os.ReadFile(filepath.Join(memoryDir, entrypointFileName))
	if err != nil {
		return 0
	}
	content := strings.TrimRight(string(data), "\n")
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}
