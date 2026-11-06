import { describe, it, expect, vi, afterEach } from 'vitest'
import { createInputBar } from './input_bar'

afterEach(() => {
  document.body.innerHTML = ''
})

function mount() {
  const ib = createInputBar({ connected: true })
  document.body.appendChild(ib.bubbles)
  document.body.appendChild(ib.root)
  return ib
}

function typeEnter(ta: HTMLTextAreaElement, shift = false) {
  ta.dispatchEvent(
    new KeyboardEvent('keydown', {
      key: 'Enter',
      shiftKey: shift,
      bubbles: true,
    }),
  )
}

function submitForm(ta: HTMLTextAreaElement) {
  const form = ta.closest('form')!
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
}

// Drives the beforeinput paste-compression path (threshold: >800 chars or
// >2 newlines) without a real clipboard.
function bigPaste(ta: HTMLTextAreaElement, text: string) {
  ta.dispatchEvent(
    new InputEvent('beforeinput', {
      inputType: 'insertFromPaste',
      data: text,
      bubbles: true,
      cancelable: true,
    }),
  )
}

// ClipboardEvent.clipboardData is read-only on the real event; jsdom lets us
// install a plain fake. items is the canonical route, files the fallback —
// a null files list exercises the "engine gave us nothing" guard.
function pasteClipboardFiles(
  ta: HTMLTextAreaElement,
  entries: { kind?: string; file?: File | null }[],
  files: File[] | null = [],
) {
  const ev = new Event('paste', { bubbles: true, cancelable: true })
  Object.defineProperty(ev, 'clipboardData', {
    value: {
      items: entries.map((e) => ({
        kind: e.kind ?? 'file',
        getAsFile: () => e.file ?? null,
      })),
      files,
    },
  })
  ta.dispatchEvent(ev)
  return ev
}

function dropFiles(ib: ReturnType<typeof createInputBar>, files: File[]) {
  const card = ib.root.querySelector('.card-bg') as HTMLElement
  const ev = new Event('drop', { bubbles: true, cancelable: true })
  Object.defineProperty(ev, 'dataTransfer', { value: { files } })
  card.dispatchEvent(ev)
  return ev
}

function sendBtnOf(ib: ReturnType<typeof createInputBar>): HTMLButtonElement {
  return ib.root.querySelector('button[aria-label="Send"]') as HTMLButtonElement
}

function micOf(ib: ReturnType<typeof createInputBar>): HTMLButtonElement {
  return ib.root.querySelector('button[aria-label="Voice input"]') as HTMLButtonElement
}

function cancelableKeydown(el: HTMLElement, key: string, init: KeyboardEventInit = {}) {
  const ev = new KeyboardEvent('keydown', {
    key,
    bubbles: true,
    cancelable: true,
    ...init,
  })
  el.dispatchEvent(ev)
  return ev
}

