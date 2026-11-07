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

describe('retranslate — data-i18n-title anchors', () => {
  it('swaps the title attribute of tooltip-only elements', () => {
    document.body.innerHTML = ''
    const chip = document.createElement('span')
    chip.setAttribute('data-i18n-title', 'rdChipControl')
    chip.setAttribute('title', 'Control')
    document.body.appendChild(chip)

    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    retranslate(document.body)
    expect(chip.getAttribute('title')).toBe('接管')

    localStorage.setItem('gbot-language', 'en')
    initLocale()
    retranslate(document.body)
    expect(chip.getAttribute('title')).toBe('Control')
    localStorage.removeItem('gbot-language')
    document.body.innerHTML = ''
  })
})

// The config/providers template entries had zero execution coverage in zh and
// zh-TW — a broken template would ship silently. Exact-string pins through
// t(), the same read path settings uses.
describe('locale template pins — providers/status entries', () => {
  it('zh renders the seven template entries verbatim', () => {
    setLocale('zh')
    expect(t('saveFailed')('kv')).toBe('保存失败：kv')
    expect(t('loadFailed')('api')).toBe('加载失败：api')
    expect(t('pinging')('http://x')).toBe('正在请求http://x…')
    expect(t('fetchedModels')(12, 3)).toBe('已获取12个模型，新增3个（现有保留）')
    expect(t('configLastReload')('10:00')).toBe('上次重载：10:00')
    expect(t('configBannerOk')(2, 1, 0)).toBe('重载成功 · 2 个变更 · 1 个未变更 · 0 失败')
    expect(t('configBannerErr')(2)).toBe('已放弃 · 2 个文件解析失败 · 现状零影响')
  })

  it('zh-TW renders the seven template entries verbatim', () => {
    setLocale('zh-TW')
    expect(t('saveFailed')('kv')).toBe('儲存失敗：kv')
    expect(t('loadFailed')('api')).toBe('載入失敗：api')
    expect(t('pinging')('http://x')).toBe('正在請求http://x…')
    expect(t('fetchedModels')(12, 3)).toBe('已擷取12個模型，新增3個（現有保留）')
    expect(t('configLastReload')('10:00')).toBe('上次重新載入：10:00')
    expect(t('configBannerOk')(2, 1, 0)).toBe('重新載入成功 · 2 個變更 · 1 個未變更 · 0 失敗')
    expect(t('configBannerErr')(2)).toBe('已放棄 · 2 個檔案解析失敗 · 現狀零影響')
  })

  it('en keeps the same entries as the source of truth', () => {
    setLocale('en')
    expect(t('saveFailed')('kv')).toBe('Save failed: kv')
    expect(t('loadFailed')('api')).toBe('Load failed: api')
    expect(t('pinging')('http://x')).toBe('Pinging http://x…')
    expect(t('fetchedModels')(12, 3)).toBe('Fetched 12 models, 3 added (existing kept)')
    expect(t('configLastReload')('10:00')).toBe('Last reload: 10:00')
    expect(t('configBannerOk')(2, 1, 0)).toBe('Reloaded · 2 changed · 1 unchanged · 0 failed')
    expect(t('configBannerErr')(2)).toBe('Aborted · 2 file parse failure · nothing applied')
  })
})

describe('retranslate — multi-digit and multi-arg values', () => {
  it('splits data-i18n-arg on commas, not into digits', () => {
    const root = document.createElement('div')
    const count = document.createElement('span')
    count.setAttribute('data-i18n', 'modelsCount')
    // Two digits: digit-splitting parses ['1','2'] and pluralizes on 1.
    count.setAttribute('data-i18n-arg', '12')
    count.textContent = '12 models'
    root.append(count)
    setLocale('zh')
    retranslate(root)
    expect(count.textContent).toBe('12个模型')
    setLocale('en')
    retranslate(root)
    expect(count.textContent).toBe('12 models')
  })

  it('carries both comma-separated args into two-parameter templates', () => {
    const root = document.createElement('div')
    const span = document.createElement('span')
    span.setAttribute('data-i18n', 'fetchedModels')
    span.setAttribute('data-i18n-arg', '7,2')
    span.textContent = 'Fetched 7 models, 2 added (existing kept)'
    root.append(span)
    setLocale('zh')
    retranslate(root)
    expect(span.textContent).toBe('已获取7个模型，新增2个（现有保留）')
  })
})

describe('detectLocale — junk tags', () => {
  it('a stem-prefixed tag without a dash never partially matches', () => {
    // RFC 4647 Lookup truncates at '-' boundaries only: 'zh1' must fall to
    // en, not leak into 'zh' by character truncation.
    vi.stubGlobal('navigator', { languages: ['zh1'] })
    expect(detectLocale()).toBe('en')
  })
})

describe('pluralCat — unregistered locales follow en rules exactly', () => {
  it('fr-classified locales do not import their native zero-is-one rule', () => {
    // Intl.PluralRules('fr').select(0) === 'one'; en rules say 'other'.
    setLocale('fr')
    expect(pluralCat(0)).toBe('other')
    expect(pluralCat(1)).toBe('one')
  })

  it('a fresh locale entry constructs its rules object exactly once', () => {
    // zh-TW is used by no other pluralCat test, so the module-level cache
    // starts empty here — the first call must build, later ones must reuse.
    // A bare spyOn replaces the constructor with a broken stub, so the spy
    // delegates construction to the real class and only counts the calls.
    const real = Intl.PluralRules
    const spy = vi.spyOn(Intl, 'PluralRules')
    spy.mockImplementation(function (locale?: string | string[]) {
      return new real(locale)
    } as unknown as typeof Intl.PluralRules)
    setLocale('zh-TW')
    expect(pluralCat(1)).toBe('other')
    expect(spy.mock.calls.length).toBe(1)
    expect(pluralCat(2)).toBe('other')
    expect(pluralCat(0)).toBe('other')
    expect(spy.mock.calls.length).toBe(1)
    spy.mockRestore()
  })
})
