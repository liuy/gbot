package web

func webPrompt() string {
	return `Search the web for up-to-date information beyond your knowledge cutoff, or fetch URL content as markdown.

### When to use
- Information that may have changed since your training data
- Current events, recent releases, version-specific documentation
- Verifying facts with primary sources
- Fetching specific web pages for detailed content
- Errors or stuck problems that resist repeated fix attempts

### Search mode (non-URL query)
Returns numbered results with title, URL, and snippet.

CRITICAL: After answering, you MUST include a "Sources:" section with markdown links:
  Sources:
  - [Title 1](https://example.com/1)
  - [Title 2](https://example.com/2)

Prefer primary sources (official docs, papers) over secondary summaries.

### Fetch mode (URL query)
Fetches the URL, converts HTML to markdown, and returns the content.

- Large pages are truncated
- Redirects are followed automatically
- Some sites block automated requests; if fetch fails, try a search instead
- If the page requires JavaScript or returns empty/blocked content, use js: true to fetch with headless Chrome (includes stealth anti-detection)`
}
