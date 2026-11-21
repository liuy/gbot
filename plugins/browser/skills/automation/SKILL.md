---
name: automation
description: Browser structured-operation discipline — AX snapshot first, ref-based targeting, code-orchestrated loops. Attach modes: references/env-setup.md; edge-case rules: references/rules.md
---

# Browser Automation

Operate the browser from `js()` through the `browser.*` harness (preloaded
global; exact tool surface in `references/tools.md` — rule 0 below). One
`js()` call = one complete subtask — the loop runs locally and only the
summary returns to the conversation.

State across `js()` calls: keep it on `globalThis` (the session persists) —
no need to re-derive page state every call. Long waits: cap with
`// @timeout: 60000` as the first line. Treat MCP tool outputs as black
boxes — consume the fields you need, never ingest full page dumps.

## Decision tree: attach mode

```
User's daily Chrome (login state) → extension mode      → references/env-setup.md
Unattended / headless             → default stdio launch (plugin default)
Chromium on a debug port          → --cdp-endpoint=http://127.0.0.1:9222
```

Login persistence (storage state): references/env-setup.md.

## Orchestration loop

Reconnaissance-then-action, per Anthropic's webapp-testing pattern. The
`browser.*` harness re-snapshots inside every action — refs can't go stale
from your side:

```js
// One subtask = one js() call; the loop below never idles the page
await browser.goto("https://example.com/login");          // health-checked navigation
await browser.type("username", /textbox "[^"]*(user|email)/i, "liuy");
await browser.type("password", /textbox "[^"]*password/i, "***", /*submit*/ true);
await browser.waitText("Welcome");                        // poll, not blind sleep
const verdict = await browser.evaluate("() => document.title");  // DOM ground truth
```

Failure fallback: helper throws with context → adjust the pattern and retry
once → still failing → **AX-blind target** (styled div, canvas, no role):
`browser.vshot("page.png")` + `image(<file>)` to see it, estimate fractional
coordinates from the image, `browser.vclick(fx, fy, desc)` — the result
reports what was actually hit. Still failing → let the human decide.

Device mode (sticky — goto re-applies; some sites gate search/login behind a desktop UA):
`await browser.setMode("desktop")` — Chrome/Win UA + 1024×768; `await browser.setMode("mobile")` — Pixel 8 baseline;
`browser.mode()` queries.

## Cost tiering

AX snapshot: free — use freely. Screenshot: expensive — only when uncertain.
`image()` to the main model: most expensive — decision forks only.

## Core discipline

0. **List the tool surface first**: Read
   `${SKILL_DIR}/references/tools.md` once per task — the `browser.*` harness
   API and the exact raw tool names/parameters for the pinned version, never
   guessed.
1. **Snapshot freshness**: refs die on navigation or major DOM change.
   Re-snapshot before acting; stale refs are the top failure source.
2. **Recon then act**: never act on remembered refs.
3. **Settle before asserting**: poll, don't sleep.
4. **One js() call per subtask**: run the whole loop (navigate → login →
   act → verify) inside a single js() — stepwise calls idle the page,
   killing refs and sessions.

## Edge cases & setup

Interrupted actions, CAPTCHA, shadow DOM, popups, new tabs — the full rule
list lives at `${SKILL_DIR}/references/rules.md` (Read it when you hit one of
those situations). Attach modes and login persistence:
`${SKILL_DIR}/references/env-setup.md`.