describe('createInputBar', () => {
  it('renders textarea placeholder Sup?', () => {
    const ib = mount()
    expect(ib.textarea.placeholder).toBe('Sup?')
  })

  it('setStreaming(true) shows STOP button', () => {
    const ib = mount()
    ib.setStreaming(true)
    expect(ib.root.textContent).toContain('STOP')
    const btn = ib.root.querySelector('button[aria-label="Stop"]')!
    expect(btn.classList.contains('pulse-blue')).toBe(true)
  })

  it('setStreaming(false) reverts button to send state', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.setStreaming(false)
    const btn = ib.root.querySelector('button[aria-label="Send"]')!
    expect(btn).toBeTruthy()
    expect(btn.classList.contains('pulse-blue')).toBe(false)
    expect(ib.root.textContent).not.toContain('STOP')
  })

  it('streaming + typing text flips button from Stop to Send', () => {
    const ib = mount()
    ib.setStreaming(true)
    // Initially empty input → Stop button
    expect(ib.root.querySelector('button[aria-label="Stop"]')).toBeTruthy()
    // User types → button must flip to Send
    ib.textarea.value = 'follow-up question'
    ib.textarea.dispatchEvent(new Event('input', { bubbles: true }))
    const sendBtn = ib.root.querySelector('button[aria-label="Send"]')
    expect(sendBtn).toBeTruthy()
    expect(sendBtn!.classList.contains('pulse-blue')).toBe(false)
    expect(ib.root.querySelector('button[aria-label="Stop"]')).toBeNull()
  })

  it('streaming + clear text reverts button from Send back to Stop', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.textarea.value = 'x'
    ib.textarea.dispatchEvent(new Event('input', { bubbles: true }))
    expect(ib.root.querySelector('button[aria-label="Send"]')).toBeTruthy()
    // Clear the text → button flips back to Stop
    ib.textarea.value = ''
    ib.textarea.dispatchEvent(new Event('input', { bubbles: true }))
    expect(ib.root.querySelector('button[aria-label="Stop"]')).toBeTruthy()
    expect(ib.root.querySelector('button[aria-label="Send"]')).toBeNull()
  })

  it('streaming + text + Send click fires onSend (does NOT stop)', () => {
    const ib = mount()
    const sendSpy = vi.fn()
    const stopSpy = vi.fn()
    ib.onSend(sendSpy)
    ib.onStop(stopSpy)
    ib.setStreaming(true)
    ib.textarea.value = 'follow-up'
    ib.textarea.dispatchEvent(new Event('input', { bubbles: true }))
    const sendBtn = ib.root.querySelector('button[aria-label="Send"]') as HTMLButtonElement
    sendBtn.click()
    expect(sendSpy).toHaveBeenCalledWith('follow-up')
    expect(stopSpy).not.toHaveBeenCalled()
  })

  it('setQueuedMsgs single shows Tap to CANCEL', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.setQueuedMsgs([{ uuid: 'u-1', text: 'hello' }])
    expect(ib.bubbles.textContent).toContain('hello')
    expect(ib.bubbles.textContent).toContain('Tap to CANCEL')
    expect(ib.bubbles.textContent).not.toContain('Tap to CANCEL all')
  })

  it('setQueuedMsgs multiple shows Tap to CANCEL all on first bubble', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.setQueuedMsgs([
      { uuid: 'u-1', text: 'one' },
      { uuid: 'u-2', text: 'two' },
    ])
    expect(ib.bubbles.textContent).toContain('Tap to CANCEL all')
    // Second bubble should still show plain CANCEL — assert exact count.
    const allText = ib.bubbles.textContent ?? ''
    expect(allText.indexOf('Tap to CANCEL all')).toBeGreaterThanOrEqual(0)
  })

  it('setQueuedMsgs with two items renders exactly two bubbles', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.setQueuedMsgs([
      { uuid: 'u-1', text: 'one' },
      { uuid: 'u-2', text: 'two' },
    ])
    const bubbles = ib.bubbles.querySelectorAll('.modal-enter')
    expect(bubbles.length).toBe(2)
  })

  it('setQueuedMsgs with attachments renders attachment chips next to text', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.setQueuedMsgs([{
      uuid: 'u-1',
      text: 'review these',
      attachments: [
        { name: 'report.pdf', mime: 'application/pdf' },
        { mime: 'image/png' },
      ],
    }])
    expect(ib.bubbles.textContent).toContain('review these')
    expect(ib.bubbles.textContent).toContain('[report.pdf]')
    // image entries without a name (backend snapshot sends mime only) fall back to the mime
    expect(ib.bubbles.textContent).toContain('[image/png]')
    // chips reuse the user-message attachment chip classes (font-mono + bg-ink2)
    const chips = ib.bubbles.querySelectorAll('span.font-mono.bg-ink2')
    expect(chips.length).toBe(2)
    expect(chips[0].textContent).toBe('[report.pdf]')
    expect(chips[1].textContent).toBe('[image/png]')
  })

  it('setQueuedMsgs without attachments renders no chips', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.setQueuedMsgs([{ uuid: 'u-1', text: 'plain' }])
    expect(ib.bubbles.querySelectorAll('span.font-mono.bg-ink2').length).toBe(0)
  })

  it('setInputText sets textarea value', () => {
    const ib = mount()
    ib.setInputText('hi')
    expect(ib.textarea.value).toBe('hi')
  })

  it('bubbles is separate from root (no card-bg backdrop bleed)', () => {
    const ib = mount()
    expect(ib.bubbles).not.toBe(ib.root)
    expect(ib.bubbles.parentElement).not.toBe(ib.root)
    expect(ib.bubbles.className).not.toContain('card-bg')
    const card = ib.root.querySelector('.card-bg')
    expect(card).not.toBeNull()
  })

  it('appendQueuedText with empty textarea sets value', () => {
    const ib = mount()
    ib.appendQueuedText('queued')
    expect(ib.textarea.value).toBe('queued')
  })

  it('appendQueuedText with existing text prefixes with newline', () => {
    const ib = mount()
    ib.setInputText('a')
    ib.appendQueuedText('queued')
    expect(ib.textarea.value).toBe('queued\na')
  })

  it('appendQueuedText empty arg is a no-op', () => {
    const ib = mount()
    ib.setInputText('a')
    ib.appendQueuedText('')
    expect(ib.textarea.value).toBe('a')
  })

  it('Enter without Shift fires onSend with trimmed text and clears', () => {
    const ib = mount()
    const spy = vi.fn()
    ib.onSend(spy)
    ib.textarea.value = '  hello world  '
    typeEnter(ib.textarea)
    expect(spy).toHaveBeenCalledWith('hello world')
    expect(ib.textarea.value).toBe('')
  })

  it('Enter with Shift does NOT fire onSend', () => {
    const ib = mount()
    const spy = vi.fn()
    ib.onSend(spy)
    ib.textarea.value = 'multi\nline'
    typeEnter(ib.textarea, true)
    expect(spy).not.toHaveBeenCalled()
  })

  it('ArrowUp while streaming with non-empty queue fires onCancelQueued', () => {
    const ib = mount()
    const spy = vi.fn()
    ib.onCancelQueued(spy)
    ib.setStreaming(true)
    ib.setQueuedMsgs([{ uuid: 'u-1', text: 'hello' }])
    ib.textarea.dispatchEvent(
      new KeyboardEvent('keydown', {
        key: 'ArrowUp',
        bubbles: true,
      }),
    )
    expect(spy).toHaveBeenCalledOnce()
  })

  it('ArrowUp while NOT streaming does NOT fire onCancelQueued', () => {
    const ib = mount()
    const spy = vi.fn()
    ib.onCancelQueued(spy)
    ib.textarea.dispatchEvent(
      new KeyboardEvent('keydown', {
        key: 'ArrowUp',
        bubbles: true,
      }),
    )
    expect(spy).not.toHaveBeenCalled()
  })

  it('clicking STOP fires onStop', () => {
    const ib = mount()
    const spy = vi.fn()
    ib.onStop(spy)
    ib.setStreaming(true)
    const stopBtn = ib.root.querySelector('button[aria-label="Stop"]')!
    stopBtn.click()
    expect(spy).toHaveBeenCalledOnce()
  })

  it('clicking a queued bubble fires onCancelQueued', () => {
    const ib = mount()
    const spy = vi.fn()
    ib.onCancelQueued(spy)
    ib.setStreaming(true)
    ib.setQueuedMsgs([{ uuid: 'u-1', text: 'hello' }])
    const bubble = ib.bubbles.querySelector('.modal-enter') as HTMLElement
    bubble.click()
    expect(spy).toHaveBeenCalledOnce()
  })

  it('connected:false disables send button initially', () => {
    const ib = createInputBar({ connected: false })
    document.body.appendChild(ib.root)
    const sendBtn = ib.root.querySelector(
      'button[aria-label="Send"]',
    ) as HTMLButtonElement
    expect(sendBtn.disabled).toBe(true)
  })

  it('setConnected(true) enables textarea and send button', () => {
    const ib = createInputBar({ connected: false })
    document.body.appendChild(ib.root)
    const textarea = ib.textarea
    const sendBtn = ib.root.querySelector(
      'button[aria-label="Send"]',
    ) as HTMLButtonElement
    expect(textarea.disabled).toBe(true)
    ib.setConnected(true)
    expect(textarea.disabled).toBe(false)
    textarea.value = 'hello'
    textarea.dispatchEvent(new Event('input', { bubbles: true }))
    expect(sendBtn.disabled).toBe(false)
  })

  it('connected:false does not fire onSend on Enter', () => {
    const ib = createInputBar({ connected: false })
    document.body.appendChild(ib.root)
    const spy = vi.fn()
    ib.onSend(spy)
    ib.textarea.value = 'hello'
    typeEnter(ib.textarea)
    expect(spy).not.toHaveBeenCalled()
  })

  it('typing enables send button; clearing keeps it enabled for history picker', () => {
    const ib = mount()
    const sendBtn = ib.root.querySelector(
      'button[aria-label="Send"]',
    ) as HTMLButtonElement
    ib.textarea.value = 'x'
    ib.textarea.dispatchEvent(new Event('input', { bubbles: true }))
    expect(sendBtn.disabled).toBe(false)
    ib.textarea.value = ''
    ib.textarea.dispatchEvent(new Event('input', { bubbles: true }))
    // Send button stays enabled when connected + empty so user can tap
    // it to open the history picker.
    expect(sendBtn.disabled).toBe(false)
  })

  it('form submit clears textarea', () => {
    const ib = mount()
    ib.onSend(() => {})
    ib.textarea.value = 'sent'
    submitForm(ib.textarea)
    expect(ib.textarea.value).toBe('')
  })

  it('setStreaming(false) dims send button when input is empty', () => {
    const ib = mount()
    const sendBtn = ib.root.querySelector(
      'button[aria-label="Send"]',
    ) as HTMLButtonElement
    // Type and enter streaming.
    ib.textarea.value = 'hello'
    ib.textarea.dispatchEvent(new Event('input', { bubbles: true }))
    ib.setStreaming(true)
    // Form submit clears textarea; query then ends.
    ib.textarea.value = ''
    ib.textarea.dispatchEvent(new Event('input', { bubbles: true }))
    ib.setStreaming(false)
    // Query end with empty input must re-dim the send icon.
    expect(sendBtn.className).toContain('opacity-50')
  })

  // ── Input history navigation ──

  function setCursor(ta: HTMLTextAreaElement, pos: number) {
    ta.selectionStart = pos
    ta.selectionEnd = pos
  }

  function dispatchArrow(
    ta: HTMLTextAreaElement,
    key: 'ArrowUp' | 'ArrowDown',
  ) {
    ta.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
  }

  it('ArrowUp when NOT streaming and historyUpCb returns text sets textarea value and cursor to start', () => {
    const ib = mount()
    ib.onHistoryUp(() => 'old query')
    ib.textarea.value = 'cur'
    setCursor(ib.textarea, 3)
    dispatchArrow(ib.textarea, 'ArrowUp')
    expect(ib.textarea.value).toBe('old query')
    expect(ib.textarea.selectionStart).toBe(0)
  })

  it('ArrowUp when NOT streaming and historyUpCb returns null is a no-op', () => {
    const ib = mount()
    const upSpy = vi.fn(() => null as string | null)
    ib.onHistoryUp(upSpy)
    ib.textarea.value = 'cur'
    setCursor(ib.textarea, 3)
    dispatchArrow(ib.textarea, 'ArrowUp')
    expect(upSpy).toHaveBeenCalledOnce()
    expect(ib.textarea.value).toBe('cur')
  })

  it('ArrowDown when NOT streaming and historyDownCb returns text sets textarea value and cursor to end', () => {
    const ib = mount()
    ib.onHistoryDown(() => 'restored')
    ib.textarea.value = 'x'
    setCursor(ib.textarea, 0)
    dispatchArrow(ib.textarea, 'ArrowDown')
    expect(ib.textarea.value).toBe('restored')
    expect(ib.textarea.selectionStart).toBe('restored'.length)
  })

  it('ArrowDown when NOT streaming and historyDownCb returns null is a no-op', () => {
    const ib = mount()
    ib.onHistoryDown(() => null)
    ib.textarea.value = 'keep'
    setCursor(ib.textarea, 0)
    dispatchArrow(ib.textarea, 'ArrowDown')
    expect(ib.textarea.value).toBe('keep')
  })

  it('ArrowUp when streaming still fires onCancelQueued (existing behavior preserved)', () => {
    const ib = mount()
    const spy = vi.fn()
    ib.onCancelQueued(spy)
    ib.onHistoryUp(() => 'should not be called')
    ib.setStreaming(true)
    ib.setQueuedMsgs([{ uuid: 'u-1', text: 'hello' }])
    dispatchArrow(ib.textarea, 'ArrowUp')
    expect(spy).toHaveBeenCalledOnce()
  })

  it('ArrowUp when streaming without queue navigates history', () => {
    const ib = mount()
    const upSpy = vi.fn(() => 'history item' as string | null)
    ib.onHistoryUp(upSpy)
    ib.setStreaming(true)
    // No queued messages — should fall through to history
    dispatchArrow(ib.textarea, 'ArrowUp')
    expect(upSpy).toHaveBeenCalledOnce()
    expect(ib.textarea.value).toBe('history item')
  })

  it('historyUpCb fires exactly once per keydown', () => {
    const ib = mount()
    const upSpy = vi.fn(() => 'r' as string | null)
    ib.onHistoryUp(upSpy)
    dispatchArrow(ib.textarea, 'ArrowUp')
    expect(upSpy).toHaveBeenCalledOnce()
  })

  it('historyDownCb fires exactly once per keydown', () => {
    const ib = mount()
    const downSpy = vi.fn(() => 'r' as string | null)
    ib.onHistoryDown(downSpy)
    dispatchArrow(ib.textarea, 'ArrowDown')
    expect(downSpy).toHaveBeenCalledOnce()
  })

  it('ArrowUp on multi-line input with cursor NOT on line 0 does NOT navigate history', () => {
    const ib = mount()
    const upSpy = vi.fn(() => 'history' as string | null)
    ib.onHistoryUp(upSpy)
    ib.textarea.value = 'line1\nline2'
    // Place cursor inside "line2" (offset 8 = after the \n)
    setCursor(ib.textarea, 8)
    dispatchArrow(ib.textarea, 'ArrowUp')
    expect(upSpy).not.toHaveBeenCalled()
    expect(ib.textarea.value).toBe('line1\nline2')
  })

  it('ArrowUp on multi-line input with cursor at start of line 0 DOES navigate', () => {
    const ib = mount()
    const upSpy = vi.fn(() => 'history' as string | null)
    ib.onHistoryUp(upSpy)
    ib.textarea.value = 'line1\nline2'
    setCursor(ib.textarea, 0)
    dispatchArrow(ib.textarea, 'ArrowUp')
    expect(upSpy).toHaveBeenCalledOnce()
    expect(ib.textarea.value).toBe('history')
  })

  it('ArrowDown on multi-line input with cursor NOT on last line does NOT navigate', () => {
    const ib = mount()
    const downSpy = vi.fn(() => 'history' as string | null)
    ib.onHistoryDown(downSpy)
    ib.textarea.value = 'line1\nline2'
    // Place cursor inside "line1" (offset 2)
    setCursor(ib.textarea, 2)
    dispatchArrow(ib.textarea, 'ArrowDown')
    expect(downSpy).not.toHaveBeenCalled()
    expect(ib.textarea.value).toBe('line1\nline2')
  })

  it('ArrowDown on single-line input DOES navigate (selectionStart at end)', () => {
    const ib = mount()
    const downSpy = vi.fn(() => 'history' as string | null)
    ib.onHistoryDown(downSpy)
    ib.textarea.value = 'single'
    setCursor(ib.textarea, 'single'.length)
    dispatchArrow(ib.textarea, 'ArrowDown')
    expect(downSpy).toHaveBeenCalledOnce()
    expect(ib.textarea.value).toBe('history')
  })

  it('typing a rune after Up navigation resets nav state', () => {
    const ib = mount()
    let callCount = 0
    const upSpy = vi.fn((_current: string): string | null => {
      // Simulate History.up behavior: first call returns "old2", second "old1"
      callCount++
      if (callCount === 1) return 'old2'
      if (callCount === 2) return 'old1'
      return null
    })
    ib.onHistoryUp(upSpy)
    ib.onHistoryReset(() => {
      callCount = 0 // reset state
    })
    // Seed history: press ArrowUp → shows "old2"
    dispatchArrow(ib.textarea, 'ArrowUp')
    expect(ib.textarea.value).toBe('old2')
    // User types a character → input event fires → reset
    ib.textarea.value = 'old2X'
    ib.textarea.dispatchEvent(new Event('input', { bubbles: true }))
    // Next ArrowUp should show "old2" again (nav state was reset)
    dispatchArrow(ib.textarea, 'ArrowUp')
    expect(ib.textarea.value).toBe('old2')
  })

  it('programmatic textarea.value assignment does NOT fire input event / reset nav', () => {
    const ib = mount()
    let callCount = 0
    const upSpy = vi.fn((_current: string): string | null => {
      callCount++
      if (callCount === 1) return 'old2'
      if (callCount === 2) return 'old1'
      return null
    })
    ib.onHistoryUp(upSpy)
    // First ArrowUp → old2 (sets textarea.value programmatically)
    dispatchArrow(ib.textarea, 'ArrowUp')
    expect(ib.textarea.value).toBe('old2')
    // Second ArrowUp without any user input event → old1 (nav NOT reset)
    dispatchArrow(ib.textarea, 'ArrowUp')
    expect(ib.textarea.value).toBe('old1')
  })

  // ── History picker: single-line truncation ──

  function openHistPicker(
    ib: ReturnType<typeof createInputBar>,
    items: string[],
  ) {
    ib.onHistoryPicker(() => items)
    const sendBtn = ib.root.querySelector(
      'button[aria-label="Send"]',
    ) as HTMLButtonElement
    sendBtn.click()
    return document.body.querySelector('div[role="button"]')
      ?.parentElement as HTMLElement
  }

  it('history items use nowrap+ellipsis truncation, not webkit-line-clamp', () => {
    const ib = mount()
    const histList = openHistPicker(ib, [
      'short',
      'a very long message that should be truncated to one line',
    ])
    const items = histList.querySelectorAll('div[role="button"]')
    expect(items.length).toBe(2)
    for (const el of items) {
      expect((el as HTMLElement).classList.contains('truncate')).toBe(true)
    }
  })

  it('clicking a history item fills textarea and closes panel', () => {
    const ib = mount()
    const histList = openHistPicker(ib, ['hello world'])
    const item = histList.querySelector('div[role="button"]') as HTMLElement
    item.click()
    expect(ib.textarea.value).toBe('hello world')
    const panel = histList.parentElement as HTMLElement
    expect(panel.classList.contains('hidden')).toBe(true)
  })

  it('history picker search filters items', () => {
    const ib = mount()
    const histList = openHistPicker(ib, [
      'apple pie',
      'banana split',
      'apple juice',
    ])
    const panel = histList.parentElement as HTMLElement
    const search = panel.querySelector('textarea') as HTMLTextAreaElement
    search.value = 'apple'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    const items = histList.querySelectorAll('div[role="button"]')
    expect(items.length).toBe(2)
    expect((items[0] as HTMLElement).textContent).toContain('apple')
  })

  it('history picker does not open when there are no items', () => {
    const ib = mount()
    ib.onHistoryPicker(() => [])
    const sendBtn = ib.root.querySelector(
      'button[aria-label="Send"]',
    ) as HTMLButtonElement
    sendBtn.click()
    const panel = document.body.querySelector('div[role="button"]')?.parentElement
    expect(panel).toBeUndefined()
  })

  it('textarea breaks long unbreakable strings (word-break)', () => {
    mount()
    const ta = document.querySelector('textarea') as HTMLTextAreaElement
    const style = window.getComputedStyle(ta)
    // Must have word-break or overflow-wrap to break long URLs/identifiers
    const wb = style.wordBreak
    const ow = style.overflowWrap
    const ok = wb === 'break-all' || wb === 'break-word' || ow === 'anywhere' || ow === 'break-word'
    expect(ok).toBe(true)
  })
})

