import { describe, it, expect } from 'vitest'
import { renderIcon, SVG_NS } from './icons'
import type { IconName } from './icons'

describe('renderIcon — basic SVG element', () => {
  it('returns an SVGElement with the SVG namespace', () => {
    const el = renderIcon('plus')
    expect(el.namespaceURI).toBe(SVG_NS)
    expect(el.tagName).toBe('svg')
  })

  it('serializes xmlns attribute on the root svg', () => {
    const el = renderIcon('plus')
    expect(el.getAttribute('xmlns')).toBe(SVG_NS)
  })

  it('defaults width/height to 24 with viewBox 0 0 24 24', () => {
    const el = renderIcon('plus')
    expect(el.getAttribute('width')).toBe('24')
    expect(el.getAttribute('height')).toBe('24')
    expect(el.getAttribute('viewBox')).toBe('0 0 24 24')
  })

  it('honours a custom size', () => {
    const el = renderIcon('plus', { size: 18 })
    expect(el.getAttribute('width')).toBe('18')
    expect(el.getAttribute('height')).toBe('18')
  })

  it('sets aria-hidden so screen readers skip decorative icons', () => {
    expect(renderIcon('plus').getAttribute('aria-hidden')).toBe('true')
  })

  it('forwards className to the svg class attribute', () => {
    expect(renderIcon('plus', { className: 'spin' }).getAttribute('class')).toBe('spin')
  })

  it('omits class attribute entirely when no className is given', () => {
    expect(renderIcon('plus').getAttribute('class')).toBe(null)
  })

  it('treats empty-string className like no className', () => {
    expect(renderIcon('plus', { className: '' }).getAttribute('class')).toBe(null)
  })
})

describe('renderIcon — outline variant', () => {
  it('plus sets outline defaults with strokeWidth 2.5', () => {
    const el = renderIcon('plus')
    expect(el.getAttribute('fill')).toBe('none')
    expect(el.getAttribute('stroke')).toBe('currentColor')
    expect(el.getAttribute('stroke-width')).toBe('2.5')
    expect(el.getAttribute('stroke-linecap')).toBe('round')
    expect(el.getAttribute('stroke-linejoin')).toBe('round')
  })

  it('caller strokeWidth overrides defaultStrokeWidth', () => {
    expect(renderIcon('plus', { strokeWidth: 3 }).getAttribute('stroke-width')).toBe('3')
  })

  it('plus strokeWidth defaults to 2.5', () => {
    expect(renderIcon('plus').getAttribute('stroke-width')).toBe('2.5')
  })

  it('camera strokeWidth defaults to 2', () => {
    expect(renderIcon('camera').getAttribute('stroke-width')).toBe('2')
  })

  it('image strokeWidth defaults to 2', () => {
    expect(renderIcon('image').getAttribute('stroke-width')).toBe('2')
  })

  it('file strokeWidth defaults to 2', () => {
    expect(renderIcon('file').getAttribute('stroke-width')).toBe('2')
  })

  it('refresh strokeWidth defaults to 2.5', () => {
    expect(renderIcon('refresh').getAttribute('stroke-width')).toBe('2.5')
  })

  it('dot strokeWidth defaults to 2.5', () => {
    expect(renderIcon('dot').getAttribute('stroke-width')).toBe('2.5')
  })

  it('moon strokeWidth defaults to 2', () => {
    expect(renderIcon('moon').getAttribute('stroke-width')).toBe('2')
  })

  it('sun strokeWidth defaults to 2', () => {
    expect(renderIcon('sun').getAttribute('stroke-width')).toBe('2')
  })

  it('user strokeWidth defaults to 2', () => {
    expect(renderIcon('user').getAttribute('stroke-width')).toBe('2')
  })

  it('chevron-right strokeWidth defaults to 1.5', () => {
    expect(renderIcon('chevron-right').getAttribute('stroke-width')).toBe('1.5')
  })

  it('outline children are parsed into the SVG namespace', () => {
    const el = renderIcon('plus')
    expect(el.children.length).toBe(1)
    expect(el.children[0].namespaceURI).toBe(SVG_NS)
    expect(el.children[0].tagName).toBe('path')
  })

  it('refresh is the canonical task_panel arc only (no arrow path)', () => {
    const el = renderIcon('refresh')
    expect(el.children.length).toBe(1)
    expect(el.children[0].tagName).toBe('path')
    expect(el.children[0].getAttribute('d')).toBe('M21 12a9 9 0 11-6.219-8.56')
  })

  it('image packs rect + circle + path in source order', () => {
    const el = renderIcon('image')
    expect(el.children.length).toBe(3)
    expect(el.children[0].tagName).toBe('rect')
    expect(el.children[1].tagName).toBe('circle')
    expect(el.children[2].tagName).toBe('path')
  })

  it('camera packs path + circle in source order', () => {
    const el = renderIcon('camera')
    expect(el.children.length).toBe(2)
    expect(el.children[0].tagName).toBe('path')
    expect(el.children[1].tagName).toBe('circle')
  })

  it('file packs path + polyline in source order', () => {
    const el = renderIcon('file')
    expect(el.children.length).toBe(2)
    expect(el.children[0].tagName).toBe('path')
    expect(el.children[1].tagName).toBe('polyline')
  })

  it('dot packs circle + path in source order', () => {
    const el = renderIcon('dot')
    expect(el.children.length).toBe(2)
    expect(el.children[0].tagName).toBe('circle')
    expect(el.children[1].tagName).toBe('path')
  })
})

