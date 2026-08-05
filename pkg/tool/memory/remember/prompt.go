package remember

// prompt is the tool prompt for remember.
const prompt = `# remember
Store a new atomic fact about the user or their close relationships.

Before remembering, call recall to check for existing or contradictory facts. If a new fact contradicts an old one, forget the old fact_id first, then remember the new one.

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
