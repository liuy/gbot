import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// jsdom's real XHR accepts send(FormData) but never fires upload.onprogress,
// so tests install this stub class over globalThis.XMLHttpRequest and drive
// the events the transport is expected to emit explicitly (real Chromium
// fires them).
class XHRStub {
  static instances: XHRStub[] = []
  method = ''
  url = ''
  requestHeaders: [string, string][] = []
  body: unknown = null
  sent = false
  aborted = false
  progressAttachedAtSend = false
  upload: { onprogress: ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null } = { onprogress: null }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  onabort: (() => void) | null = null
  status = 0
  responseText = ''

  constructor() { XHRStub.instances.push(this) }
  open(method: string, url: string) { this.method = method; this.url = url }
  setRequestHeader(k: string, v: string) { this.requestHeaders.push([k, v]) }
  send(body: unknown) {
    this.sent = true
    this.body = body
    this.progressAttachedAtSend = this.upload.onprogress !== null
  }
  abort() { this.aborted = true }
}

import { uploadAttachment, attachmentMeta, pooled } from '../src/upload'

function pngFile(): File {
  return new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a])], 'photo.png', { type: 'image/png' })
}

function okBody(id = 'i:ab12cd34ef56ab12') {
  return JSON.stringify({ id, mime: 'image/png', size: 6, kind: 'image' })
}

