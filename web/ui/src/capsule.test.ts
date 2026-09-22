import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createCapsule, notifyCapsule, registerCapsule } from './capsule'

describe('status capsule (generic transport)', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    registerCapsule(null)
  })

  it('show() displays text, default duration is sticky (no auto-dismiss)', () => {
    const capsule = createCapsule()
    capsule.show('升级中…')
    expect(capsule.active()).toBe(true)
    expect(capsule.root.textContent).toContain('升级中…')
    vi.advanceTimersByTime(60_000)
    expect(capsule.active()).toBe(true)
  })

  it('show() with durationMs auto-dismisses after the window', () => {
    const capsule = createCapsule()
    capsule.show('无法热重启 — TUI mode', { durationMs: 4000 })
    vi.advanceTimersByTime(3999)
    expect(capsule.active()).toBe(true)
    vi.advanceTimersByTime(1)
    expect(capsule.active()).toBe(false)
  })

  it('hide() never rewrites text — the fade-out shows the last notice verbatim', () => {
    // 2026-09-22 flash bug: hide() used to reset the label to the upgrade
    // default, so a refusal visibly morphed into "Upgrading…" mid-fade.
    const capsule = createCapsule()
    capsule.show('无法热重启 — TUI mode', { durationMs: 4000 })
    vi.advanceTimersByTime(4000)
    expect(capsule.active()).toBe(false)
    expect(capsule.root.textContent).toContain('无法热重启 — TUI mode')
  })

  it('registerCapsule routes notifyCapsule; unmounted registry drops the call', () => {
    const capsule = createCapsule()
    notifyCapsule('before mount')
    expect(capsule.active()).toBe(false)
    registerCapsule(capsule)
    notifyCapsule('hello')
    expect(capsule.active()).toBe(true)
    expect(capsule.root.textContent).toContain('hello')
  })

  it('a later show() replaces the running notice text', () => {
    const capsule = createCapsule()
    capsule.show('first', { durationMs: 4000 })
    capsule.show('second', { durationMs: 4000 })
    expect(capsule.root.textContent).toContain('second')
    expect(capsule.root.textContent).not.toContain('first')
    vi.advanceTimersByTime(4000)
    expect(capsule.active()).toBe(false)
  })
})
