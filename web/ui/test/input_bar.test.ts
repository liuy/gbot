import { describe, it, expect, beforeEach, afterEach, vi, type Mock } from 'vitest'
import { createInputBar, type AttachmentRef } from '../src/input_bar'

// dispatchPaste: jsdom 29 has no ClipboardEvent/DataTransfer (issue #1568).
// Build a synthetic Event with a stubbed clipboardData via
// dispatchPaste simulates an Android WebView paste via beforeinput +
// inputType=insertFromPaste (the production code uses beforeinput, not
// the 'paste' event, because Android IMEs often bypass paste events).
// Returns { evt, spy } so call sites can assert on preventDefault.
function dispatchPaste(textarea: HTMLTextAreaElement, text: string): { evt: InputEvent; spy: Mock } {
  // jsdom's InputEvent constructor doesn't accept inputType in the init dict
  // reliably, so we build a synthetic InputEvent and define the required
  // fields via Object.defineProperty.
  const evt = new InputEvent('beforeinput', { bubbles: true, cancelable: true })
  Object.defineProperty(evt, 'inputType', {
    value: 'insertFromPaste',
    writable: false, configurable: true,
  })
  Object.defineProperty(evt, 'data', {
    value: text,
    writable: false, configurable: true,
  })
  Object.defineProperty(evt, 'dataTransfer', {
    value: { getData: (t: string) => t === 'text/plain' ? text : '' },
    writable: false, configurable: true,
  })
  const spy = vi.spyOn(evt as unknown as { preventDefault: () => void }, 'preventDefault')
  textarea.dispatchEvent(evt)
  return { evt, spy }
}

// getEditPopup returns the edit popup element from document.body. The popup
// is lazily appended on first open, so callers must open it first.
function getEditPopup(): HTMLElement {
  return document.body.querySelector('[data-edit-popup]')!
}

