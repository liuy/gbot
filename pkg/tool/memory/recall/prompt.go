package recall

// prompt is the tool prompt for recall.
const prompt = `# recall
Search the user's structured facts and conversation history. Use this to look up things the user has told you before (preferences, personal context, past decisions).

Input:
- query (required): FTS5 query. Supports AND, OR, NOT and parentheses. For CJK languages, separate terms with spaces or operators (e.g. "Alex AND blue", "blue OR red").
- limit (optional, default 50, max 200): max results per source.

Output: { facts: [{fact_id, content, date}], messages: [{content, date}] }

When updating or contradicting a fact, use fact_id with the remember tool to update in place.`