describe('attach panel popover', () => {
  function mount() {
    const ib = createInputBar({ connected: true })
    document.body.appendChild(ib.bubbles)
    document.body.appendChild(ib.root)
    return ib
  }

  function plusBtn(ib: ReturnType<typeof createInputBar>): HTMLButtonElement {
    return ib.root.querySelector('button[aria-label="Attach file"]') as HTMLButtonElement
  }

  function visibleAttachPanel(): HTMLElement | null {
    // attachPanel uses anchoredPopup recipe (no modal-enter). Distinguish
    // from editPopup by looking for the camera/image/doc buttons.
    for (const p of document.body.querySelectorAll('div')) {
      if (p.classList.contains('hidden')) continue
      const hasCamera = !!p.querySelector('button[aria-label="Camera"]')
        || !!p.querySelector('button[aria-label="Image"]')
        || !!p.querySelector('button[aria-label="File"]')
      if (hasCamera) return p as HTMLElement
    }
    return null
  }

  it('click plus opens attach panel and positions it (anchored to card)', () => {
    const ib = mount()
    plusBtn(ib).click()
    const panel = visibleAttachPanel()
    expect(panel).not.toBeNull()
    // positionAnchoredPopup sets inline left/bottom style as numeric px.
    // An undefined onOpen would leave these unset.
    expect(panel!.style.left).toMatch(/^\d+(\.\d+)?px$/)
    expect(panel!.style.bottom).toMatch(/^\d+(\.\d+)?px$/)
  })

  it('outside click closes attach panel', () => {
    const ib = mount()
    plusBtn(ib).click()
    const panel = visibleAttachPanel()
    expect(panel).not.toBeNull()
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    expect(panel!.classList.contains('hidden')).toBe(true)
  })

  it('reopen after close re-appends without duplicates', () => {
    const ib = mount()
    plusBtn(ib).click()
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    plusBtn(ib).click()
    const panel = visibleAttachPanel()
    expect(panel).not.toBeNull()
    // Only one attach panel in body.
    let count = 0
    for (const p of document.body.querySelectorAll('div')) {
      const hasAttach = !!p.querySelector('button[aria-label="Camera"]')
        || !!p.querySelector('button[aria-label="Image"]')
        || !!p.querySelector('button[aria-label="File"]')
      if (hasAttach) count++
    }
    expect(count).toBe(1)
  })
})

describe('createInputBar — thinking effort control', () => {
  const strip = (ib: ReturnType<typeof createInputBar>) =>
    ib.root.querySelector('[data-effort-strip]') as HTMLElement
  const caption = (ib: ReturnType<typeof createInputBar>) =>
    ib.root.querySelector('button.text-\\[10px\\]') as HTMLElement
  const sendBtn = (ib: ReturnType<typeof createInputBar>) =>
    ib.root.querySelector('button[aria-label="Send"]') as HTMLElement

  it('default auto: no caption text visible, input row untouched', () => {
    const ib = mount()
    expect(strip(ib).classList.contains('hidden')).toBe(true)
    expect(caption(ib).classList.contains('invisible')).toBe(true)
    // Row is exactly plus → mic (hidden until STT is configured) → textarea → send.
    const plus = ib.root.querySelector('button[aria-label="Attach file"]')!
    const micAnchor = plus.nextSibling as HTMLElement
    expect(micAnchor.querySelector('button[aria-label="Voice input"]')).toBeTruthy()
    expect((micAnchor.nextSibling as HTMLElement).contains(ib.textarea as unknown as Node)).toBe(true)
    // The send anchor follows the textarea — no effort pill ever sits in
    // the row.
    const sendAnchorPrev = sendBtn(ib).closest('div')!.previousSibling as HTMLElement
    expect(sendAnchorPrev.contains(ib.textarea as unknown as Node)).toBe(true)
  })

  it('setThinking(high) shows the caption under the arrow, not in the row', () => {
    const ib = mount()
    ib.setThinking('high')
    const cap = caption(ib)
    expect(cap.textContent).toBe('high')
    expect(cap.classList.contains('invisible')).toBe(false)
    // The caption shares the send button's anchor wrapper — badge directly
    // under the arrow, no layout slot of its own.
    const anchor = sendBtn(ib).parentElement!
    expect(anchor.contains(cap)).toBe(true)
    expect(cap.classList.contains('absolute')).toBe(true)
    // auto hides again
    ib.setThinking('auto')
    expect(caption(ib).classList.contains('invisible')).toBe(true)
  })

  it('long-pressing the send arrow opens the strip and swallows the click', () => {
    vi.useFakeTimers()
    const ib = mount()
    let sent = 0
    sendBtn(ib).addEventListener('click', () => sent++)
    sendBtn(ib).dispatchEvent(new MouseEvent('pointerdown'))
    vi.advanceTimersByTime(500)
    expect(strip(ib).classList.contains('hidden')).toBe(false)
    sendBtn(ib).dispatchEvent(new MouseEvent('pointerup'))
    // The click that follows a long-press must not send.
    sendBtn(ib).dispatchEvent(new MouseEvent('click'))
    expect(sent).toBe(0)
    // A normal click (no long-press) still goes through.
    sendBtn(ib).dispatchEvent(new MouseEvent('click'))
    expect(sent).toBe(1)
    vi.useRealTimers()
  })

  it('selecting from the strip fires onThinkingSelect, updates the caption, collapses', () => {
    vi.useFakeTimers()
    const ib = mount()
    const spy = vi.fn()
    ib.onThinkingSelect(spy)
    sendBtn(ib).dispatchEvent(new MouseEvent('pointerdown'))
    vi.advanceTimersByTime(500)
    const labels = Array.from(strip(ib).querySelectorAll('button')).map((b) => b.textContent)
    for (const e of ['none', 'auto', 'low', 'medium', 'high', 'max']) {
      expect(labels).toContain(e)
    }
    const maxSeg = Array.from(strip(ib).querySelectorAll('button')).find((b) => b.textContent === 'max')!
    maxSeg.click()
    expect(spy).toHaveBeenCalledWith('max')
    expect(caption(ib).textContent).toBe('max')
    expect(strip(ib).classList.contains('hidden')).toBe(true)
    vi.useRealTimers()
  })

  it('long-press drag-away leak: the next normal tap still sends', () => {
    vi.useFakeTimers()
    const ib = mount()
    let sent = 0
    sendBtn(ib).addEventListener('click', () => sent++)
    const btn = sendBtn(ib)
    // Long-press fires, then the finger slides off before release — the
    // swallow flag leaks unconsumed.
    btn.dispatchEvent(new MouseEvent('pointerdown'))
    vi.advanceTimersByTime(500)
    btn.dispatchEvent(new MouseEvent('pointerleave'))
    // A fresh full tap must go through despite the leaked flag.
    btn.dispatchEvent(new MouseEvent('pointerdown'))
    btn.dispatchEvent(new MouseEvent('pointerup'))
    btn.dispatchEvent(new MouseEvent('click'))
    expect(sent).toBe(1)
    vi.useRealTimers()
  })

  it('focus leaving the strip collapses it without choosing', () => {
    vi.useFakeTimers()
    const ib = mount()
    const spy = vi.fn()
    ib.onThinkingSelect(spy)
    sendBtn(ib).dispatchEvent(new MouseEvent('pointerdown'))
    vi.advanceTimersByTime(500)
    strip(ib).dispatchEvent(new FocusEvent('focusout', { relatedTarget: ib.textarea as unknown as Node }))
    expect(strip(ib).classList.contains('hidden')).toBe(true)
    expect(caption(ib).classList.contains('invisible')).toBe(true)
    expect(spy).not.toHaveBeenCalled()
    vi.useRealTimers()
  })
})

describe('addRestoredAttachment', () => {
  it('renders the local blob thumbnail and original name when provided', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.addRestoredAttachment({
      id: 'fresh-1',
      name: 'cat.jpg',
      mime: 'image/jpeg',
      size: 2048,
      previewURL: 'blob:preview',
    })
    const img = ib.root.querySelector('img[alt="cat.jpg"]') as HTMLImageElement | null
    if (!img) throw new Error('restored image chip not rendered')
    expect(img.src).toBe('blob:preview')
  })

  it('falls back to a synthetic name and empty preview when local meta is gone', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.addRestoredAttachment({ id: 'fresh-2', mime: 'image/png', size: 64 })
    const img = ib.root.querySelector('img[alt^="image."]')
    if (!img) throw new Error('synthetic chip not rendered')
    expect(img.getAttribute('src')).toBe('')
  })
})

describe('queue bubble enter animation', () => {
  it('replays modal-enter only for newly added bubbles', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.setQueuedMsgs([{ uuid: '', text: 'one' }])
    let first = ib.bubbles.querySelector('div')
    if (!first) throw new Error('no bubble rendered')
    expect(first.className).toContain('modal-enter')

    // uuid stamp re-render: same bubble content, must NOT re-animate.
    ib.setQueuedMsgs([{ uuid: 'u-1', text: 'one' }])
    first = ib.bubbles.querySelector('div')
    if (!first) throw new Error('bubble vanished on stamp')
    expect(first.className).not.toContain('modal-enter')

    // a NEW message still animates.
    ib.setQueuedMsgs([{ uuid: 'u-1', text: 'one' }, { uuid: '', text: 'two' }])
    const second = ib.bubbles.querySelectorAll('div')[1]
    if (!second) throw new Error('second bubble missing')
    expect(second.className).toContain('modal-enter')
  })
})

