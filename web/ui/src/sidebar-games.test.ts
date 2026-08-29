import { describe, it } from 'vitest'
import { createSidebar, BUILTIN_GAMES } from '../src/sidebar'

function mount() {
  document.body.innerHTML = ''
  const mainContent = document.createElement('div')
  document.body.appendChild(mainContent)
  const sidebar = createSidebar({ mainContent })
  document.body.appendChild(sidebar.root)
  return { sidebar, mainContent }
}

describe('sidebar builtin games', () => {
  it('renders the games group immediately, before any setArtifacts call', () => {
    const { sidebar } = mount()
    const rows = sidebar.root.querySelectorAll<HTMLElement>('[data-game-row]')
    if (rows.length !== 1) {
      throw new Error(`game rows = ${rows.length}, want exactly 1`)
    }
    if (!rows[0].textContent?.includes('Chinese Chess')) {
      throw new Error(`game row text = ${rows[0].textContent}, want Chinese Chess`)
    }
    const header = sidebar.root.querySelector<HTMLElement>('[data-games-header]')
    if (!header || header.textContent !== 'Games') {
      throw new Error(`games header = ${header?.textContent}, want Games`)
    }
  })

  it('clicking a game row fires onArtifactClick with the id and closes the sidebar', () => {
    const { sidebar, mainContent } = mount()
    let clicked = ''
    sidebar.onArtifactClick((name) => {
      clicked = name
    })
    sidebar.open()
    if (sidebar.root.style.transform !== 'translateX(0px)') {
      throw new Error(`pre-click transform = ${sidebar.root.style.transform}, want open`)
    }
    const row = sidebar.root.querySelector<HTMLElement>('[data-game-row]')
    if (!row) throw new Error('game row not found')
    row.click()
    if (clicked !== 'chess') {
      throw new Error(`artifactClick got ${clicked}, want chess`)
    }
    if (sidebar.root.style.transform !== 'translateX(-100%)') {
      throw new Error(`post-click transform = ${sidebar.root.style.transform}, want closed`)
    }
    if (mainContent.style.transform !== 'translateX(0px)') {
      throw new Error(`mainContent transform = ${mainContent.style.transform}, want reset`)
    }
  })

  it('games group survives setArtifacts([])', () => {
    const { sidebar } = mount()
    sidebar.setArtifacts([])
    const rows = sidebar.root.querySelectorAll('[data-game-row]')
    if (rows.length !== 1) {
      throw new Error(`game rows after setArtifacts([]) = ${rows.length}, want 1`)
    }
  })

  it('BUILTIN_GAMES entries carry id, label and icon', () => {
    if (BUILTIN_GAMES.length !== 1) {
      throw new Error(`BUILTIN_GAMES.length = ${BUILTIN_GAMES.length}, want 1`)
    }
    const g = BUILTIN_GAMES[0]
    if (g.id !== 'chess' || g.label !== 'Chinese Chess' || !g.icon.includes('<svg') || !g.icon.includes('帅')) {
      throw new Error(`BUILTIN_GAMES[0] = ${JSON.stringify(g)}, want the chess entry`)
    }
  })
})
