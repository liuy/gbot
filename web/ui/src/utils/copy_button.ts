// copyText writes text to the clipboard, transparently falling back to the
// legacy textarea + execCommand path when navigator.clipboard is unavailable
// (non-secure context like http://<lan-ip>:port, or older browsers).
export async function copyText(text: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return
    } catch {
      // fall through to legacy path
    }
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  document.execCommand('copy')
  document.body.removeChild(ta)
}

// SVG icons use stroke-based style (same as header.ts copy button) so they
// adapt to color changes via `stroke: currentColor` without explicit per-icon
// color rules.
const COPY_ICON_SVG = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="copy-icon-idle"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>'
const CHECK_ICON_SVG = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="copy-icon-done"><polyline points="20 6 9 17 4 12"/></svg>'

const RESET_MS = 1500

// applyCopyBehavior wires the click handler to a single copy button element.
// Centralizes: text retrieval via getText(), clipboard write via copyText(),
// icon swap via data-state, and 1.5s revert timer. Used by both
// createCopyButton (for standalone buttons) and markdown-rendered buttons
// (which arrive as HTML strings without event listeners).
export function applyCopyBehavior(btn: HTMLButtonElement, getText: () => string): void {
  btn.dataset.state = 'idle'
  if (btn.childElementCount === 0) {
    btn.innerHTML = COPY_ICON_SVG + CHECK_ICON_SVG
  }
  btn.addEventListener('click', async () => {
    await copyText(getText())
    btn.dataset.state = 'copied'
    setTimeout(() => { btn.dataset.state = 'idle' }, RESET_MS)
  })
}

// createCopyButton returns a standalone button with click handling. The icon
// swap and clipboard logic are encapsulated; caller only supplies the text
// source. Used by header.ts and other UI components.
export function createCopyButton(getText: () => string): HTMLButtonElement {
  const btn = document.createElement('button')
  btn.type = 'button'
  btn.className = 'copy-btn'
  applyCopyBehavior(btn, getText)
  return btn
}

// copyButtonHTML returns the static HTML for a copy button, used by
// markdown.ts when rendering code blocks to HTML strings (the buttons
// have no event listeners at this stage; callers must wire click via
// applyCopyBehavior or via a delegated click handler).
export function copyButtonHTML(): string {
  return `<button type="button" class="copy-btn" data-state="idle">${COPY_ICON_SVG}${CHECK_ICON_SVG}</button>`
}