describe('createInputBar — STT rendering', () => {
  it('mic always visible; unavailable renders red with slash', () => {
    const ib = mount()
    const micSel = 'button[aria-label="Voice input"]'
    // Unavailable: resident, red, slashed, disabled.
    const mic = ib.root.querySelector(micSel)! as HTMLButtonElement
    expect(mic.classList.contains('hidden')).toBe(false)
    expect(mic.classList.contains('stt-mic-off')).toBe(true)
    expect(mic.querySelector('.stt-mic-slash')!.classList.contains('hidden')).toBe(false)
    expect(mic.disabled).toBe(true)
    ib.setSttAvailable(true)
    // Available: slash gone, blue, enabled.
    expect(mic.classList.contains('stt-mic-off')).toBe(false)
    expect(mic.querySelector('.stt-mic-slash')!.classList.contains('hidden')).toBe(true)
    expect(mic.disabled).toBe(false)
    // Position: plus → mic → textarea wrap → send. The row is mic's
    // anchor's PARENT (micAnchor is itself a flex div — closest('div.flex')
    // would resolve to the anchor and make any index assertion a tautology).
    const row = mic.closest('div')!.parentElement!
    const children = Array.from(row.children)
    expect(children.indexOf(mic.closest('div')!)).toBe(1)
    expect(children.indexOf(mic.closest('div')!)).toBeLessThan(children.length - 2)
    // Connected gating: disable on disconnect, re-enable on reconnect.
    ib.setConnected(false)
    expect(mic.disabled).toBe(true)
    ib.setConnected(true)
    expect(mic.disabled).toBe(false)
  })

  it('onMicToggle fires on mic click; setSttListening paints the state', () => {
    const ib = mount()
    ib.setSttAvailable(true)
    const fired = vi.fn()
    ib.onMicToggle(fired)
    ;(ib.root.querySelector('button[aria-label="Voice input"]') as HTMLElement).click()
    expect(fired).toHaveBeenCalledTimes(1)
    // The error red clears when the new attempt reaches listening (green
    // repaint); failed attempts stay red — the tap itself never clears it.
    ib.setSttStatus('boom')
    expect(ib.root.querySelector('.stt-mic')!.classList.contains('stt-mic-err')).toBe(true)
    ib.setSttListening(true)
    const mic = ib.root.querySelector('button[aria-label="Voice input"]')!
    // The green mic + card glow IS the listening indicator — no text.
    expect(mic.classList.contains('stt-mic-on')).toBe(true)
    expect(mic.classList.contains('stt-mic-err')).toBe(false)
    ib.setSttListening(false)
    expect(mic.classList.contains('stt-mic-on')).toBe(false)
  })

  it('setSttStatus flags the mic red on error and clears on recovery', () => {
    const ib = mount()
    ib.setSttStatus('boom')
    expect(ib.root.querySelector('.stt-mic')!.classList.contains('stt-mic-err')).toBe(true)
    ib.setSttStatus(null)
    expect(ib.root.querySelector('.stt-mic')!.classList.contains('stt-mic-err')).toBe(false)
  })

  it('appendSttDelta joins CJK gaps and ASCII word boundaries', () => {
    const ib = mount()
    ib.beginSttUtterance()
    ib.appendSttDelta('你好')
    expect(ib.textarea.value).toBe('你好')
    ib.appendSttDelta(' 是流式')
    expect(ib.textarea.value).toBe('你好是流式')
    ib.textarea.value = 'hello'
    ib.appendSttDelta('work')
    expect(ib.textarea.value).toBe('hello work')
  })
  it('caret follows the transcript to the end while deltas land', () => {
    const ib = mount()
    ib.beginSttUtterance()
    ib.appendSttDelta('你好')
    expect(ib.textarea.selectionStart).toBe(2)
    ib.appendSttDelta(' 是流式')
    expect(ib.textarea.selectionStart).toBe(5)
    ib.commitSttUtterance('定稿')
    expect(ib.textarea.selectionStart).toBe(2)
  })
  it('a same-value delta still parks the caret at the end', () => {
    const ib = mount()
    ib.beginSttUtterance()
    ib.appendSttDelta('你好')
    // Whichever way the browser parks the caret after a value assignment,
    // park it at the top; the next delta is whitespace-only after CJK, so
    // joinSttText produces a same-value assignment and jsdom leaves the
    // caret alone — only caretToEnd() brings it back to the transcript end.
    ib.textarea.setSelectionRange(0, 0)
    ib.appendSttDelta(' ')
    expect(ib.textarea.selectionStart).toBe(2)
  })

  it('commitSttUtterance replaces an unedited span with the final text', () => {
    const ib = mount()
    ib.textarea.value = '前缀 '
    ib.beginSttUtterance()
    ib.appendSttDelta('临时')
    ib.appendSttDelta(' 内容')
    ib.commitSttUtterance('全句')
    expect(ib.textarea.value).toBe('前缀 全句')
  })

  it('commitSttUtterance never touches a user-edited span', () => {
    const ib = mount()
    ib.beginSttUtterance()
    ib.appendSttDelta('临时')
    ib.textarea.value = '我改过了'
    ib.commitSttUtterance('全句')
    expect(ib.textarea.value).toBe('我改过了')
  })

  it('empty done (mis-tap auto-close) is a no-op', () => {
    const ib = mount()
    ib.textarea.value = '既有文字'
    ib.beginSttUtterance()
    ib.commitSttUtterance('')
    expect(ib.textarea.value).toBe('既有文字')
  })

  it('replaceSttSpan replaces the span with join rules', () => {
    const ib = mount()
    ib.textarea.value = '你好'
    ib.beginSttUtterance()
    ib.appendSttDelta('临时')
    ib.replaceSttSpan('替换后')
    expect(ib.textarea.value).toBe('你好替换后')
    // Leading space before a CJK span gets stripped by the join rule.
    ib.replaceSttSpan(' 是流式')
    expect(ib.textarea.value).toBe('你好是流式')
  })

  it('commit after partial still replaces (partials are not user edits)', () => {
    const ib = mount()
    ib.textarea.value = '前缀 '
    ib.beginSttUtterance()
    ib.appendSttDelta('临时')
    ib.replaceSttSpan('替换后')
    ib.commitSttUtterance('定稿')
    expect(ib.textarea.value).toBe('前缀 定稿')
  })

  it('endSttUtterance keeps the text but freezes the span', () => {
    const ib = mount()
    ib.beginSttUtterance()
    ib.appendSttDelta('保留文字')
    ib.endSttUtterance()
    ib.commitSttUtterance('不应生效')
    expect(ib.textarea.value).toBe('保留文字')
  })
})

// --- mic long-press → voice route (350ms), tap stays dictation -----------

describe('mic long-press (voice entry)', () => {
  function micOf(ib: ReturnType<typeof createInputBar>): HTMLButtonElement {
    return ib.root.querySelector('button[aria-label="Voice input"]')! as HTMLButtonElement
  }
  const pd = (el: HTMLElement, x = 10, y = 10) =>
    el.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: x, clientY: y }))
  const pu = (el: HTMLElement, x = 10, y = 10) =>
    el.dispatchEvent(new MouseEvent('pointerup', { bubbles: true, cancelable: true, clientX: x, clientY: y }))
  const mv = (el: HTMLElement, x: number, y: number) =>
    el.dispatchEvent(new MouseEvent('pointermove', { bubbles: true, cancelable: true, clientX: x, clientY: y }))

  it('350ms hold fires onMicHold once and never the click toggle', () => {
    vi.useFakeTimers()
    const ib = mount()
    const hold = vi.fn()
    ib.onMicHold(hold)
    ib.setSttAvailable(true)
    const mic = micOf(ib)
    pd(mic)
    vi.advanceTimersByTime(349)
    expect(hold).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(hold).toHaveBeenCalledTimes(1)
    expect(hold).toHaveBeenCalledWith(true)
    pu(mic)
    mic.click()
    expect(hold).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })

  it('release before 350ms keeps the dictation toggle', () => {
    vi.useFakeTimers()
    const ib = mount()
    const hold = vi.fn()
    const toggle = vi.fn()
    ib.onMicHold(hold)
    ib.onMicToggle(toggle)
    const mic = micOf(ib)
    ib.setSttAvailable(true)
    pd(mic)
    vi.advanceTimersByTime(200)
    pu(mic)
    vi.advanceTimersByTime(500)
    expect(hold).not.toHaveBeenCalled()
    mic.click()
    expect(toggle).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })

  it('moving >12px cancels the hold timer', () => {
    vi.useFakeTimers()
    const ib = mount()
    const hold = vi.fn()
    ib.onMicHold(hold)
    const mic = micOf(ib)
    pd(mic, 10, 10)
    mv(mic, 25, 10) // 15px
    vi.advanceTimersByTime(600)
    expect(hold).not.toHaveBeenCalled()
    vi.useRealTimers()
  })

  it('contextmenu (desktop right-click) enters voice WITHOUT a hold gesture', () => {
    const ib = mount()
    const hold = vi.fn()
    ib.onMicHold(hold)
    ib.setSttAvailable(true)
    const mic = micOf(ib)
    const ev = new MouseEvent('contextmenu', { bubbles: true, cancelable: true })
    mic.dispatchEvent(ev)
    expect(hold).toHaveBeenCalledTimes(1)
    // Right-click has no finger to release: the entry must not start
    // recording (holds=false), only open the page.
    expect(hold).toHaveBeenCalledWith(false)
    expect(ev.defaultPrevented).toBe(true)
  })

  it('right-button pointerdown does not arm the hold timer (macOS right-press >350ms must not enter voice)', () => {
    vi.useFakeTimers()
    const ib = mount()
    const hold = vi.fn()
    ib.onMicHold(hold)
    ib.setSttAvailable(true)
    const mic = micOf(ib)
    mic.dispatchEvent(
      new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 10, clientY: 10, button: 2 }),
    )
    vi.advanceTimersByTime(600)
    expect(hold).not.toHaveBeenCalled()
    // The contextmenu path on the same gesture still enters (enter-only).
    mic.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))
    expect(hold).toHaveBeenCalledTimes(1)
    expect(hold).toHaveBeenCalledWith(false)
    vi.useRealTimers()
  })
})

// ── Mutation kill tests (StrykerJS census 2026-11-04) ──
// Each group pins survivors from one functional area of input_bar.ts.

