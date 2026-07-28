import { createPopupPanel, createAnchoredPopup, positionAnchoredPopup, createPopupHost } from './utils'
import { createElement, createNode } from './dom'
import { renderIcon } from './icons'
import { createIconButton } from './buttons'

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
  // paste refs carry failed?/uploadProgress? purely for union compat —
  // markAttachmentFailures and setAttachmentProgress write to the
  // unconstrained AttachmentRef type. Paste never enters the upload loop
  // (filtered in chat.ts onSend), so these stay undefined at runtime and
  // renderChips' guards skip them.
  | { kind: 'paste'; text: string; lineCount: number; seq: number; failed?: boolean; uploadProgress?: number }

// INTENTIONAL DIVERGENCE from the TUI (pkg/tui/app.go newlineCount at lines
// 1736-1744), which does `\r`→`\n` then counts `\n` — that double-counts
// `\r\n` (a Windows paste of N CRLFs yields newlineCount === 2N). This
// implementation counts `\r\n` as ONE line break (skipping the trailing
// `\n`), matching what a user sees in an editor. Bare `\r` and bare `\n`
// each count as 1.
function countNewlines(text: string): number {
  let count = 0
  for (let i = 0; i < text.length; i++) {
    const ch = text.charCodeAt(i)
    if (ch === 0x0d) { count++; i++; continue }
    if (ch === 0x0a) { count++ }
  }
  return count
}

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
  let nextPasteID = 1

  const histItemClass =
    'w-full px-3 py-2 rounded-lg text-left text-[13px] text-t2 cursor-pointer leading-[1.4] truncate'

  const root = createElement('div', 'sticky bottom-0 z-10 px-5 pb-3 pt-1')

  const bubbles = createElement('div', 'sticky bottom-16 z-10 px-5')

  const form = createElement('form')
  const card = createElement('div', 'card-bg rounded-xl border border-hairline glow-blue')
  const chipRow = createElement('div', 'flex flex-wrap gap-2 px-4 pt-2 empty:hidden')
  const row = createElement('div', 'flex items-end gap-2 px-4 py-2.5')

  // Three specialized inputs surfaced through the attach popup. cameraInput
  // uses setAttribute for `capture` because the IDL property is non-standard
  // and not in TS's lib.dom.d.ts.
  // makeFileInput factory: shared change handler resets value='' after
  // dispatching so the same file can be re-picked.
  const makeFileInput = (accept: string, opts: { capture?: string; multiple?: boolean } = {}) => {
    const input = createNode('input', {
      props: { type: 'file', accept },
      attrs: opts.capture ? { capture: opts.capture } : undefined,
      style: { display: 'none' },
    })
    if (opts.multiple) input.multiple = true
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
  const taWrap = createElement('div', 'flex-1 flex justify-center min-h-[20px] cursor-text')
  const textarea = createNode('textarea', {
    className:
      'bg-transparent text-[15px] text-t1 placeholder-t3 resize-none outline-none text-center disabled:opacity-40 w-fit max-w-full max-h-[120px] overflow-hidden',
    props: { rows: 1, placeholder: 'Sup?', disabled: !connected },
    style: { wordBreak: 'break-all' },
  })
  // fieldSizing is non-standard TS lib type; cast via setProperty.
  ;(textarea.style as unknown as Record<string, string>).fieldSizing = 'content'
  taWrap.appendChild(textarea)
  taWrap.addEventListener('click', () => textarea.focus())

  // + button — toggles the attach popup panel (camera/image/doc). Initial
  // state: enabled when connected, disabled when not.
  // Caller-side attach-popup-toggle state machine (plusBtn.addEventListener
  // below) drives open/close via attachHost; the factory stays out of the
  // click path (no onClick), so disabled gating and panel coordination stay
  // at the call site exactly as before.
  const plusBtn = createIconButton({
    icon: 'plus',
    label: 'Attach file',
    variant: 'default',
    size: 'sm',
    iconSize: 24,
    className: 'flex-shrink-0 disabled:opacity-40 disabled:cursor-not-allowed',
  })
  plusBtn.disabled = !connected

  // Attach popup — three icon buttons (camera/image/doc), each opening its
  // own file picker. Anchored above the + button via positionAnchoredPopup.
  const attachPanel = createAnchoredPopup('flex items-center gap-3 px-4 py-3')

  const cameraBtn = createIconButton({
    icon: 'camera',
    label: 'Camera',
    variant: 'subtle',
    size: 'lg',
    iconSize: 20,
    className: 'flex-shrink-0',
    onClick: () => {
      if (plusBtn.disabled) return
      closeAttachPanel()
      cameraInput.click()
    },
  })

  const imageBtn = createIconButton({
    icon: 'image',
    label: 'Image',
    variant: 'subtle',
    size: 'lg',
    iconSize: 20,
    className: 'flex-shrink-0',
    onClick: () => {
      if (plusBtn.disabled) return
      closeAttachPanel()
      imageInput.click()
    },
  })

  const docBtn = createIconButton({
    icon: 'file',
    label: 'File',
    variant: 'subtle',
    size: 'lg',
    iconSize: 20,
    className: 'flex-shrink-0',
    onClick: () => {
      if (plusBtn.disabled) return
      closeAttachPanel()
      docInput.click()
    },
  })

  attachPanel.appendChild(cameraBtn)
  attachPanel.appendChild(imageBtn)
  attachPanel.appendChild(docBtn)

  // Built once at construct time so the open/close handlers can reference
  // its textarea in their closures. Lazily appended to body on first open
  // (avoids the chipRow's empty:hidden rule). No Save/Cancel buttons —
  // edits are auto-saved to editingRef on every input event; close popup
  // by clicking outside or pressing Esc.
  let editPopupOpen = false
  let editingRef: Extract<AttachmentRef, { kind: 'paste' }> | null = null
  const editPopup = createPopupPanel({ bottom: true, className: 'p-0 w-[90vw] max-w-sm overflow-hidden' })
  // editPopup comes from createPopupPanel (no attrs channel on that helper);
  // setAttribute keeps data-* routing consistent with the pasteChip element
  // built via createNode({ attrs: { 'data-paste-chip': '' } }).
  editPopup.setAttribute('data-edit-popup', '')
  const editTextarea = createNode('textarea', {
    className: 'w-full bg-transparent text-t1 text-[13px] resize-none outline-none p-3 font-mono',
    props: { rows: 8 },
  })
  editPopup.appendChild(editTextarea)

  const attachHost = createPopupHost({
    trigger: plusBtn,
    panel: attachPanel,
    onOpen: () => positionAnchoredPopup(attachPanel, card),
  })
  const openAttachPanel = () => attachHost.open()
  const closeAttachPanel = () => attachHost.close()

  plusBtn.addEventListener('click', () => {
    if (plusBtn.disabled) return
    if (attachHost.isOpen()) closeAttachPanel()
    else openAttachPanel()
  })

  // Send/Stop button — toggles between send and stop based on streaming state.
  // Caller-side STOP/send/history-picker state machine (sendBtn.addEventListener
  // below) drives behavior; factory supplies the initial Send icon and stays
  // out of the click path. Runtime icon swap (replaceChildren(renderSendIcon())
  // / innerHTML = stopIcon) replaces the factory-installed svg directly.
  const sendBtn = createIconButton({
    icon: 'send',
    label: 'Send',
    variant: 'default',
    size: 'sm',
    iconSize: 24,
    className: 'flex-shrink-0 disabled:opacity-30',
  })
  const renderSendIcon = (): SVGElement => renderIcon('send', { className: 'h-6 w-6' })
  const stopIcon =
    '<span class="text-[8px] mono font-bold tracking-wide">STOP</span>'

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

  const histSearchWrap = createElement('div', 'flex items-center gap-2 mx-3 my-2 px-3 py-2 rounded-lg bg-ink3/40 shrink-0')
  histSearchWrap.appendChild(renderIcon('search', { size: 14, className: 'text-t3 shrink-0' }))
  const histSearch = createNode('textarea', {
    className:
      'flex-1 bg-transparent text-[13px] text-t1 placeholder-t3 outline-none resize-none',
    props: { rows: 1, placeholder: 'Search...', spellcheck: false },
    attrs: { autocapitalize: 'off', autocorrect: 'off' },
    style: { fontFamily: 'inherit' },
  })
  histSearchWrap.appendChild(histSearch)
  histPanel.appendChild(histSearchWrap)

  const histList = createElement('div', 'flex-1 overflow-y-auto p-1 min-h-0')
  histPanel.appendChild(histList)

  // onClose clears the search box so a subsequent open starts fresh; onOpen
  // re-renders the list from the current historyPickerCb snapshot. The
  // openHistPanel wrapper still does the empty-items early return so an empty
  // history doesn't arm the outside-click listener.
  const histHost = createPopupHost({
    trigger: sendBtn,
    panel: histPanel,
    onOpen: () => {
      histSearch.value = ''
      renderHistList(historyPickerCb?.() ?? [])
    },
    onClose: () => { histSearch.value = '' },
  })
  const closeHistPanel = () => histHost.close()

  const renderHistList = (items: string[]) => {
    histList.innerHTML = ''
    const query = histSearch.value.trim().toLowerCase()
    const filtered = query
      ? items.filter((s) => s.toLowerCase().includes(query))
      : items
    if (filtered.length === 0) {
      const empty = createElement('div', 'px-3 py-4 text-center text-[13px] text-t3')
      empty.textContent = 'No history'
      histList.appendChild(empty)
      return
    }
    for (const item of filtered) {
      const el = createNode('div', {
        className: histItemClass,
        attrs: { role: 'button' },
        props: { tabIndex: 0 },
        text: item,
      })
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
    histHost.open()
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
      if (histHost.isOpen()) {
        closeHistPanel()
      } else {
        openHistPanel()
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

  // addPasteAttachment captures large/multi-line pastes as a chip instead of
  // inserting them into the textarea. Threshold check happens in the paste
  // listener. seq is stable: removing paste #1 does NOT renumber paste #2.
  const addPasteAttachment = (text: string) => {
    const lineCount = countNewlines(text)
    const ref: AttachmentRef = { kind: 'paste', text, lineCount, seq: nextPasteID }
    nextPasteID++
    attachments.push(ref)
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
    if (!attachments.some(r => r.kind === 'paste')) nextPasteID = 1
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
      const wrap = createElement('div', 'relative')
      if (ref.kind === 'image') {
        const img = createElement('img', 'w-12 h-12 object-cover rounded-lg')
        img.src = ref.previewURL
        img.alt = ref.file.name
        if (ref.failed) {
          img.classList.add('border-2', 'border-red-500')
        }
        wrap.appendChild(img)
      } else if (ref.kind === 'document') {
        const span = createElement('span', 'font-mono text-[12px] bg-ink2 text-t2 rounded-md px-2 py-1')
        span.textContent = `[${ref.file.name}]`
        if (ref.failed) {
          span.classList.add('border-2', 'border-red-500')
        }
        wrap.appendChild(span)
      } else {
        // Paste chip: clipboard icon + #N [+L lines] label + ~20-char
        // preview. data-paste-chip marker is read by the edit popup's
        // outside-click handler to keep the popup open when switching chips.
        const click = createNode('div', {
          className: 'flex items-center gap-1.5 bg-ink2 text-t2 rounded-md pl-2 pr-3 py-1 cursor-pointer hover:bg-ink2/80',
          attrs: { 'data-paste-chip': '', role: 'button' },
          props: { tabIndex: 0 },
        })
        // 'clipboard' is not in IconName; kept inline as a multi-string concat.
        click.innerHTML =
          '<svg class="h-3 w-3 shrink-0 text-blue" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
          '<rect x="8" y="2" width="8" height="4" rx="1"/>' +
          '<path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/>' +
          '</svg>'
        const labelSpan = createElement('span', 'font-mono text-[11px] leading-tight whitespace-nowrap')
        // Display as visual line count (newline count + 1). A single-line
        // 800+ char paste shows "+1 lines"; a 4-line paste shows "+4 lines".
        labelSpan.textContent = `#${ref.seq} +${ref.lineCount + 1} lines`
        const preview = createElement('span', 'text-[10px] text-t3 truncate max-w-[140px] leading-tight')
        preview.textContent = ref.text.slice(0, 20).replace(/\n/g, ' ')
        const stack = createElement('div', 'flex flex-col min-w-0')
        stack.append(labelSpan, preview)
        click.append(stack)
        click.addEventListener('click', () => {
          // Toggle: if popup is already showing this ref, close it.
          if (editPopupOpen && editingRef === ref) {
            closeEditPopup()
            return
          }
          openEditPopup(ref)
        })
        click.addEventListener('keydown', (e: KeyboardEvent) => {
          if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); openEditPopup(ref) }
        })
        wrap.appendChild(click)
      }
      if (ref.failed) {
        // Retry button: circular-arrow icon. Sibling to × so the user can
        // either retry or dismiss. disabled during an in-flight retry to
        // prevent duplicate uploads (retryCb is async).
        // Caller-side disabled-state management (retry.disabled = true/false
        // in the async handler) replaces the factory's onClick — we don't
        // pass onClick because the in-flight guard needs to read/set
        // retry.disabled, which requires the button reference.
        const retry = createIconButton({
          icon: 'refresh',
          label: 'Retry upload',
          variant: 'solid',
          size: 'xs',
          iconSize: 9,
          strokeWidth: 2.5,
          className: 'absolute -top-1 -left-1 disabled:opacity-40',
        })
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
      const x = createIconButton({
        icon: 'x',
        label: 'Remove attachment',
        variant: 'ghost',
        size: 'xs',
        iconSize: 10,
        className: 'absolute -top-1 -right-1 bg-ink2 text-t3 hover:text-red',
        onClick: () => removeAttachment(ref),
      })
      wrap.appendChild(x)
      // Thin blue progress bar at the chip's bottom edge during upload.
      // Hidden once uploadProgress reaches 1 (caller then removes the chip
      // via removeAttachments so the rendered user message takes over).
      if (ref.uploadProgress !== undefined && ref.uploadProgress < 1) {
        const bar = createElement('div', 'absolute bottom-0 left-0 right-0 h-0.5 bg-blue/20 rounded-b-lg overflow-hidden')
        const fill = createElement('div', 'h-full bg-blue transition-[width] duration-150')
        fill.style.width = `${Math.min(Math.max(ref.uploadProgress, 0), 1) * 100}%`
        bar.appendChild(fill)
        wrap.appendChild(bar)
      }
      chipRow.appendChild(wrap)
    }
  }

  // Lazily appends to body on first open so it never appears in the input
  // bar DOM subtree (avoids the chipRow's empty:hidden rule and lets the
  // popup float freely).
  const openEditPopup = (ref: Extract<AttachmentRef, { kind: 'paste' }>) => {
    if (!editPopup.parentElement) document.body.appendChild(editPopup)
    editingRef = ref
    editTextarea.value = ref.text
    editPopup.classList.remove('hidden')
    editPopupOpen = true
    editTextarea.focus()
  }
  const closeEditPopup = () => {
    editPopup.classList.add('hidden')
    editPopupOpen = false
    editingRef = null
  }
  // Auto-save on every input — no Save button. Edits go directly to
  // editingRef.text/lineCount and the chip re-renders to reflect new preview.
  editTextarea.addEventListener('input', () => {
    if (!editingRef) return
    editingRef.text = editTextarea.value
    editingRef.lineCount = countNewlines(editingRef.text)
    renderChips()
    attachmentsChangeCb?.()
  })
  // Esc closes the popup.
  editTextarea.addEventListener('keydown', (e: KeyboardEvent) => {
    if (e.key === 'Escape') closeEditPopup()
  })
  // Outside-click dismiss — but clicks on another paste chip are exempt: the
  // chip's own click handler then calls openEditPopup, repopulating rather
  // than flicker-closing. Order of dispatch: the chip's click is a 'click'
  // event that fires AFTER mousedown, so the chip mousedown would close the
  // popup before its click repopulates. Exempting [data-paste-chip] mousedowns
  // keeps the popup open through the click.
  document.addEventListener('mousedown', (e: MouseEvent) => {
    if (!editPopupOpen) return
    const target = e.target as HTMLElement
    if (editPopup.contains(target)) return
    if (target.closest('[data-paste-chip]')) return
    closeEditPopup()
  })

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
        sendBtn.replaceChildren(renderSendIcon())
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

  // Paste compression: large or multi-line pastes become a paste chip
  // rather than inserted into the textarea. Threshold matches the TUI
  // (pkg/tui/app.go:1742): >800 chars OR >2 newlines. Small pastes fall
  // through to the browser default (insert-text) action.
  //
  // Uses beforeinput + inputType === 'insertFromPaste' instead of the
  // 'paste' event: Android WebView's IME paste (especially Samsung Keyboard
  // clipboard history) often bypasses the paste event entirely. beforeinput
  // is the spec-compliant way to intercept text insertion and works across
  // desktop Chrome, Firefox, Safari, and Android WebView.
  textarea.addEventListener('beforeinput', (e: InputEvent) => {
    // Android IME (Sogou/Baidu etc.) sometimes dispatches insertText
    // instead of insertFromPaste for clipboard operations. Accept both —
    // the threshold check (>800 chars or >2 newlines) filters out normal
    // typing, which never produces that much text in a single event.
    if (e.inputType !== 'insertFromPaste' && e.inputType !== 'insertText') return
    const text = e.dataTransfer?.getData('text/plain') ?? e.data ?? ''
    if (!text) return
    const lineCount = countNewlines(text)
    if (text.length > 800 || lineCount > 2) {
      e.preventDefault()
      addPasteAttachment(text)
    }
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
      const bub = createElement(
        'div',
        'mb-2 mx-auto bg-ink2/75 backdrop-blur-[20px] backdrop-saturate-[1.5] border border-hairline rounded-xl px-4 py-2 flex items-center gap-2 w-fit modal-enter cursor-pointer',
      )
      bub.replaceChildren(renderIcon('dot', { size: 11, strokeWidth: 2.5, className: 'text-t3' }))
      const label = createElement('span', 'text-[10px] text-t2 font-light italic truncate max-w-[240px]')
      label.textContent = m.text
      bub.appendChild(label)
      if (i === 0 && queuedMsgs.length > 1) {
        const more = createElement('span', 'text-[10px] text-t3 mono ml-1')
        more.textContent = `+${queuedMsgs.length - 1} more`
        bub.appendChild(more)
      }
      const cancelLabel = createElement('span', 'text-[10px] text-t3 mono ml-1')
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
        sendBtn.replaceChildren(renderSendIcon())
        sendBtn.classList.remove('pulse-blue', 'bg-blue/12')
        sendBtn.setAttribute('aria-label', 'Send')
        queuedMsgs = []
        // Re-evaluate opacity-50: query end with empty input should dim.
        recomputeCanSend()
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
      if (u && attachHost.isOpen()) closeAttachPanel()
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
      if (!attachments.some(r => r.kind === 'paste')) nextPasteID = 1
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
      nextPasteID = 1
      if (editPopupOpen) closeEditPopup()
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
      if (!c && attachHost.isOpen()) closeAttachPanel()
      recomputeCanSend()
    },
  }
}
