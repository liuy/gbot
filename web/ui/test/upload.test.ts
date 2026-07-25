import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// getConnection is mocked per-test to capture sent frames + binary writes
// and to allow flipping `connected` mid-stream.
type SendCall = { kind: 'text'; payload: unknown } | { kind: 'binary'; data: ArrayBuffer }

interface FakeConn {
  connected: boolean
  send: (p: unknown) => void
  sendBinary: (d: ArrayBuffer) => void
}

let fakeConn: FakeConn
let sendCalls: SendCall[]

vi.mock('../src/ws', () => ({
  getConnection: () => fakeConn,
}))

import { sendAttachmentViaWS, attachmentMeta, newAttachmentID } from '../src/upload'

function installFakeConn(opts: { connected?: boolean; disconnectAfterBinary?: number } = {}): FakeConn {
  sendCalls = []
  let binaryCount = 0
  fakeConn = {
    connected: opts.connected ?? true,
    send: (p: unknown) => {
      sendCalls.push({ kind: 'text', payload: p })
    },
    sendBinary: (d: ArrayBuffer) => {
      sendCalls.push({ kind: 'binary', data: d })
      binaryCount++
      if (opts.disconnectAfterBinary !== undefined && binaryCount >= opts.disconnectAfterBinary) {
        fakeConn.connected = false
      }
    },
  }
  return fakeConn
}

