import { describe, it, expect } from 'vitest'
import { morphHtml } from './morph'
import { renderMarkdown } from './markdown'
import { textBlock, thinkingText } from './styles/recipes'
import MarkdownIt from 'markdown-it'

const md = new MarkdownIt({ linkify: true, breaks: true })

// Guards the 2026-10-11 wui incident: during streaming, an unclosed `**`
// renders as literal text; when the closing delimiter arrives the span must
// become <strong>, and the finished DOM must contain both strongs with no
// literal `**` left over. The multi-delta history on the element is what
// breaks naive morphing — a single-shot control is not enough.
//
// Tests 1, 2 and 4 below pass on stock idiomorph 0.7.4/0.8.0 (verified
// empirically during review) — test 3 ("morphs a <p> target in place") is
// the linchpin distinguishing the gbot flatten-self-root patch.
describe('morphHtml CJK strong emphasis across streaming deltas', () => {
  const cjkDeltas = [
    '一个反直觉的点：全 4-bit 化后单流反而从 46 涨到 **124 t/s**——因为"草稿变便宜"比"接受率掉一点"划算（每步 32ms→24ms 但带更多 draft）。这套调优的本质就是',
    '一个反直觉的点：全 4-bit 化后单流反而从 46 涨到 **124 t/s**——因为"草稿变便宜"比"接受率掉一点"划算（每步 32ms→24ms 但带更多 draft）。这套调优的本质就是**',
    '一个反直觉的点：全 4-bit 化后单流反而从 46 涨到 **124 t/s**——因为"草稿变便宜"比"接受率掉一点"划算（每步 32ms→24ms 但带更多 draft）。这套调优的本质就是**在 4-bit 的预算内，把每字节的接受率最大化',
    '一个反直觉的点：全 4-bit 化后单流反而从 46 涨到 **124 t/s**——因为"草稿变便宜"比"接受率掉一点"划算（每步 32ms→24ms 但带更多 draft）。这套调优的本质就是**在 4-bit 的预算内，把每字节的接受率最大化**。',
  ]
  const latinDeltas = [
    'A counterintuitive point: the single stream went from 46 up to **124 t/s** — because "cheaper drafts" beat "slightly lower acceptance" (32ms→24ms per step with more drafts). The essence of this tuning is',
    'A counterintuitive point: the single stream went from 46 up to **124 t/s** — because "cheaper drafts" beat "slightly lower acceptance" (32ms→24ms per step with more drafts). The essence of this tuning is**',
    'A counterintuitive point: the single stream went from 46 up to **124 t/s** — because "cheaper drafts" beat "slightly lower acceptance" (32ms→24ms per step with more drafts). The essence of this tuning is**within the 4-bit budget, maximize acceptance per byte',
    'A counterintuitive point: the single stream went from 46 up to **124 t/s** — because "cheaper drafts" beat "slightly lower acceptance" (32ms→24ms per step with more drafts). The essence of this tuning is**within the 4-bit budget, maximize acceptance per byte**.',
  ]

  it('turns literal ** text into <strong> when the closer arrives (CJK)', () => {
    const el = document.createElement('div')
    for (const d of cjkDeltas) morphHtml(el, md.render(d))
    expect([...el.querySelectorAll('strong')].map((s) => s.textContent)).toEqual([
      '124 t/s',
      '在 4-bit 的预算内，把每字节的接受率最大化',
    ])
    expect(el.textContent).not.toContain('**')
  })

  it('latin full-paragraph delta sequence (control: not CJK-specific)', () => {
    const el = document.createElement('div')
    for (const d of latinDeltas) morphHtml(el, md.render(d))
    expect([...el.querySelectorAll('strong')].map((s) => s.textContent)).toEqual([
      '124 t/s',
      'within the 4-bit budget, maximize acceptance per byte',
    ])
    expect(el.textContent).not.toContain('**')
  })

  // Regression for the vendored idiomorph flatten-self-root fix: when the
  // morph target itself sits at the same level as the new content's root
  // (a <p> target whose children are inline, against freshly rendered
  // `<p>...</p>` markdown), the root must morph in place — not be
  // clone-nested inside the target, which corrupted every later delta.
  // The target carries the app's thinking-block classes; the flatten
  // branch morphs children only, so those classes must survive.
  it('morphs a <p> target in place when the html root is also a <p>', () => {
    const p = document.createElement('p')
    p.className = thinkingText()
    for (const d of latinDeltas) morphHtml(p, md.render(d))
    expect(p.className).toBe('md-body md-text ml-6 text-t2 text-sm break-words')
    expect(p.querySelectorAll('p')).toHaveLength(0)
    expect([...p.querySelectorAll('strong')].map((s) => s.textContent)).toEqual([
      '124 t/s',
      'within the 4-bit budget, maximize acceptance per byte',
    ])
    expect(p.textContent).not.toContain('**')
  })

  it('latin bold across the same delta shape (control)', () => {
    const el = document.createElement('div')
    morphHtml(el, md.render('the essence is **the budget, maximize it**.'))
    expect(el.querySelector('strong')).not.toBeNull()
  })

  // Code-block-first messages render a single root div that soft-matches the
  // empty streaming target div (chat.ts morphs textBlock()-classed divs).
  // The flatten branch must not adopt that root's attributes (the target's
  // classes are app-owned) and must not flatten the wrapper away — existing
  // tests all start with paragraphs, which is why this escaped review.
  it('code-block-first streaming keeps the target classes and one wrapper', () => {
    const el = document.createElement('div')
    el.className = textBlock()
    const codeDeltas = [
      '```go\nfunc main() {',
      '```go\nfunc main() {\n\tfmt.Println("hi")',
      '```go\nfunc main() {\n\tfmt.Println("hi")\n}',
      '```go\nfunc main() {\n\tfmt.Println("hi")\n}\n```',
      '```go\nfunc main() {\n\tfmt.Println("hi")\n}\n```\n\nDone.',
    ]
    for (const d of codeDeltas) morphHtml(el, renderMarkdown(d))
    expect(el.className).toBe('md-body md-text text-t1 text-[15px] break-words')
    const wrappers = el.querySelectorAll('.code-block-wrapper')
    expect(wrappers).toHaveLength(1)
    expect(wrappers[0].getAttribute('data-lang')).toBe('go')
    expect(wrappers[0].querySelector('pre')?.textContent).toContain('fmt.Println("hi")')
    expect(el.textContent).toContain('Done.')
  })
})
