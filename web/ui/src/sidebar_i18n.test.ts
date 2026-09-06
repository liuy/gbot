import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { createSidebar } from './sidebar'
import { createSettingsPage } from './settings'
import { initLocale, retranslate } from './i18n'

// Sidebar chrome is app-wide (built once at startup), so its locale coverage
// spans two layers: build-time copy under a pinned locale, and the settings
// language switch retranslating nodes OUTSIDE the settings overlay.

function mountSidebar() {
  const mainContent = document.createElement('div')
  document.body.appendChild(mainContent)
  const sidebar = createSidebar({ mainContent })
  document.body.appendChild(sidebar.root)
  return sidebar
}

const sessionsTitleOf = (sidebar: ReturnType<typeof createSidebar>) =>
  sidebar.root.querySelector('[data-sessions-header] > span') as HTMLElement
const artifactsTitleOf = (sidebar: ReturnType<typeof createSidebar>) =>
  sidebar.root.querySelector('.sidebar-artifacts > div > span') as HTMLElement

describe('sidebar i18n', () => {
  it('chess row renders 中国象棋 in zh and swaps via the global retranslate', () => {
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    const sidebar = mountSidebar()
    const row = sidebar.root.querySelector('[data-game-row="chess"] span[data-i18n]') as HTMLElement
    expect(row.textContent).toBe('中国象棋')
    localStorage.setItem('gbot-language', 'en')
    initLocale()
    retranslate(document.body)
    expect(row.textContent).toBe('Chinese Chess')
    expect(sidebar.root.querySelector('[data-game-row="chess"] span[data-i18n]')).toBe(row)
  })

  beforeEach(() => {
    localStorage.removeItem('gbot-language')
    document.body.innerHTML = ''
  })
  afterEach(() => {
    localStorage.removeItem('gbot-language')
    // jsdom's navigator is en — re-resolve so a pinned zh cannot leak into a
    // later test through the i18n module's locale state.
    initLocale()
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('renders chrome in Chinese when localStorage pins zh', () => {
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    const sidebar = mountSidebar()
    expect(sessionsTitleOf(sidebar).textContent).toBe('会话')
    expect(sidebar.root.querySelector('[data-games-header]')?.textContent).toBe('游戏')
    // aria-only copy resolves at build time (same precedent as the settings
    // page's title attributes) — no anchor needed on icon buttons.
    expect(sidebar.root.querySelector('[data-new-session]')?.getAttribute('aria-label')).toBe('新建会话')
    expect(sidebar.root.querySelector('[data-settings-btn]')?.getAttribute('aria-label')).toBe('设置')
    expect(sidebar.root.querySelector('[data-clear-artifacts]')?.getAttribute('aria-label')).toBe('清空全部 Artifacts')
  })

  it('empty artifacts state renders in Chinese when pinned zh', () => {
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    const sidebar = mountSidebar()
    sidebar.setArtifacts([])
    const section = sidebar.root.querySelector('.sidebar-artifacts') as HTMLElement
    expect(section.textContent).toContain('暂无 Artifacts')
  })

  it('Artifacts heading stays literal in both locales (product name)', () => {
    const enSidebar = mountSidebar()
    expect(artifactsTitleOf(enSidebar).textContent).toBe('Artifacts')
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    const zhSidebar = mountSidebar()
    expect(artifactsTitleOf(zhSidebar).textContent).toBe('Artifacts')
  })

  it('switching language in settings retranslates the mounted sidebar in place', async () => {
    const fetchMock = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ providers: [], default: { provider: '', model: '' } }),
    }))
    vi.stubGlobal('fetch', fetchMock)
    const sidebar = mountSidebar()
    const sessionsTitle = sessionsTitleOf(sidebar)
    const gamesHeader = sidebar.root.querySelector('[data-games-header]') as HTMLElement
    expect(sessionsTitle.textContent).toBe('Sessions')

    const page = createSettingsPage()
    document.body.appendChild(page.root)
    page.open()
    ;(page.root.querySelectorAll('[data-general-row]')[0] as HTMLElement).click()
    ;(page.root.querySelector('[data-lang-opt="zh"]') as HTMLElement).click()

    // Same DOM node: the switch scans document.body, so sidebar anchors
    // outside the settings overlay swap without a sidebar rebuild.
    expect(sessionsTitle.textContent).toBe('会话')
    expect(gamesHeader.textContent).toBe('游戏')
    expect(sessionsTitleOf(sidebar)).toBe(sessionsTitle)
    expect(localStorage.getItem('gbot-language')).toBe('zh')
  })
})
