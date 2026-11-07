import { describe, it, expect, vi } from 'vitest'

// The console wrap binds its original reference at import time, so the
// call-through can only be observed from OUTSIDE the wrap: this file has NO
// static import of ./log (that would hoist ahead of the spy), spies on
// console first, and imports the module dynamically — making the spy the
// "original" the wrapper must call through to.
describe('console wrap passthrough', () => {
  it('the wrapped method still calls the original console function', async () => {
    const spy = vi.spyOn(console, 'log')
    const { getDebugLogs } = await import('./log')
    // The import overwrites console.log with the wrapper and binds our spy
    // as its original — so the ONLY spy call is the wrapper's call-through;
    // our own console.log goes to the wrapper first, never to the spy.
    console.log('passthrough-marker')
    expect(getDebugLogs().find((l) => / passthrough-marker$/.test(l))).toBeTruthy()
    expect(spy).toHaveBeenCalledTimes(1)
    expect(spy).toHaveBeenCalledWith('passthrough-marker')
    spy.mockRestore()
  })
})
