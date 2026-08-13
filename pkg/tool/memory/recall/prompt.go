package recall

// prompt is the tool prompt for recall.
const prompt = `# Recall
Search the user's conversation history. Use this to look up things the user has told you before (preferences, personal context, past decisions).

Input:
- query (required): FTS5 query. Supports AND, OR, NOT and parentheses. For CJK languages, separate terms with spaces or operators (e.g. "Alex AND blue", "blue OR red").
- since (optional): time range like '7d', '12h', '2w', '3m', '1y'.
- limit (optional, default 50, max 200): max results.

Output: { messages: [{content (snippet), date}] }

Messages return snippets (~50 chars) around the search term, not full text.`
