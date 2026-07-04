export interface HeaderHandles {
  root: HTMLElement
  setStatus: (connected: boolean) => void
}

interface DropdownOption {
  label: string
  active?: boolean
}

// Dropdowns are decorative placeholders matching Header.tsx markup. The React
// version's dropdowns never fired send() calls; that non-functional behavior
// is preserved.
function createDropdown(
  triggerHTML: string,
  options: DropdownOption[],
  width: string,
): HTMLElement {
  const wrap = document.createElement('div')
  wrap.className = 'relative group'

  const trigger = document.createElement('button')
  trigger.className = 'flex items-center gap-1 group'
  trigger.innerHTML =
    triggerHTML +
    '<svg width="8" height="8" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" class="text-t3 group-hover:text-t1"><path d="M6 9l6 6 6-6" /></svg>'

  const panel = document.createElement('div')
  panel.className = `absolute top-full left-0 mt-1 glass-solid border border-hairline rounded-lg shadow-xl modal-enter z-40 hidden ${width}`
  const inner = document.createElement('div')
  inner.className = 'p-1 max-h-60 overflow-y-auto'
  for (const opt of options) {
    const item = document.createElement('button')
    item.className =
      'w-full flex items-center justify-between px-2.5 py-2 rounded-md hover:bg-ink3 text-left'
    const span = document.createElement('span')
    span.className = `text-[13px] ${opt.active ? 'text-t1' : 'text-t2'}`
    span.textContent = opt.label
    item.appendChild(span)
    if (opt.active) {
      const check = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
      check.setAttribute('width', '12')
      check.setAttribute('height', '12')
      check.setAttribute('viewBox', '0 0 24 24')
      check.setAttribute('fill', 'none')
      check.setAttribute('stroke', '#00B4FF')
      check.setAttribute('stroke-width', '2.5')
      const p = document.createElementNS('http://www.w3.org/2000/svg', 'path')
      p.setAttribute('d', 'M20 6L9 17l-5-5')
      check.appendChild(p)
      item.appendChild(check)
    }
    inner.appendChild(item)
  }
  panel.appendChild(inner)

  let open = false
  const close = () => {
    open = false
    panel.classList.add('hidden')
    document.removeEventListener('mousedown', onDocClick)
  }
  const onDocClick = (e: MouseEvent) => {
    if (!wrap.contains(e.target as Node)) close()
  }
  trigger.addEventListener('click', () => {
    open = !open
    if (open) {
      panel.classList.remove('hidden')
      document.addEventListener('mousedown', onDocClick)
    } else close()
  })

  wrap.appendChild(trigger)
  wrap.appendChild(panel)
  return wrap
}

export function createHeader(): HeaderHandles {
  const root = document.createElement('header')
  root.className = 'sticky top-0 z-30 card-bg'

  const inner = document.createElement('div')
  inner.className =
    'flex items-center gap-2.5 px-4 h-11 max-w-2xl mx-auto'

  const wordmark = document.createElement('span')
  wordmark.className =
    'font-semibold tracking-tight text-[14px] transition-colors text-t3'
  wordmark.textContent = 'GBot'

  inner.appendChild(wordmark)
  inner.appendChild(
    createDropdown(
      '<span class="mono text-[11px] text-t2 group-hover:text-t1 transition-colors">glm-5.2</span>',
      [
        { label: 'glm-5.2', active: true },
        { label: 'glm-4.6' },
        { label: 'gpt-5' },
        { label: 'claude-sonnet-4.5' },
      ],
      'w-56',
    ),
  )
  inner.appendChild(
    createDropdown(
      '<span class="text-[12px] text-t2 group-hover:text-t1 transition-colors truncate-sm">modality-fix</span>',
      [
        { label: 'modality-fix', active: true },
        { label: 'ws-reversal' },
      ],
      'w-52',
    ),
  )
  inner.appendChild(
    createDropdown(
      '<span class="text-[12px] text-t2 group-hover:text-t1 transition-colors truncate-sm">main</span>',
      [
        { label: 'main', active: true },
        { label: 'wechat-bot' },
      ],
      'w-52',
    ),
  )

  const flex = document.createElement('div')
  flex.className = 'flex-1'
  inner.appendChild(flex)

  const settings = document.createElement('button')
  settings.className =
    'w-6 h-6 rounded-md flex items-center justify-center hover:ring-2 hover:ring-blue/40 transition-all text-t2 hover:text-t1'
  settings.innerHTML =
    '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 01-2.83 2.83l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-4 0v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 010-4h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 012.83-2.83l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 014 0v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 010 4h-.09a1.65 1.65 0 00-1.51 1z" /></svg>'
  inner.appendChild(settings)

  root.appendChild(inner)

  const setStatus = (connected: boolean) => {
    wordmark.className =
      'font-semibold tracking-tight text-[14px] transition-colors ' +
      (connected ? 'text-blue pulse' : 'text-t3')
  }

  return { root, setStatus }
}
