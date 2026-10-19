import { describe, it, expect, beforeEach, vi } from 'vitest'

const binaryFrames: Uint8Array[] = []
const textFrames: unknown[] = []

vi.mock('./ws', () => ({
  getConnection: () => ({
    send: (p: unknown) => textFrames.push(p),
    sendBinary: (data: ArrayBuffer | Uint8Array) => {
      binaryFrames.push(data instanceof Uint8Array ? data : new Uint8Array(data))
    },
    connected: true,
  }),
}))

import { attachmentChunkSize, sendAttachmentViaWS } from './upload'

function makeFile(size: number): File {
  // Deterministic non-zero bytes: a payload starting with 0x01 proves the
  // lane prefix is prepended rather than the file's own first byte. The
  // 251-byte pattern is blitted (not per-byte filled) — a per-byte loop on
  // 256 KiB makes the two-chunk case exceed the test timeout under
  // full-suite CPU contention.
  const pat = Uint8Array.from({ length: 251 }, (_, i) => i + 1)
  const body = new Uint8Array(size)
  for (let off = 0; off < size; off += pat.length) {
    body.set(pat.subarray(0, Math.min(pat.length, size - off)), off)
  }
  return new File([body], 'blob.bin', { type: 'application/octet-stream' })
}

beforeEach(() => {
  binaryFrames.length = 0
  textFrames.length = 0
})

describe('sendAttachmentViaWS framing', () => {
  it('prefixes every chunk with the 0x00 attachment lane byte', async () => {
    const size = attachmentChunkSize + 1234 // forces exactly two chunks
    const file = makeFile(size)
    await sendAttachmentViaWS(file, 'id-1')
    expect(textFrames[0]).toMatchObject({ type: 'attachment_start', id: 'id-1', size })
    expect(textFrames.at(-1)).toMatchObject({ type: 'attachment_end', id: 'id-1' })
    expect(binaryFrames.length).toBe(2)

    const rebuilt = new Uint8Array(size)
    let at = 0
    for (const frame of binaryFrames) {
      // The frame is [0x00][payload]; a first byte of anything else would
      // route the chunk into the transport hub and drop it server-side.
      expect(frame[0]).toBe(0)
      const payload = frame.subarray(1)
      expect(payload.length).toBeLessThanOrEqual(attachmentChunkSize)
      rebuilt.set(payload, at)
      at += payload.length
    }
    expect(at).toBe(size)
    // Whole-payload integrity via one-pass checksum (sum+xor: any single
    // flipped byte changes it), plus byte-exact checks at both chunk edges
    // — per-byte expect over 256 KiB blows the timeout under full-suite
    // CPU contention.
    const pat = Uint8Array.from({ length: 251 }, (_, i) => i + 1)
    let sum = 0
    let sumPat = 0
    let xrebuilt = 0
    let xpat = 0
    for (let i = 0; i < size; i++) {
      const want = pat[i % 251]
      sum = (sum + rebuilt[i]) >>> 0
      sumPat = (sumPat + want) >>> 0
      xrebuilt ^= rebuilt[i]
      xpat ^= want
    }
    expect(sum).toBe(sumPat)
    expect(xrebuilt).toBe(xpat)
    for (let i = 0; i < pat.length; i++) {
      expect(rebuilt[i]).toBe(pat[i])
      expect(rebuilt[size - 1 - i]).toBe(pat[(size - 1 - i) % 251])
    }
  })

  it('frames a single-chunk file too', async () => {
    const file = makeFile(10)
    await sendAttachmentViaWS(file, 'id-2')
    expect(binaryFrames).toHaveLength(1)
    expect(binaryFrames[0]).toHaveLength(11)
    expect(binaryFrames[0][0]).toBe(0)
    expect(binaryFrames[0][1]).toBe(1)
  })
})