beforeEach(() => {
  XHRStub.instances = []
  vi.stubGlobal('XMLHttpRequest', XHRStub)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('uploadAttachment', () => {
  it('XHRPostsFormDataToUploadRoute', async () => {
    const file = pngFile()
    const p = uploadAttachment(file, {})
    expect(XHRStub.instances.length).toBe(1)
    const x = XHRStub.instances[0]
    expect(x.method).toBe('POST')
    expect(x.url).toBe('/api/upload')
    // No manual Content-Type: the browser must set the multipart boundary.
    expect(x.requestHeaders.length).toBe(0)
    expect(x.sent).toBe(true)
    expect(x.body).toBeInstanceOf(FormData)
    expect((x.body as FormData).get('file')).toBe(file)
    x.status = 200
    x.responseText = okBody()
    x.onload!()
    await expect(p).resolves.toEqual({ id: 'i:ab12cd34ef56ab12', mime: 'image/png', size: 6, kind: 'image' })
  })

  it('UploadProgressPlumbsToCallback', async () => {
    const onProgress = vi.fn()
    const p = uploadAttachment(pngFile(), { onProgress })
    const x = XHRStub.instances[0]
    // The callback must be attached before send so the first byte event is
    // never dropped.
    expect(x.progressAttachedAtSend).toBe(true)
    x.upload.onprogress!({ lengthComputable: true, loaded: 512, total: 1024 })
    expect(onProgress).toHaveBeenCalledTimes(1)
    expect(onProgress).toHaveBeenCalledWith(0.5)
    onProgress.mockClear()
    // lengthComputable=false must not divide by a useless total (NaN widths).
    x.upload.onprogress!({ lengthComputable: false, loaded: 10, total: 0 })
    expect(onProgress).not.toHaveBeenCalled()
    x.status = 200
    x.responseText = okBody('i:progress')
    x.onload!()
    await p
  })

  it('HttpError_SurfacesServerMessage', async () => {
    const p = uploadAttachment(pngFile(), {})
    const x = XHRStub.instances[0]
    x.status = 413
    x.responseText = JSON.stringify({ error: 'upload exceeds 50.0MB limit' })
    x.onload!()
    await expect(p).rejects.toThrow(/upload exceeds 50\.0MB limit/)
  })

  it('HttpError_NonJsonBody', async () => {
    const p = uploadAttachment(pngFile(), {})
    const x = XHRStub.instances[0]
    x.status = 500
    x.responseText = ''
    x.onload!()
    // No JSON.parse throw may escape — a clean fallback message instead.
    await expect(p).rejects.toThrow('upload failed: 500')
  })

  it('AbortSignalAbortsRequest', async () => {
    const ac = new AbortController()
    const p = uploadAttachment(pngFile(), { signal: ac.signal })
    const x = XHRStub.instances[0]
    expect(x.aborted).toBe(false)
    ac.abort()
    expect(x.aborted).toBe(true)
    x.onabort!()
    await expect(p).rejects.toThrow(/.+/) // non-empty message
  })

  it('AlreadyAbortedSignal_ConstructsNoXHR', async () => {
    const ac = new AbortController()
    ac.abort()
    await expect(uploadAttachment(pngFile(), { signal: ac.signal })).rejects.toThrow('upload aborted')
    expect(XHRStub.instances.length).toBe(0)
  })

  it('NetworkError_OnerrorRejects', async () => {
    const p = uploadAttachment(pngFile(), {})
    XHRStub.instances[0].onerror!()
    await expect(p).rejects.toThrow(/.+/) // non-empty message, no raw exception
  })
})

describe('attachmentMeta', () => {
  it('KeepsMimeFallbackAndSizeOverride', () => {
    const file = new File([new Uint8Array([0x01])], 'photo.png', { type: 'image/png' })
    const meta = attachmentMeta(file, 'i:abc')
    expect(meta.mime).toBe('image/png')
    expect(meta.name).toBe('photo.png')
    expect(meta.id).toBe('i:abc')
    expect(meta.size).toBe(1)

    // Restored chips carry a zero-byte stub File; sizeOverride carries the
    // REAL byte count (the retry guard depends on this shape).
    const stub = new File([], 'photo.png', { type: 'image/png' })
    expect(attachmentMeta(stub, 'i:abc', 4321).size).toBe(4321)

    // Empty file.type falls back to the extension sniff.
    const pdf = new File([new Uint8Array([0x01])], 'report.pdf', { type: '' })
    expect(attachmentMeta(pdf, 'd:pdf').mime).toBe('application/pdf')
    const unknown = new File([new Uint8Array([0x01])], 'weird.xyz', { type: '' })
    expect(attachmentMeta(unknown, 'd:x').mime).toBe('application/octet-stream')
  })
})

describe('pooled', () => {
  // deferred exposes a promise's resolve so a test can park tasks at a
  // chosen point and release them in a chosen order.
  function deferred(): { promise: Promise<void>; resolve: () => void } {
    let resolve!: () => void
    const promise = new Promise<void>((r) => {
      resolve = r
    })
    return { promise, resolve }
  }

  it('StartOrder_ItemOrder_ThirdWaitsForSlot', async () => {
    const started: number[] = []
    const gates = [deferred(), deferred(), deferred()]
    let inFlight = 0
    let maxInFlight = 0
    const p = pooled([0, 1, 2], 2, async (i) => {
      started.push(i)
      inFlight++
      maxInFlight = Math.max(maxInFlight, inFlight)
      try {
        await gates[i].promise
      } finally {
        inFlight--
      }
    })
    // Both workers start synchronously inside pooled's prologue, in item
    // order — the third item must not start until a slot frees.
    expect(started).toEqual([0, 1])
    gates[0].resolve()
    await vi.waitFor(() => {
      expect(started).toEqual([0, 1, 2])
    })
    gates[1].resolve()
    gates[2].resolve()
    await p
    expect(maxInFlight).toBe(2)
  })

  it('ImmediateTasks_AllComplete_NeverExceedTwo', async () => {
    const calls: number[] = []
    let inFlight = 0
    let maxInFlight = 0
    await pooled([0, 1, 2, 3, 4], 2, async (i) => {
      calls.push(i)
      inFlight++
      maxInFlight = Math.max(maxInFlight, inFlight)
      await Promise.resolve()
      inFlight--
    })
    expect(calls).toEqual([0, 1, 2, 3, 4])
    expect(maxInFlight).toBe(2)
  })

  it('EmptyItems_Resolves_NoTasks', async () => {
    const task = vi.fn(async () => {})
    await pooled([], 2, task)
    expect(task).toHaveBeenCalledTimes(0)
  })

  it('LimitClamp_ZeroLimitStillRunsAll', async () => {
    const done: number[] = []
    // limit above item count: workers clamp to the item count.
    await pooled([0, 1], 5, async (i) => {
      done.push(i)
    })
    expect(done).toEqual([0, 1])
    // A nonsense limit still runs every item rather than dropping them.
    await pooled([7], 0, async (i) => {
      done.push(i)
    })
    expect(done).toEqual([0, 1, 7])
  })

  it('TaskRejection_RunsSiblings_RethrowsFirstError', async () => {
    const started: number[] = []
    const p = pooled([0, 1, 2], 1, async (i) => {
      started.push(i)
      if (i === 0) throw new Error('boom-first')
    })
    await expect(p).rejects.toThrow('boom-first')
    // Siblings still ran to completion despite the first task's rejection —
    // falsifies both early-abort and error-swallowing.
    expect(started).toEqual([0, 1, 2])
  })

  it('SyncThrowingTask_IsCaught_AllItemsAttempted', async () => {
    const started: number[] = []
    const p = pooled([0, 1], 2, (i): Promise<void> => {
      started.push(i)
      if (i === 0) throw new Error('sync-boom') // thrown before any await
      return Promise.resolve()
    })
    await expect(p).rejects.toThrow('sync-boom')
    expect(started).toEqual([0, 1])
  })
})
