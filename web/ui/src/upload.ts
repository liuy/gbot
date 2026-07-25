// upload.ts — stream a single File to the server over the existing WS chat
// connection as ordered binary frames bracketed by attachment_start/end text
// frames. 256 KiB per chunk mirrors pkg/tool/computer/android.go SendFile.
// Reports progress via onProgress (0..1) after each chunk flush.

import { getConnection } from './ws'

export const attachmentChunkSize = 256 * 1024

export interface AttachmentMeta {
  id: string
  name: string
  mime: string
  size: number
}

// sniffFromExt derives a mime from filename extension when file.type is empty
// (some browsers don't synthesize file.type for .pdf, .docx, etc.). Mirrors
// pkg/media/mime.go ExtFromMime in reverse.
function sniffFromExt(name: string): string {
  const ext = name.slice(name.lastIndexOf('.') + 1).toLowerCase()
  const m: Record<string, string> = {
    png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg', gif: 'image/gif',
    webp: 'image/webp', bmp: 'image/bmp',
    pdf: 'application/pdf', doc: 'application/msword', docx: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
    xls: 'application/vnd.ms-excel', xlsx: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
    ppt: 'application/vnd.ms-powerpoint', pptx: 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
    epub: 'application/epub+zip', csv: 'text/csv', txt: 'text/plain', md: 'text/markdown',
    json: 'application/json', xml: 'application/xml', html: 'text/html', ipynb: 'application/x-ipynb+json', zip: 'application/zip',
  }
  return m[ext] || 'application/octet-stream'
}

export function attachmentMeta(file: File, id: string): AttachmentMeta {
  return {
    id,
    name: file.name,
    mime: file.type || sniffFromExt(file.name),
    size: file.size,
  }
}

// newAttachmentID returns an RFC 4122 v4 UUID. Uses crypto.getRandomValues
// directly (not crypto.randomUUID) because getRandomValues is available in
// ALL browser contexts — including non-secure HTTP on a LAN IP, where
// randomUUID is undefined. Same crypto-strong randomness, broader support.
export function newAttachmentID(): string {
  const b = crypto.getRandomValues(new Uint8Array(16))
  b[6] = (b[6] & 0x0f) | 0x40 // version 4
  b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant (10xxxxxx)
  const h = (n: number) => n.toString(16).padStart(2, '0')
  return (
    h(b[0]) + h(b[1]) + h(b[2]) + h(b[3]) + '-' +
    h(b[4]) + h(b[5]) + '-' +
    h(b[6]) + h(b[7]) + '-' +
    h(b[8]) + h(b[9]) + '-' +
    h(b[10]) + h(b[11]) + h(b[12]) + h(b[13]) + h(b[14]) + h(b[15])
  )
}

// sendAttachmentViaWS streams one file. Serial: caller awaits this for each
// attachment in turn. Resolves on attachment_end send. Rejects on WS not-open
// at start OR on a disconnect detected between chunks (sendBinary silently
// no-ops when readyState != OPEN, so without this re-check the loop would
// happily "complete" a multi-chunk upload whose tail frames were all dropped).
export async function sendAttachmentViaWS(
  file: File,
  id: string,
  onProgress?: (frac: number) => void,
): Promise<void> {
  const conn = getConnection()
  if (!conn.connected) {
    throw new Error('WS not connected')
  }
  const meta = attachmentMeta(file, id)
  conn.send({ type: 'attachment_start', ...meta })
  const chunkSize = attachmentChunkSize
  for (let offset = 0; offset < file.size; offset += chunkSize) {
    const slice = file.slice(offset, Math.min(offset + chunkSize, file.size))
    const buf = await slice.arrayBuffer()
    conn.sendBinary(buf)
    if (!conn.connected) {
      throw new Error('WS disconnected mid-upload')
    }
    if (onProgress) {
      onProgress((offset + buf.byteLength) / file.size)
    }
  }
  conn.send({ type: 'attachment_end', id })
}
