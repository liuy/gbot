import { en, type Dict } from './en'
import { zh } from './zh'

export type { Dict } from './en'
export type Locale = 'en' | 'zh'

// Keys whose entries are plain strings, and keys with interpolation
// templates. Both are anchorable: static entries via data-i18n alone,
// interpolated ones with the numbers carried in data-i18n-arg.
type StringEntries = { [K in keyof Dict as Dict[K] extends string ? K : never]: never }
export type StaticKey = keyof StringEntries
type FnEntries = { [K in keyof Dict as Dict[K] extends (...args: never[]) => unknown ? K : never]: never }
type AnchorKey = StaticKey | keyof FnEntries

// 'gbot-language' holds the user's PINNED locale ('en'|'zh'). Absent (or
// holding anything else) means "auto" — resolve from navigator.language.
const STORAGE_KEY = 'gbot-language'

let cur: Locale = 'en'

export const setLocale = (l: Locale): void => {
  cur = l
}

export const currentLocale = (): Locale => cur

// Resolved at CALL time, not at import time — module evaluation order varies
// (chat.ts's transitive imports run before main.ts's body), so a const
// snapshot would bind before initLocale ever ran. Interpolation entries come
// back as functions: t('modelsCount')(3).
export function t<K extends keyof Dict>(key: K): Dict[K] {
  const d: Dict = cur === 'zh' ? (zh as Dict) : en
  return d[key]
}

export const detectLocale = (): Locale =>
  (navigator.language || '').slice(0, 2).toLowerCase() === 'zh' ? 'zh' : 'en'

// A pinned 'en'/'zh' wins; a missing, empty, or unknown value means auto and
// falls back to navigator detection (non-zh → en).
export function persistedLocale(): Locale | null {
  const v = localStorage.getItem(STORAGE_KEY)
  return v === 'en' || v === 'zh' ? v : null
}

export function initLocale(): void {
  const persisted = persistedLocale()
  if (persisted) {
    setLocale(persisted)
    return
  }
  setLocale(detectLocale())
}

// setItem/removeItem rethrow on private-mode storage and quota exhaustion —
// the caller toasts; a silent catch would lose the user's choice. On success
// the live locale follows the write, so callers can retranslate the visible
// DOM in place instead of reloading.
export function saveLocale(l: Locale): void {
  localStorage.setItem(STORAGE_KEY, l)
  setLocale(l)
}

export function saveLocaleAuto(): void {
  localStorage.removeItem(STORAGE_KEY)
  setLocale(detectLocale())
}

// Swaps the text of every anchored node ([data-i18n]) to the active locale.
// Anchors carry static or interpolated keys; interpolated ones pair with
    // data-i18n-arg.
export function retranslate(root: ParentNode): void {
  for (const el of root.querySelectorAll('[data-i18n]')) {
    const key = el.getAttribute('data-i18n') as AnchorKey
    const val = t(key)
    if (typeof val === 'string') {
      el.textContent = val
      continue
    }
    // Interpolated entries carry their numbers on the element so the
    // retranslate pass can re-run the template (e.g. "3 models" ↔ "3个模型").
    // Without the arg the template cannot run — leave the node untouched
    // (stale-but-readable) rather than rendering "undefined".
    const argAttr = el.getAttribute('data-i18n-arg')
    if (argAttr === null) continue
    const args = argAttr.split(',').map(Number)
    el.textContent = (val as (...nums: number[]) => string)(...args)
  }
}

// Self-initialize at module scope: localStorage is synchronous, and every
// t() call happens later still (inside createChat), so import order cannot
// race this init and nothing renders in the wrong locale first.
initLocale()
