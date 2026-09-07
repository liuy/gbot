import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createHeader } from './header'
import { initLocale, retranslate } from './i18n'

// Header picker chrome: dropdown bodies re-render on every open (build-time
// t() self-heals), the search placeholder is a boot-resident attribute
// (data-i18n-placeholder + retranslate), and icon-button labels are aria-only
// copy resolved at build time (same precedent as the sidebar/settings chrome).

function mountHeader() {
  const header = createHeader({
    onModelSelect: () => {},
    onEngineSwitch: () => {},
    onEngineNew: () => {},
  })
  document.body.appendChild(header.root)
  return header
}

// breadcrumb order: [enginePicker] [sep] [modelPicker]; the context trigger
// starts hidden, so the model trigger is the LAST visible button.text-[15px].
function clickModelTrigger(header: ReturnType<typeof createHeader>): void {
  const triggers = header.root.querySelectorAll('button.text-\\[15px\\]:not(.hidden)')
  ;(triggers[triggers.length - 1] as HTMLButtonElement).dispatchEvent(
    new MouseEvent('click', { bubbles: true }),
  )
}

function clickEngineTrigger(header: ReturnType<typeof createHeader>): void {
  ;(header.root.querySelector('button.text-\\[15px\\]:not(.hidden)') as HTMLButtonElement).dispatchEvent(
    new MouseEvent('click', { bubbles: true }),
  )
}

function modelSearch(): HTMLTextAreaElement {
  return document.body.querySelector('textarea[data-i18n-placeholder]') as HTMLTextAreaElement
}

describe('header i18n', () => {
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

  it('model picker chrome renders in zh when pinned (placeholder, empty state, Menu aria)', () => {
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    const header = mountHeader()
    clickModelTrigger(header)
    expect(modelSearch().placeholder).toBe('搜索…')
    // No models set → the empty-state row re-renders per open.
    const panel = modelSearch().closest('.modal-enter') as HTMLElement
    expect(panel.textContent).toContain('未找到模型')
    expect(panel.textContent).not.toContain('No models found')
    // aria-only copy resolves at build time — no anchor on icon buttons.
    expect(header.root.querySelector('button[aria-label="菜单"]')).not.toBeNull()
  })

  it('engine picker footer resolves 新建引擎 at render time', () => {
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    const header = mountHeader()
    clickEngineTrigger(header)
    const panel = document.body.querySelector('[data-testid="engine-picker-panel"]') as HTMLElement
    expect(panel.querySelector('button[aria-label="新建引擎"]')).not.toBeNull()
  })

  it('a language switch retranslates the boot-resident placeholder in place', () => {
    const header = mountHeader()
    clickModelTrigger(header)
    expect(modelSearch().placeholder).toBe('Search...')
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    retranslate(document.body)
    expect(modelSearch().placeholder).toBe('搜索…')
  })

  it('the engine footer label re-resolves on reopen after a language switch', () => {
    const header = mountHeader()
    clickEngineTrigger(header)
    const panel = document.body.querySelector('[data-testid="engine-picker-panel"]') as HTMLElement
    expect(panel.querySelector('button[aria-label="New engine"]')).not.toBeNull()
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    clickEngineTrigger(header)
    expect(panel.querySelector('button[aria-label="新建引擎"]')).not.toBeNull()
  })

  it('the GBot wordmark stays literal in both locales (brand name)', () => {
    const enHeader = mountHeader()
    expect(enHeader.root.querySelector('header span.text-\\[15px\\]')?.textContent).toBe('GBot')
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    const zhHeader = mountHeader()
    expect(zhHeader.root.querySelector('header span.text-\\[15px\\]')?.textContent).toBe('GBot')
  })
})