describe('input bar mutation kills: send click routing and history picker gating', () => {
  function openHist(ib: ReturnType<typeof createInputBar>, items: string[]) {
    ib.onHistoryPicker(() => items)
    sendBtnOf(ib).click()
    return document.body.querySelector('div[role="button"]')?.parentElement as HTMLElement
  }

  it('streaming with a pending attachment (no text) sends instead of stopping', () => {
    const ib = mount()
    const sendSpy = vi.fn()
    const stopSpy = vi.fn()
    ib.onSend(sendSpy)
    ib.onStop(stopSpy)
    ib.setStreaming(true)
    ib.addRestoredAttachment({ id: 'r1', mime: 'text/plain', name: 'n.txt', size: 3 })
    sendBtnOf(ib).click()
    expect(sendSpy).toHaveBeenCalledTimes(1)
    expect(sendSpy).toHaveBeenCalledWith('')
    expect(stopSpy).not.toHaveBeenCalled()
  })

  it('streaming with whitespace-only input (no attachments) still STOPs', () => {
    const ib = mount()
    const sendSpy = vi.fn()
    const stopSpy = vi.fn()
    ib.onSend(sendSpy)
    ib.onStop(stopSpy)
    ib.setStreaming(true)
    // Whitespace without an input event keeps the STOP affordance mounted.
    ib.textarea.value = '   '
    const stopBtn = ib.root.querySelector('button[aria-label="Stop"]') as HTMLButtonElement
    stopBtn.click()
    expect(stopSpy).toHaveBeenCalledTimes(1)
    expect(sendSpy).not.toHaveBeenCalled()
  })

  it('the STOP click preventDefaults the event', () => {
    const ib = mount()
    ib.setStreaming(true)
    const btn = ib.root.querySelector('button[aria-label="Stop"]') as HTMLButtonElement
    const ev = new MouseEvent('click', { bubbles: true, cancelable: true })
    btn.dispatchEvent(ev)
    expect(ev.defaultPrevented).toBe(true)
  })

  it('empty-input send click preventDefaults and re-click closes the history panel', () => {
    const ib = mount()
    ib.onHistoryPicker(() => ['alpha'])
    const btn = sendBtnOf(ib)
    const ev1 = new MouseEvent('click', { bubbles: true, cancelable: true })
    btn.dispatchEvent(ev1)
    expect(ev1.defaultPrevented).toBe(true)
    const histList = document.body.querySelector('div[role="button"]')?.parentElement as HTMLElement
    const panel = histList.parentElement as HTMLElement
    expect(panel.classList.contains('hidden')).toBe(false)
    const ev2 = new MouseEvent('click', { bubbles: true, cancelable: true })
    btn.dispatchEvent(ev2)
    expect(ev2.defaultPrevented).toBe(true)
    expect(panel.classList.contains('hidden')).toBe(true)
  })

  it('send click with no picker registered renders nothing', () => {
    const ib = mount()
    sendBtnOf(ib).click()
    expect(document.body.querySelector('div[role="button"]')).toBeNull()
    expect(document.body.querySelector('[data-i18n-placeholder="histSearch"]')).toBeNull()
    expect(document.body.textContent ?? '').not.toContain('Stryker')
  })

  it('send click with an empty history never opens the panel (no No-history fallback)', () => {
    const ib = mount()
    ib.onHistoryPicker(() => [])
    sendBtnOf(ib).click()
    expect(document.body.querySelector('[data-i18n-placeholder="histSearch"]')).toBeNull()
    expect(document.body.textContent ?? '').not.toContain('No history')
  })

  it('a search matching nothing renders the No-history empty state exactly once', () => {
    const ib = mount()
    const histList = openHist(ib, ['alpha', 'beta'])
    const search = histList.parentElement!.querySelector('textarea') as HTMLTextAreaElement
    search.value = 'zzz'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    expect(histList.textContent).toContain('No history')
    expect(histList.querySelectorAll('div[role="button"]').length).toBe(0)
    const empty = histList.querySelector('div') as HTMLElement
    expect(empty.classList.contains('text-t3')).toBe(true)
    expect(empty.classList.contains('py-4')).toBe(true)
    expect(histList.childNodes.length).toBe(1)
  })

  it('the search query is trimmed and lowercased before matching', () => {
    const ib = mount()
    const histList = openHist(ib, ['apple pie', 'banana split', 'apple juice'])
    const search = histList.parentElement!.querySelector('textarea') as HTMLTextAreaElement
    search.value = '  APPLE '
    search.dispatchEvent(new Event('input', { bubbles: true }))
    const items = histList.querySelectorAll('div[role="button"]')
    expect(items.length).toBe(2)
    expect(items[0].textContent).toBe('apple pie')
    expect(items[1].textContent).toBe('apple juice')
  })

  it('re-rendering the list replaces the previous items instead of appending', () => {
    const ib = mount()
    const histList = openHist(ib, ['apple pie', 'banana split', 'apple juice'])
    const search = histList.parentElement!.querySelector('textarea') as HTMLTextAreaElement
    search.value = 'apple'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    expect(histList.childNodes.length).toBe(2)
    expect(histList.textContent).toBe('apple pieapple juice')
  })

  it('history items are keyboard-focusable (tabIndex 0)', () => {
    const ib = mount()
    const histList = openHist(ib, ['one', 'two'])
    const items = histList.querySelectorAll('div[role="button"]')
    expect((items[0] as HTMLElement).tabIndex).toBe(0)
    expect((items[1] as HTMLElement).tabIndex).toBe(0)
  })

  it('picking an item focuses the textarea and undims send', () => {
    const ib = mount()
    const btn = sendBtnOf(ib)
    expect(btn.className).toContain('opacity-50')
    const histList = openHist(ib, ['hello world'])
    const item = histList.querySelector('div[role="button"]') as HTMLElement
    item.click()
    expect(document.activeElement).toBe(ib.textarea)
    expect(btn.className).not.toContain('opacity-50')
  })

  it('picking an item equal to the current whitespace text parks the caret at its end', () => {
    const ib = mount()
    ib.textarea.value = '  '
    const histList = openHist(ib, ['  '])
    // The diff-value assignment above parked the caret at the end already —
    // rewind it so only the item-click handler can restore it.
    ib.textarea.setSelectionRange(0, 0)
    const item = histList.querySelector('div[role="button"]') as HTMLElement
    item.click()
    expect(ib.textarea.selectionStart).toBe(2)
  })
})

describe('input bar mutation kills: send long-press effort strip mechanics', () => {
  const strip = (ib: ReturnType<typeof createInputBar>) =>
    ib.root.querySelector('[data-effort-strip]') as HTMLElement
  const sendBtn = (ib: ReturnType<typeof createInputBar>) => sendBtnOf(ib)

  it('releasing before 500ms cancels the pending long-press', () => {
    vi.useFakeTimers()
    const ib = mount()
    const btn = sendBtn(ib)
    btn.dispatchEvent(new MouseEvent('pointerdown'))
    vi.advanceTimersByTime(100)
    btn.dispatchEvent(new MouseEvent('pointerup'))
    vi.advanceTimersByTime(400)
    expect(strip(ib).classList.contains('hidden')).toBe(true)
    vi.useRealTimers()
  })

  it('the click swallowed after a long-press is preventDefaulted', () => {
    vi.useFakeTimers()
    const ib = mount()
    const btn = sendBtn(ib)
    btn.dispatchEvent(new MouseEvent('pointerdown'))
    vi.advanceTimersByTime(500)
    const ev = new MouseEvent('click', { bubbles: true, cancelable: true })
    btn.dispatchEvent(ev)
    expect(ev.defaultPrevented).toBe(true)
    vi.useRealTimers()
  })

  it('the strip marks the current effort and styles the others as idle', () => {
    vi.useFakeTimers()
    const ib = mount()
    sendBtn(ib).dispatchEvent(new MouseEvent('pointerdown'))
    vi.advanceTimersByTime(500)
    const buttons = Array.from(strip(ib).querySelectorAll('button')) as HTMLElement[]
    expect(buttons.length).toBe(6)
    for (const b of buttons) {
      expect(b.classList.contains('cursor-pointer')).toBe(true)
      expect(b.classList.contains('text-[13px]')).toBe(true)
    }
    const current = buttons.find((b) => b.textContent === 'auto')!
    expect(current.classList.contains('text-t2')).toBe(true)
    expect(current.classList.contains('underline')).toBe(true)
    expect(current.classList.contains('decoration-t2/60')).toBe(true)
    const idle = buttons.find((b) => b.textContent === 'max')!
    expect(idle.classList.contains('text-t3')).toBe(true)
    expect(idle.classList.contains('underline')).toBe(false)
    vi.useRealTimers()
  })

  it('the strip rebuild contains exactly the six effort buttons', () => {
    vi.useFakeTimers()
    const ib = mount()
    sendBtn(ib).dispatchEvent(new MouseEvent('pointerdown'))
    vi.advanceTimersByTime(500)
    expect(strip(ib).childNodes.length).toBe(6)
    expect(strip(ib).textContent).toBe('autononelowmediumhighmax')
    vi.useRealTimers()
  })

  it('setThinking collapses an open strip', () => {
    vi.useFakeTimers()
    const ib = mount()
    sendBtn(ib).dispatchEvent(new MouseEvent('pointerdown'))
    vi.advanceTimersByTime(500)
    expect(strip(ib).classList.contains('hidden')).toBe(false)
    ib.setThinking('high')
    expect(strip(ib).classList.contains('hidden')).toBe(true)
    vi.useRealTimers()
  })

  it('focus moving within the strip keeps it open', () => {
    vi.useFakeTimers()
    const ib = mount()
    sendBtn(ib).dispatchEvent(new MouseEvent('pointerdown'))
    vi.advanceTimersByTime(500)
    const first = strip(ib).querySelector('button')!
    strip(ib).dispatchEvent(new FocusEvent('focusout', { relatedTarget: first }))
    expect(strip(ib).classList.contains('hidden')).toBe(false)
    vi.useRealTimers()
  })
})

describe('input bar mutation kills: mic gating, hold timer, and pointer tracking', () => {
  const pd = (el: HTMLElement, x = 10, y = 10) =>
    el.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: x, clientY: y }))
  const pu = (el: HTMLElement) =>
    el.dispatchEvent(new MouseEvent('pointerup', { bubbles: true, cancelable: true }))
  const mv = (el: HTMLElement, x: number, y: number) =>
    el.dispatchEvent(new MouseEvent('pointermove', { bubbles: true, cancelable: true, clientX: x, clientY: y }))

  it('clicking the mic while STT is unavailable fires nothing', () => {
    const ib = mount()
    const toggle = vi.fn()
    ib.onMicToggle(toggle)
    micOf(ib).dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    expect(toggle).not.toHaveBeenCalled()
  })

  it('pointerdown on the disabled mic never arms the hold timer', () => {
    vi.useFakeTimers()
    const ib = mount()
    const hold = vi.fn()
    ib.onMicHold(hold)
    pd(micOf(ib), 5, 5)
    vi.advanceTimersByTime(600)
    expect(hold).not.toHaveBeenCalled()
    vi.useRealTimers()
  })

  it('a second pointerdown restarts the hold window instead of stacking timers', () => {
    vi.useFakeTimers()
    const ib = mount()
    const hold = vi.fn()
    ib.onMicHold(hold)
    ib.setSttAvailable(true)
    const mic = micOf(ib)
    pd(mic, 5, 5)
    vi.advanceTimersByTime(300)
    pd(mic, 5, 5)
    vi.advanceTimersByTime(100)
    expect(hold).not.toHaveBeenCalled()
    vi.useRealTimers()
  })

  it('after a completed hold the first click is suppressed and the second toggles', () => {
    vi.useFakeTimers()
    const ib = mount()
    const hold = vi.fn()
    const toggle = vi.fn()
    ib.onMicHold(hold)
    ib.onMicToggle(toggle)
    ib.setSttAvailable(true)
    const mic = micOf(ib)
    pd(mic, 5, 5)
    vi.advanceTimersByTime(350)
    pu(mic)
    mic.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    expect(toggle).not.toHaveBeenCalled()
    mic.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    expect(toggle).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })

  it('right-click cancels an armed hold timer so the hold never double-fires', () => {
    vi.useFakeTimers()
    const ib = mount()
    const hold = vi.fn()
    ib.onMicHold(hold)
    ib.setSttAvailable(true)
    const mic = micOf(ib)
    pd(mic, 5, 5)
    vi.advanceTimersByTime(100)
    mic.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))
    vi.advanceTimersByTime(600)
    expect(hold).toHaveBeenCalledTimes(1)
    expect(hold).toHaveBeenCalledWith(false)
    vi.useRealTimers()
  })

  it('right-click on the disabled mic does not enter voice', () => {
    const ib = mount()
    const hold = vi.fn()
    ib.onMicHold(hold)
    micOf(ib).dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))
    expect(hold).not.toHaveBeenCalled()
  })

  it('a system pointercancel clears the armed hold timer', () => {
    vi.useFakeTimers()
    const ib = mount()
    const hold = vi.fn()
    ib.onMicHold(hold)
    ib.setSttAvailable(true)
    pd(micOf(ib), 5, 5)
    window.dispatchEvent(new Event('pointercancel'))
    vi.advanceTimersByTime(600)
    expect(hold).not.toHaveBeenCalled()
    vi.useRealTimers()
  })

  it('move-cancel thresholds: 13px cancels, 12px and 7px do not, on both axes (OR)', () => {
    vi.useFakeTimers()
    const holdsAfter = (to: [number, number]) => {
      document.body.innerHTML = ''
      const ib = mount()
      const hold = vi.fn()
      ib.onMicHold(hold)
      ib.setSttAvailable(true)
      const mic = micOf(ib)
      pd(mic, 10, 10)
      mv(mic, to[0], to[1])
      vi.advanceTimersByTime(600)
      return hold
    }
    expect(holdsAfter([23, 10])).not.toHaveBeenCalled()
    expect(holdsAfter([10, 23])).not.toHaveBeenCalled()
    expect(holdsAfter([22, 23])).not.toHaveBeenCalled()
    expect(holdsAfter([22, 10])).toHaveBeenCalledTimes(1)
    expect(holdsAfter([10, 22])).toHaveBeenCalledTimes(1)
    expect(holdsAfter([3, 10])).toHaveBeenCalledTimes(1)
    expect(holdsAfter([10, 3])).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })
})

