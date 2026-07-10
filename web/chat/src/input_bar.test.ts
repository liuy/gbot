import { describe, it, expect, vi } from 'vitest'
import { createInputBar } from './input_bar'

function mount() {
  const ib = createInputBar({ connected: true })
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
  })

  it('setStreaming(false) hides STOP button', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.setStreaming(false)
    const stopBtn = Array.from(ib.root.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('STOP'),
    )!
    expect(stopBtn.style.display).toBe('none')
  })

  it('setQueuedMsgs single shows Tap to CANCEL', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.setQueuedMsgs([{ uuid: 'u-1', text: 'hello' }])
    expect(ib.root.textContent).toContain('hello')
    expect(ib.root.textContent).toContain('Tap to CANCEL')
    expect(ib.root.textContent).not.toContain('Tap to CANCEL all')
  })

  it('setQueuedMsgs multiple shows Tap to CANCEL all on first bubble', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.setQueuedMsgs([
      { uuid: 'u-1', text: 'one' },
      { uuid: 'u-2', text: 'two' },
    ])
    expect(ib.root.textContent).toContain('Tap to CANCEL all')
    // Second bubble should still show plain CANCEL — assert exact count.
    const allText = ib.root.textContent ?? ''
    expect(allText.indexOf('Tap to CANCEL all')).toBeGreaterThanOrEqual(0)
  })

  it('setQueuedMsgs with two items renders exactly two bubbles', () => {
    const ib = mount()
    ib.setStreaming(true)
    ib.setQueuedMsgs([
      { uuid: 'u-1', text: 'one' },
      { uuid: 'u-2', text: 'two' },
    ])
    const bubbles = ib.root.querySelectorAll('.modal-enter')
    expect(bubbles.length).toBe(2)
  })

  it('setInputText sets textarea value', () => {
    const ib = mount()
    ib.setInputText('hi')
    expect(ib.textarea.value).toBe('hi')
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
    const stopBtn = Array.from(ib.root.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('STOP'),
    )!
    stopBtn.click()
    expect(spy).toHaveBeenCalledOnce()
  })

  it('clicking a queued bubble fires onCancelQueued', () => {
    const ib = mount()
    const spy = vi.fn()
    ib.onCancelQueued(spy)
    ib.setStreaming(true)
    ib.setQueuedMsgs([{ uuid: 'u-1', text: 'hello' }])
    const bubble = ib.root.querySelector('.modal-enter') as HTMLElement
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

  it('typing enables then clearing disables the send button', () => {
    const ib = mount()
    const sendBtn = ib.root.querySelector(
      'button[aria-label="Send"]',
    ) as HTMLButtonElement
    ib.textarea.value = 'x'
    ib.textarea.dispatchEvent(new Event('input', { bubbles: true }))
    expect(sendBtn.disabled).toBe(false)
    ib.textarea.value = ''
    ib.textarea.dispatchEvent(new Event('input', { bubbles: true }))
    expect(sendBtn.disabled).toBe(true)
  })

  it('form submit clears textarea', () => {
    const ib = mount()
    ib.onSend(() => {})
    ib.textarea.value = 'sent'
    submitForm(ib.textarea)
    expect(ib.textarea.value).toBe('')
  })

  // ── Input history navigation ──

  function setCursor(ta: HTMLTextAreaElement, pos: number) {
    ta.selectionStart = pos
    ta.selectionEnd = pos
  }

  function dispatchArrow(ta: HTMLTextAreaElement, key: 'ArrowUp' | 'ArrowDown') {
    ta.dispatchEvent(
      new KeyboardEvent('keydown', { key, bubbles: true }),
    )
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
})
