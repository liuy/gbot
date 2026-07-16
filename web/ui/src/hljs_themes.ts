// All hljs theme pairs. Each entry has a dark and light variant.
// At runtime we inject only the active theme's CSS into a <style> tag,
// swapping when the user selects a different theme or toggles dark/light.

import githubDark from 'highlight.js/styles/github-dark.css?raw'
import githubLight from 'highlight.js/styles/github.css?raw'
import atomOneDark from 'highlight.js/styles/atom-one-dark.css?raw'
import atomOneLight from 'highlight.js/styles/atom-one-light.css?raw'
import a11yDark from 'highlight.js/styles/a11y-dark.css?raw'
import a11yLight from 'highlight.js/styles/a11y-light.css?raw'
import gradientDark from 'highlight.js/styles/gradient-dark.css?raw'
import gradientLight from 'highlight.js/styles/gradient-light.css?raw'
import isblEditorDark from 'highlight.js/styles/isbl-editor-dark.css?raw'
import isblEditorLight from 'highlight.js/styles/isbl-editor-light.css?raw'
import kimbieDark from 'highlight.js/styles/kimbie-dark.css?raw'
import kimbieLight from 'highlight.js/styles/kimbie-light.css?raw'
import nnfxDark from 'highlight.js/styles/nnfx-dark.css?raw'
import nnfxLight from 'highlight.js/styles/nnfx-light.css?raw'
import pandaDark from 'highlight.js/styles/panda-syntax-dark.css?raw'
import pandaLight from 'highlight.js/styles/panda-syntax-light.css?raw'
import paraisoDark from 'highlight.js/styles/paraiso-dark.css?raw'
import paraisoLight from 'highlight.js/styles/paraiso-light.css?raw'
import qtcreatorDark from 'highlight.js/styles/qtcreator-dark.css?raw'
import qtcreatorLight from 'highlight.js/styles/qtcreator-light.css?raw'
import stackoverflowDark from 'highlight.js/styles/stackoverflow-dark.css?raw'
import stackoverflowLight from 'highlight.js/styles/stackoverflow-light.css?raw'
import tokyoNightDark from 'highlight.js/styles/tokyo-night-dark.css?raw'
import tokyoNightLight from 'highlight.js/styles/tokyo-night-light.css?raw'
import rosePine from 'highlight.js/styles/rose-pine.css?raw'
import rosePineDawn from 'highlight.js/styles/rose-pine-dawn.css?raw'

export interface HljsTheme {
  key: string
  label: string
  darkCss: string
  lightCss: string
}

export const HLJS_THEMES: HljsTheme[] = [
  { key: 'a11y',           label: 'A11y',             darkCss: a11yDark,         lightCss: a11yLight },
  { key: 'atom-one',       label: 'Atom One',         darkCss: atomOneDark,      lightCss: atomOneLight },
  { key: 'github',         label: 'GitHub',           darkCss: githubDark,       lightCss: githubLight },
  { key: 'gradient',       label: 'Gradient',         darkCss: gradientDark,     lightCss: gradientLight },
  { key: 'isbl-editor',    label: 'ISBL Editor',      darkCss: isblEditorDark,   lightCss: isblEditorLight },
  { key: 'kimbie',         label: 'Kimbie',           darkCss: kimbieDark,       lightCss: kimbieLight },
  { key: 'nnfx',           label: 'NNFX',             darkCss: nnfxDark,         lightCss: nnfxLight },
  { key: 'panda-syntax',   label: 'Panda Syntax',     darkCss: pandaDark,        lightCss: pandaLight },
  { key: 'paraiso',        label: 'Paraíso',          darkCss: paraisoDark,      lightCss: paraisoLight },
  { key: 'qtcreator',      label: 'Qt Creator',       darkCss: qtcreatorDark,    lightCss: qtcreatorLight },
  { key: 'rose-pine',      label: 'Rosé Pine',        darkCss: rosePine,         lightCss: rosePineDawn },
  { key: 'stackoverflow',  label: 'Stack Overflow',   darkCss: stackoverflowDark, lightCss: stackoverflowLight },
  { key: 'tokyo-night',    label: 'Tokyo Night',      darkCss: tokyoNightDark,   lightCss: tokyoNightLight },
]

const HLJS_DEFAULT = 'atom-one'
const STORAGE_KEY = 'gbot-hljs-theme'

export function getSavedHljsTheme(): string {
  return localStorage.getItem(STORAGE_KEY) || HLJS_DEFAULT
}

export function saveHljsTheme(key: string): void {
  localStorage.setItem(STORAGE_KEY, key)
}

let styleEl: HTMLStyleElement | null = null

function getStyleEl(): HTMLStyleElement {
  if (!styleEl || !styleEl.isConnected) {
    styleEl = document.createElement('style')
    styleEl.id = 'hljs-theme-css'
    document.head.appendChild(styleEl)
  }
  return styleEl
}

export function applyHljsTheme(key: string, isDark: boolean): void {
  const theme = HLJS_THEMES.find(t => t.key === key) || HLJS_THEMES[0]
  getStyleEl().textContent = isDark ? theme.darkCss : theme.lightCss
}
