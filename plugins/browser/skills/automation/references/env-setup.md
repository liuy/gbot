# Environment attach & login state (low-frequency reference)

Daily tasks don't need this file — consult it only when configuring an
environment for the first time or when login state is lost.

## Three attach modes

### 1. Chrome extension mode (user's daily browser, with login state)

1. Install the "Playwright MCP" extension from the Chrome/Edge Web Store
2. Add `--extension` to `mcp.json` args
3. Confirm the browser prompt on first connect
- Pros: zero flags, reuses login state and browser fingerprint
- Limits: only the focused tab is visible; the extension must allow the site

### 2. CDP endpoint (Chromium already on a debug port)

```bash
# Start Chrome with a debug port (persistent profile keeps logins)
google-chrome --remote-debugging-port=9222 --user-data-dir=$HOME/.cache/gbot-chrome
```
Point `mcp.json` args at `["--cdp-endpoint=http://127.0.0.1:9222"]`.

### 3. Default stdio launch (headless / unattended)

This plugin's mcp.json works as-is, no extra steps. Add `--headless` for
fully windowless runs; add `--isolated` for a throwaway empty profile.

## Login persistence (storage state)

Log in once, export, then skip login afterwards:

```
browser_navigate to the target site → log in (manual or automated)
→ export storage state to a file (cookies + localStorage)
→ add --storage-state=<path> to mcp.json args
```

## Dependencies

- node/npx on PATH (first `npx @playwright/mcp` fetch pulls the package)
- Browser binary: playwright downloads chromium automatically on first run;
  or use the system Chrome (modes 1/2)