describe('input bar mutation kills: paste chip compression and clipboard naming', () => {
  it('non-paste inputTypes never become chips regardless of size', () => {
    const ib = mount()
    const ev = new InputEvent('beforeinput', {
      inputType: 'insertReplacementText',
      data: 'x'.repeat(900),
      bubbles: true,
      cancelable: true,
    })
    ib.textarea.dispatchEvent(ev)
    expect(ev.defaultPrevented).toBe(false)
    expect(ib.root.querySelector('[data-paste-chip]')).toBeNull()
  })

  it('a large paste renders a full paste chip and undims send', () => {
    const ib = mount()
    const btn = sendBtnOf(ib)
    expect(btn.className).toContain('opacity-50')
    bigPaste(ib.textarea, 'aaaa\nbbbb\ncccc\nddddddd')
    const chip = ib.root.querySelector('[data-paste-chip]') as HTMLElement
    expect(chip).toBeTruthy()
    expect(chip.getAttribute('data-paste-chip')).toBe('')
    expect(chip.getAttribute('role')).toBe('button')
    expect(chip.tabIndex).toBe(0)
    expect(chip.classList.contains('cursor-pointer')).toBe(true)
    expect(chip.innerHTML).toContain('<rect')
    expect(chip.innerHTML).toContain('<path')
    expect(chip.innerHTML).toContain('</svg>')
    const label = chip.querySelector('.whitespace-nowrap') as HTMLElement
    expect(label.classList.contains('font-mono')).toBe(true)
    expect(label.classList.contains('text-[11px]')).toBe(true)
    expect(label.textContent).toBe('#1 +4 lines')
    const preview = chip.querySelector('.max-w-\\[140px\\]') as HTMLElement
    expect(preview.classList.contains('truncate')).toBe(true)
    expect(preview.textContent).toBe('aaaa bbbb cccc ddddd')
    const stack = chip.querySelector('div.min-w-0') as HTMLElement
    expect(stack.classList.contains('flex-col')).toBe(true)
    expect(chip.parentElement!.classList.contains('relative')).toBe(true)
    expect(btn.className).not.toContain('opacity-50')
  })

  it('colliding extension-less names get -2/-3 before the (absent) extension', () => {
    const ib = mount()
    const mk = () => new File(['data'], 'aaa', { type: 'text/plain' })
    pasteClipboardFiles(ib.textarea, [{ file: mk() }, { file: mk() }, { file: mk() }])
    const chips = ib.root.querySelectorAll('span.font-mono.bg-ink2')
    expect(chips.length).toBe(3)
    expect(chips[1].textContent).toBe('[aaa-2 4 B]')
    expect(chips[2].textContent).toBe('[aaa-3 4 B]')
  })

  it('dotfile collisions keep the full name as the base', () => {
    const ib = mount()
    const mk = () => new File(['data'], '.gitignore', { type: 'text/plain' })
    pasteClipboardFiles(ib.textarea, [{ file: mk() }, { file: mk() }])
    const chips = ib.root.querySelectorAll('span.font-mono.bg-ink2')
    expect(chips.length).toBe(2)
    expect(chips[1].textContent).toBe('[.gitignore-2 4 B]')
  })

  it('a file whose name is kept is attached as the original object', () => {
    const ib = mount()
    const orig = new File(['data'], 'keep.txt', { type: 'text/plain' })
    pasteClipboardFiles(ib.textarea, [{ file: orig }])
    expect(ib.getAttachments()[0].file).toBe(orig)
  })

  it('only file-kind clipboard items attach; string items are skipped', () => {
    const ib = mount()
    const f = new File(['abc'], 'q.txt', { type: 'text/plain' })
    pasteClipboardFiles(ib.textarea, [{ kind: 'string', file: null }, { file: f }])
    const chips = ib.root.querySelectorAll('span.font-mono.bg-ink2')
    expect(chips.length).toBe(1)
    expect(chips[0].textContent).toBe('[q.txt 3 B]')
  })

  it('clipboard with only files (no items) falls back to the files list', () => {
    const ib = mount()
    const f = new File(['xyz'], 'z.txt', { type: 'text/plain' })
    pasteClipboardFiles(ib.textarea, [], [f])
    const chips = ib.root.querySelectorAll('span.font-mono.bg-ink2')
    expect(chips.length).toBe(1)
    expect(chips[0].textContent).toBe('[z.txt 3 B]')
  })

  it('clipboard with neither items nor files is a no-op', () => {
    const ib = mount()
    const ev = pasteClipboardFiles(ib.textarea, [], null)
    expect(ib.root.querySelectorAll('span.font-mono.bg-ink2').length).toBe(0)
    expect(ev.defaultPrevented).toBe(false)
  })

  it('engine-synthesized image.png names get stamped and collide with -2', () => {
    const ib = mount()
    const mk = () => new File(['x'], 'image.png', { type: 'image/png' })
    pasteClipboardFiles(ib.textarea, [{ file: mk() }, { file: mk() }])
    const alts = Array.from(ib.root.querySelectorAll('img')).map((i) => (i as HTMLImageElement).alt)
    expect(alts.length).toBe(2)
    expect(alts[0]).toMatch(/^pasted-\d{6}\.png$/)
    expect(alts[1]).toMatch(/^pasted-\d{6}-2\.png$/)
  })

  it('removeAttachments keeps numbering while a paste chip remains, resets after the last', () => {
    const ib = mount()
    bigPaste(ib.textarea, 'a'.repeat(900))
    bigPaste(ib.textarea, 'b'.repeat(900))
    ib.removeAttachments([ib.getAttachments()[0]])
    bigPaste(ib.textarea, 'c'.repeat(900))
    const chips = ib.root.querySelectorAll('[data-paste-chip] .whitespace-nowrap')
    expect(chips.length).toBe(2)
    expect(chips[chips.length - 1].textContent).toBe('#3 +1 lines')
    ib.removeAttachments(ib.getAttachments())
    bigPaste(ib.textarea, 'd'.repeat(900))
    const fresh = ib.root.querySelector('[data-paste-chip] .whitespace-nowrap')!
    expect(fresh.textContent).toBe('#1 +1 lines')
  })

  it('removeAttachments resets numbering once no paste chip remains (docs only left)', () => {
    const ib = mount()
    bigPaste(ib.textarea, 'a'.repeat(900))
    ib.addRestoredAttachment({ id: 'd1', mime: 'text/plain', name: 'a.txt' })
    ib.removeAttachments([ib.getAttachments()[0]])
    expect(ib.getAttachments()[0].kind).toBe('document')
    bigPaste(ib.textarea, 'c'.repeat(900))
    const labels = Array.from(ib.root.querySelectorAll('[data-paste-chip] .whitespace-nowrap')).map(
      (el) => el.textContent,
    )
    expect(labels).toEqual(['#1 +1 lines'])
    // Dropping the last chip re-dims send once the input is empty again.
    ib.removeAttachments(ib.getAttachments())
    expect(sendBtnOf(ib).className).toContain('opacity-50')
  })
})

describe('input bar mutation kills: chip removal numbering via the × button', () => {
  function chipLabels(ib: ReturnType<typeof createInputBar>): (string | null)[] {
    return Array.from(ib.root.querySelectorAll('[data-paste-chip] .whitespace-nowrap')).map(
      (el) => el.textContent,
    )
  }
  function clickRemove(ib: ReturnType<typeof createInputBar>, index: number) {
    const xs = ib.root.querySelectorAll('button[aria-label="Remove attachment"]')
    ;(xs[index] as HTMLButtonElement).click()
  }

  it('removing the last remaining paste chip restarts numbering', () => {
    const ib = mount()
    bigPaste(ib.textarea, 'a'.repeat(900))
    clickRemove(ib, 0)
    expect(ib.getAttachments().length).toBe(0)
    bigPaste(ib.textarea, 'b'.repeat(900))
    expect(chipLabels(ib)).toEqual(['#1 +1 lines'])
  })

  it('removing one paste chip keeps numbering for the survivor', () => {
    const ib = mount()
    bigPaste(ib.textarea, 'a'.repeat(900))
    bigPaste(ib.textarea, 'b'.repeat(900))
    clickRemove(ib, 0)
    bigPaste(ib.textarea, 'c'.repeat(900))
    expect(chipLabels(ib)).toEqual(['#2 +1 lines', '#3 +1 lines'])
  })

  it('removing a doc chip while a paste chip remains keeps numbering', () => {
    const ib = mount()
    bigPaste(ib.textarea, 'a'.repeat(900))
    ib.addRestoredAttachment({ id: 'd1', mime: 'text/plain', name: 'a.txt' })
    ib.addRestoredAttachment({ id: 'd2', mime: 'text/plain', name: 'b.txt' })
    clickRemove(ib, 1)
    bigPaste(ib.textarea, 'c'.repeat(900))
    expect(chipLabels(ib)).toEqual(['#1 +1 lines', '#2 +1 lines'])
  })

  it('removing the only paste chip among docs restarts numbering', () => {
    const ib = mount()
    bigPaste(ib.textarea, 'a'.repeat(900))
    ib.addRestoredAttachment({ id: 'd1', mime: 'text/plain', name: 'a.txt' })
    clickRemove(ib, 0)
    bigPaste(ib.textarea, 'c'.repeat(900))
    expect(chipLabels(ib)).toEqual(['#1 +1 lines'])
  })
})

