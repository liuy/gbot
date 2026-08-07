package remember

// prompt is the tool prompt for remember.
const prompt = `# Remember
Store a new atomic fact about the user or their close relationships, or update an existing fact in place.

Two call forms:
- Remember(content): create a new fact
- Remember(fact_id, content): update an existing fact (use Recall to find the fact_id)

Before remembering, call Recall to check for existing or contradictory facts. If a new fact contradicts or supersedes an old one, Recall to find its fact_id, then Remember(fact_id, new_content) to update in place.

Each fact must be:
- Self-contained (understandable without other facts)
- Long-lived (still meaningful in 5 years) or a persistent state (with date + trigger)
- About the user's life world (not project/code/technical knowledge — that goes in notes)

Each fact must include:
- Subject (who) — always
- Time (when) — required for non-permanent facts
- Relation (to whom) — required when involving others
- Trigger (why) — required for emotions/states

Use the user's conversation language. Do not translate or unify languages.`
