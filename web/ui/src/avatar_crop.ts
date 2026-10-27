import 'cropperjs'
import type { CropperCanvas, CropperImage, CropperSelection } from 'cropperjs'
import { AVATAR_SIDE, encodeCanvas } from './avatar'
import { createElement, createNode } from './dom'
import { renderIcon } from './icons'
import { t } from './i18n'

// Single active overlay: a second open supersedes the first as canceled —
// same anti-double-scrim rule as artifact.ts confirmArtifactDownload.
let settleActive: ((r: Blob | null) => void) | null = null

export function openAvatarCropper(file: File): Promise<Blob | null> {
  return new Promise((resolve, reject) => {
    // Everything below runs synchronously inside the executor so this
    // overlay's settle is registered before any later open can supersede it.
    settleActive?.(null)

    const url = URL.createObjectURL(file)

    // Cropper elements are bare-built with a cast: createNode's tag type
    // locks to known HTMLElementTagNameMap keys, and the side-effect import
    // above already registered every element, so createElement returns
    // upgraded instances.
    const canvas = document.createElement('cropper-canvas') as CropperCanvas
    canvas.className = 'aspect-square w-full bg-ink2 rounded-lg'
    // The only theme entry point: interpolated into the shadow DOM as
    // --theme-color and inherited down to the selection outline, so the
    // light theme's remap of --color-blue follows automatically.
    canvas.setAttribute('theme-color', 'var(--color-blue)')
    const image = document.createElement('cropper-image') as CropperImage
    image.setAttribute('src', url)
    image.setAttribute('translatable', '')
    // v2 naming trap: `scalable` gates pinch/wheel ZOOM (must stay on); v1's
    // "flip" meaning maps to skewable. skewable stays off; `rotatable` is on
    // (see its own comment below).
    image.setAttribute('scalable', '')
    // `rotatable` gates BOTH the two-finger gesture AND the programmatic
    // $rotate below (the action handler checks the same flag) — it must be
    // on for the rotate button to work; free-angle two-finger rotation as
    // a side effect is an accepted trade.
    image.setAttribute('rotatable', '')
    const canvasHandle = document.createElement('cropper-handle')
    canvasHandle.setAttribute('action', 'move')
    canvasHandle.setAttribute('plain', '')
    const selection = document.createElement('cropper-selection') as CropperSelection
    // Fixed square viewport (image moves, frame never does): initial-coverage
    // 1 makes the first frame the image's centered inscribed square.
    // movable/resizable/dynamic/zoomable/keyboard all default false and are
    // deliberately left unwritten. No `outlined`: our rounded-square frame
    // overlay below is the visual — the stock square outline would double it.
    selection.setAttribute('aspect-ratio', '1')
    selection.setAttribute('initial-coverage', '1')
    const selectionHandle = document.createElement('cropper-handle')
    selectionHandle.setAttribute('action', 'move')
    selectionHandle.setAttribute('plain', '')
    selection.append(selectionHandle)
    // The selection covers its canvas area, so drags inside the viewport
    // need the inner move handle; the canvas-level one covers the letterbox
    // edges. `plain` is mandatory — a themed handle would tint the whole
    // viewport.
    canvas.append(image, canvasHandle, selection)
    // Rounded-square crop frame: the chat thumbnails (the primary consumer)
    // render rounded-md squares, so the viewport shows that shape — not a
    // circle. The huge box-shadow dims everything outside the frame
    // (WeChat-style); geometry comes from the selection below (image moves,
    // frame never does → one sync after the image settles is enough).
    const stage = createElement('div', 'relative overflow-hidden')
    const frame = createElement(
      'div',
      'absolute rounded-lg border-2 pointer-events-none z-10',
    )
    frame.style.borderColor = 'var(--color-blue)'
    frame.style.boxShadow = '0 0 0 9999px rgba(0,0,0,.55)'
    frame.setAttribute('data-avatar-crop-frame', '')
    stage.append(canvas, frame)
    const syncFrame = () => {
      const w = Number(selection.width) || 0
      const h = Number(selection.height) || 0
      const side = Math.min(w, h)
      if (!side) return
      frame.style.width = `${side}px`
      frame.style.height = `${side}px`
      frame.style.left = `${Number(selection.x) || 0}px`
      frame.style.top = `${Number(selection.y) || 0}px`
    }

    const title = createNode('h3', {
      className: 'text-base font-semibold',
      text: t('avatarCropTitle'),
      attrs: { 'data-avatar-crop-title': '' },
    })
    const cancelBtn = createNode('button', {
      className:
        'px-4 py-1.5 text-[12.5px] rounded-lg cursor-pointer border bg-ink3 text-t2 border-transparent',
      text: t('cancelBtn'),
      attrs: { 'data-avatar-crop-cancel': '' },
    })
    const confirmBtn = createNode('button', {
      className:
        'px-4 py-1.5 text-[12.5px] rounded-lg cursor-pointer border text-blue border-blue/40 bg-blue/10',
      text: t('avatarCropConfirm'),
      attrs: { 'data-avatar-crop-confirm': '' },
    })
    // 90°-step rotate for sideways photos. Free-angle two-finger rotation
    // also works now (rotatable is on for the button) — an accepted
    // trade-off; confirm of an angled image simply crops what the frame
    // shows.
    const rotateBtn = createNode('button', {
      className:
        'px-3 py-1.5 rounded-lg cursor-pointer border bg-ink3 text-t2 border-transparent',
      attrs: { 'data-avatar-crop-rotate': '', 'aria-label': t('avatarCropRotate') },
    })
    rotateBtn.append(renderIcon('rotate-cw', { size: 15 }))
    const actions = createElement('div', 'flex gap-2 items-center')
    const actionsRight = createElement('div', 'flex gap-2 justify-end flex-1')
    actionsRight.append(cancelBtn, confirmBtn)
    actions.append(rotateBtn, actionsRight)

    const mask = createElement('div', 'fixed inset-0 z-[75] bg-black/50')
    mask.setAttribute('data-avatar-crop-mask', '')
    const card = createElement(
      'div',
      'fixed left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 z-[76] flex flex-col gap-3 ' +
        'w-[min(340px,calc(100vw-32px))] p-4 bg-ink border border-hairline rounded-2xl modal-enter',
    )
    card.setAttribute('data-avatar-crop', '')
    rotateBtn.addEventListener('click', () => {
      // No center args: v2 expects element-LOCAL coordinates for the pivot
      // (viewport clientX/Y leaks a huge translation into e/f — pinned by
      // the black-box rotate test); the centerless form rotates around the
      // image's own center, which is what we want. Bare numbers are radians
      // — π/2 = one 90° step.
      image.$rotate(Math.PI / 2)
    })
    card.append(title, stage, actions)
    document.body.append(mask, card)

    let settled = false
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') done(null)
    }
    // v2 has no destroy(): disconnectedCallback unbinds the pointer/wheel/
    // action listeners, so removing the overlay is the complete teardown.
    const close = () => {
      mask.remove()
      card.remove()
      URL.revokeObjectURL(url)
      document.removeEventListener('keydown', onKey)
      settleActive = null
    }
    const done = (r: Blob | null) => {
      if (settled) return
      settled = true
      close()
      resolve(r)
    }
    const fail = (err: unknown) => {
      if (settled) return
      settled = true
      close()
      reject(err)
    }
    settleActive = done
    document.addEventListener('keydown', onKey)
    mask.addEventListener('click', () => done(null))
    cancelBtn.addEventListener('click', () => done(null))

    confirmBtn.addEventListener('click', () => {
      if (settled) return
      void (async () => {
        try {
          // $toCanvas rejects while disconnected, so the canvas must be
          // awaited BEFORE the teardown; a cancel racing the await lands in
          // the settled guards below and on the next line.
          const cropped = await selection.$toCanvas({
            width: AVATAR_SIDE,
            height: AVATAR_SIDE,
            beforeDraw: (ctx) => {
              ctx.imageSmoothingEnabled = true
              ctx.imageSmoothingQuality = 'high'
            },
          })
          if (settled) return
          done(await encodeCanvas(cropped))
        } catch (err) {
          fail(err)
        }
      })()
    })

    // Deferred one microtask: jsdom tests stub $ready on the unknown element
    // after openAvatarCropper returns; production timing is unaffected since
    // image decoding is async regardless.
    queueMicrotask(() => {
      if (settled) return
      // Broken files must fail the open at load time (v1's img error
      // listener equivalent), not at first confirm.
      void image.$ready()
        .then(() => syncFrame())
        .catch(() => fail(new Error('image load failed')))
    })
  })
}