describe('createInputBar attachments', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  afterEach(() => {
    // vi.spyOn replaces methods on the host object; without restore, the
    // spy from a prior test keeps accumulating call counts into the next
    // test's assertion. Restore between tests so each sees a fresh spy.
    vi.restoreAllMocks()
  })

  it('PlusButton_InitiallyEnabledWhenConnected', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const plusBtn = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Attach file"]',
    )
    if (!plusBtn) throw new Error('plus button not found')
    // Token-gating is gone: connected at construct time → + is enabled.
    expect(plusBtn.disabled).toBe(false)
  })

  it('PlusButton_InitiallyDisabledWhenDisconnected', () => {
    const handles = createInputBar({ connected: false })
    document.body.appendChild(handles.root)
    const plusBtn = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Attach file"]',
    )!
    expect(plusBtn.disabled).toBe(true)
  })

  it('PlusButton_ClickOpensAttachPopup', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const plusBtn = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Attach file"]',
    )!
    // Popup is lazily appended to document.body on first open.
    expect(document.body.querySelector('button[aria-label="Camera"]')).toBeNull()
    plusBtn.click()
    // Three icon buttons are now visible inside the popup.
    expect(document.body.querySelector('button[aria-label="Camera"]')).not.toBeNull()
    expect(document.body.querySelector('button[aria-label="Image"]')).not.toBeNull()
    expect(document.body.querySelector('button[aria-label="File"]')).not.toBeNull()
  })

  it('PlusButton_ClickAgainClosesAttachPopup', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const plusBtn = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Attach file"]',
    )!
    plusBtn.click()
    // Camera button's parentElement is the attach panel itself.
    const panel = document.body.querySelector('button[aria-label="Camera"]')!
      .parentElement as HTMLElement
    expect(panel.classList.contains('hidden')).toBe(false)
    plusBtn.click()
    expect(panel.classList.contains('hidden')).toBe(true)
  })

  it('PopupIcons_ClickTriggersCorrectInput', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const plusBtn = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Attach file"]',
    )!
    plusBtn.click()

    const cameraInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][capture]',
    )!
    const imageInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    const docInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept]:not([accept*="image/*"])',
    )!

    const cameraSpy = vi.spyOn(cameraInput, 'click')
    const imageSpy = vi.spyOn(imageInput, 'click')
    const docSpy = vi.spyOn(docInput, 'click')

    document.body.querySelector<HTMLButtonElement>('button[aria-label="Camera"]')!.click()
    expect(cameraSpy).toHaveBeenCalledTimes(1)
    // Popup closes after click, so we need to reopen for next icon.
    plusBtn.click()
    document.body.querySelector<HTMLButtonElement>('button[aria-label="Image"]')!.click()
    expect(imageSpy).toHaveBeenCalledTimes(1)
    plusBtn.click()
    document.body.querySelector<HTMLButtonElement>('button[aria-label="File"]')!.click()
    expect(docSpy).toHaveBeenCalledTimes(1)
  })

  it('AttachPopup_ClosesOnOutsideClick', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const plusBtn = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Attach file"]',
    )!
    plusBtn.click()
    const panel = document.body.querySelector('button[aria-label="Camera"]')!
      .parentElement as HTMLElement
    expect(panel.classList.contains('hidden')).toBe(false)
    // Click on something outside both + button and panel.
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    expect(panel.classList.contains('hidden')).toBe(true)
  })

  it('AttachPopup_ClosesOnSetUploading', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const plusBtn = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Attach file"]',
    )!
    plusBtn.click()
    const panel = document.body.querySelector('button[aria-label="Camera"]')!
      .parentElement as HTMLElement
    expect(panel.classList.contains('hidden')).toBe(false)
    handles.setUploading(true)
    expect(panel.classList.contains('hidden')).toBe(true)
  })

  it('AttachPopup_ClosesOnSetConnectedFalse', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const plusBtn = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Attach file"]',
    )!
    plusBtn.click()
    const panel = document.body.querySelector('button[aria-label="Camera"]')!
      .parentElement as HTMLElement
    expect(panel.classList.contains('hidden')).toBe(false)
    handles.setConnected(false)
    expect(panel.classList.contains('hidden')).toBe(true)
  })

  it('CameraInput_LacksMultipleAttribute', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    // cameraInput is the only one with capture — distinguishes it from imageInput.
    const cameraInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][capture]',
    )!
    expect(cameraInput.multiple).toBe(false)
  })

  it('PlusButton_DisabledClickDoesNotOpenFilePicker', () => {
    // Button is disabled because the connector is disconnected (the new
    // gating signal — no more uploadToken to wait for).
    const handles = createInputBar({ connected: false })
    document.body.appendChild(handles.root)
    const plusBtn = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Attach file"]',
    )!
    const cameraInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][capture]',
    )!
    const imageInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    const docInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept]:not([accept*="image/*"])',
    )!
    const cameraSpy = vi.spyOn(cameraInput, 'click')
    const imageSpy = vi.spyOn(imageInput, 'click')
    const docSpy = vi.spyOn(docInput, 'click')
    plusBtn.click()
    expect(cameraSpy).not.toHaveBeenCalled()
    expect(imageSpy).not.toHaveBeenCalled()
    expect(docSpy).not.toHaveBeenCalled()
  })

  it('ImageAttachment_RendersThumbnailChip', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const fileInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    // Synthesize a File via the file input's files property.
    const blob = new Blob([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    // Simulate the user picking the file.
    Object.defineProperty(fileInput, 'files', { value: [file], writable: false })
    fileInput.dispatchEvent(new Event('change'))
    const chips = handles.root.querySelectorAll('img')
    expect(chips.length).toBe(1)
    expect(chips[0].src.startsWith('blob:')).toBe(true)
  })

  it('DocumentAttachment_RendersFilenameChip', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const fileInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept]:not([accept*="image/*"])',
    )!
    const blob = new Blob([new Uint8Array([0x25, 0x50, 0x44, 0x46])], { type: 'application/pdf' })
    const file = new File([blob], 'report.pdf', { type: 'application/pdf' })
    Object.defineProperty(fileInput, 'files', { value: [file], writable: false })
    fileInput.dispatchEvent(new Event('change'))
    const chipText = handles.root.textContent ?? ''
    expect(chipText).toContain('[report.pdf]')
  })

  it('RemoveChipButton_ClearsAttachment', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const fileInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    Object.defineProperty(fileInput, 'files', { value: [file], writable: false })
    fileInput.dispatchEvent(new Event('change'))
    expect(handles.getAttachments().length).toBe(1)
    const xBtn = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Remove attachment"]',
    )!
    xBtn.click()
    expect(handles.getAttachments().length).toBe(0)
    // Chip is gone from DOM.
    expect(handles.root.querySelectorAll('img').length).toBe(0)
  })

  it('AttachmentsEnableSend_EvenWithEmptyText', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const fileInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    Object.defineProperty(fileInput, 'files', { value: [file], writable: false })
    fileInput.dispatchEvent(new Event('change'))
    const sendBtn = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Send"]',
    )!
    expect(sendBtn.disabled).toBe(false)
  })

  it('AcceptAttribute_MatchesFilereadConvertibleExtensions', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const docInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept]:not([accept*="image/*"])',
    )!
    const imageInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    // docInput must include ALL of fileread.convertibleExtensions (11 entries).
    const docAccept = docInput.accept
    for (const ext of ['.pdf', '.doc', '.docx', '.ppt', '.pptx', '.xls', '.xlsx', '.epub', '.ipynb', '.csv', '.zip']) {
      expect(docAccept).toContain(ext)
    }
    // Plus plain-text formats fileread handles as text.
    for (const ext of ['.txt', '.md', '.json', '.xml', '.html']) {
      expect(docAccept).toContain(ext)
    }
    // docInput intentionally excludes image/* — images go through imageInput.
    expect(docAccept).not.toContain('image/*')
    expect(imageInput.accept).toContain('image/*')
    // RTF/ODT must NOT be present — fileread does not convert them.
    expect(docAccept).not.toContain('.rtf')
    expect(docAccept).not.toContain('.odt')
  })

  it('SetUploading_DisablesTextareaAndPlusAndSend', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const plusBtn = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Attach file"]',
    )!
    const sendBtn = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Send"]',
    )!
    expect(handles.textarea.disabled).toBe(false)
    expect(plusBtn.disabled).toBe(false)
    handles.setUploading(true)
    expect(handles.textarea.disabled).toBe(true)
    expect(plusBtn.disabled).toBe(true)
    expect(sendBtn.disabled).toBe(true)
  })

  it('SetUploading_DoesNotToggleStopIcon', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const sendBtn = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Send"]',
    )!
    const before = sendBtn.innerHTML
    handles.setUploading(true)
    expect(sendBtn.innerHTML).toBe(before)  // No STOP icon swap
  })

  it('ClearAttachments_ReleasesBlobURLsByDefault', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const fileInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    Object.defineProperty(fileInput, 'files', { value: [file], writable: false })
    fileInput.dispatchEvent(new Event('change'))
    const spy = vi.spyOn(URL, 'revokeObjectURL')
    handles.clearAttachments()
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('ClearAttachments_KeepSentBlobURLsDoesNotRevoke', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const fileInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    Object.defineProperty(fileInput, 'files', { value: [file], writable: false })
    fileInput.dispatchEvent(new Event('change'))
    const spy = vi.spyOn(URL, 'revokeObjectURL')
    handles.clearAttachments({ keepSentBlobURLs: true })
    expect(spy).not.toHaveBeenCalled()
    // Array still cleared.
    expect(handles.getAttachments().length).toBe(0)
  })

  it('MarkAttachmentFailures_AddsRedBorder', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const fileInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    Object.defineProperty(fileInput, 'files', { value: [file], writable: false })
    fileInput.dispatchEvent(new Event('change'))
    const ref = handles.getAttachments()[0]
    handles.markAttachmentFailures([ref])
    const img = handles.root.querySelector<HTMLImageElement>('img')!
    expect(img.className).toContain('border-red-500')
  })

  it('OnAttachmentsChange_FiresOnAdd', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    let calls = 0
    handles.onAttachmentsChange(() => { calls++ })
    const fileInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    const blob = new Blob([new Uint8Array([0x89])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    Object.defineProperty(fileInput, 'files', { value: [file], writable: false })
    fileInput.dispatchEvent(new Event('change'))
    expect(calls).toBe(1)
  })

  it('FailedChip_RendersRetryButton', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const fileInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    Object.defineProperty(fileInput, 'files', { value: [file], writable: false })
    fileInput.dispatchEvent(new Event('change'))
    const ref = handles.getAttachments()[0]
    handles.markAttachmentFailures([ref])
    // Retry button is only present when ref.failed === true.
    const retry = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Retry upload"]',
    )
    expect(retry).not.toBeNull()
  })

  it('RetryButton_ClickInvokesCallbackAndClearsFailedOnSuccess', async () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const fileInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    Object.defineProperty(fileInput, 'files', { value: [file], writable: false })
    fileInput.dispatchEvent(new Event('change'))
    const ref = handles.getAttachments()[0]
    handles.markAttachmentFailures([ref])
    let retriedRef: AttachmentRef | null = null
    handles.onRetryAttachment(async (r) => {
      retriedRef = r
      return true
    })
    const retry = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Retry upload"]',
    )!
    retry.click()
    // The click handler awaits retryCb; flush microtasks.
    await new Promise((r) => { setTimeout(r, 0) })
    expect(retriedRef).toBe(ref)
    // Success cleared failed state — the retry button is gone (renderChips
    // only renders it on failed chips) and the red border is removed.
    const retryAfter = handles.root.querySelector(
      'button[aria-label="Retry upload"]',
    )
    expect(retryAfter).toBeNull()
    const img = handles.root.querySelector<HTMLImageElement>('img')!
    expect(img.className).not.toContain('border-red-500')
  })

  it('RetryButton_FailureKeepsFailedState', async () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const fileInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    Object.defineProperty(fileInput, 'files', { value: [file], writable: false })
    fileInput.dispatchEvent(new Event('change'))
    const ref = handles.getAttachments()[0]
    handles.markAttachmentFailures([ref])
    handles.onRetryAttachment(async () => false)
    const retry = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Retry upload"]',
    )!
    retry.click()
    await new Promise((r) => { setTimeout(r, 0) })
    // Failed state stays — retry button still rendered so the user can retry again.
    const retryAfter = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Retry upload"]',
    )
    expect(retryAfter).not.toBeNull()
    const img = handles.root.querySelector<HTMLImageElement>('img')!
    expect(img.className).toContain('border-red-500')
  })

  it('RetryButton_DisabledDuringInFlight', async () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const fileInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    Object.defineProperty(fileInput, 'files', { value: [file], writable: false })
    fileInput.dispatchEvent(new Event('change'))
    const ref = handles.getAttachments()[0]
    handles.markAttachmentFailures([ref])
    let resolveRetry: (ok: boolean) => void = () => {}
    handles.onRetryAttachment(
      () => new Promise<boolean>((res) => { resolveRetry = res }),
    )
    const retry = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Retry upload"]',
    )!
    retry.click()
    await new Promise((r) => { setTimeout(r, 0) })
    expect(retry.disabled).toBe(true)
    // Second click while in-flight must not re-enter retryCb.
    let secondCalls = 0
    handles.onRetryAttachment(async () => { secondCalls++; return true })
    retry.click()
    await new Promise((r) => { setTimeout(r, 0) })
    expect(secondCalls).toBe(0)
    resolveRetry(true)
    await new Promise((r) => { setTimeout(r, 0) })
    expect(retry.disabled).toBe(false)
  })

  it('RemoveAttachments_DropsListedRefsOnly', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const fileInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    const blob1 = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const f1 = new File([blob1], 'a.png', { type: 'image/png' })
    const blob2 = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const f2 = new File([blob2], 'b.png', { type: 'image/png' })
    Object.defineProperty(fileInput, 'files', { value: [f1, f2], writable: false })
    fileInput.dispatchEvent(new Event('change'))
    const [r1, r2] = handles.getAttachments()
    expect(handles.getAttachments().length).toBe(2)
    const revokeSpy = vi.spyOn(URL, 'revokeObjectURL')
    handles.removeAttachments([r1])
    // Only r1 dropped; r2 stays for retry.
    const remaining = handles.getAttachments()
    expect(remaining.length).toBe(1)
    expect(remaining[0]).toBe(r2)
    // Blob URLs are NOT revoked — the rendered user message <img> still
    // references previewURL; revoking here would blank the image.
    expect(revokeSpy).not.toHaveBeenCalled()
  })

  it('SetAttachmentProgress_RendersProgressBar', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const fileInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    Object.defineProperty(fileInput, 'files', { value: [file], writable: false })
    fileInput.dispatchEvent(new Event('change'))
    const ref = handles.getAttachments()[0]
    handles.setAttachmentProgress(ref, 0.5)
    const fill = handles.root.querySelector<HTMLElement>('.bg-blue')!
    expect(fill).not.toBeNull()
    expect(fill.style.width).toBe('50%')
  })

  it('SetAttachmentProgress_AtOneHidesProgressBar', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const fileInput = handles.root.querySelector<HTMLInputElement>(
      'input[accept="image/*"][multiple]',
    )!
    const blob = new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })
    const file = new File([blob], 'photo.png', { type: 'image/png' })
    Object.defineProperty(fileInput, 'files', { value: [file], writable: false })
    fileInput.dispatchEvent(new Event('change'))
    const ref = handles.getAttachments()[0]
    handles.setAttachmentProgress(ref, 0.5)
    expect(handles.root.querySelector('.bg-blue')).not.toBeNull()
    handles.setAttachmentProgress(ref, 1)
    expect(handles.root.querySelector('.bg-blue')).toBeNull()
  })
})

