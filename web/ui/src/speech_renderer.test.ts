import { describe, it, expect } from 'vitest'
import { renderForSpeech } from './speech_renderer'

describe('renderForSpeech — symbol strip', () => {
  it('strips bold/italic/strike/inline-code/hash markers', () => {
    // The space where '#' stood stays (ported plainForSpeech has no space
    // collapsing; TTS reads runs of spaces as one pause).
    expect(renderForSpeech('**bold** _it_ ~~s~~ `code` # H').replace(/ {2,}/g, ' ')).toBe(
      'bold it s code H',
    )
  })
})

describe('renderForSpeech — fenced code blocks swallowed whole', () => {
  it('removes the whole fenced block (zero replacement), keeps surrounding text', () => {
    const md = 'before\n```go\nfunc a.b() {}\n```\nafter'
    expect(renderForSpeech(md)).toBe('before\nafter')
  })
  it('swallows multiple blocks independently and keeps between-text', () => {
    const md = '一\n```js\nx()\n```\n二\n~~~\ny()\n~~~\n三'
    expect(renderForSpeech(md)).toBe('一\n二\n三')
  })
  it('EOF-unclosed fence swallows to the end', () => {
    expect(renderForSpeech('keep\n```python\nprint(1)')).toBe('keep')
  })
  it('inline code loses only the backticks', () => {
    expect(renderForSpeech('run `go test` now')).toBe('run go test now')
  })
})

describe('renderForSpeech — tables swallowed whole', () => {
  it('removes header/separator/data rows, keeps outer text', () => {
    const md = 'intro\n| a | b |\n|---|---|\n| 1 | 2 |\n outro'
    expect(renderForSpeech(md)).toBe('intro\noutro')
  })
})

describe('renderForSpeech — lists are never swallowed', () => {
  it('keeps every item as text, line markers stripped (6-item and 3-item)', () => {
    const six = '- one\n- two\n- three\n- four\n- five\n- six'
    expect(renderForSpeech(six)).toBe('one\ntwo\nthree\nfour\nfive\nsix')
    const three = '1. alpha\n2. beta\n3. gamma'
    expect(renderForSpeech(three)).toBe('alpha\nbeta\ngamma')
  })
  it('loose lists (blank lines between items) keep all items', () => {
    const md = '- a\n\n- b\n\n- c'
    expect(renderForSpeech(md)).toBe('a\n\nb\n\nc')
  })
  it('strips * list markers', () => {
    expect(renderForSpeech('* x\n* y')).toBe('x\ny')
  })
})

describe('renderForSpeech — blank-line collapse', () => {
  it('collapses runs of >=2 blank lines into one after block removal', () => {
    const md = 'a\n```\ncode\n```\n\n\n\nb'
    expect(renderForSpeech(md)).toBe('a\n\nb')
  })
})

describe('renderForSpeech — numbers, dates, money untouched', () => {
  it('keeps 3.14 / 2026-10-27 / 98% / $5.20 verbatim', () => {
    expect(renderForSpeech('pi 3.14 on 2026-10-27 at 98% costs $5.20')).toBe(
      'pi 3.14 on 2026-10-27 at 98% costs $5.20',
    )
  })
  it('line-leading 3.14 is not an ordered-list marker', () => {
    expect(renderForSpeech('3.14 是圆周率')).toBe('3.14 是圆周率')
  })
})