describe('renderIcon — solid variant', () => {
  it('send uses fill=currentColor and sets no stroke', () => {
    const el = renderIcon('send')
    expect(el.getAttribute('fill')).toBe('currentColor')
    expect(el.getAttribute('stroke')).toBe(null)
  })

  it('send path is the redrawn 24vb arrow (strict equality)', () => {
    const el = renderIcon('send')
    const path = el.querySelector('path')
    expect(path?.getAttribute('d')).toBe('M4 12l16-8-8 16-2-6-6-2z')
  })
})

describe('renderIcon — mixed variant', () => {
  it('tai-chi leaves svg-level fill/stroke unset', () => {
    const el = renderIcon('tai-chi')
    expect(el.getAttribute('fill')).toBe(null)
    expect(el.getAttribute('stroke')).toBe(null)
  })

  it('tai-chi preserves path/circle source order including the CSS-var fill', () => {
    const el = renderIcon('tai-chi')
    expect(el.children.length).toBe(3)
    expect(el.children[0].tagName).toBe('path')
    expect(el.children[0].getAttribute('fill')).toBe('currentColor')
    expect(el.children[1].tagName).toBe('circle')
    expect(el.children[1].getAttribute('fill')).toBe('currentColor')
    expect(el.children[2].tagName).toBe('circle')
    expect(el.children[2].getAttribute('fill')).toBe('var(--color-ink2, white)')
  })

  it('menu renders two rects with their own fill/stroke', () => {
    const el = renderIcon('menu')
    expect(el.children.length).toBe(2)
    expect(el.children[0].tagName).toBe('rect')
    expect(el.children[1].tagName).toBe('rect')
    expect(el.children[0].getAttribute('fill')).toBe('currentColor')
    expect(el.children[0].getAttribute('stroke')).toBe('none')
  })

  it('menu preserves the longer-top / shorter-bottom hamburger geometry', () => {
    const el = renderIcon('menu')
    const top = el.children[0]
    const bottom = el.children[1]
    expect(parseFloat(top.getAttribute('width')!)).toBeGreaterThan(
      parseFloat(bottom.getAttribute('width')!),
    )
  })
})

describe('renderIcon — future icons are registered', () => {
  const future: IconName[] = ['x', 'upload', 'chevron-right', 'scroll-to-bottom']
  for (const name of future) {
    it(`${name} renders at least one child element`, () => {
      const el = renderIcon(name)
      expect(el.children.length).toBeGreaterThanOrEqual(1)
      expect(el.children[0].namespaceURI).toBe(SVG_NS)
    })
  }
})

describe('renderIcon — DOM integration', () => {
  it('mounts inside a div via appendChild', () => {
    const div = document.createElement('div')
    div.appendChild(renderIcon('plus'))
    expect(div.querySelector('svg')).not.toBeNull()
    expect(div.querySelector('svg path')).not.toBeNull()
  })

  it('two consecutive calls return distinct svg instances with distinct children', () => {
    const a = renderIcon('plus')
    const b = renderIcon('plus')
    expect(a).not.toBe(b)
    expect(a.children[0]).not.toBe(b.children[0])
  })

  it('replaceChildren on a button yields a single svg child', () => {
    const btn = document.createElement('button')
    btn.replaceChildren(renderIcon('plus', { className: 'h-6 w-6' }))
    expect(btn.children.length).toBe(1)
    expect(btn.children[0].tagName).toBe('svg')
    expect(btn.children[0].getAttribute('class')).toBe('h-6 w-6')
  })

  it('outerHTML round-trip retains xmlns so the svg stays well-formed', () => {
    const html = renderIcon('plus').outerHTML
    expect(html).toContain('xmlns="http://www.w3.org/2000/svg"')
    expect(html).toContain('viewBox="0 0 24 24"')
    expect(html).toContain('d="M12 5v14M5 12h14"')
  })
})