describe('sendAttachmentViaWS', () => {
  beforeEach(() => {
    installFakeConn()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('SplitsBigFileInto256KiBChunks', async () => {
    // 600 KiB → ceil(600/256) = 3 chunks (256K + 256K + 88K tail).
    const totalBytes = 600 * 1024
    const data = new Uint8Array(totalBytes)
    for (let i = 0; i < totalBytes; i++) data[i] = i & 0xff
    const file = new File([data], 'big.bin', { type: 'application/octet-stream' })

    const fracs: number[] = []
    await sendAttachmentViaWS(file, 'id-1', (f) => fracs.push(f))

    // Exactly one attachment_start, three binary frames, one attachment_end.
    const textCalls = sendCalls.filter((c) => c.kind === 'text')
    const binaryCalls = sendCalls.filter((c) => c.kind === 'binary')
    expect(binaryCalls.length).toBe(3)
    expect(textCalls.length).toBe(2)

    // First text frame is attachment_start with full metadata.
    expect((textCalls[0] as { payload: unknown }).payload).toEqual({
      type: 'attachment_start',
      id: 'id-1',
      name: 'big.bin',
      mime: 'application/octet-stream',
      size: totalBytes,
    })
    // Last text frame is attachment_end.
    expect((textCalls[1] as { payload: unknown }).payload).toEqual({
      type: 'attachment_end',
      id: 'id-1',
    })

    // Binary frames appear between the two text frames in send order.
    expect(sendCalls[0].kind).toBe('text')
    expect(sendCalls[sendCalls.length - 1].kind).toBe('text')
    for (let i = 1; i < sendCalls.length - 1; i++) {
      expect(sendCalls[i].kind).toBe('binary')
    }

    // Progress is strictly increasing and ends at exactly 1.0.
    for (let i = 1; i < fracs.length; i++) {
      expect(fracs[i]).toBeGreaterThan(fracs[i - 1])
    }
    expect(fracs[fracs.length - 1]).toBe(1)
    // First chunk reports 256KiB/600KiB.
    expect(fracs[0]).toBeCloseTo(256 / 600, 5)
  })

  it('RejectsWhenDisconnectedAtStart', async () => {
    installFakeConn({ connected: false })
    const file = new File([new Uint8Array([0x01])], 'tiny.bin')
    await expect(sendAttachmentViaWS(file, 'id-x')).rejects.toThrow(/WS not connected/)
    expect(sendCalls.length).toBe(0)
  })

  it('RejectsWhenDisconnectDetectedMidStream', async () => {
    // Flip connected=false after the first binary frame.
    installFakeConn({ disconnectAfterBinary: 1 })
    const totalBytes = 600 * 1024
    const data = new Uint8Array(totalBytes)
    const file = new File([data], 'big.bin')

    await expect(sendAttachmentViaWS(file, 'id-mid')).rejects.toThrow(/disconnected mid-upload/)

    const binaryCalls = sendCalls.filter((c) => c.kind === 'binary')
    expect(binaryCalls.length).toBe(1) // only the first chunk got out before the re-check fired
    // attachment_end must NOT have been sent (upload did not complete).
    const textCalls = sendCalls.filter((c) => c.kind === 'text')
    const hasEnd = textCalls.some(
      (c) => (c as { payload: { type?: string } }).payload instanceof Object &&
        (c as { payload: { type?: string } }).payload.type === 'attachment_end',
    )
    expect(hasEnd).toBe(false)
  })

  it('EmptyFile_SendsNoBinaryFrames', async () => {
    const file = new File([], 'empty.bin', { type: 'application/octet-stream' })
    await sendAttachmentViaWS(file, 'id-empty')
    const binaryCalls = sendCalls.filter((c) => c.kind === 'binary')
    expect(binaryCalls.length).toBe(0)
    const textCalls = sendCalls.filter((c) => c.kind === 'text')
    expect(textCalls.length).toBe(2) // start + end still bracket the empty body
    expect((textCalls[0] as { payload: unknown }).payload).toMatchObject({
      type: 'attachment_start',
      id: 'id-empty',
      size: 0,
    })
  })

  it('ExactChunkSize_SendsOneFullChunkAndStops', async () => {
    const size = 256 * 1024
    const data = new Uint8Array(size)
    const file = new File([data], 'exact.bin', { type: 'application/octet-stream' })
    await sendAttachmentViaWS(file, 'id-exact')
    const binaryCalls = sendCalls.filter((c) => c.kind === 'binary')
    expect(binaryCalls.length).toBe(1) // NOT 2 — no trailing empty chunk
  })
})

describe('attachmentMeta', () => {
  it('PrefersFileTypeWhenPresent', () => {
    const file = new File([new Uint8Array([0x01])], 'photo.png', { type: 'image/png' })
    const meta = attachmentMeta(file, 'id-1')
    expect(meta.mime).toBe('image/png')
    expect(meta.name).toBe('photo.png')
    expect(meta.id).toBe('id-1')
    expect(meta.size).toBe(1)
  })

  it('FallsBackToExtensionWhenFileTypeEmpty', () => {
    // Some browsers omit file.type for .pdf / .docx — must derive from name.
    const file = new File([new Uint8Array([0x01])], 'report.pdf', { type: '' })
    expect(attachmentMeta(file, 'id-pdf').mime).toBe('application/pdf')
    const filePng = new File([new Uint8Array([0x01])], 'image.png', { type: '' })
    expect(attachmentMeta(filePng, 'id-png').mime).toBe('image/png')
    // Unknown extension falls back to octet-stream.
    const fileUnknown = new File([new Uint8Array([0x01])], 'weird.xyz', { type: '' })
    expect(attachmentMeta(fileUnknown, 'id-x').mime).toBe('application/octet-stream')
  })
})

describe('newAttachmentID', () => {
  // newAttachmentID uses crypto.getRandomValues directly (not crypto.randomUUID)
  // because getRandomValues works in ALL browser contexts — including the
  // non-secure HTTP-on-LAN-IP case where randomUUID is undefined. We still
  // verify the output matches the RFC 4122 v4 shape so a future refactor
  // can't silently drop the version/variant bits.
  const uuidV4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

  it('ReturnsValidUUIDv4Shape', () => {
    expect(newAttachmentID()).toMatch(uuidV4)
  })

  it('ReturnsValidUUIDv4ShapeWhenRandomUUIDUnavailable', () => {
    // Mirrors the HTTP-on-LAN-IP case: crypto.getRandomValues is still
    // available but randomUUID is undefined. The helper must not rely on
    // randomUUID; getRandomValues alone is enough.
    const cryptoObj = (globalThis as { crypto: Crypto }).crypto
    const orig = Object.getOwnPropertyDescriptor(cryptoObj, 'randomUUID')
    Reflect.deleteProperty(cryptoObj, 'randomUUID')
    try {
      expect(newAttachmentID()).toMatch(uuidV4)
    } finally {
      if (orig) Object.defineProperty(cryptoObj, 'randomUUID', orig)
    }
  })

  it('GeneratesUniqueIDs', () => {
    const ids = new Set<string>()
    for (let i = 0; i < 1000; i++) ids.add(newAttachmentID())
    expect(ids.size).toBe(1000)
  })
})
