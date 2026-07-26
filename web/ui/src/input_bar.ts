import { createPopupPanel, createOutsideClick, createAnchoredPopup, positionAnchoredPopup } from './utils'

// AttachmentRef is the in-memory representation of a file the user has added
// to the chip strip but not yet sent. Image refs carry a blob URL for the
// thumbnail preview; document refs only carry metadata. uploadProgress
// (0..1) is set during WS chunked upload so renderChips can paint a thin
// blue progress bar; undefined when no upload is in flight.
//
// uploadedID is the server-side id assigned when this ref's bytes have
// successfully landed in the server's saved map (set after a successful
// upload, kept across retries). Undefined means the upload has not yet
// completed or has failed; onSend uses this to skip re-uploading refs
// whose bytes are already staged server-side.
export type AttachmentRef =
  | { kind: 'image'; file: File; previewURL: string; remotePath?: string; mime?: string; failed?: boolean; uploadProgress?: number; uploadedID?: string }
  | { kind: 'document'; file: File; remotePath?: string; failed?: boolean; uploadProgress?: number; uploadedID?: string }

export interface InputBarHandles {
  root: HTMLElement
  bubbles: HTMLElement
  textarea: HTMLTextAreaElement
  setStreaming: (s: boolean) => void
  setUploading: (u: boolean) => void      // distinct from setStreaming — disables + and textarea
  setQueuedMsgs: (q: { uuid: string; text: string }[]) => void
  setInputText: (text: string) => void
  appendQueuedText: (text: string) => void
  setConnected: (c: boolean) => void
  getAttachments: () => AttachmentRef[]
  markAttachmentFailures: (refs: AttachmentRef[]) => void
  setAttachmentProgress: (ref: AttachmentRef, frac: number) => void
  // removeAttachments drops the listed refs from the chip strip without
  // revoking their blob URLs. Used after a successful send: the rendered
  // user-message <img> still holds a reference to previewURL, so revoking
  // here would blank the image. Failed refs NOT in the list stay in the
  // strip so the user can hit the retry button.
  removeAttachments: (refs: AttachmentRef[]) => void
  clearAttachments: (opts?: { keepSentBlobURLs?: boolean }) => void
  onAttachmentsChange: (cb: () => void) => void
  // onRetryAttachment registers the callback fired when the user taps a
  // failed chip's retry button. The callback performs the upload and
  // returns true on success (caller clears failed state) or false on
  // failure (chip stays red).
  onRetryAttachment: (cb: (ref: AttachmentRef) => Promise<boolean>) => void
  onSend: (cb: (text: string) => void) => void
  onStop: (cb: () => void) => void
  onCancelQueued: (cb: () => void) => void
  onHistoryUp: (cb: (current: string) => string | null) => void
  onHistoryDown: (cb: () => string | null) => void
  onHistoryReset: (cb: () => void) => void
  onHistoryPicker: (cb: () => string[]) => void
}