describe('input bar mutation kills: drag-drop ring and attachment removal effects', () => {
  it('dragenter preventDefaults and depth-tracking keeps the ring through nested leaves', () => {
    const ib = mount()
    const card = ib.root.querySelector('.card-bg') as HTMLElement
    const enter = new Event('dragenter', { bubbles: true, cancelable: true })
    card.dispatchEvent(enter)
    expect(enter.defaultPrevented).toBe(true)
    expect(card.classList.contains('ring-2')).toBe(true)
    card.dispatchEvent(new Event('dragenter', { bubbles: true }))
    card.dispatchEvent(new Event('dragleave'))
    expect(card.classList.contains('ring-2')).toBe(true)
    card.dispatchEvent(new Event('dragleave'))
    expect(card.classList.contains('ring-2')).toBe(false)
  })

  it('dropping an image chip revokes its blob URL on × and re-dims send', () => {
    const ib = mount()
    const btn = sendBtnOf(ib)
    dropFiles(ib, [new File(['img'], 'pic.png', { type: 'image/png' })])
    const img = ib.root.querySelector('img[alt="pic.png"]') as HTMLImageElement
    expect(img).toBeTruthy()
    expect(btn.className).not.toContain('opacity-50')
    const previewURL = (ib.getAttachments()[0] as { previewURL: string }).previewURL
    expect(img.src).toBe(previewURL)
    expect(previewURL.startsWith('blob:')).toBe(true)
    const revoke = vi.spyOn(URL, 'revokeObjectURL')
    try {
      const x = ib.root.querySelector('button[aria-label="Remove attachment"]') as HTMLButtonElement
      expect(x.classList.contains('absolute')).toBe(true)
      expect(x.classList.contains('-top-1')).toBe(true)
      x.click()
      expect(revoke).toHaveBeenCalledWith(previewURL)
    } finally {
      revoke.mockRestore()
    }
    expect(ib.root.querySelector('img[alt="pic.png"]')).toBeNull()
    expect(btn.className).toContain('opacity-50')
  })

  it('dropping a document renders a plain (unfailed) doc chip and preventDefaults the drop', () => {
    const ib = mount()
    const ev = dropFiles(ib, [new File(['abc'], 'note.txt', { type: 'text/plain' })])
    expect(ev.defaultPrevented).toBe(true)
    const span = ib.root.querySelector('span.text-\\[12px\\]') as HTMLElement
    expect(span).toBeTruthy()
    expect(span.classList.contains('bg-ink2')).toBe(true)
    expect(span.classList.contains('border-red-500')).toBe(false)
    expect(span.textContent).toBe('[note.txt 3 B]')
  })

  it('clearAttachments wipes the chip strip and re-dims send', () => {
    const ib = mount()
    const btn = sendBtnOf(ib)
    dropFiles(ib, [new File(['abc'], 'note.txt', { type: 'text/plain' })])
    ib.clearAttachments({ keepSentBlobURLs: true })
    expect(ib.getAttachments().length).toBe(0)
    expect(ib.root.querySelector('button[aria-label="Remove attachment"]')).toBeNull()
    expect(btn.className).toContain('opacity-50')
  })
})

describe('input bar mutation kills: failed chip retry affordances', () => {
  function failedDocChip() {
    const ib = mount()
    dropFiles(ib, [new File(['abc'], 'note.txt', { type: 'text/plain' })])
    ib.markAttachmentFailures(ib.getAttachments())
    const span = ib.root.querySelector('span.text-\\[12px\\]') as HTMLElement
    return { ib, span }
  }

  it('a failed doc chip gets the red border and a positioned retry button', () => {
    const { ib, span } = failedDocChip()
    expect(span.classList.contains('border-red-500')).toBe(true)
    const retry = ib.root.querySelector('button[aria-label="Retry upload"]') as HTMLButtonElement
    expect(retry).toBeTruthy()
    expect(retry.classList.contains('absolute')).toBe(true)
    expect(retry.classList.contains('disabled:opacity-40')).toBe(true)
  })

  it('retry without a registered callback leaves the chip failed', () => {
    const { ib, span } = failedDocChip()
    const retry = ib.root.querySelector('button[aria-label="Retry upload"]') as HTMLButtonElement
    retry.click()
    expect(span.classList.contains('border-red-500')).toBe(true)
    expect(ib.root.querySelector('button[aria-label="Retry upload"]')).toBeTruthy()
  })

  it('a second retry click while one is in flight is ignored', () => {
    const { ib } = failedDocChip()
    const cb = vi.fn(() => new Promise<boolean>(() => {}))
    ib.onRetryAttachment(cb)
    const retry = ib.root.querySelector('button[aria-label="Retry upload"]') as HTMLButtonElement
    retry.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    retry.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    expect(cb).toHaveBeenCalledTimes(1)
  })

  it('a successful retry clears the failed state and re-renders', async () => {
    const { ib } = failedDocChip()
    const refs = ib.getAttachments()
    ib.onRetryAttachment(async (ref) => {
      expect(ref).toBe(refs[0])
      return true
    })
    const retry = ib.root.querySelector('button[aria-label="Retry upload"]') as HTMLButtonElement
    retry.click()
    // renderChips rebuilds the strip: re-query inside the wait, the captured
    // span is detached by the re-render.
    await vi.waitFor(() => {
      const fresh = ib.root.querySelector('span.text-\\[12px\\]') as HTMLElement
      expect(fresh.classList.contains('border-red-500')).toBe(false)
    })
    expect(ib.root.querySelector('button[aria-label="Retry upload"]')).toBeNull()
  })

  it('upload progress paints a half-width bar inside the chip', () => {
    const ib = mount()
    dropFiles(ib, [new File(['abcdef'], 'p.txt', { type: 'text/plain' })])
    ib.setAttachmentProgress(ib.getAttachments()[0], 0.5)
    const chipSpan = ib.root.querySelector('span.text-\\[12px\\]') as HTMLElement
    const bar = chipSpan.parentElement!.querySelector('.h-0\\.5') as HTMLElement
    expect(bar).toBeTruthy()
    expect((bar.firstElementChild as HTMLElement).style.width).toBe('50%')
  })
})

describe('input bar mutation kills: paste chip edit popup interactions', () => {
  const editPopup = () => document.body.querySelector('[data-edit-popup]') as HTMLElement
  const editTa = () => editPopup().querySelector('textarea') as HTMLTextAreaElement

  function openPopup(ib: ReturnType<typeof createInputBar>) {
    bigPaste(ib.textarea, 'line1\nline2\nline3\nline4')
    const chip = ib.root.querySelector('[data-paste-chip]') as HTMLElement
    chip.click()
    return chip
  }

  it('chip click opens the popup focused with the paste text', () => {
    const ib = mount()
    openPopup(ib)
    expect(editPopup().classList.contains('hidden')).toBe(false)
    expect(editTa().value).toBe('line1\nline2\nline3\nline4')
    expect(document.activeElement).toBe(editTa())
  })

  it('Enter and Space open the popup from the keyboard; other keys do not', () => {
    const ib = mount()
    bigPaste(ib.textarea, 'line1\nline2\nline3\nline4')
    const chip = ib.root.querySelector('[data-paste-chip]') as HTMLElement
    const enterEv = cancelableKeydown(chip, 'Enter')
    expect(enterEv.defaultPrevented).toBe(true)
    expect(editPopup().classList.contains('hidden')).toBe(false)
    cancelableKeydown(editTa(), 'Escape')
    expect(editPopup().classList.contains('hidden')).toBe(true)
    cancelableKeydown(chip, ' ')
    expect(editPopup().classList.contains('hidden')).toBe(false)
    cancelableKeydown(editTa(), 'Escape')
    cancelableKeydown(chip, 'x')
    expect(editPopup().classList.contains('hidden')).toBe(true)
  })

  it('typing a plain key in the popup keeps it open; Escape closes', () => {
    const ib = mount()
    openPopup(ib)
    cancelableKeydown(editTa(), 'a')
    expect(editPopup().classList.contains('hidden')).toBe(false)
    cancelableKeydown(editTa(), 'Escape')
    expect(editPopup().classList.contains('hidden')).toBe(true)
  })

  it('mousedown inside the popup does not dismiss it', () => {
    const ib = mount()
    openPopup(ib)
    editTa().dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }))
    expect(editPopup().classList.contains('hidden')).toBe(false)
  })
})

describe('input bar mutation kills: STT span state machine edges', () => {
  it('deltas, partials, and commits before begin are all no-ops', () => {
    const ib = mount()
    ib.textarea.value = '既有'
    ib.appendSttDelta('新')
    ib.replaceSttSpan('新')
    ib.commitSttUtterance('新')
    expect(ib.textarea.value).toBe('既有')
  })

  it('commit before begin on an empty textarea is a no-op', () => {
    const ib = mount()
    ib.commitSttUtterance('X')
    expect(ib.textarea.value).toBe('')
  })

  it('the caret rides the end on same-value partial and commit renders', () => {
    const ib = mount()
    ib.beginSttUtterance()
    ib.appendSttDelta('你好')
    ib.textarea.setSelectionRange(0, 0)
    ib.commitSttUtterance('你好')
    expect(ib.textarea.value).toBe('你好')
    expect(ib.textarea.selectionStart).toBe(2)
    ib.beginSttUtterance()
    ib.textarea.setSelectionRange(0, 0)
    ib.replaceSttSpan('')
    expect(ib.textarea.selectionStart).toBe(2)
  })

  it('STT renders recompute send availability as text arrives', () => {
    const ib = mount()
    const btn = sendBtnOf(ib)
    expect(btn.className).toContain('opacity-50')
    ib.beginSttUtterance()
    ib.appendSttDelta('你')
    expect(btn.className).not.toContain('opacity-50')
    ib.textarea.value = ''
    ib.textarea.dispatchEvent(new Event('input', { bubbles: true }))
    expect(btn.className).toContain('opacity-50')
    ib.beginSttUtterance()
    ib.replaceSttSpan('好')
    expect(btn.className).not.toContain('opacity-50')
    ib.textarea.value = ''
    ib.textarea.dispatchEvent(new Event('input', { bubbles: true }))
    ib.beginSttUtterance()
    ib.commitSttUtterance('定稿')
    expect(btn.className).not.toContain('opacity-50')
  })

  it('endSttUtterance freezes the span so a later commit is inert at the boundary', () => {
    const ib = mount()
    ib.beginSttUtterance()
    ib.appendSttDelta('a')
    ib.endSttUtterance()
    ib.commitSttUtterance('X')
    expect(ib.textarea.value).toBe('a')
  })

  it('setSttStatus logs the error line exactly and stays silent on recovery', () => {
    const ib = mount()
    const log = vi.spyOn(console, 'log').mockImplementation(() => {})
    try {
      ib.setSttStatus('boom')
      expect(log).toHaveBeenCalledWith('[stt] boom')
      log.mockClear()
      ib.setSttStatus(null)
      expect(log).not.toHaveBeenCalled()
    } finally {
      log.mockRestore()
    }
  })
})

