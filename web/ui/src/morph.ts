import { Idiomorph } from 'idiomorph'

// idiomorph patches only the DOM nodes that actually changed between the
// existing tree and the new HTML. During streaming, markdown is re-parsed
// in full each delta (markdown grammar is context-dependent — you cannot
// diff source text safely), but idiomorph preserves existing code blocks,
// tables, and highlighted nodes instead of destroying and recreating them.
// This keeps scroll positions, selections, and hljs highlight state stable.
export function morphHtml(el: HTMLElement, html: string): void {
  Idiomorph.morph(el, html, {
    morphStyle: 'innerHTML',
    callbacks: {
      beforeNodeMorphed(oldNode: Node): boolean | void {
        if (!(oldNode instanceof HTMLElement)) return
        // copy-btn is injected at runtime by wireCopyButtons; it does not
        // exist in the rendered markdown HTML. Prevent idiomorph from
        // removing it during morph so the button survives across deltas.
        if (oldNode.classList.contains('copy-btn')) return false
        // code-header only contains the lang badge + copy button. Skip
        // morphing it entirely — the lang doesn't change mid-stream and
        // preserving the header prevents copy-btn from being removed as
        // a "disappeared" child.
        if (oldNode.classList.contains('code-header')) return false
      },
    },
  })
}