export function createInputBar(initial: {
  connected: boolean
}): InputBarHandles {
  let connected = initial.connected
  let streaming = false
  let uploading = false
  let attachments: AttachmentRef[] = []
  let attachmentsChangeCb: (() => void) | null = null
  let retryCb: ((ref: AttachmentRef) => Promise<boolean>) | null = null
  let queuedMsgs: { uuid: string; text: string }[] = []
  let sendCb: ((text: string) => void) | null = null
  let stopCb: (() => void) | null = null
  let cancelCb: (() => void) | null = null
  let historyUpCb: ((current: string) => string | null) | null = null
  let historyDownCb: (() => string | null) | null = null
  let historyResetCb: (() => void) | null = null
  let histPanelOpen = false

  const histItemClass =
    'w-full px-3 py-2 rounded-lg text-left text-[13px] text-t2 cursor-pointer leading-[1.4] truncate'

  const root = document.createElement('div')
  root.className = 'sticky bottom-0 z-10 px-5 pb-3 pt-1'

  const bubbles = document.createElement('div')
  bubbles.className = 'sticky bottom-16 z-10 px-5'

  const form = document.createElement('form')
  const card = document.createElement('div')
  card.className = 'card-bg rounded-xl border border-hairline glow-blue'
  const chipRow = document.createElement('div')
  chipRow.className = 'flex flex-wrap gap-2 px-4 pt-2 empty:hidden'
  const row = document.createElement('div')
  row.className = 'flex items-end gap-2 px-4 py-2.5'

  // Three specialized inputs surfaced through the attach popup. cameraInput
  // uses setAttribute for `capture` because the IDL property is non-standard
  // and not in TS's lib.dom.d.ts.
  // makeFileInput factory: shared change handler resets value='' after
  // dispatching so the same file can be re-picked.
  const makeFileInput = (accept: string, opts: { capture?: string; multiple?: boolean } = {}) => {
    const input = document.createElement('input')
    input.type = 'file'
    input.accept = accept
    if (opts.capture) input.setAttribute('capture', opts.capture)
    if (opts.multiple) input.multiple = true
    input.style.display = 'none'
    input.addEventListener('change', () => {
      for (const f of Array.from(input.files ?? [])) addAttachment(f)
      input.value = ''
    })
    return input
  }

  const cameraInput = makeFileInput('image/*', { capture: 'environment' })
  const imageInput = makeFileInput('image/*', { multiple: true })
  // Extension-only accept: works on desktop browsers; Android WebView ignores
  // extensions and MIME types alike for the doc set, so it falls back to
  // "any file" — acceptable since we still validate on the backend.
  const docInput = makeFileInput(
    '.pdf,.doc,.docx,.ppt,.pptx,.xls,.xlsx,.epub,.csv,.zip,.ipynb,.txt,.md,.json,.xml,.html',
    { multiple: true },
  )

  // Textarea wrap.
  const taWrap = document.createElement('div')
  taWrap.className = 'flex-1 flex justify-center min-h-[20px] cursor-text'
  const textarea = document.createElement('textarea')
  textarea.rows = 1
  textarea.placeholder = 'Sup?'
  textarea.disabled = !connected
  textarea.className =
    'bg-transparent text-[15px] text-t1 placeholder-t3 resize-none outline-none text-center disabled:opacity-40 w-fit max-w-full max-h-[120px] overflow-hidden'
  textarea.style.wordBreak = 'break-all'
  // fieldSizing is non-standard TS lib type; cast via setProperty.
  ;(textarea.style as unknown as Record<string, string>).fieldSizing = 'content'
  taWrap.appendChild(textarea)
  taWrap.addEventListener('click', () => textarea.focus())

  // + button — toggles the attach popup panel (camera/image/doc). Initial
  // state: enabled when connected, disabled when not.
  const plusBtn = document.createElement('button')
  plusBtn.type = 'button'
  plusBtn.disabled = !connected
  plusBtn.setAttribute('aria-label', 'Attach file')
  plusBtn.className =
    'flex-shrink-0 flex items-center justify-center w-7 h-7 rounded-full text-blue hover:text-white transition-colors disabled:opacity-40 disabled:cursor-not-allowed'
  plusBtn.innerHTML =
    '<svg class="h-6 w-6" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round"><path d="M12 5v14M5 12h14" /></svg>'

  // Attach popup — three icon buttons (camera/image/doc), each opening its
  // own file picker. Anchored above the + button via positionAnchoredPopup.
  let attachPanelOpen = false
  const attachPanel = createAnchoredPopup('flex items-center gap-3 px-4 py-3')
  const attachIconClass =
    'flex items-center justify-center w-9 h-9 rounded-lg text-blue hover:text-white hover:bg-blue/10 transition-colors'
  const attachSvgAttrs =
    'class="h-5 w-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"'

  const cameraBtn = document.createElement('button')
  cameraBtn.type = 'button'
  cameraBtn.setAttribute('aria-label', 'Camera')
  cameraBtn.className = attachIconClass
  cameraBtn.innerHTML =
    `<svg ${attachSvgAttrs}><path d="M14.5 4h-5L7 7H4a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V9a2 2 0 0 0-2-2h-3l-2.5-3z"/><circle cx="12" cy="13" r="3"/></svg>`
  cameraBtn.addEventListener('click', () => {
    if (plusBtn.disabled) return
    closeAttachPanel()
    cameraInput.click()
  })

  const imageBtn = document.createElement('button')
  imageBtn.type = 'button'
  imageBtn.setAttribute('aria-label', 'Image')
  imageBtn.className = attachIconClass
  imageBtn.innerHTML =
    `<svg ${attachSvgAttrs}><rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8.5" cy="8.5" r="1.5"/><path d="M21 15l-5-5L5 21"/></svg>`
  imageBtn.addEventListener('click', () => {
    if (plusBtn.disabled) return
    closeAttachPanel()
    imageInput.click()
  })

  const docBtn = document.createElement('button')
  docBtn.type = 'button'
  docBtn.setAttribute('aria-label', 'File')
  docBtn.className = attachIconClass
  docBtn.innerHTML =
    `<svg ${attachSvgAttrs}><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/></svg>`
  docBtn.addEventListener('click', () => {
    if (plusBtn.disabled) return
    closeAttachPanel()
    docInput.click()
  })

  attachPanel.appendChild(cameraBtn)
  attachPanel.appendChild(imageBtn)
  attachPanel.appendChild(docBtn)

  const closeAttachPanel = () => {
    attachPanel.classList.add('hidden')
    attachPanelOpen = false
    attachOutside.remove()
  }
  const attachOutside = createOutsideClick(plusBtn, attachPanel, closeAttachPanel)
  const openAttachPanel = () => {
    if (!attachPanel.parentElement) document.body.appendChild(attachPanel)
    attachPanel.classList.remove('hidden')
    positionAnchoredPopup(attachPanel, card)
    attachPanelOpen = true
    attachOutside.add()
  }

  plusBtn.addEventListener('click', () => {
    if (plusBtn.disabled) return
    if (attachPanelOpen) closeAttachPanel()
    else openAttachPanel()
  })

  // Send/Stop button — toggles between send and stop based on streaming state.
  const sendBtn = document.createElement('button')
  sendBtn.type = 'button'
  sendBtn.setAttribute('aria-label', 'Send')
  sendBtn.className =
    'flex-shrink-0 flex items-center justify-center w-7 h-7 rounded-full text-blue hover:text-white transition-colors pb-0.5 disabled:opacity-30'
  const sendIcon =
    '<svg class="h-6 w-6" viewBox="0 0 20 20" fill="currentColor"><path d="M3 10l14-7-7 14-2-5-5-2z" /></svg>'
  const stopIcon =
    '<span class="text-[8px] mono font-bold tracking-wide">STOP</span>'
  sendBtn.innerHTML = sendIcon

  row.appendChild(plusBtn)
  row.appendChild(taWrap)
  row.appendChild(sendBtn)
  card.appendChild(chipRow)
  card.appendChild(row)
  form.appendChild(card)
  root.appendChild(form)
  root.appendChild(cameraInput)
  root.appendChild(imageInput)
  root.appendChild(docInput)

  // History picker panel — shown when user taps send on empty input.
  let historyPickerCb: (() => string[]) | null = null
  const histPanel = createPopupPanel({ bottom: true, className: 'flex flex-col h-[300px]' })

  const histSearch = document.createElement('textarea')
  histSearch.rows = 1
  histSearch.placeholder = 'Search history...'
  histSearch.setAttribute('autocapitalize', 'off')
  histSearch.setAttribute('autocorrect', 'off')
  histSearch.spellcheck = false
  histSearch.className =
    'w-full bg-transparent px-4 py-2.5 text-[14px] text-t1 placeholder-t3 outline-none border-b border-hairline resize-none shrink-0 font-[inherit] text-[length:inherit]'
  histPanel.appendChild(histSearch)

  const histList = document.createElement('div')
  histList.className = 'flex-1 overflow-y-auto p-1 min-h-0'
  histPanel.appendChild(histList)

  const closeHistPanel = () => {
    histPanel.classList.add('hidden')
    histPanelOpen = false
    histSearch.value = ''
    histOutside.remove()
  }

  const histOutside = createOutsideClick(sendBtn, histPanel, closeHistPanel)

  const renderHistList = (items: string[]) => {
    histList.innerHTML = ''
    const query = histSearch.value.trim().toLowerCase()
    const filtered = query
      ? items.filter((s) => s.toLowerCase().includes(query))
      : items
    if (filtered.length === 0) {
      const empty = document.createElement('div')
      empty.className = 'px-3 py-4 text-center text-[13px] text-t3'
      empty.textContent = 'No history'
      histList.appendChild(empty)
      return
    }
    for (const item of filtered) {
      const el = document.createElement('div')
      el.setAttribute('role', 'button')
      el.tabIndex = 0
      el.className = histItemClass
      el.textContent = item
      el.addEventListener('click', () => {
        textarea.value = item
        textarea.focus()
        textarea.setSelectionRange(item.length, item.length)
        recomputeCanSend()
        closeHistPanel()
      })
      histList.appendChild(el)
    }
  }

  const openHistPanel = () => {
    const items = historyPickerCb?.() ?? []
    if (items.length === 0) return
    if (!histPanel.parentElement) document.body.appendChild(histPanel)
    histPanel.classList.remove('hidden')
    histPanelOpen = true
    histSearch.value = ''
    renderHistList(items)
  }

  histSearch.addEventListener('input', () => {
    const items = historyPickerCb?.() ?? []
    renderHistList(items)
  })

  sendBtn.addEventListener('click', (e) => {
    if (streaming && textarea.value.trim() === '' && attachments.length === 0) {
      e.preventDefault()
      stopCb?.()
      return
    }
    const hasInput = textarea.value.trim() !== '' || attachments.length > 0
    if (!hasInput) {
      e.preventDefault()
      if (histPanelOpen) {
        closeHistPanel()
      } else {
        openHistPanel()
        histOutside.add()
      }
    } else {
      onSubmit(e)
    }
  })

  // addAttachment pushes a new AttachmentRef into the chip strip. Image files
  // produce a blob URL for the thumbnail preview; documents only carry metadata.
  const addAttachment = (file: File) => {
    if (file.type.startsWith('image/')) {
      const ref: AttachmentRef = {
        kind: 'image',
        file,
        previewURL: URL.createObjectURL(file),
        mime: file.type,
      }
      attachments.push(ref)
    } else {
      const ref: AttachmentRef = { kind: 'document', file }
      attachments.push(ref)
    }
    renderChips()
    attachmentsChangeCb?.()
    recomputeCanSend()
  }

  // removeAttachment splices a ref out of the array and revokes its blob URL
  // (image only). Used by the chip × button.
  const removeAttachment = (ref: AttachmentRef) => {
    const i = attachments.indexOf(ref)
    if (i < 0) return
    if (ref.kind === 'image') {
      URL.revokeObjectURL(ref.previewURL)
    }
    attachments.splice(i, 1)
    renderChips()
    attachmentsChangeCb?.()
    recomputeCanSend()
  }

  // renderChips rebuilds the chip strip DOM from the attachments array.
  // Each chip is a thumbnail (image) or a [filename] label (document) with a
  // × button top-right. Failed uploads get a red border and a retry button
  // next to × — the retry button re-fires uploadOne for that ref via
  // onRetryAttachment; on success the failed flag is cleared and the chip
  // returns to its normal appearance.
  const renderChips = () => {
    chipRow.replaceChildren()
    for (const ref of attachments) {
      const wrap = document.createElement('div')
      wrap.className = 'relative'
      if (ref.kind === 'image') {
        const img = document.createElement('img')
        img.src = ref.previewURL
        img.className = 'w-12 h-12 object-cover rounded-lg'
        img.alt = ref.file.name
        if (ref.failed) {
          img.classList.add('border-2', 'border-red-500')
        }
        wrap.appendChild(img)
      } else {
        const span = document.createElement('span')
        span.className = 'font-mono text-[12px] bg-ink2 text-t2 rounded-md px-2 py-1'
        span.textContent = `[${ref.file.name}]`
        if (ref.failed) {
          span.classList.add('border-2', 'border-red-500')
        }
        wrap.appendChild(span)
      }
      if (ref.failed) {
        // Retry button: circular-arrow icon. Sibling to × so the user can
        // either retry or dismiss. disabled during an in-flight retry to
        // prevent duplicate uploads (retryCb is async).
        const retry = document.createElement('button')
        retry.type = 'button'
        retry.setAttribute('aria-label', 'Retry upload')
        retry.className =
          'absolute -top-1 -left-1 w-4 h-4 rounded-full bg-blue text-white flex items-center justify-center hover:bg-blue/80 disabled:opacity-40'
        retry.innerHTML =
          '<svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-3-6.7" /><path d="M21 3v6h-6" /></svg>'
        retry.addEventListener('click', async () => {
          if (retry.disabled) return
          retry.disabled = true
          try {
            const ok = retryCb ? await retryCb(ref) : false
            if (ok) {
              ref.failed = false
              renderChips()
              attachmentsChangeCb?.()
              recomputeCanSend()
            }
          } finally {
            retry.disabled = false
          }
        })
        wrap.appendChild(retry)
      }
      const x = document.createElement('button')
      x.type = 'button'
      x.setAttribute('aria-label', 'Remove attachment')
      x.className =
        'absolute -top-1 -right-1 w-4 h-4 rounded-full bg-ink2 text-t3 hover:text-red flex items-center justify-center text-[10px] leading-none'
      x.textContent = '×'
      x.addEventListener('click', () => removeAttachment(ref))
      wrap.appendChild(x)
      // Thin blue progress bar at the chip's bottom edge during upload.
      // Hidden once uploadProgress reaches 1 (caller then removes the chip
      // via removeAttachments so the rendered user message takes over).
      if (ref.uploadProgress !== undefined && ref.uploadProgress < 1) {
        const bar = document.createElement('div')
        bar.className = 'absolute bottom-0 left-0 right-0 h-0.5 bg-blue/20 rounded-b-lg overflow-hidden'
        const fill = document.createElement('div')
        fill.className = 'h-full bg-blue transition-[width] duration-150'
        fill.style.width = `${Math.min(Math.max(ref.uploadProgress, 0), 1) * 100}%`
        bar.appendChild(fill)
        wrap.appendChild(bar)
      }
      chipRow.appendChild(wrap)
    }
  }

  const recomputeCanSend = () => {
    const hasText = textarea.value.trim().length > 0
    const hasAttachments = attachments.length > 0
    // Button is enabled whenever connected & not uploading — even with no
    // text, because the user may want to click STOP during streaming or
    // open the history picker. The actual behavior (STOP / picker / send)
    // is decided in the click handler based on state.
    sendBtn.disabled = uploading || !connected
    if (!hasText && !hasAttachments && !streaming) {
      sendBtn.classList.add('opacity-50')
    } else {
      sendBtn.classList.remove('opacity-50')
    }
    // During streaming, button shows Stop only when input is empty.
    // Once the user types, it flips to Send so they can append a message
    // without interrupting the current query.
    if (streaming) {
      if (hasText || hasAttachments) {
        sendBtn.innerHTML = sendIcon
        sendBtn.classList.remove('pulse-blue', 'bg-blue/12')
        sendBtn.setAttribute('aria-label', 'Send')
      } else {
        sendBtn.innerHTML = stopIcon
        sendBtn.classList.add('pulse-blue', 'bg-blue/12')
        sendBtn.setAttribute('aria-label', 'Stop')
      }
    }
  }

  const onSubmit = (e: Event) => {
    e.preventDefault()
    const text = textarea.value.trim()
    if (!connected) return
    if (!text && attachments.length === 0) return
    sendCb?.(text)
    textarea.value = ''
    recomputeCanSend()
  }
  form.addEventListener('submit', onSubmit)

  textarea.addEventListener('input', () => {
    recomputeCanSend()
    historyResetCb?.()
  })

  textarea.addEventListener('keydown', (e: KeyboardEvent) => {
    // Mobile: Enter inserts newline (browser default). Send via button only.
    // Desktop: Enter sends, Shift+Enter for newline.
    // (pointer: coarse) detects true touch devices; maxTouchPoints misreports
    // on Windows laptops with precision touchpads.
    const isTouch = window.matchMedia('(pointer: coarse)').matches
    if (isTouch) return
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

    // Up: streaming → cancel queued (existing behavior); idle → history
    if (e.key === 'ArrowUp') {
      if (streaming && queuedMsgs.length > 0) {
        e.preventDefault()
        cancelCb?.()
        return
      }
      const atFirstLine =
        textarea.selectionStart === 0 ||
        textarea.value.lastIndexOf('\n', textarea.selectionStart - 1) === -1
      if (atFirstLine) {
        e.preventDefault()
        const newText = historyUpCb?.(textarea.value) ?? null
        if (newText !== null) {
          textarea.value = newText
          textarea.setSelectionRange(0, 0)
          recomputeCanSend()
        }
      }
      return
    }
    if (e.key === 'ArrowDown') {
      const atLastLine =
        textarea.selectionStart >= textarea.value.length ||
        textarea.value.indexOf('\n', textarea.selectionStart) === -1
      if (atLastLine) {
        e.preventDefault()
        const newText = historyDownCb?.() ?? null
        if (newText !== null) {
          textarea.value = newText
          const len = newText.length
          textarea.setSelectionRange(len, len)
          recomputeCanSend()
        }
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
        'mb-2 mx-auto bg-ink2/75 backdrop-blur-[20px] backdrop-saturate-[1.5] border border-hairline rounded-xl px-4 py-2 flex items-center gap-2 w-fit modal-enter cursor-pointer'
      bub.innerHTML =
        '<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" class="text-t3"><circle cx="12" cy="12" r="10" /><path d="M12 8v4M12 16h.01" /></svg>'
      const label = document.createElement('span')
      label.className =
        'text-[10px] text-t2 font-light italic truncate max-w-[240px]'
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
    bubbles,
    textarea,
    setStreaming: (s: boolean) => {
      streaming = s
      if (s) {
        // Respect current input state: empty → Stop, non-empty → Send
        // (recomputeCanSend handles the icon/aria-label flip).
        recomputeCanSend()
      } else {
        sendBtn.innerHTML = sendIcon
        sendBtn.classList.remove('pulse-blue', 'bg-blue/12')
        sendBtn.setAttribute('aria-label', 'Send')
        queuedMsgs = []
      }
      renderBubbles()
    },
    // setUploading disables the textarea, + button, AND send button for the
    // duration of an upload. It does NOT toggle the STOP icon — STOP aborts
    // the engine, and we are uploading, not streaming. Distinct from
    // setStreaming to avoid conflating the two states.
    setUploading: (u: boolean) => {
      uploading = u
      textarea.disabled = u || !connected
      plusBtn.disabled = u || !connected
      if (u && attachPanelOpen) closeAttachPanel()
      recomputeCanSend()
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
    getAttachments: () => attachments,
    markAttachmentFailures: (refs: AttachmentRef[]) => {
      // Mutate the failed flag on each ref and re-render. Refs are compared
      // by reference (they live in the attachments array), so the matching
      // chip gets the red border applied by renderChips.
      for (const ref of refs) {
        ref.failed = true
      }
      renderChips()
    },
    setAttachmentProgress: (ref: AttachmentRef, frac: number) => {
      ref.uploadProgress = frac
      renderChips()
    },
    removeAttachments: (refs: AttachmentRef[]) => {
      // Drop the given refs from the chip strip without revoking blob URLs.
      // Used after a successful send: each ref's previewURL is still
      // referenced by the rendered user-message <img>, so revoking here
      // would blank the image. Failed refs not in the list stay in the
      // strip so the user can hit retry.
      const removeSet = new Set(refs)
      attachments = attachments.filter((r) => !removeSet.has(r))
      renderChips()
      attachmentsChangeCb?.()
      recomputeCanSend()
    },
    clearAttachments: (opts?: { keepSentBlobURLs?: boolean }) => {
      // When keepSentBlobURLs is true (post-send), do NOT revoke blob URLs —
      // they are still referenced by the just-rendered <img> in the user
      // message. The browser GCs them on page unload. Otherwise (× button on
      // a not-yet-sent chip, or session switch) revoke them.
      if (!opts?.keepSentBlobURLs) {
        for (const ref of attachments) {
          if (ref.kind === 'image') {
            URL.revokeObjectURL(ref.previewURL)
          }
        }
      }
      attachments = []
      renderChips()
      attachmentsChangeCb?.()
      recomputeCanSend()
    },
    onAttachmentsChange: (cb: () => void) => {
      attachmentsChangeCb = cb
    },
    onRetryAttachment: (cb: (ref: AttachmentRef) => Promise<boolean>) => {
      retryCb = cb
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
    onHistoryUp: (cb) => {
      historyUpCb = cb
    },
    onHistoryDown: (cb) => {
      historyDownCb = cb
    },
    onHistoryReset: (cb) => {
      historyResetCb = cb
    },
    onHistoryPicker: (cb) => {
      historyPickerCb = cb
    },
    setConnected: (c: boolean) => {
      connected = c
      textarea.disabled = !c
      plusBtn.disabled = !c
      if (!c && attachPanelOpen) closeAttachPanel()
      recomputeCanSend()
    },
  }
}
