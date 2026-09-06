import type { dict as enDict } from './locales/en'

// en is the source dictionary and the type truth: every other locale
// satisfies this shape. String entries widen to string (en's `as const`
// literals would forbid any translation); function entries keep en's
// signatures verbatim.
export type Dict = {
  [K in keyof typeof enDict]: (typeof enDict)[K] extends (...args: never[]) => unknown
    ? (typeof enDict)[K]
    : string
}

// Keys whose entries are plain strings, and keys with interpolation
// templates. Both are anchorable: static entries via data-i18n alone,
// interpolated ones with the numbers carried in data-i18n-arg.
type StringEntries = { [K in keyof Dict as Dict[K] extends string ? K : never]: never }
export type StaticKey = keyof StringEntries
type FnEntries = { [K in keyof Dict as Dict[K] extends (...args: never[]) => unknown ? K : never]: never }
type AnchorKey = StaticKey | keyof FnEntries

// Locale ids are locales/ file stems, discovered at runtime via the glob
// below. tsc cannot enumerate a directory (import.meta.glob keys type as
// plain string, not a literal union), so Locale cannot be the stem union —
// the tests glob the directory independently and lock the shipped set.
export type Locale = string

interface LocaleModule {
  dict: Dict
  endonym: string
}

const PATH_PREFIX = './locales/'
const PATH_SUFFIX = '.ts'

// eager: the registry must be populated before initLocale() runs at module
// scope below.
const modules = import.meta.glob<LocaleModule>('./locales/*.ts', { eager: true })

// Registered by file stem ('./locales/en.ts' → 'en') — dropping a new file
// into locales/ adds a language with zero further edits.
const locales: Record<Locale, LocaleModule> = {}
for (const [path, mod] of Object.entries(modules)) {
  locales[path.slice(PATH_PREFIX.length, path.length - PATH_SUFFIX.length)] = mod
}

// 'gbot-language' holds the user's PINNED locale (a registered stem). Absent
// (or holding anything else) means "auto" — resolve from navigator.language.
const STORAGE_KEY = 'gbot-language'

let cur: Locale = 'en'

export const setLocale = (l: Locale): void => {
  cur = l
}

export const currentLocale = (): Locale => cur

export interface LocaleOption {
  locale: Locale
  endonym: string
}

// Option list for pickers — endonyms ('中文', not "Chinese") per locale
// convention. Order follows the glob's sorted paths, so the segment renders
// the same order every build.
export function localeOptions(): LocaleOption[] {
  return Object.entries(locales).map(([locale, m]) => ({ locale, endonym: m.endonym }))
}

// Resolved at CALL time, not at import time — module evaluation order varies
// (chat.ts's transitive imports run before main.ts's body), so a const
// snapshot would bind before initLocale ever ran. Interpolation entries come
// back as functions: t('modelsCount')(3).
export function t<K extends keyof Dict>(key: K): Dict[K] {
  // An unregistered id can only arrive via a stray setLocale call — read the
  // source dictionary rather than crash; en is guaranteed present because
  // Dict's type import above fails the build without locales/en.ts.
  const m = locales[cur] ?? locales['en']
  return m.dict[key]
}

// The two-letter navigator prefix picks the locale it names when one is
// registered; any other language reads the source dictionary (en).
export const detectLocale = (): Locale => {
  const lang = (navigator.language || '').slice(0, 2).toLowerCase()
  return locales[lang] !== undefined ? lang : 'en'
}

// A pinned registered locale wins; a missing, empty, or unknown value means
// auto and falls back to navigator detection.
export function persistedLocale(): Locale | null {
  const v = localStorage.getItem(STORAGE_KEY)
  return v !== null && locales[v] !== undefined ? v : null
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
