// Black-box rotate test: real cropperjs elements in jsdom (no module mock).
// $rotate is pure matrix math — it runs without layout, so this file can
// assert the IMAGE ACTUALLY ROTATES, which the mock-based call assertions
// in avatar_crop.test.ts cannot (a gated no-op call still "was called").
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { openAvatarCropper } from './avatar_crop'
import { resetAvatarStateForTest } from './avatar'
import { t } from './i18n'

vi.unmock('cropperjs')

// $matrix is a protected member on the public types — reaching into it is
// the price of a true black-box rotation assertion. cropperjs is pinned
// exactly (2.3.1); bumping it means re-auditing this file.

const file = () => new File([new Uint8Array([1, 2, 3])], 'x.png', { type: 'image/png' })

beforeEach(() => {
  document.body.innerHTML = ''
  resetAvatarStateForTest()
  URL.createObjectURL = vi.fn(() => 'blob:rotate-test')
  URL.revokeObjectURL = vi.fn()
})

describe('avatar crop rotate (black box)', () => {
  it('clicking rotate turns the image 90° — the matrix is not identity', async () => {
    void openAvatarCropper(file())
    // Open settles only on confirm/cancel; the overlay DOM is mounted
    // synchronously, which is all this test needs.
    const image = document.querySelector('cropper-image') as unknown as {
      $matrix: number[]
    }
    expect(image).toBeTruthy()
    // jsdom reports all-zero rects, which would make ANY pivot coincide and
    // hide the coordinate-space bug — give the image a real viewport rect.
    vi.spyOn(image, 'getBoundingClientRect').mockReturnValue({
      x: 800, y: 400, left: 800, top: 400,
      width: 280, height: 280, right: 1080, bottom: 680,
    } as DOMRect)
    const before = [...image.$matrix]
    ;(document.querySelector('[data-avatar-crop-rotate]') as HTMLElement).click()
    const after = image.$matrix
    // 90° rotation: [cos, sin, -sin, cos, 0, 0] ≈ [0, 1, -1, 0, 0, 0].
    // The gated no-op leaves the identity matrix [1,0,0,1,0,0] — this
    // assertion is exactly what that bug looked like.
    expect(after[0]).toBeCloseTo(0)
    expect(after[1]).toBeCloseTo(1)
    expect(after[2]).toBeCloseTo(-1)
    expect(after[3]).toBeCloseTo(0)
    // Around the image's OWN center: the translation terms stay zero. A
    // viewport-coord pivot (clientX/Y) would leak huge e/f values here.
    expect(after[4]).toBeCloseTo(0)
    expect(after[5]).toBeCloseTo(0)
    expect(before).toEqual([1, 0, 0, 1, 0, 0])
  })
  it('each click steps another quarter turn (two clicks = 180°)', async () => {
    void openAvatarCropper(file())
    const image = document.querySelector('cropper-image') as unknown as {
      $matrix: number[]
    }
    const rotate = document.querySelector('[data-avatar-crop-rotate]') as HTMLElement
    rotate.click()
    rotate.click()
    const m = image.$matrix
    expect(m[0]).toBeCloseTo(-1)
    expect(m[1]).toBeCloseTo(0)
  })

  it('overlay still renders its chrome (title/buttons) without the mock', async () => {
    void openAvatarCropper(file())
    expect(document.querySelector('[data-avatar-crop-title]')?.textContent).toBe(
      t('avatarCropTitle'),
    )
    expect(document.querySelector('[data-avatar-crop-frame]')).toBeTruthy()
    expect(document.querySelector('[data-avatar-crop-confirm]')).toBeTruthy()
  })
})
