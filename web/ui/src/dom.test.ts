import { describe, it, expect } from 'vitest'
import {
  createElement,
  createElementInDocument,
  createFragment,
  createNode,
  cx,
} from './dom'

describe('createElement', () => {
  it('returns element of given tag without className', () => {
    const el = createElement('div')
    expect(el.tagName).toBe('DIV')
    expect(el.className).toBe('')
  })

  it('sets className when provided', () => {
    expect(createElement('div', 'foo').className).toBe('foo')
  })

  it('treats empty string className the same as no className', () => {
    expect(createElement('div', '').className).toBe('')
  })

  it('returns correct subclass for each migrated tag', () => {
    expect(createElement('button') instanceof HTMLButtonElement).toBe(true)
    expect(createElement('span') instanceof HTMLSpanElement).toBe(true)
    expect(createElement('textarea') instanceof HTMLTextAreaElement).toBe(true)
    expect(createElement('input') instanceof HTMLInputElement).toBe(true)
    expect(createElement('style') instanceof HTMLStyleElement).toBe(true)
  })

  it('preserves multi-class className (cx output scenario)', () => {
    expect(createElement('div', 'a b c').className).toBe('a b c')
  })
})

describe('createElementInDocument', () => {
  const otherDoc = document.implementation.createHTMLDocument('test')

  it('creates element in the provided document, not the global one', () => {
    const el = createElementInDocument(otherDoc, 'div', 'foo')
    expect(el.ownerDocument).toBe(otherDoc)
    expect(el.className).toBe('foo')
  })

  it('works without className and still targets provided document', () => {
    const el = createElementInDocument(otherDoc, 'span')
    expect(el.tagName).toBe('SPAN')
    expect(el.ownerDocument).toBe(otherDoc)
  })
})

describe('createFragment', () => {
  it('returns a DocumentFragment', () => {
    const f = createFragment()
    expect(f.nodeType).toBe(Node.DOCUMENT_FRAGMENT_NODE)
    expect(f.childNodes.length).toBe(0)
  })

  it('supports appendChild and grows child list', () => {
    const f = createFragment()
    f.appendChild(document.createElement('div'))
    expect(f.childNodes.length).toBe(1)
  })
})

