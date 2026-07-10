import { describe, it, expect } from 'vitest'
import { History } from './history'

describe('History', () => {
  it('up on empty items returns current, cursor none', () => {
    const h = new History()
    const r = h.up('current')
    expect(r.text).toBe('current')
    expect(r.cursor).toBe('none')
  })

  it('up cycles newest to oldest, cursor home', () => {
    const h = new History()
    h.load(['a', 'b', 'c'])
    const r1 = h.up('draft')
    expect(r1.text).toBe('c')
    expect(r1.cursor).toBe('home')
    const r2 = h.up(r1.text)
    expect(r2.text).toBe('b')
    expect(r2.cursor).toBe('home')
    const r3 = h.up(r2.text)
    expect(r3.text).toBe('a')
    expect(r3.cursor).toBe('home')
  })

  it('up clamps at oldest (rollback) returns empty text, cursor none', () => {
    const h = new History()
    h.load(['only'])
    const r1 = h.up('x')
    expect(r1.text).toBe('only')
    expect(r1.cursor).toBe('home')
    const r2 = h.up('x')
    expect(r2.text).toBe('')
    expect(r2.cursor).toBe('none')
  })

  it('down restores draft when navigating back', () => {
    const h = new History()
    h.load(['a', 'b'])
    h.up('mydraft') // -> b
    h.up('mydraft') // -> a
    const r1 = h.down()
    expect(r1.text).toBe('b')
    expect(r1.cursor).toBe('end')
    const r2 = h.down()
    expect(r2.text).toBe('mydraft')
    expect(r2.cursor).toBe('end')
  })

  it('down at draft (index 0) is no-op, cursor none', () => {
    const h = new History()
    h.load(['a'])
    const r = h.down()
    expect(r.cursor).toBe('none')
  })

  it('down with empty savedDraft clears input, cursor end', () => {
    const h = new History()
    h.load(['a'])
    h.up('') // savedDraft stays empty
    const r = h.down()
    expect(r.text).toBe('')
    expect(r.cursor).toBe('end')
  })

  it('full cycle: up,up,down,down matches draft text', () => {
    const h = new History()
    h.load(['a', 'b', 'c'])
    const r1 = h.up('typing...')
    expect(r1.text).toBe('c')
    const r2 = h.up('typing...')
    expect(r2.text).toBe('b')
    const r3 = h.down()
    expect(r3.text).toBe('c')
    const r4 = h.down()
    expect(r4.text).toBe('typing...')
    // Now at draft. Down is no-op.
    const r5 = h.down()
    expect(r5.cursor).toBe('none')
    // Up from draft re-enters newest.
    const r6 = h.up('typing...')
    expect(r6.text).toBe('c')
    expect(r6.cursor).toBe('home')
  })

  it('add resets nav state', () => {
    const h = new History()
    h.load(['old'])
    h.up('draft')
    h.add('new')
    // After add, up should start fresh from newest.
    const r = h.up('x')
    expect(r.text).toBe('new')
  })

  it('add skips empty', () => {
    const h = new History()
    h.add('')
    h.load([])
    // items should still be empty
    const r = h.up('x')
    expect(r.cursor).toBe('none')
  })

  it('add skips trailing duplicate', () => {
    const h = new History()
    h.load(['dup'])
    h.add('dup')
    // Still only 1 item — duplicate was skipped
    const r1 = h.up('x')
    expect(r1.text).toBe('dup')
    // Second up should rollback (no more items)
    const r2 = h.up('dup')
    expect(r2.cursor).toBe('none')
  })

  it('load replaces items and resets state', () => {
    const h = new History()
    h.load(['a', 'b'])
    h.up('draft')
    h.load(['x', 'y', 'z'])
    const r = h.up('cur')
    expect(r.text).toBe('z')
    expect(r.cursor).toBe('home')
  })
})