describe('input bar mutation kills: desktop keyboard routing edges', () => {
  it('Escape while idle neither stops nor preventDefaults', () => {
    const ib = mount()
    const stop = vi.fn()
    ib.onStop(stop)
    const ev = cancelableKeydown(ib.textarea, 'Escape')
    expect(ev.defaultPrevented).toBe(false)
    expect(stop).not.toHaveBeenCalled()
  })

  it('Escape while streaming stops and preventDefaults', () => {
    const ib = mount()
    const stop = vi.fn()
    ib.onStop(stop)
    ib.setStreaming(true)
    const ev = cancelableKeydown(ib.textarea, 'Escape')
    expect(ev.defaultPrevented).toBe(true)
    expect(stop).toHaveBeenCalledTimes(1)
  })

  it('the ArrowUp cancel-queued branch preventDefaults', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.setQueuedMsgs([{ uuid: 'u-1', text: 'hello' }])
    const ev = cancelableKeydown(ib.textarea, 'ArrowUp')
    expect(ev.defaultPrevented).toBe(true)
  })

  it('cursor at position 0 of a leading-newline value navigates history', () => {
    const ib = mount()
    const up = vi.fn(() => 'replaced' as string | null)
    ib.onHistoryUp(up)
    ib.textarea.value = '\nabc'
    ib.textarea.setSelectionRange(0, 0)
    cancelableKeydown(ib.textarea, 'ArrowUp')
    expect(up).toHaveBeenCalledTimes(1)
    expect(ib.textarea.value).toBe('replaced')
  })

  it('cursor before the newline of line 0 still navigates history', () => {
    const ib = mount()
    const up = vi.fn(() => 'replaced' as string | null)
    ib.onHistoryUp(up)
    ib.textarea.value = 'ab\ncd'
    ib.textarea.setSelectionRange(2, 2)
    cancelableKeydown(ib.textarea, 'ArrowUp')
    expect(up).toHaveBeenCalledTimes(1)
  })

  it('history navigation on an empty input preventDefaults and undims send (up and down)', () => {
    const ib = mount()
    const btn = sendBtnOf(ib)
    expect(btn.className).toContain('opacity-50')
    ib.onHistoryUp(() => 'old query')
    const upEv = cancelableKeydown(ib.textarea, 'ArrowUp')
    expect(upEv.defaultPrevented).toBe(true)
    expect(btn.className).not.toContain('opacity-50')
    ib.textarea.value = ''
    ib.textarea.dispatchEvent(new Event('input', { bubbles: true }))
    expect(btn.className).toContain('opacity-50')
    ib.onHistoryDown(() => 'restored')
    const downEv = cancelableKeydown(ib.textarea, 'ArrowDown')
    expect(downEv.defaultPrevented).toBe(true)
    expect(btn.className).not.toContain('opacity-50')
  })

  it('ArrowDown parks the caret at the end when the restored text equals the current one', () => {
    const ib = mount()
    ib.onHistoryDown(() => 'x')
    ib.textarea.value = 'x'
    ib.textarea.setSelectionRange(0, 0)
    cancelableKeydown(ib.textarea, 'ArrowDown')
    expect(ib.textarea.selectionStart).toBe(1)
  })

  it('unhandled keys while streaming with a queue do not cancel', () => {
    const ib = mount()
    const cancel = vi.fn()
    ib.onCancelQueued(cancel)
    ib.setStreaming(true)
    ib.setQueuedMsgs([{ uuid: 'u-1', text: 'hello' }])
    const ev = cancelableKeydown(ib.textarea, 'z')
    expect(cancel).not.toHaveBeenCalled()
    expect(ev.defaultPrevented).toBe(false)
  })
})

describe('input bar mutation kills: uploading and connection gating', () => {
  it('setUploading(false) keeps every control gated by connection and STT config', () => {
    const offline = createInputBar({ connected: false })
    document.body.appendChild(offline.root)
    offline.setUploading(false)
    const plus = offline.root.querySelector(
      'button[aria-label="Attach file"]',
    ) as HTMLButtonElement
    expect(plus.disabled).toBe(true)
    const ib = mount()
    ib.setUploading(false)
    expect(micOf(ib).disabled).toBe(true)
    ib.setSttAvailable(true)
    ib.setUploading(false)
    expect(micOf(ib).disabled).toBe(false)
  })

  it('an upload finishing does not close an open attach panel', () => {
    const ib = mount()
    const plus = ib.root.querySelector('button[aria-label="Attach file"]') as HTMLButtonElement
    plus.click()
    const open = () => {
      for (const p of document.body.querySelectorAll('div')) {
        if (p.classList.contains('hidden')) continue
        const hasAttach =
          !!p.querySelector('button[aria-label="Camera"]')
          || !!p.querySelector('button[aria-label="Image"]')
          || !!p.querySelector('button[aria-label="File"]')
        if (hasAttach) return p as HTMLElement
      }
      return null
    }
    expect(open()).not.toBeNull()
    ib.setUploading(false)
    expect(open()).not.toBeNull()
    // A full upload cycle disables + while in flight and re-enables after.
    ib.setUploading(true)
    expect(plus.disabled).toBe(true)
    ib.setUploading(false)
    expect(plus.disabled).toBe(false)
  })

  it('setInputText and appendQueuedText focus the textarea and undim send', () => {
    const ib = mount()
    const btn = sendBtnOf(ib)
    expect(btn.className).toContain('opacity-50')
    ib.setInputText('hi')
    expect(document.activeElement).toBe(ib.textarea)
    expect(btn.className).not.toContain('opacity-50')
    ib.textarea.value = ''
    ib.textarea.dispatchEvent(new Event('input', { bubbles: true }))
    expect(btn.className).toContain('opacity-50')
    // setInputText above already focused the textarea — blur so only
    // appendQueuedText can reclaim focus.
    ib.textarea.blur()
    expect(document.activeElement).not.toBe(ib.textarea)
    ib.appendQueuedText('queued')
    expect(document.activeElement).toBe(ib.textarea)
    expect(btn.className).not.toContain('opacity-50')
  })
})

describe('input bar mutation kills: queue bubble +N more badge', () => {
  it('the first bubble of a multi-message queue shows +N more with idle styling', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.setQueuedMsgs([
      { uuid: 'a', text: 'one' },
      { uuid: 'b', text: 'two' },
      { uuid: 'c', text: 'three' },
    ])
    expect(ib.bubbles.textContent).toContain('+2 more')
    const badges = Array.from(ib.bubbles.querySelectorAll('span')).filter((s) =>
      (s.textContent ?? '').endsWith(' more'),
    )
    expect(badges.length).toBe(1)
    expect(ib.bubbles.children[0].textContent).toContain('+2 more')
    expect(ib.bubbles.children[1].textContent).not.toContain('more')
    expect(badges[0].classList.contains('text-t3')).toBe(true)
    ib.setQueuedMsgs([{ uuid: 'a', text: 'one' }])
    expect(ib.bubbles.textContent).not.toContain('more')
  })

  it('bubble label and cancel hint carry their truncation and idle styling', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.setQueuedMsgs([{ uuid: 'a', text: 'one' }])
    const bubble = ib.bubbles.children[0] as HTMLElement
    const label = bubble.querySelector('span.truncate') as HTMLElement
    expect(label.textContent).toBe('one')
    expect(label.classList.contains('text-t2')).toBe(true)
    const cancel = bubble.querySelector('span.mono') as HTMLElement
    expect(cancel.textContent).toBe('Tap to CANCEL')
    expect(cancel.classList.contains('text-t3')).toBe(true)
  })
})

describe('input bar mutation kills: restored attachment metadata', () => {
  it('document restore without a name renders the generic label and a typed stub File', () => {
    const ib = mount()
    ib.addRestoredAttachment({ id: 'r1', mime: 'application/pdf', size: 2048 })
    const chip = ib.root.querySelector('span.text-\\[12px\\]') as HTMLElement
    expect(chip.textContent).toBe('[document 0 B]')
    const ref = ib.getAttachments()[0]
    expect(ref.kind).toBe('document')
    expect(ref.file.type).toBe('application/pdf')
    expect(ref.file.size).toBe(0)
    expect((ref as { restoredSize?: number }).restoredSize).toBe(2048)
  })

  it('image restore with a bare image mime falls back to the png extension', () => {
    const ib = mount()
    ib.addRestoredAttachment({ id: 'r2', mime: 'image/', size: 8 })
    const img = ib.root.querySelector('img') as HTMLImageElement
    expect(img).toBeTruthy()
    expect(img.alt).toBe('image.png')
  })

  it('image restore stubs an empty, correctly-typed File', () => {
    const ib = mount()
    ib.addRestoredAttachment({ id: 'r4', mime: 'image/jpeg', size: 4096, name: 'cam.jpg' })
    const ref = ib.getAttachments()[0]
    expect(ref.kind).toBe('image')
    expect(ref.file.type).toBe('image/jpeg')
    expect(ref.file.size).toBe(0)
    expect((ref as { uploadedID?: string }).uploadedID).toBe('r4')
  })

  it('restoring an attachment undims send', () => {
    const ib = mount()
    const btn = sendBtnOf(ib)
    expect(btn.className).toContain('opacity-50')
    ib.addRestoredAttachment({ id: 'r3', mime: 'text/plain', size: 1 })
    expect(btn.className).not.toContain('opacity-50')
  })
})

describe('input bar mutation kills: attach panel close on selection and disabled plus', () => {
  const visiblePanel = () => {
    for (const p of document.body.querySelectorAll('div')) {
      if (p.classList.contains('hidden')) continue
      const hasAttach =
        !!p.querySelector('button[aria-label="Camera"]')
        || !!p.querySelector('button[aria-label="Image"]')
        || !!p.querySelector('button[aria-label="File"]')
      if (hasAttach) return p as HTMLElement
    }
    return null
  }

  it('picking any attach action closes the panel', () => {
    const ib = mount()
    const plus = ib.root.querySelector('button[aria-label="Attach file"]') as HTMLButtonElement
    for (const label of ['Camera', 'Image', 'File']) {
      plus.click()
      expect(visiblePanel()).not.toBeNull()
      const action = visiblePanel()!.querySelector(`button[aria-label="${label}"]`) as HTMLButtonElement
      action.click()
      expect(visiblePanel()).toBeNull()
    }
  })

  it('clicking the disabled + button never opens the panel', () => {
    const ib = createInputBar({ connected: false })
    document.body.appendChild(ib.root)
    const plus = ib.root.querySelector('button[aria-label="Attach file"]') as HTMLButtonElement
    plus.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    expect(visiblePanel()).toBeNull()
  })

  it('setConnected gates the + button, re-enables send without typing, and only closes the panel on disconnect', () => {
    const ib = createInputBar({ connected: false })
    document.body.appendChild(ib.root)
    const plus = ib.root.querySelector('button[aria-label="Attach file"]') as HTMLButtonElement
    const send = ib.root.querySelector('button[aria-label="Send"]') as HTMLButtonElement
    ib.setConnected(true)
    expect(plus.disabled).toBe(false)
    expect(send.disabled).toBe(false)
    plus.click()
    expect(visiblePanel()).not.toBeNull()
    // A reconnect while already open must not steal the panel.
    ib.setConnected(true)
    expect(visiblePanel()).not.toBeNull()
    ib.setConnected(false)
    expect(plus.disabled).toBe(true)
    expect(visiblePanel()).toBeNull()
  })
})
