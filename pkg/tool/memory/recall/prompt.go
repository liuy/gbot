package recall

// prompt is the tool prompt for recall.
const prompt = `# Recall
Search the user's conversation history. Use this to look up things the user has told you before (preferences, personal context, past decisions).

Input:
- query (optional): keywords to search for, separated by spaces (e.g. "alice blue", "数据库 migration"). Messages matching more keywords rank first. Do not use boolean operators — they are ignored.
- uuid (optional): read a single message by its UUID (returned from a previous search). Returns full content, not a snippet. Mutually exclusive with query.
- since (optional): time range like '7d', '12h', '2w', '3m', '1y'.
- limit (optional, default 50, max 200): max results.

Either query or uuid is required.

Output: { messages: [{uuid, content, date}] }

Messages return snippets (~50 chars) around the search term. To read a message's full content, call Recall again with its uuid parameter.`
