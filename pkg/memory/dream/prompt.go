package dream

import (
	"fmt"

	"github.com/liuy/gbot/pkg/memory/long"
)

// factsExtractionSection teaches the dream agent how to use
// recall/remember/forget and sets the filtering criteria for what
// qualifies as a durable fact.
const factsExtractionSection = `You have three tools for structured memory:
- recall(query): search existing facts AND message history (FTS5 syntax: AND/OR/NOT, parentheses, e.g. "Alex AND blue")
- remember(content): store a new atomic fact
- forget(fact_id): delete a fact by id (from recall results)

Before writing new facts, recall existing ones to avoid duplicates and detect contradictions. If a new fact contradicts an old one, forget the old fact_id and remember the new one.

[Scope] About the user (use their real name from memory/*.md) and their close relationships (family/friends/colleagues).

[Extract]
1. Long-term stable facts
   - User identity, preferences, habits
   - Family/social relationships
   - Life experiences

2. Life events (one-off but milestone-worthy)
   - Must include: date + what happened + emotional context
   - Example: "Alex's daughter (Lily) born 2026-08-06, overjoyed"
   - Example: "Alex received poor annual review in Jan 2026, felt discouraged"

3. Persistent states (lasting days/weeks)
   - Stress levels or moods sustained over a period
   - Must include: time range + trigger
   - Example: "Alex under heavy work pressure during Aug 2026"

[Do NOT extract]
- Project/code/tech knowledge → goes to notes
- Objective world knowledge → skip
- Current work in progress → goes to notes
- Transient emotions without milestone → skip (e.g. "had a good lunch")

[Decision criteria]
1. Will this fact still matter in 5 years? (Long-term stable or life event)
2. Is it a persistent state? (Lasting days/weeks)
3. Is it about the user's life world?

[Each fact must include]
- Subject (who) — always
- Time (when) — required for non-permanent facts
- Relation (to whom) — required when involving others
- Trigger (why) — required for emotions/states

[Language]
- Use the user's conversational language, keep their wording
- Do not translate or unify languages
- Keep proper nouns in original form

[Format]
- One fact = one atomic fact
- Self-contained, understandable without other facts`

// BuildConsolidationPrompt builds the dream prompt. The facts extraction
// section is always included — facts are an integral part of dream.
func BuildConsolidationPrompt(memoryDir, extra string) string {
	prompt := "# Dream: Memory Consolidation\n\n" +
		"You are reviewing recent conversations and updating memory.\n\n" +
		fmt.Sprintf("Memory directory: `%s`\n", memoryDir) +
		"This directory already exists — write to it directly with the Write tool (do not run mkdir or check for its existence).\n\n" +
		"---\n\n"

	if extra != "" {
		prompt += "## Recent conversations\n\n" + extra + "\n---\n\n"
	}

	prompt += "## Step 1 — Notes\n\n" +
		"1. ls the memory directory, Read MEMORY.md and existing topic files\n" +
		"2. From the conversations above, update notes (Write/Edit memory/*.md)\n" +
		fmt.Sprintf("3. Update `%s` index (under %d lines, one line per entry: `- [Title](file.md) — one-line hook`)\n\n",
			long.EntrypointName, long.MaxEntrypointLines) +
		"## Step 2 — Facts\n\n" +
		"4. recall existing facts related to the conversations to avoid duplicates and detect contradictions\n" +
		"5. forget stale or contradicted facts\n" +
		"6. Extract new durable facts and remember them\n\n" +
		factsExtractionSection + "\n\n" +
		"---\n\n" +
		"Return a brief summary of what you updated."

	return prompt
}