describe('createNode', () => {
  describe('defaults', () => {
    it('returns bare element with no opts and no children', () => {
      const el = createNode('div')
      expect(el.tagName).toBe('DIV')
      expect(el.className).toBe('')
      expect(el.childElementCount).toBe(0)
    })

    it('empty-string className is the same as no className', () => {
      expect(createNode('div', { className: '' }).className).toBe('')
    })
  })

  describe('className', () => {
    it('sets className from opts', () => {
      expect(createNode('div', { className: 'foo' }).className).toBe('foo')
    })
  })

  describe('text', () => {
    it('sets textContent as a TextNode child', () => {
      const el = createNode('p', { text: 'hello' })
      expect(el.textContent).toBe('hello')
      expect(el.childNodes.length).toBe(1)
      expect(el.firstChild?.nodeType).toBe(Node.TEXT_NODE)
    })

    it('empty-string text is treated the same as undefined (textContent="" clears children)', () => {
      const el = createNode('p', { text: '' })
      expect(el.textContent).toBe('')
      expect(el.childNodes.length).toBe(0)
    })

    it('text is set before children are appended (Text first, child second)', () => {
      const el = createNode('div', { text: 'pre' }, document.createElement('span'))
      expect(el.childNodes.length).toBe(2)
      expect(el.childNodes[0].nodeType).toBe(Node.TEXT_NODE)
      expect((el.childNodes[1] as HTMLElement).tagName).toBe('SPAN')
    })
  })

  describe('attrs', () => {
    it('sets a single attribute via setAttribute', () => {
      const el = createNode('span', { attrs: { role: 'button' } })
      expect(el.getAttribute('role')).toBe('button')
    })

    it('sets multiple attributes independently', () => {
      const el = createNode('span', { attrs: { role: 'button', 'aria-label': 'foo' } })
      expect(el.getAttribute('role')).toBe('button')
      expect(el.getAttribute('aria-label')).toBe('foo')
    })
  })

  describe('props', () => {
    it('disabled round-trips through the property channel (see value test for distinguishing check)', () => {
      const el = createNode('button', { props: { disabled: true } }) as HTMLButtonElement
      expect(el.disabled).toBe(true)
      // Boolean properties reflect to attributes in jsdom/browsers, so the
      // distinguishing check between props and attrs channels lives in the
      // "value" test below (input.value does NOT reflect to its attribute).
    })

    it('value is a property, not an attribute (attribute is the default value)', () => {
      const el = createNode('input', { props: { value: 'hello' } }) as HTMLInputElement
      expect(el.value).toBe('hello')
      expect(el.getAttribute('value')).toBe(null)
    })

    it('checked and type both go through the property channel', () => {
      const el = createNode('input', { props: { checked: true, type: 'checkbox' } }) as HTMLInputElement
      expect(el.checked).toBe(true)
      expect(el.type).toBe('checkbox')
    })

    it('tabIndex is a reflected property', () => {
      const el = createNode('span', { props: { tabIndex: 0 } })
      expect(el.tabIndex).toBe(0)
    })

    it('attrs and props can coexist independently', () => {
      const el = createNode('button', {
        attrs: { 'aria-label': 'x' },
        props: { disabled: true },
      }) as HTMLButtonElement
      expect(el.getAttribute('aria-label')).toBe('x')
      expect(el.disabled).toBe(true)
    })
  })

  describe('children', () => {
    it('appends all Node children in order', () => {
      const a = document.createElement('span')
      const b = document.createElement('div')
      const el = createNode('p', {}, a, b)
      expect(el.childElementCount).toBe(2)
      expect(el.children[0]).toBe(a)
      expect(el.children[1]).toBe(b)
    })

    it('mixes Node and string children', () => {
      const el = createNode('p', {}, 'hello', document.createElement('br'))
      expect(el.childNodes.length).toBe(2)
      expect(el.childNodes[0].nodeType).toBe(Node.TEXT_NODE)
      expect((el.childNodes[1] as HTMLElement).tagName).toBe('BR')
    })

    it('null children are dropped (no "null" TextNode)', () => {
      const el = createNode('p', {}, null, 'x', null)
      expect(el.childNodes.length).toBe(1)
      expect(el.textContent).toBe('x')
    })

    it('undefined children are dropped', () => {
      const el = createNode('p', {}, undefined, 'x', undefined)
      expect(el.childNodes.length).toBe(1)
      expect(el.textContent).toBe('x')
    })

    it('false children are dropped (boolean-children-skip pattern)', () => {
      const el = createNode('p', {}, false, 'x', false)
      expect(el.childNodes.length).toBe(1)
    })

    it('inline conditional via `cond && child` works end-to-end', () => {
      const cond = false
      const el = createNode('div', {}, cond && document.createElement('span'))
      expect(el.childElementCount).toBe(0)
    })

    it('all-falsy children produces an empty element', () => {
      const el = createNode('div', {}, null, undefined, false)
      expect(el.childNodes.length).toBe(0)
    })
  })

  describe('style', () => {
    it('sets inline styles via per-property loop', () => {
      const el = createNode('div', { style: { display: 'none' } })
      expect(el.style.display).toBe('none')
    })

    it('passes CSS variable strings through verbatim', () => {
      const el = createNode('div', { style: { color: 'var(--t1)' } })
      expect(el.style.color).toBe('var(--t1)')
    })

    it('writes multiple style properties in one call', () => {
      const el = createNode('div', {
        style: { display: 'flex', position: 'absolute', top: '0px' },
      })
      expect(el.style.display).toBe('flex')
      expect(el.style.position).toBe('absolute')
      expect(el.style.top).toBe('0px')
    })

    // jsdom normalizes `el.style.x = undefined|null` to '' just like the
    // persona loop does, so this test cannot distinguish the two in jsdom.
    // Real browsers may stringify undefined; the persona loop is the source
    // of truth, not this assertion. Kept to lock the observable contract.
    it('nullish style values never appear on the element', () => {
      const el = createNode('div', {
        style: { display: 'none', color: undefined, background: null },
      })
      expect(el.style.display).toBe('none')
      expect(el.style.color).toBe('')
      expect(el.style.background).toBe('')
    })

    it('style and className channels are independent', () => {
      const el = createNode('div', { className: 'foo', style: { display: 'none' } })
      expect(el.className).toBe('foo')
      expect(el.style.display).toBe('none')
    })

    it('style and props coexist', () => {
      const el = createNode('button', {
        props: { disabled: true },
        style: { display: 'none' },
      }) as HTMLButtonElement
      expect(el.disabled).toBe(true)
      expect(el.style.display).toBe('none')
    })
  })
})

describe('cx', () => {
  it('joins truthy parts with spaces', () => {
    expect(cx('a', 'b', 'c')).toBe('a b c')
  })

  it('single-element passthrough', () => {
    expect(cx('a')).toBe('a')
    expect(cx('')).toBe('')
  })

  it('drops false', () => {
    expect(cx('a', false, 'b')).toBe('a b')
  })

  it('drops null', () => {
    expect(cx('a', null, 'b')).toBe('a b')
  })

  it('drops undefined', () => {
    expect(cx('a', undefined, 'b')).toBe('a b')
  })

  it('drops empty strings', () => {
    expect(cx('a', '', 'b')).toBe('a b')
  })

  it('all-falsy returns empty string', () => {
    expect(cx(false, null, undefined, '')).toBe('')
  })

  it('no arguments returns empty string', () => {
    expect(cx()).toBe('')
  })

  it('ternary composition (header.ts pattern) — active', () => {
    const active = true
    expect(cx('base', active ? 'text-blue' : 'text-t2')).toBe('base text-blue')
  })

  it('ternary composition (header.ts pattern) — inactive', () => {
    const active = false
    expect(cx('base', active ? 'text-blue' : 'text-t2')).toBe('base text-t2')
  })
})

describe('integration', () => {
  it('createElement + createNode + createFragment cooperate without side effects', () => {
    const div = createElement('div')
    div.appendChild(createNode('span', { text: 'hi' }))
    div.appendChild(createFragment())
    expect(div.childElementCount).toBe(1)
    expect(div.firstChild?.textContent).toBe('hi')
  })
})
