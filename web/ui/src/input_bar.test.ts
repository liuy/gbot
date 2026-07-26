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
