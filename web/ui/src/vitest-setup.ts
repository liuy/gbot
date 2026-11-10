// jsdom-only setup. @testing-library/jest-dom is removed with React.
// Tests assert via standard DOM (textContent, classList, querySelector).

// Some environments (Termux jsdom build) initialize the window without a
// Web Storage — stub an in-memory Storage so theme-pref code under test
// (sidebar.ts reads/writes localStorage) does not crash.
if (typeof window !== 'undefined' && !window.localStorage) {
  const mem = new Map<string, string>()
  const storage: Storage = {
    get length() {
      return mem.size
    },
    clear: () => mem.clear(),
    getItem: (k: string) => (mem.has(k) ? mem.get(k)! : null),
    key: (i: number) => Array.from(mem.keys())[i] ?? null,
    removeItem: (k: string) => void mem.delete(k),
    setItem: (k: string, v: string) => void mem.set(k, String(v)),
  }
  Object.defineProperty(window, 'localStorage', { value: storage, configurable: true })
}

// jsdom lacks matchMedia — stub it (sidebar.ts uses it for system theme).
if (typeof window !== 'undefined' && !window.matchMedia) {
  window.matchMedia = (() => {
    const listeners = new Map<string, Set<(e: MediaQueryListEvent) => void>>()
    const mq = (query: string): MediaQueryList => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: (_: string, cb: (e: MediaQueryListEvent) => void) => {
        if (!listeners.has(query)) listeners.set(query, new Set())
        listeners.get(query)!.add(cb)
      },
      removeEventListener: (_: string, cb: (e: MediaQueryListEvent) => void) => {
        listeners.get(query)?.delete(cb)
      },
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => true,
    }) as unknown as MediaQueryList
    return mq
  })()
}

// jsdom lacks scrollIntoView — stub it (chat.ts calls it on bottomSentinel).
if (
  typeof Element !== 'undefined' &&
  !Element.prototype.scrollIntoView
) {
  Element.prototype.scrollIntoView = function () {}
}

// Module-level timers must not outlive a test file: a pending jsdom timer
// fires after that file's jsdom is gone (window undefined) and crashes the
// worker with an unhandled ReferenceError. The crash lands on whichever
// file the worker runs NEXT, so the blamed file rotates (task_panel,
// cancel_result, subagent_nesting all took it). stopStatusSweep is a no-op
// when no sweep is running.
import { afterEach, vi } from 'vitest'
import { stopStatusSweep } from './components/status_sweep'
afterEach(() => stopStatusSweep())
// The daemon-served novnc prebundle does not exist in the test environment.
// A real dynamic import fails inside vite's module runner, whose fetch error
// is re-raised as an unhandled runnerError that bypasses the caller's catch —
// any test opening the VNC face (chat, vnc_console, …) can poison the worker.
// File-level vi.mock calls still override this one.
vi.mock('./rfb_loader', () => ({
  NOVNC_MODULE_URL: '/assets/novnc.esm.js',
  loadRFB: vi.fn(async () => {
    throw new Error('novnc is not available in tests')
  }),
}))

export {}
