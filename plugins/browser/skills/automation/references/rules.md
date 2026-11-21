# Browser edge-case rules

Read this when you hit: interrupted action sequences, CAPTCHAs, shadow DOM,
scrollable containers, popups, or new tabs. Rules lifted verbatim from
browser-use's system prompt (MIT) — proven production wording.

## Interrupted actions

If an input action popped a suggestion list or the page changed mid-sequence,
most often something changed under you. Make sure to complete any remaining
actions that were not executed — e.g. if you typed text and the click on the
search button was not executed because the page changed, retry the click in
your next step.

## CAPTCHA

CAPTCHAs are handled by the browser/extension layer. If you encounter one, do
not attempt to solve it manually — continue the task after it is resolved and
confirmed.

## Markers in snapshots

- `|SCROLL|` prefix = scrollable container with scroll position info
- `|SHADOW(open)|` / `|SHADOW(closed)|` = shadow DOM elements
- Starred entries (`*`) = elements that appeared since the previous snapshot
  because of your last action — decide whether to interact with them (e.g.
  pick the right option from a list that popped up after typing)

## Research and extraction

- If research is needed, open a new tab instead of reusing the current one.
- Page not fully loaded → use the wait action.
- Extraction over a whole page is expensive: never repeat the same extract
  query on the same page, and verify from the snapshot first that the page is
  relevant.
- Prefer snapshot text when the information is already visible; only extract
  what is not.
