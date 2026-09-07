import { describe, it, expect, afterEach, vi } from 'vitest'
import {
  t,
  setLocale,
  currentLocale,
  initLocale,
  detectLocale,
  persistedLocale,
  saveLocale,
  saveLocaleAuto,
  retranslate,
  localeOptions,
  pluralCat,
} from './index'

// An independent glob of locales/ — the registry under test must derive
// from these files, so the suite compares against the directory itself
// rather than a hand-maintained list (a dropped-in locale file has to show
// up with zero registration code).
const files = import.meta.glob<{ dict: Record<string, unknown>; endonym: string }>(
  './locales/*.ts',
  { eager: true },
)
const stems = Object.keys(files).map((p) => p.slice('./locales/'.length, -'.ts'.length))

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.removeItem('gbot-language')
})

describe('locale registry', () => {
  it('localeOptions reflects every locales/ file in glob order', () => {
    const fromFiles = stems.map((s) => ({ locale: s, endonym: files[`./locales/${s}.ts`].endonym }))
    expect(localeOptions()).toEqual(fromFiles)
  })

  it('shipped endonyms are each locale self-named', () => {
    const endonyms = new Map(localeOptions().map((o) => [o.locale, o.endonym]))
    expect(endonyms.get('en')).toBe('English')
    expect(endonyms.get('zh')).toBe('简体中文')
    expect(endonyms.get('zh-TW')).toBe('繁體中文')
  })
})

describe('i18n dictionary', () => {
  it('every locale file exposes exactly the source dictionary key set', () => {
    const enKeys = Object.keys(files['./locales/en.ts'].dict).sort()
    for (const s of stems) {
      expect(Object.keys(files[`./locales/${s}.ts`].dict).sort()).toEqual(enKeys)
    }
  })

  it('t() resolves at call time against the active locale', () => {
    setLocale('en')
    expect(t('settingsTitle')).toBe('Settings')
    expect(t('modelsCount')(2)).toBe('2 models')
    setLocale('zh')
    expect(t('settingsTitle')).toBe('设置')
    expect(t('modelsCount')(2)).toBe('2个模型')
    setLocale('zh-TW')
    expect(t('settingsTitle')).toBe('設定')
    expect(t('modelsCount')(2)).toBe('2個模型')
  })
})

describe('pluralized counts', () => {
  it('en modelsCount: 1 model / 2 models', () => {
    setLocale('en')
    expect(t('modelsCount')(1)).toBe('1 model')
    expect(t('modelsCount')(2)).toBe('2 models')
    expect(t('modelsCount')(0)).toBe('0 models')
  })

  it('en fetchedModels pluralizes on the fetched total', () => {
    setLocale('en')
    expect(t('fetchedModels')(1, 0)).toBe('Fetched 1 model, 0 added (existing kept)')
    expect(t('fetchedModels')(2, 1)).toBe('Fetched 2 models, 1 added (existing kept)')
  })

  it('zh and zh-TW are single-class: the digit hugs the noun even at n=1', () => {
    setLocale('zh')
    expect(t('modelsCount')(1)).toBe('1个模型')
    setLocale('zh-TW')
    expect(t('modelsCount')(1)).toBe('1個模型')
  })

  it('the data-i18n-arg re-run path re-pluralizes on locale switch', () => {
    const root = document.createElement('div')
    const count = document.createElement('span')
    count.setAttribute('data-i18n', 'modelsCount')
    count.setAttribute('data-i18n-arg', '1')
    count.textContent = '1 model'
    root.append(count)
    setLocale('zh')
    retranslate(root)
    expect(count.textContent).toBe('1个模型')
    setLocale('en')
    retranslate(root)
    expect(count.textContent).toBe('1 model')
  })
})

