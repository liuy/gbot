// browser.* — harness over this plugin's playwright tools (pinned @0.0.83).
//
// Helper bar: a helper exists to enforce an INVARIANT or carry a battle
// scar — never to save keystrokes. Sugar without an invariant belongs in
// task code, not here.
//
// Freshness by construction: every action helper re-snapshots internally and
// resolves refs from THAT snapshot, so a caller can never act on a stale ref.
//
// Micro-behaviors aligned with browser-use tools/service.py (read 2026-10-03):
// navigate empty-DOM health check (:519-544), network error classification
// (:564-578), post-type value verification (:829-835), autocomplete detection
// (:838-844), new-tab detection after click (:629-654), JS code repair +
// result truncation for evaluate (:1835-2006), screenshot root restriction.
//
// Philosophy difference kept: errors THROW (our loop lives inside js(); the
// conversation must see subtask failure) but messages carry browser-use's
// diagnostic + recovery-suggestion shape.
//
// Trust note: this file runs with full tools.* access, same trust level as
// the plugin's MCP server itself.
globalThis.browser = {
  PREFIX: "mcp__plugin_browser_playwright__",

  _t(name) {
    const fn = tools[this.PREFIX + name];
    if (!fn) throw new Error("playwright tool missing: " + name);
    return fn;
  },

  _sleep(ms) {
    return new Promise((r) => setTimeout(r, ms));
  },

  _classifyNetworkError(e) {
    const msg = String((e && e.message) || e);
    if (/ERR_NAME_NOT_RESOLVED|ERR_INTERNET_DISCONNECTED|ERR_CONNECTION_REFUSED|ERR_TIMED_OUT|ERR_TUNNEL_CONNECTION_FAILED|net::/.test(msg)) {
      return new Error("Site unreachable (network/DNS/proxy): " + msg.slice(0, 200));
    }
    return null;
  },

  // ---- device mode: runtime desktop/mobile switch (session-scoped) ----
  // setMode applies UA + viewport + page-scale via raw CDP (Emulation.*) and
  // STICKS: goto re-applies after navigation, so overrides survive reloads.
  // 'mobile' is the launch default; its baseline is the static Pixel 8
  // descriptor below (_mobileBaseline), never sampled from the live page.
  // The host card needs desktop mode for sites that gate their web
  // experience behind a desktop UA (XHS search).
  _mode: "mobile",
  _desktop: {
    ua: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
    w: 1024,
    h: 768,
    dsf: 3,
    mobile: false,
    scale: 1,
  },
  // Pixel 8 descriptor (playwright-core devices registry, pinned 0.0.83) —
  // static, never sampled from a possibly-overridden live page.
  _mobileBaseline: {
    ua: "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.8059.12 Mobile Safari/537.36",
    w: 412,
    h: 839,
    dsf: 2.625,
    mobile: true,
    scale: 1,
    platform: "Android",
  },
  async _applyMode(p) {
    await this._t("browser_run_code_unsafe")({
      code:
        "async (page) => {" +
        "  const cdp = await page.context().newCDPSession(page);" +
        "  await cdp.send('Emulation.setUserAgentOverride', { userAgent: " +
        JSON.stringify(p.ua) +
        " });" +
        "  await cdp.send('Emulation.setDeviceMetricsOverride', { width: " +
        p.w +
        ", height: " +
        p.h +
        ", deviceScaleFactor: " +
        p.dsf +
        ", mobile: " +
        !!p.mobile +
        " });" +
        "  await cdp.send('Emulation.setPageScaleFactor', { pageScaleFactor: " +
        (p.scale || 1) +
        " });" +
        "  return 'ok';" +
        "}",
    });
  },
  async setMode(mode) {
    if (mode !== "desktop" && mode !== "mobile") throw new Error("unknown mode: " + mode);
    const p = mode === "desktop" ? this._desktop : this._mobileBaseline;
    await this._applyMode(p);
    this._mode = mode;
    return this._mode;
  },
  mode() {
    return this._mode || "mobile";
  },
  async snapshot() {
    return String(await this._t("browser_snapshot")({}));
  },

  // True when an http(s) page rendered no actionable content (SPA not yet
  // mounted, empty body, anti-bot wall).
  _looksEmpty(yaml) {
    const body = yaml.split("### Snapshot")[1] || "";
    return body.split("\n").filter((l) => l.trim()).length < 3;
  },

  // Navigate with the three-stage health check: settle → recheck (3s) →
  // reload (5s) → diagnostic error.
  async goto(url) {
    await this._gotoBase(url);
    // Sticky device mode: re-apply overrides after navigation so desktop UA
    // and viewport survive reloads (CDP emulation follows the page, but an
    // explicit replay also covers chromium relaunches mid-session).
    if (this._mode === "desktop") await this._applyMode(this._desktop);
  },
  async _gotoBase(url) {
    try {
      await this._t("browser_navigate")({ url });
    } catch (e) {
      throw this._classifyNetworkError(e) || e;
    }
    await this._sleep(600);
    if (!/^https?:/i.test(url)) return; // health check only makes sense for http(s)
    let yaml = await this.snapshot();
    if (!this._looksEmpty(yaml)) return;
    await this._sleep(3000);
    yaml = await this.snapshot();
    if (!this._looksEmpty(yaml)) return;
    await this._t("browser_navigate")({ url });
    await this._sleep(5000);
    yaml = await this.snapshot();
    if (this._looksEmpty(yaml)) {
      throw new Error(
        `Page loaded but returned empty content for ${url}. Possible causes: JS failed to render, ` +
          `anti-bot measures, or a tunnel/proxy issue. Try a different URL or approach.`,
      );
    }
  },

  // Pure: extract a ref from snapshot text. Throws with context on a miss.
  find(yaml, pattern) {
    const m = yaml.match(pattern);
    if (!m) throw new Error("ref not found for " + pattern + " in:\n" + yaml.slice(0, 500));
    return m[1];
  },

  // Tab listing is "- N: url" lines — parse real indices, never count lines
  // (headers/footers inflate naive line counts and misalign close indices).
  async _tabIndices() {
    try {
      const s = String(await this._t("browser_tabs")({ action: "list" }));
      return [...s.matchAll(/^\s*-\s*(\d+):/gm)].map((m) => +m[1]);
    } catch (_) {
      return [0];
    }
  },

  async _tabCount() {
    return (await this._tabIndices()).length || 1;
  },

  async _maybeSwitchNewTab(before) {
    try {
      await this._sleep(150);
      const after = await this._tabIndices();
      const fresh = after.filter((i) => !before.includes(i));
      if (fresh.length) {
        await this._t("browser_tabs")({ action: "select", index: Math.max(...fresh) });
        return true;
      }
    } catch (_) {
      /* detection is best-effort; a missed switch must not fail the click */
    }
    return false;
  },

  // Action helpers — each starts from a FRESH snapshot.
  async click(desc, pattern) {
    const yaml = await this.snapshot();
    const target = this.find(yaml, pattern);
    const before = await this._tabIndices();
    try {
      await this._t("browser_click")({ element: desc, target });
    } catch (e) {
      if (/select|option|combobox/i.test(String(e))) {
        throw new Error(
          String(e) + "\nHint: this looks like a dropdown — use browser.select(desc, pattern, values) instead of click.",
        );
      }
      throw e;
    }
    const switched = await this._maybeSwitchNewTab(before);
    if (switched) return "clicked; auto-switched to new tab";
    return "clicked";
  },

  async type(desc, pattern, text, submit) {
    const yaml = await this.snapshot();
    const target = this.find(yaml, pattern);
    // Autocomplete hint: detect combobox/autocomplete on the matched line.
    const line = yaml.split("\n").find((l) => l.includes(target)) || "";
    const isAutocomplete = /combobox|autocomplete/i.test(line);
    await this._t("browser_type")({ element: desc, target, text, submit: !!submit });
    if (isAutocomplete) {
      await this._sleep(400); // let the JS dropdown populate before the next action
      return (
        "typed. NOTE: autocomplete field — wait for suggestions and CLICK the right one instead of pressing Enter."
      );
    }
    // Post-check: did the field actually hold what we typed? (Reformat /
    // autocomplete can silently mutate input.) Best-effort — with submit the
    // form navigation can destroy the evaluation context or move focus
    // before this runs; the submit itself already succeeded, so a failed
    // check must not fail the call.
    try {
      const actual = await this.evaluate("() => { const el = document.activeElement; return el ? String(el.value ?? '') : ''; }");
      if (actual && actual !== text) {
        return `typed. WARNING: field's actual value '${actual}' differs from typed text — the page may have reformatted or autocompleted your input.`;
      }
    } catch (_) {
      /* post-check only; the type call itself resolved above */
    }
    return "typed";
  },

  async select(desc, pattern, values) {
    const yaml = await this.snapshot();
    const target = this.find(yaml, pattern);
    await this._t("browser_select_option")({ element: desc, target, values });
  },

  // Poll fresh snapshots until text appears (or budget runs out).
  async waitText(text, tries = 10) {
    for (let i = 0; i < tries; i++) {
      const yaml = await this.snapshot();
      if (yaml.includes(text)) return true;
      await this._sleep(300);
    }
    throw new Error("waitText timeout: " + text);
  },

  async waitGone(text, tries = 10) {
    for (let i = 0; i < tries; i++) {
      const yaml = await this.snapshot();
      if (!yaml.includes(text)) return true;
      await this._sleep(300);
    }
    throw new Error("waitGone timeout: " + text);
  },

  // DOM ground truth with LLM-code repair + result governance.
  // Repairs common LLM escaping bugs before execution (browser-use
  // _validate_and_fix_javascript); truncates huge results; masks inline
  // images so they never flood the context.
  async evaluate(fn) {
    let code = String(fn)
      .replace(/\\"/g, '"')
      .replace(/\\\\([dDsSwWbBnrtfv])/g, "\\$1")
      .replace(/\\\\([.*+?^${}()|[\]])/g, "\\$1")
      .replace(/document\.evaluate\s*\(\s*"([^"]*)"\s*,/g, "document.evaluate(`$1`,")
      .replace(/(querySelector(?:All)?)\s*\(\s*"([^"]*)"\s*\)/g, "$1(`$2`)")
      .replace(/\.(closest|matches)\s*\(\s*"([^"]*)"\s*\)/g, ".$1(`$2`)");
    let out = String(await this._t("browser_evaluate")({ function: code }));
    out = out.replace(/data:image\/[^;]+;base64,[A-Za-z0-9+/=]+/g, "[Image]");
    if (out.length > 20000) out = out.slice(0, 19950) + "\n... [Truncated after 20000 characters]";
    return out;
  },

  // Filename guard: screenshot names become path segments under the
  // --output-dir root; anything outside [A-Za-z0-9._-] is flattened so a
  // name can never escape that root.
  _safeName(name) {
    return String(name).replace(/[^A-Za-z0-9._-]/g, "_");
  },

  // Screenshot resolves under the MCP --output-dir root
  // (${GBOT_CACHE_DIR}/traces/browser/<name>) — inside the 30-day media
  // sweep, so shots age out without manual cleanup.
  async shot(name) {
    return await this._t("browser_take_screenshot")({
      filename: ".playwright-mcp/" + this._safeName(name),
      type: "png",
    });
  },

  // browser_evaluate returns "### Result\n\"{\\\"k\\\":...}\"\n### Ran
  // Playwright code\n<code echo>" — the code echo can itself contain quotes,
  // so cut at the section boundary BEFORE hunting for the JSON string.
  _parseEval(raw) {
    const head = String(raw).split("\n###")[0];
    const a = head.indexOf('"'), b = head.lastIndexOf('"');
    if (a >= 0 && b > a) {
      try { return JSON.parse(JSON.parse(head.slice(a, b + 1))); } catch (_) { /* fall through */ }
    }
    const l = head.indexOf("{"), r = head.lastIndexOf("}");
    if (l >= 0 && r > l) {
      try { return JSON.parse(head.slice(l, r + 1)); } catch (_) { /* fall through */ }
    }
    throw new Error("unparseable evaluate result: " + head.slice(0, 120));
  },

  // Tab hygiene: the browser is one resident instance; tasks work in tabs.
  // tabBaseline() at task start, tabCleanup() at end — task tabs close, the
  // baseline (index 0, the login-state carrier) stays.
  async tabBaseline() {
    this._baseTabs = await this._tabCount();
    return this._baseTabs;
  },

  async tabCleanup() {
    const keep = this._baseTabs || 1; // no baseline → keep tab 0 only
    const doomed = (await this._tabIndices()).filter((i) => i >= keep).sort((a, b) => b - a);
    for (const i of doomed) {
      await this._t("browser_tabs")({ action: "close", index: i });
    }
    this._baseTabs = null;
    return `closed ${doomed.length} task tab(s), baseline kept`;
  },

  // Vision fallback for AX-blind targets (styled divs, canvas, shadow UI):
  // shot the page AND capture viewport dimensions, then attach the image via
  // the REPL image() global — the model reads it and estimates FRACTIONAL
  // coordinates for vclick (fractions avoid DPR/scaling math entirely,
  // mirroring browser-use's coordinate conversion but in normalized space).
  async vshot(name) {
    const file = ".playwright-mcp/" + this._safeName(name);
    await this._t("browser_take_screenshot")({ filename: file, type: "png" });
    const d = this._parseEval(
      await this.evaluate("() => JSON.stringify({ vw: window.innerWidth, vh: window.innerHeight })"),
    );
    return { file, vw: d.vw, vh: d.vh };
  },

  // Click at fractional viewport coordinates (fx/fy in [0,1] estimated from
  // the vshot image). Routes through page.mouse.click (CDP trusted events —
  // synthetic el.click() gets rejected by isTrusted checks on real UIs).
  // Returns what was actually hit — ground truth, not assumption.
  async vclick(fx, fy, desc) {
    if (!(fx >= 0 && fx <= 1 && fy >= 0 && fy <= 1)) {
      throw new Error(`vclick fractions out of range: (${fx}, ${fy}) — use values in [0,1] from the vshot image`);
    }
    const before = await this._tabIndices();
    const d = this._parseEval(
      await this._t("browser_run_code_unsafe")({
        code: `async (page) => {
          const x = Math.round(page.viewportSize().width * ${fx}), y = Math.round(page.viewportSize().height * ${fy});
          await page.mouse.click(x, y);
          await page.waitForTimeout(200);
          const el = await page.evaluate(([x, y]) => {
            const e = document.elementFromPoint(x, y);
            return e ? e.tagName.toLowerCase() + (e.id ? "#" + e.id : "") : "none";
          }, [x, y]);
          return JSON.stringify({ x, y, hit: el });
        }`,
      }),
    );
    if (d.hit === "none") throw new Error(`vclick: nothing at (${d.x}, ${d.y}) — the point is outside the viewport`);
    const switched = await this._maybeSwitchNewTab(before);
    return `clicked ${desc || ""} at (${d.x}, ${d.y}) → hit <${d.hit}>${switched ? "; auto-switched to new tab" : ""}`;
  },
};
