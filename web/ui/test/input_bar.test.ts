import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createInputBar, type AttachmentRef } from '../src/input_bar'

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