describe('pluralCat', () => {
  it('en selects one/other; zh selects other for every n', () => {
    setLocale('en')
    expect(pluralCat(1)).toBe('one')
    expect(pluralCat(2)).toBe('other')
    expect(pluralCat(0)).toBe('other')
    setLocale('zh')
    expect(pluralCat(1)).toBe('other')
  })

  it('an unregistered locale pluralizes by en rules, mirroring the t() fallback', () => {
    setLocale('xx')
    expect(pluralCat(1)).toBe('one')
    expect(pluralCat(2)).toBe('other')
  })

  it('reuses the cached instance for repeated calls under one locale', () => {
    const spy = vi.spyOn(Intl, 'PluralRules')
    setLocale('en')
    pluralCat(3)
    const afterFirst = spy.mock.calls.length
    pluralCat(4)
    pluralCat(5)
    expect(spy.mock.calls.length).toBe(afterFirst)
    spy.mockRestore()
  })
})

describe('locale bootstrap', () => {
  it('initLocale adopts a persisted registered locale verbatim', () => {
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    expect(currentLocale()).toBe('zh')
    localStorage.setItem('gbot-language', 'en')
    initLocale()
    expect(currentLocale()).toBe('en')
  })

  it('auto (no stored value) resolves from navigator.language', () => {
    vi.stubGlobal('navigator', { language: 'zh-CN' })
    initLocale()
    expect(currentLocale()).toBe('zh')
    // 'xx' is not a real language, so it can never become a locale file —
    // the fallback premise stays true no matter how many locales ship.
    vi.stubGlobal('navigator', { language: 'xx' })
    initLocale()
    expect(currentLocale()).toBe('en')
  })

  it('unknown stored value falls through to navigator detection', () => {
    localStorage.setItem('gbot-language', 'klingon')
    vi.stubGlobal('navigator', { language: 'zh-CN' })
    initLocale()
    expect(currentLocale()).toBe('zh')
  })
})

describe('persistedLocale', () => {
  it('maps stored registered values through and everything else to null (auto)', () => {
    expect(persistedLocale()).toBeNull()
    localStorage.setItem('gbot-language', 'zh')
    expect(persistedLocale()).toBe('zh')
    localStorage.setItem('gbot-language', 'en')
    expect(persistedLocale()).toBe('en')
    localStorage.setItem('gbot-language', 'auto')
    expect(persistedLocale()).toBeNull()
  })
})

describe('saveLocale / saveLocaleAuto', () => {
  it('writes and removes the gbot-language key', () => {
    saveLocale('zh')
    expect(localStorage.getItem('gbot-language')).toBe('zh')
    saveLocaleAuto()
    expect(localStorage.getItem('gbot-language')).toBeNull()
  })

  it('saveLocale syncs the live locale so t() reads the new language immediately', () => {
    setLocale('zh')
    saveLocale('en')
    expect(currentLocale()).toBe('en')
    expect(t('settingsTitle')).toBe('Settings')
  })

  it('saveLocaleAuto re-resolves the live locale from the system', () => {
    setLocale('zh')
    vi.stubGlobal('navigator', { language: 'en-US' })
    saveLocaleAuto()
    expect(currentLocale()).toBe('en')
    expect(t('settingsTitle')).toBe('Settings')
  })

  it('a storage failure in saveLocale leaves the live locale untouched', () => {
    setLocale('zh')
    vi.stubGlobal('localStorage', {
      getItem: () => 'zh',
      setItem: () => {
        throw new Error('quota exceeded')
      },
      removeItem: () => {},
    })
    expect(() => saveLocale('en')).toThrow('quota exceeded')
    expect(currentLocale()).toBe('zh')
    expect(t('settingsTitle')).toBe('设置')
  })

  it('saveLocale rethrows storage failures for the caller to toast', () => {
    vi.stubGlobal('localStorage', {
      getItem: () => null,
      setItem: () => {
        throw new Error('quota exceeded')
      },
      removeItem: () => {},
    })
    expect(() => saveLocale('zh')).toThrow('quota exceeded')
  })

  it('saveLocaleAuto rethrows storage failures for the caller to toast', () => {
    vi.stubGlobal('localStorage', {
      getItem: () => null,
      setItem: () => {},
      removeItem: () => {
        throw new Error('quota exceeded')
      },
    })
    expect(() => saveLocaleAuto()).toThrow('quota exceeded')
  })
})

