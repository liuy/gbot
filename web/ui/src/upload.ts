// upload.ts — upload a single File to the server with one multipart POST to
// /api/upload (single-shot, no chunking: the server streams the part into its
// media cache and answers with the content-addressed id the commit frame
// later references). Uses XMLHttpRequest, not fetch, because only
// xhr.upload.onprogress keeps the chip progress bar alive; AbortController
// maps to xhr.abort() for stop/disconnect cancellation.

export interface AttachmentMeta {
  id: string
  name: string
  mime: string
  size: number
}

export interface UploadResult {
  id: string
  mime: string
  size: number
  kind: string
}

export interface UploadOptions {
  signal?: AbortSignal
  onProgress?: (fraction: number) => void
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

// sizeOverride carries the REAL byte count for restored attachments whose
// chip holds a zero-byte stub File (bytes live server-side under the id).
export function attachmentMeta(file: File, id: string, sizeOverride?: number): AttachmentMeta {
  return {
    id,
    name: file.name,
    mime: file.type || sniffFromExt(file.name),
    size: sizeOverride ?? file.size,
  }
}

// uploadAttachment POSTs file as the multipart "file" field and resolves with
// the server's envelope {id, mime, size, kind}. Not async at the boundary:
// the promise is built around the XHR callbacks so onprogress/onload/onerror/
// onabort map 1:1. Rejections carry the server's error text verbatim when the
// body is a JSON error envelope (413 cap messages surface as-is), a clean
// fallback otherwise.
export function uploadAttachment(file: File, opts: UploadOptions = {}): Promise<UploadResult> {
  if (opts.signal?.aborted) {
    // An already-dead signal must not construct or send anything — a
    // pointless request would still move bytes.
    return Promise.reject(new Error('upload aborted'))
  }
  return new Promise<UploadResult>((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', '/api/upload')
    const form = new FormData()
    form.append('file', file)
    // No setRequestHeader('Content-Type', …): the browser derives the
    // multipart boundary, and a manual header would drop it.
    xhr.upload.onprogress = (e) => {
      // lengthComputable is false when the total is unknowable; dividing by
      // it would yield NaN widths.
      if (opts.onProgress && e.lengthComputable && e.total > 0) {
        opts.onProgress(e.loaded / e.total)
      }
    }
    xhr.onload = () => {
      if (xhr.status === 200) {
        try {
          resolve(JSON.parse(xhr.responseText) as UploadResult)
        } catch {
          reject(new Error('upload failed: bad response'))
        }
        return
      }
      let serverMsg = ''
      try {
        const parsed = JSON.parse(xhr.responseText)?.error
        if (typeof parsed === 'string' && parsed !== '') serverMsg = parsed
      } catch {
        // HTML error page or empty body — fall through to the status text.
      }
      reject(new Error(serverMsg !== '' ? serverMsg : 'upload failed: ' + xhr.status))
    }
    xhr.onerror = () => reject(new Error('upload failed: network'))
    xhr.onabort = () => reject(new Error('upload aborted'))
    if (opts.signal) {
      // Attached before send so a stop/disconnect mid-request terminates it
      // and lands in onabort.
      opts.signal.addEventListener('abort', () => xhr.abort(), { once: true })
    }
    xhr.send(form)
  })
}