describe('createInputBar paste compression', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('SmallPaste_FallsThroughToDefault', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const { spy } = dispatchPaste(handles.textarea, 'hello')
    expect(handles.getAttachments().length).toBe(0)
    expect(spy).not.toHaveBeenCalled()
  })

  it('ExactlyAtThreshold_DoesNotTrigger', () => {
    // Off-by-one guard: 800 chars (exactly at threshold) must NOT trigger.
    // Flipping `> 800` to `>= 800` in the handler would fire here.
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const { spy } = dispatchPaste(handles.textarea, 'a'.repeat(800))
    expect(handles.getAttachments().length).toBe(0)
    expect(spy).not.toHaveBeenCalled()
  })

  it('LargeCharCount_TriggersAttachment', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const { spy } = dispatchPaste(handles.textarea, 'a'.repeat(801))
    const atts = handles.getAttachments()
    expect(atts.length).toBe(1)
    expect(atts[0].kind).toBe('paste')
    const paste = atts[0] as Extract<AttachmentRef, { kind: 'paste' }>
    expect(paste.text.length).toBe(801)
    expect(paste.lineCount).toBe(0)
    expect(paste.seq).toBe(1)
    expect(spy).toHaveBeenCalled()
  })

  it('MultiNewline_TriggersAttachment', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    dispatchPaste(handles.textarea, 'a\nb\nc\nd')
    const atts = handles.getAttachments()
    expect(atts.length).toBe(1)
    const paste = atts[0] as Extract<AttachmentRef, { kind: 'paste' }>
    expect(paste.lineCount).toBe(3)
    expect(paste.text).toBe('a\nb\nc\nd')
  })

  it('ExactlyTwoNewlines_DoesNotTrigger', () => {
    // Boundary: 2 newlines (≤ threshold) must NOT trigger.
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    dispatchPaste(handles.textarea, 'a\nb\nc')
    expect(handles.getAttachments().length).toBe(0)
  })

  it('CarriageReturn_IntentionalDivergence_CRLFcountsAsOne', () => {
    // Input: one \r\n, two \n, one bare \r.
    // TUI normalize-then-count would yield 5 (double-counts \r\n).
    // Our CRLF=1 divergence yields 4. The test name documents the
    // intentional divergence so a future maintainer doesn't "fix" it.
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const { spy } = dispatchPaste(handles.textarea, 'a\r\nb\nc\nd\r')
    const atts = handles.getAttachments()
    expect(atts.length).toBe(1)
    const paste = atts[0] as Extract<AttachmentRef, { kind: 'paste' }>
    expect(paste.lineCount).toBe(4)
    expect(spy).toHaveBeenCalled()
  })

  it('SequenceNumber_IncrementsAndResetsOnEmpty', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    // Two pastes → seq 1 and 2.
    dispatchPaste(handles.textarea, 'a\nb\nc\nd')
    dispatchPaste(handles.textarea, 'e\nf\ng\nh')
    const atts = handles.getAttachments()
    expect(atts.length).toBe(2)
    const labelOf = (chip: Element) => chip.querySelector('span')!.textContent
    const chips = () => handles.root.querySelectorAll('[data-paste-chip]')
    expect(labelOf(chips()[0])).toBe('#1 +4 lines')
    expect(labelOf(chips()[1])).toBe('#2 +4 lines')

    // Remove first: #2 stays (no renumber).
    handles.removeAttachments([atts[0]])
    expect(chips().length).toBe(1)
    expect(labelOf(chips()[0])).toBe('#2 +4 lines')

    // Remove second: strip empties → nextPasteID resets → next paste is #1.
    handles.removeAttachments([atts[1]])
    expect(chips().length).toBe(0)
    dispatchPaste(handles.textarea, 'x\ny\nz\nw')
    const newAtt = handles.getAttachments()
    expect(newAtt.length).toBe(1)
    expect(labelOf(chips()[0])).toBe('#1 +4 lines')
  })

  it('PasteChip_RendersIcon_Label_Preview', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    dispatchPaste(handles.textarea, 'line1\nline2\nline3\nline4')
    const chip = handles.root.querySelector('[data-paste-chip]')!
    expect(chip).not.toBeNull()
    expect(chip.querySelector('svg')).not.toBeNull()
    const label = chip.querySelector('span')!
    expect(label.textContent).toBe('#1 +4 lines')
    const preview = chip.querySelectorAll('span')[1]!
    expect(preview.textContent!.startsWith('line1')).toBe(true)
    expect(preview.textContent!.includes('\n')).toBe(false)
  })

  it('LargePaste_DuringStreaming_StillTriggersAttachment', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    handles.setStreaming(true)
    dispatchPaste(handles.textarea, 'line1\nline2\nline3\nline4')
    expect(handles.getAttachments().length).toBe(1)
    expect(handles.getAttachments()[0].kind).toBe('paste')
  })

  it('LargeInsertText_AndroidIMEstyle_TriggersAttachment', () => {
    // Android IMEs (Sogou/Baidu etc.) sometimes dispatch insertText
    // instead of insertFromPaste on the first paste after page load.
    // Without this support, paste compression silently fails on Android
    // until the user sends their first message.
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    const evt = new InputEvent('beforeinput', { bubbles: true, cancelable: true })
    Object.defineProperty(evt, 'inputType', { value: 'insertText', writable: false, configurable: true })
    Object.defineProperty(evt, 'data', { value: 'line1\nline2\nline3\nline4', writable: false, configurable: true })
    // insertText events have no dataTransfer — the handler must fall back
    // to e.data to get the pasted text.
    handles.textarea.dispatchEvent(evt)
    expect(handles.getAttachments().length).toBe(1)
    expect(handles.getAttachments()[0].kind).toBe('paste')
  })

  it('PasteChip_SingleLine_ShowsOneLine', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    dispatchPaste(handles.textarea, 'a'.repeat(801))
    const chip = handles.root.querySelector('[data-paste-chip]')!
    const label = chip.querySelector('span')!
    expect(label.textContent).toBe('#1 +1 lines')
  })

  it('ClickChip_OpensPopup_WithContent', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    dispatchPaste(handles.textarea, 'hello\nworld\nfoo\nbar')
    const chip = handles.root.querySelector('[data-paste-chip]')! as HTMLElement
    chip.click()
    const popup = getEditPopup()
    expect(popup.classList.contains('hidden')).toBe(false)
    const editTA = popup.querySelector('textarea') as HTMLTextAreaElement
    expect(editTA.value).toBe('hello\nworld\nfoo\nbar')
  })

  it('PopupEdit_AutoSavesOnInput', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    dispatchPaste(handles.textarea, 'hello\nworld\nfoo\nbar')
    const chip = handles.root.querySelector('[data-paste-chip]')! as HTMLElement
    chip.click()
    const popup = getEditPopup()
    const editTA = popup.querySelector('textarea') as HTMLTextAreaElement
    editTA.value = 'x\ny\nz'
    editTA.dispatchEvent(new Event('input', { bubbles: true }))
    const paste = handles.getAttachments()[0] as Extract<AttachmentRef, { kind: 'paste' }>
    expect(paste.text).toBe('x\ny\nz')
    expect(paste.lineCount).toBe(2)
    const label = handles.root.querySelector('[data-paste-chip] span')!
    expect(label.textContent).toBe('#1 +3 lines')
  })

  it('PopupEdit_EscClosesPopup', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    dispatchPaste(handles.textarea, 'original\na\nb\nc')
    const chip = handles.root.querySelector('[data-paste-chip]')! as HTMLElement
    chip.click()
    const popup = getEditPopup()
    const editTA = popup.querySelector('textarea') as HTMLTextAreaElement
    editTA.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(popup.classList.contains('hidden')).toBe(true)
  })

  it('PopupOutsideClick_ClosesPopup', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    dispatchPaste(handles.textarea, 'keep\nme\nfoo\nbar')
    const chip = handles.root.querySelector('[data-paste-chip]')! as HTMLElement
    chip.click()
    const popup = getEditPopup()
    expect(popup.classList.contains('hidden')).toBe(false)
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    expect(popup.classList.contains('hidden')).toBe(true)
  })

  it('ClickSameChip_TogglesPopupClosed', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    dispatchPaste(handles.textarea, 'hello\na\nb\nc')
    const chip = handles.root.querySelector('[data-paste-chip]')! as HTMLElement
    chip.click()
    const popup = getEditPopup()
    expect(popup.classList.contains('hidden')).toBe(false)
    // Click same chip again — should toggle close.
    chip.click()
    expect(popup.classList.contains('hidden')).toBe(true)
  })

  it('PopupOutsideClick_OnOtherPasteChip_DoesNotClose', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    // Two paste refs.
    dispatchPaste(handles.textarea, 'first\na\nb\nc')
    dispatchPaste(handles.textarea, 'second\nx\ny\nz')
    const chips = handles.root.querySelectorAll('[data-paste-chip]')
    const chip1 = chips[0] as HTMLElement
    const chip2 = chips[1] as HTMLElement

    // Open popup for ref1.
    chip1.click()
    const popup = getEditPopup()
    expect(popup.classList.contains('hidden')).toBe(false)

    // Dispatch mousedown on chip2 — without the [data-paste-chip] exemption,
    // the outside-click handler would close the popup here.
    chip2.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    expect(popup.classList.contains('hidden')).toBe(false)

    // Click on chip2 repopulates the popup with ref2's text.
    chip2.click()
    const editTA = popup.querySelector('textarea') as HTMLTextAreaElement
    expect(editTA.value).toBe('second\nx\ny\nz')
  })

  it('ClearAttachments_ClosesEditPopup', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    dispatchPaste(handles.textarea, 'text\na\nb\nc')
    const chip = handles.root.querySelector('[data-paste-chip]') as HTMLElement
    chip.click()
    const popup = getEditPopup()
    expect(popup.classList.contains('hidden')).toBe(false)
    // clearAttachments (session switch path) must close the popup so
    // editingRef does not point to a detached ref.
    handles.clearAttachments()
    expect(popup.classList.contains('hidden')).toBe(true)
  })

  it('RemoveButton_DeletesPasteAttachment', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    dispatchPaste(handles.textarea, 'hello\nworld\nfoo\nbar')
    expect(handles.getAttachments().length).toBe(1)
    const xBtn = handles.root.querySelector<HTMLButtonElement>(
      'button[aria-label="Remove attachment"]',
    )!
    xBtn.click()
    expect(handles.getAttachments().length).toBe(0)
    expect(handles.root.querySelector('[data-paste-chip]')).toBeNull()
  })

  it('OnAttachmentsChange_FiresOnPaste', () => {
    const handles = createInputBar({ connected: true })
    document.body.appendChild(handles.root)
    let calls = 0
    handles.onAttachmentsChange(() => { calls++ })
    dispatchPaste(handles.textarea, 'hello\nworld\nfoo\nbar')
    expect(calls).toBe(1)
  })
})