describe('detectLocale', () => {
  it('RFC 4647 Lookup: region, script, and script+region tags all land on zh-TW', () => {
    vi.stubGlobal('navigator', { languages: ['zh-TW'] })
    expect(detectLocale()).toBe('zh-TW')
    vi.stubGlobal('navigator', { languages: ['zh-Hant-TW'] })
    expect(detectLocale()).toBe('zh-TW')
    vi.stubGlobal('navigator', { languages: ['zh-Hant'] })
    expect(detectLocale()).toBe('zh-TW')
  })

  it('truncates unregistered regions down to the language stem', () => {
    vi.stubGlobal('navigator', { languages: ['zh-CN'] })
    expect(detectLocale()).toBe('zh')
    vi.stubGlobal('navigator', { languages: ['en-US'] })
    expect(detectLocale()).toBe('en')
  })

  it('walks the list in order — an unmatched head falls through to the next candidate', () => {
    vi.stubGlobal('navigator', { languages: ['ko-KR', 'zh-TW'] })
    expect(detectLocale()).toBe('zh-TW')
  })

  it('falls back to en when nothing matches', () => {
    vi.stubGlobal('navigator', { languages: ['xx'] })
    expect(detectLocale()).toBe('en')
  })

  it('uses [navigator.language] when the languages list is absent', () => {
    vi.stubGlobal('navigator', { language: 'zh-TW' })
    expect(detectLocale()).toBe('zh-TW')
  })

  it('survives an empty navigator.language', () => {
    vi.stubGlobal('navigator', { language: '' })
    expect(detectLocale()).toBe('en')
  })
})

it('t() falls back to English for an unregistered locale', () => {
    setLocale('xx')
    expect(t('settingsTitle')).toBe('Settings')
    expect(currentLocale()).toBe('xx')
  })

  it('every registered locale declares a non-empty endonym', () => {
    for (const o of localeOptions()) expect(o.endonym.length > 0).toBe(true)
  })

  describe('retranslate', () => {
  it('rewrites every [data-i18n] node to the active locale and leaves others alone', () => {
    const root = document.createElement('div')
    const anchored = document.createElement('span')
    anchored.setAttribute('data-i18n', 'settingsTitle')
    anchored.textContent = 'Settings'
    const plain = document.createElement('span')
    plain.textContent = 'literal'
    root.append(anchored, plain)

    setLocale('zh')
    retranslate(root)
    expect(anchored.textContent).toBe('设置')
    expect(plain.textContent).toBe('literal')

    setLocale('en')
    retranslate(root)
    expect(anchored.textContent).toBe('Settings')
  })

  it('re-runs interpolation templates from data-i18n-arg', () => {
    const root = document.createElement('div')
    const count = document.createElement('span')
    count.setAttribute('data-i18n', 'modelsCount')
    count.setAttribute('data-i18n-arg', '3')
    count.textContent = '3 models'
    root.append(count)
    setLocale('zh')
    retranslate(root)
    expect(count.textContent).toBe('3个模型')
    setLocale('en')
    retranslate(root)
    expect(count.textContent).toBe('3 models')
  })

  it('an interpolated anchor without data-i18n-arg is left untouched, never rendered as undefined', () => {
    const root = document.createElement('div')
    const bare = document.createElement('span')
    bare.setAttribute('data-i18n', 'modelsCount')
    bare.textContent = '3 models'
    root.append(bare)
    setLocale('zh')
    retranslate(root)
    // No arg → template cannot run; the old text stays instead of "undefined个模型".
    expect(bare.textContent).toBe('3 models')
  })

  it('rewrites placeholder attributes from data-i18n-placeholder anchors', () => {
    const root = document.createElement('div')
    const input = document.createElement('textarea')
    input.setAttribute('data-i18n-placeholder', 'headerSearch')
    input.setAttribute('placeholder', 'Search...')
    root.append(input)
    setLocale('zh')
    retranslate(root)
    expect(input.getAttribute('placeholder')).toBe('搜索…')
    setLocale('en')
    retranslate(root)
    expect(input.getAttribute('placeholder')).toBe('Search...')
  })
})
