// copyText writes text to the clipboard, transparently falling back to the
// legacy textarea + execCommand path when non-secure context or old browsers.
import { createNode } from '../dom'
import { createIconButton } from '../buttons'

export async function copyText(text: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return
    } catch {
      // fall through to legacy path
    }
  }
  const ta = createNode('textarea', {
    props: { value: text },
    style: { position: 'fixed', opacity: '0' },
  })
  document.body.appendChild(ta)
  ta.select()
  document.execCommand('copy')
  document.body.removeChild(ta)
}

const RESET_MS = 1500

// createCopyButton uses createIconButton + setIcon so the icon swap reuses
// the standard factory (paths live in icons.ts under 'copy' / 'check').
export function createCopyButton(getText: () => string): HTMLButtonElement {
  return createIconButton({
    icon: 'copy',
    label: 'Copy',
    size: 'sm',
    variant: 'ghost',
    iconSize: 14,
    className: 'copy-btn',
    onClick: async (_e, setIcon) => {
      await copyText(getText())
      setIcon('check')
      setTimeout(() => setIcon('copy'), RESET_MS)
    },
  })
}
