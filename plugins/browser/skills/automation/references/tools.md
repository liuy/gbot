# playwright-mcp tool surface (pinned @0.0.83)

## The interface: `browser.*` harness (REPL global)

Preloaded into every session by this plugin (`repls/harness.js`). Each action
helper re-snapshots internally and resolves refs from THAT snapshot — stale
refs are impossible from your side; pass a regex pattern over the fresh
snapshot text.

```js
await browser.goto(url);                    // navigate + empty-page health check + net-err classification
const yaml = await browser.snapshot();      // fresh AX snapshot (YAML text)
browser.find(yaml, /pattern/);              // pure ref extraction (throws with context)
await browser.click(desc, /pattern/);       // fresh ref + new-tab auto-switch + dropdown hint
await browser.type(desc, /pattern/, text);  // fresh ref + value post-check + autocomplete hint
await browser.select(desc, /pattern/, vals);// fresh ref dropdown selection
await browser.waitText("marker");           // poll snapshots (never blind-sleep)
await browser.waitGone("marker");
await browser.evaluate("() => ...");        // DOM ground truth (code repair + 20k truncation + image mask)
await browser.shot("name.png");             // screenshot; lands under ${GBOT_CACHE_DIR}/traces/browser/, inside the 30-day sweep
await browser.vshot("page.png");            // vision fallback: screenshot + viewport dims {file, vw, vh}
await browser.vclick(fx, fy, desc);         // click at fractional coords from the vshot image; reports what was hit
```

If something here doesn't cover the task, `ALL_TOOLS` at runtime is the
authoritative list of raw tools — names and schemas, always fresh.
