export interface InputBarHandles {
  root: HTMLElement
  textarea: HTMLTextAreaElement
  setStreaming: (s: boolean) => void
  setQueuedMsgs: (q: { uuid: string; text: string }[]) => void
  setInputText: (text: string) => void
  appendQueuedText: (text: string) => void
  setConnected: (c: boolean) => void
  onSend: (cb: (text: string) => void) => void
  onStop: (cb: () => void) => void
  onCancelQueued: (cb: () => void) => void
}

export function createInputBar(initial: {
  connected: boolean
}): InputBarHandles {
  let connected = initial.connected
  let streaming = false
  let queuedMsgs: { uuid: string; text: string }[] = []
  let sendCb: ((text: string) => void) | null = null
  let stopCb: (() => void) | null = null
  let cancelCb: (() => void) | null = null

  const root = document.createElement('div')
  root.className = 'sticky bottom-0 z-10 px-5 pb-3 pt-1'

  // Bubble row (rendered only while streaming and non-empty).
  const bubbles = document.createElement('div')
  root.appendChild(bubbles)

  const form = document.createElement('form')
  const card = document.createElement('div')
  card.className = 'card-bg rounded-xl border border-hairline glow-blue'
  const row = document.createElement('div')
  row.className = 'flex items-end gap-2 px-4 py-2.5'

  // STOP button (hidden unless streaming).
  const stopBtn = document.createElement('button')
  stopBtn.type = 'button'
  stopBtn.className =
    'flex-shrink-0 flex items-center justify-center w-7 h-7 rounded-full text-blue transition-colors pulse-blue'
  stopBtn.style.background = 'rgba(0,180,255,0.12)'
  stopBtn.innerHTML =
    '<span class="text-[8px] mono font-bold tracking-wide">STOP</span>'
  stopBtn.style.display = 'none'
  stopBtn.addEventListener('click', () => stopCb?.())

  // Textarea wrap.
  const taWrap = document.createElement('div')
  taWrap.className =
    'flex-1 flex justify-center min-h-[20px] cursor-text'
  const textarea = document.createElement('textarea')
  textarea.rows = 1
  textarea.placeholder = 'Sup?'
  textarea.disabled = !connected
  textarea.className =
    'bg-transparent text-[15px] text-t1 placeholder-t3 resize-none outline-none text-center disabled:opacity-40'
  // fieldSizing is non-standard TS lib type; cast via setProperty.
  ;(textarea.style as unknown as Record<string, string>).fieldSizing =
    'content'
  textarea.style.width = 'fit-content'
  textarea.style.maxWidth = '100%'
  textarea.style.maxHeight = '120px'
  textarea.style.overflow = 'hidden'
  taWrap.appendChild(textarea)
  taWrap.addEventListener('click', () => textarea.focus())

  // Send button.
  const sendBtn = document.createElement('button')
  sendBtn.type = 'submit'
  sendBtn.setAttribute('aria-label', 'Send')
  sendBtn.className =
    'flex-shrink-0 text-blue hover:text-white transition-colors pb-0.5 disabled:opacity-30'
  sendBtn.innerHTML =
    '<svg class="h-6 w-6" viewBox="0 0 20 20" fill="currentColor"><path d="M3 10l14-7-7 14-2-5-5-2z" /></svg>'

  row.appendChild(stopBtn)
  row.appendChild(taWrap)
  row.appendChild(sendBtn)
  card.appendChild(row)
  form.appendChild(card)
  root.appendChild(form)

  const recomputeCanSend = () => {
    const can = textarea.value.trim().length > 0 && connected
    sendBtn.disabled = !can
  }

  const onSubmit = (e: Event) => {
    e.preventDefault()
    const text = textarea.value.trim()
    if (!text || !connected) return
    sendCb?.(text)
    textarea.value = ''
    recomputeCanSend()
  }
  form.addEventListener('submit', onSubmit)

  textarea.addEventListener('input', recomputeCanSend)

  textarea.addEventListener('keydown', (e: KeyboardEvent) => {
    // Enter — send (Shift+Enter for newline, handled by browser default)
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      onSubmit(new Event('submit'))
      return
    }

    // Esc — stop streaming / cancel queued messages
    if (e.key === 'Escape') {
      if (streaming) {
        e.preventDefault()
        stopCb?.()
      }
      return
    }

    // ArrowUp while streaming — pop all queued messages back to input
    if (streaming && e.key === 'ArrowUp' && queuedMsgs.length > 0) {
      e.preventDefault()
      cancelCb?.()
      return
    }

    const mod = e.metaKey || e.ctrlKey
    if (!mod) return

    // Browser defaults kept: Ctrl+A (select all), Ctrl+C/X/V (clipboard),
    // Ctrl+Z/Y (undo/redo), Ctrl+W (close tab), Ctrl+K (address bar),
    // Ctrl+U (view source), Ctrl+F (page search), Alt+←/→ (browser nav).
    // No custom shortcuts — textarea already provides all needed editing.
  })

  const renderBubbles = () => {
    bubbles.replaceChildren()
    if (!streaming) return
    queuedMsgs.forEach((m, i) => {
      const bub = document.createElement('div')
      bub.className =
        'mb-2 mx-auto bg-ink2/80 backdrop-blur border border-hairline rounded-full px-4 py-2 flex items-center gap-2 w-fit modal-enter cursor-pointer'
      bub.innerHTML =
        '<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" class="text-t3"><circle cx="12" cy="12" r="10" /><path d="M12 8v4M12 16h.01" /></svg>'
      const label = document.createElement('span')
      label.className =
        'text-[12px] text-t2 font-light italic truncate max-w-[240px]'
      label.textContent = m.text
      bub.appendChild(label)
      if (i === 0 && queuedMsgs.length > 1) {
        const more = document.createElement('span')
        more.className = 'text-[10px] text-t3 mono ml-1'
        more.textContent = `+${queuedMsgs.length - 1} more`
        bub.appendChild(more)
      }
      const cancelLabel = document.createElement('span')
      cancelLabel.className = 'text-[10px] text-t3 mono ml-1'
      cancelLabel.textContent =
        queuedMsgs.length > 1 ? 'Tap to CANCEL all' : 'Tap to CANCEL'
      bub.appendChild(cancelLabel)
      bub.addEventListener('click', () => cancelCb?.())
      bubbles.appendChild(bub)
    })
  }

  recomputeCanSend()

  return {
    root,
    textarea,
    setStreaming: (s: boolean) => {
      streaming = s
      stopBtn.style.display = s ? '' : 'none'
      if (!s) {
        queuedMsgs = []
      }
      renderBubbles()
    },
    setQueuedMsgs: (q) => {
      queuedMsgs = q
      renderBubbles()
    },
    setInputText: (text: string) => {
      textarea.value = text
      textarea.focus()
      recomputeCanSend()
    },
    appendQueuedText: (text: string) => {
      if (!text) return
      const existing = textarea.value
      textarea.value = existing === '' ? text : text + '\n' + existing
      textarea.focus()
      recomputeCanSend()
    },
    onSend: (cb) => {
      sendCb = cb
    },
    onStop: (cb) => {
      stopCb = cb
    },
    onCancelQueued: (cb) => {
      cancelCb = cb
    },
    setConnected: (c: boolean) => {
      connected = c
      textarea.disabled = !c
      recomputeCanSend()
    },
  }
}
