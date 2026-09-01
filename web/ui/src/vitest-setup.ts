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

export {}
