import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { readFileSync } from 'fs'
import { resolve } from 'path'

// Single source of truth: the lib and the game code are extracted from the
// embedded template itself — no copied lib file can drift.
const html = readFileSync(resolve(__dirname, '../../../../pkg/connector/wui/games/chess.html'), 'utf-8')

function extractScript(id: string): string {
  const m = html.match(new RegExp(`<script id="${id}">\\n([\\s\\S]*?)\\n</script>`))
  if (!m) throw new Error(`script #${id} not found in chess.html`)
  return m[1]
}

// zh-chess ships untyped (runtime-evaluated min.js); these structural types
// cover exactly the surface the page and the tests touch.
interface LibPiece {
  x: number
  y: number
  name: string
  side: string
}

interface LibMove {
  from: { x: number; y: number }
  to: { x: number; y: number }
  captured: LibPiece | null
}

interface LibGame {
  gameStart(side: string): void
  setPenCodeList(pen: string): void
  getPiecesOfSide(side: string): LibPiece[]
  generateLegalMoves(side: string): LibMove[]
  update(from: { x: number; y: number }, to: { x: number; y: number }, side: string, post: boolean): unknown
  currentLivePieceList: LibPiece[]
}

interface ZhChessModule {
  default: new (opts: Record<string, unknown>) => LibGame
  gen_PEN_Str(list: LibPiece[], side: string): string
}

interface ChessGameNS {
  renderObservation(): string
  moveToNotation(move: LibMove, side: string, pieces: LibPiece[]): string
  legalNotations(side: string): string[]
  newGame(): void
}

function loadZhChess(): ZhChessModule {
  return new Function(extractScript('zh-chess-lib') + ';return ZhChess')() as ZhChessModule
}

// newGame instantiates a headless ZhChess. gameStart MUST come before any
// setPenCodeList: the constructor leaves gameState='INIT' (update/moves all
// rejected) and gameStart's initPiece resets the board.
function newGame(Zh: ZhChessModule, pen?: string): LibGame {
  const g = new Zh.default({})
  g.gameStart('RED')
  if (pen) g.setPenCodeList(pen)
  return g
}

const START_PEN = 'rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w'

// evalGamePage mounts the real page in jsdom and returns its ChessGame
// namespace with the live closure game inside.
function evalGamePage(): ChessGameNS {
  document.body.innerHTML = html.slice(html.indexOf('<body>') + 6, html.indexOf('</body>'))
  const w = window as unknown as { ZhChess: ZhChessModule; ChessGame: ChessGameNS }
  w.ZhChess = loadZhChess()
  window.eval(extractScript('game-code'))
  return w.ChessGame
}

function notationOf(ChessGame: ChessGameNS, g: LibGame, side: string, from: [number, number], to: [number, number]): string {
  const pieces = g.getPiecesOfSide(side)
  const mv = g.generateLegalMoves(side).find(
    (m) => m.from.x === from[0] && m.from.y === from[1] && m.to.x === to[0] && m.to.y === to[1],
  )
  if (!mv) throw new Error(`move ${JSON.stringify({ from, to, side })} not legal in fixture`)
  return ChessGame.moveToNotation(mv, side, pieces)
}

// LCG keeps the random games reproducible run-to-run.
function lcg(seed: number): () => number {
  let s = seed >>> 0
  return () => {
    s = (s * 1103515245 + 12345) % 2147483648
    return s / 2147483648
  }
}

// randomGame plays one full game of uniformly random legal moves, asserting
// the token-uniqueness property at every position, and returns the token
// sequence plus the final board's PEN string.
function randomGame(
  ChessGame: ChessGameNS,
  Zh: ZhChessModule,
  rand: () => number,
  maxPlies = 200,
): { tokens: string[]; endPen: string } {
  const g = newGame(Zh)
  const tokens: string[] = []
  let side = 'RED'
  for (let ply = 0; ply < maxPlies; ply++) {
    const pieces = g.getPiecesOfSide(side)
    const legal = g.generateLegalMoves(side)
    if (legal.length === 0) break
    const toks = legal.map((m) => ChessGame.moveToNotation(m, side, pieces))
    if (new Set(toks).size !== toks.length) {
      throw new Error(`duplicate notation tokens at ply ${ply}: ${toks.join(',')}`)
    }
    const mv = legal[Math.floor(rand() * legal.length)]
    tokens.push(ChessGame.moveToNotation(mv, side, pieces))
    g.update({ x: mv.from.x, y: mv.from.y }, { x: mv.to.x, y: mv.to.y }, side, true)
    side = side === 'RED' ? 'BLACK' : 'RED'
  }
  return { tokens, endPen: Zh.gen_PEN_Str(g.currentLivePieceList, 'RED') }
}

describe('chess notation generator', () => {
  const Zh = loadZhChess()
  // evaluate the page once so the generator under test is the shipped code
  const ChessGame = evalGamePage()

  const cases: { name: string; pen: string; side: string; from: [number, number]; to: [number, number]; want: string }[] = [
    { name: 'red cannon central', pen: START_PEN, side: 'RED', from: [1, 7], to: [4, 7], want: '炮二平五' },
    // Red notation counts files from red's view (colFrom=x+1): x=7 is file 8.
    { name: 'red horse to 7', pen: START_PEN, side: 'RED', from: [7, 9], to: [6, 7], want: '马八进七' },
    { name: 'red horse to 9', pen: START_PEN, side: 'RED', from: [7, 9], to: [8, 7], want: '马八进九' },
    { name: 'red advisor advance', pen: START_PEN, side: 'RED', from: [3, 9], to: [4, 8], want: '士四进五' },
    { name: 'red elephant advance', pen: START_PEN, side: 'RED', from: [2, 9], to: [4, 7], want: '相三进五' },
    { name: 'black cannon to 5', pen: START_PEN, side: 'BLACK', from: [1, 2], to: [4, 2], want: '砲8平5' },
    { name: 'black chariot advance 2', pen: START_PEN, side: 'BLACK', from: [8, 0], to: [8, 2], want: '車1进2' },
    { name: 'black advisor advance', pen: START_PEN, side: 'BLACK', from: [3, 0], to: [4, 1], want: '仕6进5' },
    // Stacked pieces replace the file number with a rank prefix.
    // Kings on different files so the facing rule does not filter the pawns.
    // two pawns on one file: positional prefixes (red: smaller y is front)
    { name: 'front of two pawns', pen: '3k5/9/9/9/4P4/9/9/9/4P4/5K3 w', side: 'RED', from: [4, 4], to: [4, 3], want: '前兵进一' },
    { name: 'rear of two pawns', pen: '3k5/9/9/9/4P4/9/9/9/4P4/5K3 w', side: 'RED', from: [4, 8], to: [4, 7], want: '后兵进一' },
    // three pawns on one file (all crossed the river, sideways legal): 前/中/后
    { name: 'front of three pawns', pen: '3k5/9/4P4/4P4/4P4/9/9/9/9/5K3 w', side: 'RED', from: [4, 2], to: [4, 1], want: '前兵进一' },
    { name: 'middle of three pawns', pen: '3k5/9/4P4/4P4/4P4/9/9/9/9/5K3 w', side: 'RED', from: [4, 3], to: [3, 3], want: '中兵平四' },
    { name: 'rear of three pawns', pen: '3k5/9/4P4/4P4/4P4/9/9/9/9/5K3 w', side: 'RED', from: [4, 4], to: [3, 4], want: '后兵平四' },
  ]

  for (const c of cases) {
    it(c.name, () => {
      const g = newGame(Zh, c.pen)
      expect(notationOf(ChessGame, g, c.side, c.from, c.to)).toBe(c.want)
    })
  }
})

describe('chess token uniqueness', () => {
  const Zh = loadZhChess()
  const ChessGame = evalGamePage()

  const FIXED_POSITIONS = [
    START_PEN,
    '2bakab2/9/1cn4cn/9/9/9/9/1CNC4C/9/2BAKAB2 w',
    '3k5/9/9/9/9/9/9/9/9/3K1C3 w',
    'rnbakabnr/9/1c5c1/p1p1p1p1p/9/2N6/9/2N6/9/R1BAKAB1R w',
  ]

  it('every legal-move token set is duplicate-free in fixed positions, both sides', () => {
    for (const pen of FIXED_POSITIONS) {
      const g = newGame(Zh, pen)
      for (const side of ['RED', 'BLACK']) {
        const pieces = g.getPiecesOfSide(side)
        const toks = g.generateLegalMoves(side).map((m) => ChessGame.moveToNotation(m, side, pieces))
        if (new Set(toks).size !== toks.length) {
          throw new Error(`duplicates in ${pen} ${side}: ${toks.join(',')}`)
        }
      }
    }
  })

  it('random games stay duplicate-free across 50 seeded runs', () => {
    for (let seed = 1; seed <= 50; seed++) {
      const played = randomGame(ChessGame, Zh, lcg(seed))
      if (played.tokens.length === 0) {
        throw new Error(`seed ${seed} produced no moves`)
      }
    }
  })
})

describe('chess replay equivalence', () => {
  const Zh = loadZhChess()
  const ChessGame = evalGamePage()

  it('replaying recorded tokens reproduces the final board exactly', () => {
    for (let seed = 101; seed <= 104; seed++) {
      const { tokens, endPen } = randomGame(ChessGame, Zh, lcg(seed))
      const g = newGame(Zh)
      let side = 'RED'
      for (const token of tokens) {
        const pieces = g.getPiecesOfSide(side)
        const mv = g
          .generateLegalMoves(side)
          .find((m) => ChessGame.moveToNotation(m, side, pieces) === token)
        if (!mv) {
          throw new Error(`seed ${seed}: token ${token} (side ${side}) not reproducible`)
        }
        g.update({ x: mv.from.x, y: mv.from.y }, { x: mv.to.x, y: mv.to.y }, side, true)
        side = side === 'RED' ? 'BLACK' : 'RED'
      }
      expect(Zh.gen_PEN_Str(g.currentLivePieceList, 'RED')).toBe(endPen)
    }
  })
})

describe('chess renderObservation', () => {
  const saveKey = () => 'chess-save:' + location.pathname

  beforeEach(() => {
    localStorage.clear()
  })

  it('cold start renders round 1 for red perspective with legal-move list', () => {
    const ChessGame = evalGamePage()
    const obs = ChessGame.renderObservation() as string
    const lines = obs.split('\n')
    expect(lines[0]).toBe('对局记录:')
    expect(lines[1]).toBe('──────────')
    expect(lines[2]).toBe('轮到黑方·第 2 手')
    // ASCII board: header row, y0 black back rank lowercase, y9 red uppercase.
    expect(lines[3]).toBe('     x0 x1 x2 x3 x4 x5 x6 x7 x8')
    expect(lines[4]).toBe('y0  r  n  b  a  k  a  b  n  r')
    expect(lines[13]).toBe('y9  R  N  B  A  K  A  B  N  R')
    expect(lines[14].startsWith('你的棋子: ')).toBe(true)
    expect(lines[14]).toContain('車(0,0)')
    expect(lines[14]).toContain('将(4,0)')
    expect(lines[15].startsWith('对方棋子: ')).toBe(true)
    expect(lines[15]).toContain('车(0,9)')
    expect(lines[15]).toContain('帅(4,9)')
    const penIdx = lines.findIndex((l) => l.startsWith('FEN: rnbakabnr'))
    if (penIdx < 0) throw new Error('PEN line missing')
    // Lookahead eval locks the full chain: the vertical cannon capture is
    // engine-legal, it eats the horse, and the rook recaptures — the risky
    // line must carry both facts. Categorization puts captures first.
    const evalIdx = lines.findIndex((l) => l === '步法评估:')
    if (evalIdx < 0) throw new Error('步法评估 header missing')
    if (!lines[evalIdx + 1].startsWith('- 安全·有吃子: ')) throw new Error('captures line missing: ' + lines[evalIdx + 1])
    if (!lines[evalIdx + 2].startsWith('- 安全: ')) throw new Error('safe line missing')
    if (!lines[evalIdx + 3].startsWith('- 有代价: ')) throw new Error('risky line missing')
    const capsLine = lines[evalIdx + 1]
    // Even trade (砲 4.5 for 馬 4.5) is materially safe — the exchange note
    // replaces the old 会被吃 warning.
    if (!capsLine.includes('砲8进7(吃马·车反吃,净得0)')) throw new Error('cannon exchange not evaluated: ' + capsLine)
    if (!lines[evalIdx + 3].startsWith('- 有代价: 无')) throw new Error('even trade must leave 有代价 empty: ' + lines[evalIdx + 3])
    if (!lines[evalIdx + 2].includes('馬8进7')) throw new Error('safe move not listed')
    // Positional metrics: symmetric opening — equal material, nothing
    // developed, nothing across the river, equal mobility.
    // Mate sweep sanity on the opening: no 绝杀/会致杀 tags anywhere.
    for (let i = evalIdx; i < evalIdx + 4; i++) {
      if (lines[i].includes('绝杀') || lines[i].includes('会致杀'))
        throw new Error('phantom mate tag in opening: ' + lines[i])
    }
    const sit = lines[evalIdx + 4]
    if (!sit.startsWith('局面要素: ')) throw new Error('situation line missing: ' + lines[evalIdx + 4])
    if (!sit.includes('子力 红49:黑49')) throw new Error('material sum wrong: ' + sit)
    if (!sit.includes('出动大子 红0:黑0')) throw new Error('opening development must be zero: ' + sit)
    if (!sit.includes('过河 红0:黑0')) throw new Error('opening river must be zero: ' + sit)
    if (!/活动度 红(4[0-9]):黑\1/.test(sit)) throw new Error('opening mobility should match: ' + sit)
    if (obs.includes('上一步：')) {
      throw new Error('cold start must not carry an 上一步 line')
    }
    if (obs.includes('附言:')) {
      throw new Error('cold start must not carry a 附言 line')
    }
    const markerIdx = lines.indexOf('legal-moves:')
    if (markerIdx < 0) throw new Error('legal-moves: marker missing')
    const list = lines.slice(markerIdx + 1).filter((l) => l !== '')
    const want = ChessGame.legalNotations('BLACK')
    expect(list).toEqual(want)
  })

  it('history lines of an earlier observation are a stable prefix of a later one', () => {
    // Two black replies recorded as a finished save; the page restores them.
    const Zh = loadZhChess()
    const probe = evalGamePage()
    const seedTok = (from: [number, number], to: [number, number], side: string): string => {
      const g = newGame(Zh, START_PEN)
      return notationOf(probe, g, side, from, to)
    }
    const t1 = seedTok([1, 7], [4, 7], 'RED')
    const t2 = seedTok([1, 0], [2, 2], 'BLACK')
    localStorage.setItem(saveKey(), JSON.stringify({ v: 1, moves: [t1, t2] }))

    const twoMoves = evalGamePage()
    const obs = twoMoves.renderObservation() as string
    const lines = obs.split('\n')
    expect(lines[0]).toBe('对局记录:')
    expect(lines[1]).toBe(t1)
    expect(lines[2]).toBe(t2)
    expect(lines[3]).toBe('──────────')
    expect(lines[4]).toBe('轮到黑方·第 4 手')
    expect(lines).toContain('上一步：' + t2)

    // Cold-start observation (empty history): everything up to its divider is
    // shared by the two-move observation — the divider is the FIRST divergence.
    localStorage.clear()
    const cold = evalGamePage()
    const obs0 = cold.renderObservation() as string
    const cut = obs0.indexOf('─')
    if (cut < 0) throw new Error('divider missing in cold observation')
    if (obs.slice(0, cut) !== obs0.slice(0, cut)) {
      throw new Error(`observation history diverged before the divider:\nA=${obs0.slice(0, cut)}\nB=${obs.slice(0, cut)}`)
    }
    if (obs[cut] === '─') {
      throw new Error('later observation must append history lines before its own divider')
    }
  })
})

// ---- page integration in jsdom (DOM / localStorage / fetch payload) ----
// postTurn is async but only awaits the stubbed fetch/json promises, so a pure
// microtask flush after answering a captured POST is deterministic — no timers.
async function flushAsync(rounds = 16): Promise<void> {
  for (let i = 0; i < rounds; i++) await Promise.resolve()
}

interface ResponseLike {
  ok: boolean
  status: number
  json?: () => Promise<unknown>
  // NDJSON stream: each string becomes one decoded chunk.
  lines?: string[]
}

// Builds a ResponseLike whose body streams the given event objects as
// NDJSON lines — one chunk per event, so the page's cross-chunk line
// reassembly is exercised (chunks rarely align with line boundaries).
function streamReply(...events: object[]): ResponseLike {
  return { ok: true, status: 200, lines: events.map((e) => JSON.stringify(e) + '\n') }
}

function readerFor(lines: string[]) {
  const enc = new TextEncoder()
  let i = 0
  return {
    read: async () => {
      if (i >= lines.length) return { done: true as const, value: undefined }
      return { done: false as const, value: enc.encode(lines[i++]) }
    },
  }
}

function stubFetch(): { posts: { url: string; body: string }[]; next(v: ResponseLike): void } {
  const posts: { url: string; body: string }[] = []
  const resolvers: ((v: ResponseLike) => void)[] = []
  vi.stubGlobal('fetch', (url: unknown, init: { body?: unknown }) => {
    posts.push({ url: String(url), body: String(init && init.body) })
    return new Promise<ResponseLike>((resolve) =>
      resolvers.push((v) => resolve(v.lines ? { ...v, body: { getReader: () => readerFor(v.lines!) } } : v)))
  })
  return {
    posts,
    next(v) {
      const r = resolvers.shift()
      if (!r) throw new Error('no in-flight fetch to answer')
      r(v)
    },
  }
}

function cellEl(c: number, r: number): Element {
  const el = document.querySelector(`#board rect.cell[data-r="${r}"][data-c="${c}"]`)
  if (!el) throw new Error(`interactive cell (c=${c},r=${r}) not rendered`)
  return el
}

function clickCell(c: number, r: number): void {
  cellEl(c, r).dispatchEvent(new MouseEvent('click', { bubbles: true }))
}

function pieceAt(x: number, y: number): Element | null {
  return document.querySelector(`#board g.piece[data-r="${y}"][data-c="${x}"]`)
}

function dotCount(): number {
  return document.querySelectorAll('#board circle.dot').length
}

function boardWrap(): Element {
  const el = document.getElementById('boardWrap')
  if (!el) throw new Error('boardWrap missing')
  return el
}

describe('chess page integration', () => {
  const saveKey = () => 'chess-save:' + location.pathname

  beforeEach(() => {
    localStorage.clear()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('cold start: 32 pieces, red to move, no save, exact legal-target dots', () => {
    evalGamePage()
    expect(document.querySelectorAll('#board g.piece').length).toBe(32)
    expect(boardWrap().classList.contains('no-click')).toBe(false)
    expect(localStorage.getItem(saveKey())).toBe(null)
    // Red to move: cannon (x=1,y=7) has 12 targets; chariot (x=0,y=9) is blocked by its own pawn (2).
    clickCell(1, 7)
    expect(dotCount()).toBe(12)
    clickCell(0, 9)
    expect(dotCount()).toBe(2)
    // Clicking a black piece shows no targets (black never moves by hand).
    clickCell(4, 0)
    expect(dotCount()).toBe(0)
  })

  it('hot path: red cannon move POSTs observation, black reply lands, save written', async () => {
    const ChessGame = evalGamePage()
    const f = stubFetch()
    clickCell(1, 7)
    clickCell(4, 7)
    // The red move landed: cannon moved from (1,7) to (4,7).
    expect(pieceAt(1, 7)).toBe(null)
    expect(pieceAt(4, 7)?.textContent).toBe('炮')
    // POST payload: prompt verbatim, state carries the last move and the black legal list (馬8进7 included).
    expect(f.posts.length).toBe(1)
    expect(f.posts[0].url).toBe(location.pathname)
    const body = JSON.parse(f.posts[0].body) as { prompt: string; state: string }
    const promptConst = html.match(/const OBSERVE_PROMPT = `([\s\S]*?)`;/)
    if (!promptConst) throw new Error('OBSERVE_PROMPT not found in template')
    expect(body.prompt).toBe(promptConst[1])
    const stateLines = body.state.split('\n')
    expect(stateLines).toContain('上一步：炮二平五')
    const markerIdx = stateLines.indexOf('legal-moves:')
    if (markerIdx < 0) throw new Error('legal-moves marker missing in POST state')
    // The list equals the page-generated notations for the same position (馬8进7 included).
    const list = stateLines.slice(markerIdx + 1).filter((l) => l !== '')
    expect(list).toEqual(ChessGame.legalNotations('BLACK'))
    expect(list).toContain('馬8进7')
    // Pending state: clicks locked, pending bubble, clicks inert.
    expect(boardWrap().classList.contains('no-click')).toBe(true)
    expect(document.querySelector('.bubble.pending')?.textContent).toContain('思考中')
    clickCell(0, 9)
    expect(f.posts.length).toBe(1)
    expect(dotCount()).toBe(0)
    // Reply {move,note} (real Go handler shape): black horse (1,0)->(2,2), exact save written.
    f.next(streamReply({ type: 'note', text: '稳一手' }, { type: 'final', move: '馬8进7', note: '稳一手' }))
    await flushAsync()
    expect(pieceAt(1, 0)).toBe(null)
    expect(pieceAt(2, 2)?.textContent).toBe('馬')
    expect(localStorage.getItem(saveKey())).toBe('{"v":2,"moves":["炮二平五","馬8进7"],"notes":["稳一手"]}')
    expect(boardWrap().classList.contains('no-click')).toBe(false)
    expect(document.querySelector('.bubble.pending')).toBe(null)
    const bubbles = [...document.querySelectorAll('.bubble')]
    expect(bubbles.length).toBe(1)
    expect(bubbles[0].textContent).toContain('稳一手')
  })

  it('restore: valid save replays to the mid-game position with red to move', () => {
    localStorage.setItem(saveKey(), '{"v":1,"moves":["炮二平五","馬8进7"]}')
    evalGamePage()
    expect(pieceAt(1, 0)).toBe(null)
    expect(pieceAt(2, 2)?.textContent).toBe('馬')
    expect(pieceAt(4, 7)?.textContent).toBe('炮')
    expect(pieceAt(1, 7)).toBe(null)
    expect(boardWrap().classList.contains('no-click')).toBe(false)
    // Red's turn: red horse (7,9) has two targets; clicking the black horse shows none.
    clickCell(7, 9)
    expect(dotCount()).toBe(2)
    clickCell(2, 2)
    expect(dotCount()).toBe(0)
  })
  it('see: defended cannon reads 有根 with net, rescue list kept, gameState restored', async () => {
    // Red chariot attacks the black cannon on file x=1, black chariot sits
    // behind it: recapture costs red 9 for 4.5 — the engine must report the
    // exchange account, not an urgency verdict, and keep the rescue list.
    localStorage.setItem(saveKey(), '{"v":2,"moves":[],"notes":[]}')
    const game = evalGamePage()
    ;(game as unknown as { _testSetPen: (pen: string) => void })._testSetPen(
      '1r1k5/9/1c7/9/9/9/9/1R7/9/4K4 b')
    const obs = (game as unknown as { renderObservation: () => string }).renderObservation()
    if (!obs.includes('砲(1,2)被车捉(有根:車反吃,红若吃则净亏4.5)')) {
      throw new Error('root=' + obs.split('\n').filter(l => l.includes('捉') || l.includes('受威胁') || l.includes('安全') || l.includes('代价')).join(' || '))
    }
    if (!obs.includes('可救着法:')) throw new Error('rescue list must stay (fleeing is the LLM call)')
    const st = (game as unknown as { _testGameState: () => string })._testGameState?.()
    if (st && st !== 'START') throw new Error('gameState leaked: ' + st)
  })

  it('victory by mate: the click-through shows the banner, not a crash', async () => {
    // Regression: a stale `who` reference inside bubble() only fired on the
    // mate path (the sole click-reachable bubble call), freezing the board
    // instead of declaring victory.
    localStorage.setItem(saveKey(), '{"v":2,"moves":[],"notes":[]}')
    const game = evalGamePage()
    // Bare black king; three red rooks pre-cover every palace square on
    // files 3 and 5. The mating rook slides onto the king's file — full
    // coverage, no capture, block or escape.
    ;(game as unknown as { _testSetPen: (pen: string) => void })._testSetPen(
      '4k4/9/9/2R6/3R1R3/9/9/9/9/3K5 w')
    clickCell(2, 3)
    clickCell(4, 3)
    const banner = document.getElementById('resultBanner')
    if (!banner || banner.style.display !== 'flex') {
      throw new Error('victory banner must show after mate: ' + String(banner && banner.style.display))
    }
    if (!document.getElementById('resultText')?.textContent?.includes('赢')) {
      throw new Error('victory text missing')
    }
    if (document.querySelector('.bubble.error')) throw new Error('no error bubble allowed on the mate path')
  })

  it('fuzz: every red checking move from a midgame-ish board survives the build', async () => {
    localStorage.setItem(saveKey(), '{"v":2,"moves":[],"notes":[]}')
    const game = evalGamePage()
    ;(game as unknown as { _testSetPen: (pen: string) => void })._testSetPen(
      '1r1kab3/9/1cn4c1/p1p3p2/9/9/9/4R4/9/2BAKAB2 w')
    const g = (game as unknown as { _game: () => { update: Function; generateLegalMoves: Function; gameState: string; setPenCodeList: Function } })
    const base = g._game ? g._game() : null
    if (!base) throw new Error('need _game hook')
    const pen0 = (window as unknown as { ZhChess: { gen_PEN_Str: Function } }).ZhChess
      .gen_PEN_Str(base.currentLivePieceList, 'RED')
    let checked = 0
    for (const r of base.generateLegalMoves('RED')) {
      base.update(r.from, r.to, 'RED', true)
      // black king attacked after this red move = a check for us to render
      const blackKing = base.currentLivePieceList.find((p: { name: string }) => p.name === '将')
      const attacked = blackKing && base.generateLegalMoves('RED')
        .some((m: { captured?: { x: number; y: number } }) => m.captured && m.captured.x === blackKing.x && m.captured.y === blackKing.y)
      if (attacked) {
        checked++
        try {
          ;(game as unknown as { renderObservation: () => string }).renderObservation()
        } catch (e) {
          // restore for the next iteration before failing
          base.setPenCodeList(pen0)
          throw new Error('crash under check after red move: ' + String(e))
        }
      }
      base.setPenCodeList(pen0)
    }
    if (checked === 0) throw new Error('fuzz found no checking positions — test is vacuous')
  })

  it('see: a checked general does not crash the observation build', async () => {
    // Red chariot checks the bare black general. The threat scan used to
    // simulate the king capture and then generate moves for a kingless
    // side, throwing before the POST ever left the page.
    localStorage.setItem(saveKey(), '{"v":2,"moves":[],"notes":[]}')
    const game = evalGamePage()
    ;(game as unknown as { _testSetPen: (pen: string) => void })._testSetPen(
      '4k4/9/9/9/9/9/9/4R4/9/3K5 b')
    let obs = ''
    try {
      obs = (game as unknown as { renderObservation: () => string }).renderObservation()
    } catch (e) {
      throw new Error('renderObservation threw under check: ' + String(e))
    }
    if (!obs.includes('将(4,0)被车捉')) throw new Error('threat on the general must still be listed')
    const st = (game as unknown as { _testGameState: () => string })._testGameState?.()
    if (st && st !== 'START') throw new Error('gameState leaked: ' + st)
  })

  it('mate sweep: horse mate landing on file 2 is tagged 会致杀', async () => {
    // The old 3-5 palace filter skipped mating replies that LAND on x2/x6 —
    // horse checks on a central king land there, so real blunders passed
    // untagged. Red threatens 马(3,3)->(2,1)# after any quiet black move.
    localStorage.setItem(saveKey(), '{"v":2,"moves":[],"notes":[]}')
    const game = evalGamePage()
    ;(game as unknown as { _testSetPen: (pen: string) => void })._testSetPen(
      '4k4/7R1/9/3N5/5R3/9/p2R5/9/9/3K5 b')
    const obs = (game as unknown as { renderObservation: () => string }).renderObservation()
    const risky = obs.split('\n').find(l => l.startsWith('- 有代价'))
    if (!risky) throw new Error('eval lines missing')
    if (!risky.includes('卒9进1(会致杀')) {
      throw new Error('non-palace mating reply must be tagged: ' + risky)
    }
  })

  it('mate net: an existing one-move mate triggers the warning', async () => {
    // The horse-mate position: red already has 马(3,3)->(2,1)# available if
    // given the move — the null-move threat fires the alarm.
    localStorage.setItem(saveKey(), '{"v":2,"moves":[],"notes":[]}')
    const game = evalGamePage()
    ;(game as unknown as { _testSetPen: (pen: string) => void })._testSetPen(
      '4k4/7R1/9/3N5/5R3/9/p2R5/9/9/3K5 b')
    const obs = (game as unknown as { renderObservation: () => string }).renderObservation()
    if (!obs.includes('杀网警报: 红现有一步绝杀手段')) {
      throw new Error('standing mate must trigger the alarm: ' +
        obs.split('\n').filter(l => l.startsWith('- ')).join(' | '))
    }
  })

  it('mate net: a forced two-move ladder (双车错) is reported', async () => {
    // 双车错: the y1 rook guards the sacrifice square; after the x4 capture-
    // check both king retreats get mated. No standing mate (the king has real
    // escapes now), so only the forced-net branch can fire.
    localStorage.setItem(saveKey(), '{"v":2,"moves":[],"notes":[]}')
    const game = evalGamePage()
    ;(game as unknown as { _testSetPen: (pen: string) => void })._testSetPen(
      '4k4/R3a4/9/9/9/9/p8/4R4/9/8K1 b')
    const obs = (game as unknown as { renderObservation: () => string }).renderObservation()
    const line = obs.split('\n').find(l => l.startsWith('杀网警报'))
    if (!line || !line.includes('后两步内强制绝杀') || !line.includes('车')) {
      throw new Error('forced ladder must be reported: ' + String(line) + ' | eval=' +
        obs.split('\n').filter(l => l.startsWith('- ')).join(' | '))
    }
    if (line && line.includes('现有一步绝杀')) {
      throw new Error('king escapes survive — standing-mate branch must stay off')
    }
  })

  it('mate net: quiet opening position raises no alarm', async () => {
    localStorage.setItem(saveKey(), '{"v":2,"moves":[],"notes":[]}')
    const game = evalGamePage()
    const t0 = performance.now()
    const obs = (game as unknown as { renderObservation: () => string }).renderObservation()
    const ms = Math.round(performance.now() - t0)
    if (obs.includes('杀网警报')) throw new Error('false positive on the opening board')
    // The full (ii) scan runs here (40 preparations, each refuted early) —
    // keep the observation build comfortably interactive.
    if (ms > 2000) throw new Error('observation build too slow: ' + ms + 'ms')
  })

  it('dynamics: five quiet setup moves report zero accumulation', async () => {
    localStorage.setItem(saveKey(), JSON.stringify({ v: 2,
      moves: ['炮二平五', '馬8进7', '马二进三', '車9平8', '车一平二', '砲8进4',
              '兵三进一', '卒7进1', '兵七进一', '象3进5'],
      notes: ['', '', '', '', ''] }))
    const game = evalGamePage()
    const obs = (game as unknown as { renderObservation: () => string }).renderObservation()
    if (!obs.includes('近期动态:')) throw new Error('dynamics line missing after 5 black moves')
    // One cannon crossed (砲8进4 -> y6); no captures, threat count may rise
    // with development — assert the exact accumulated facts we control.
    if (!obs.includes('吃子0')) throw new Error('no captures in the window')
    if (!obs.includes('过河+1')) throw new Error('exactly one river crossing expected')
  })

  it('exposure: vacating the shield line tags the hanging chariot behind', async () => {
    // The exact production incident: 砲(1,2) shields 車(1,0) from red 車(1,7).
    // Sliding the cannon OFF the file is a discovered attack on our own
    // chariot with no recapture — must land in 有代价, not 安全.
    localStorage.setItem(saveKey(), '{"v":2,"moves":[],"notes":[]}')
    const game = evalGamePage()
    ;(game as unknown as { _testSetPen: (pen: string) => void })._testSetPen(
      '1r1k5/9/1c7/9/9/9/9/1R7/9/4K4 b')
    const obs = (game as unknown as { renderObservation: () => string }).renderObservation()
    const risky = obs.split('\n').find(l => l.startsWith('- 有代价'))
    const safe = obs.split('\n').find(l => l.startsWith('- 安全:'))
    if (!risky || !safe) throw new Error('eval lines missing')
    if (!risky.includes('砲8平9(暴露:車(1,0)被车捉,净亏9)')) {
      throw new Error('vacating move must carry the exposure tag: ' + risky)
    }
    if (!safe.includes('砲8进1(')) throw new Error('staying on the file must remain safe: ' + safe)
  })

  it('exposure: defended piece behind the vacated line stays untagged', async () => {
    // Same geometry plus a horse covering (1,0): the exposure nets to an
    // even trade, so no tag — silence is the contract for neutral facts.
    localStorage.setItem(saveKey(), '{"v":2,"moves":[],"notes":[]}')
    const game = evalGamePage()
    ;(game as unknown as { _testSetPen: (pen: string) => void })._testSetPen(
      '1r1k5/9/1cn6/9/9/9/9/1R7/9/4K4 b')
    const obs = (game as unknown as { renderObservation: () => string }).renderObservation()
    if (obs.includes('暴露:')) throw new Error('defended exposure must stay silent')
    const risky = obs.split('\n').find(l => l.startsWith('- 有代价'))
    if (risky && risky.includes('砲8平9')) throw new Error('no material loss — must not be 有代价')
  })

  it('see: hanging cannon reads 净得 without 有根', async () => {
    localStorage.setItem(saveKey(), '{"v":2,"moves":[],"notes":[]}')
    const game = evalGamePage()
    ;(game as unknown as { _testSetPen: (pen: string) => void })._testSetPen(
      '3k5/9/1c7/9/9/9/9/1R7/9/4K4 b')
    const obs = (game as unknown as { renderObservation: () => string }).renderObservation()
    if (!obs.includes('被车捉(红吃则净得4.5)')) {
      throw new Error('hanging piece must show the real loss')
    }
    if (obs.includes('有根')) throw new Error('no defender here — must not claim 有根')
  })

  it('see: landing on an attacked-but-defended square lands in the safe line', async () => {
    localStorage.setItem(saveKey(), '{"v":2,"moves":[],"notes":[]}')
    const game = evalGamePage()
    ;(game as unknown as { _testSetPen: (pen: string) => void })._testSetPen(
      '1r1k5/9/1c7/9/9/9/9/1R7/9/4K4 b')
    const obs = (game as unknown as { renderObservation: () => string }).renderObservation()
    const safe = obs.split('\n').find(l => l.startsWith('- 安全:'))
    const risky = obs.split('\n').find(l => l.startsWith('- 有代价'))
    if (!safe || !risky) throw new Error('eval lines missing')
    // 砲8进2 steps along the attacked file, still covered by our 車 —
    // materially safe, exchange note attached.
    if (!safe.includes('砲8进2(') || !safe.includes('净得4.5')) {
      throw new Error('defended advance should sit in safe with net note: ' + safe)
    }
  })
  it('mate sweep: tags a real mate-in-1, restores gameState, other moves still evaluated', async () => {
    // Black chariot pair mates the bare red king: the tag must appear, the
    // sweep must leave gameState playable, and non-mating moves must not be
    // broken by the OVER state leaking from the simulation.
    localStorage.setItem(saveKey(), '{"v":2,"moves":[],"notes":[]}')
    const game = evalGamePage()
    // Build directly: black 車(4,3)+車(0,0) vs red 帅(4,9) — 車4进6 mates.
    ;(game as unknown as { _testSetPen: (pen: string) => void })._testSetPen?.('4r4/9/9/9/9/9/9/9/r3r4/4K4 b')
    const obs = (game as unknown as { renderObservation: () => string }).renderObservation()
    const lines = obs.split('\n')
    const risky = lines.find((l) => l.startsWith('- 有代价'))
    const caps = lines.find((l) => l.startsWith('- 安全·有吃子'))
    if (!risky || !caps) throw new Error('eval lines missing after mate sweep')
    if (!risky.includes('绝杀!') && !caps.includes('绝杀!')) throw new Error('mate-in-1 not tagged: ' + risky)
    // gameState must be restored: the page object still evaluates other
    // moves and the game code must not believe the game is over.
    const st = (game as unknown as { _testGameState: () => string })._testGameState?.()
    if (st && st !== 'START') throw new Error('gameState leaked OVER: ' + st)
  })
  it('new game: wipes the save and resets to the initial board', () => {
    localStorage.setItem(saveKey(), '{"v":1,"moves":["炮二平五","馬8进7"]}')
    const game = evalGamePage()
    expect(pieceAt(2, 2)?.textContent).toBe('馬') // restored mid-game
    game.newGame()
    // Save gone: a refresh must not resurrect the finished game.
    expect(localStorage.getItem(saveKey())).toBe(null)
    // Initial position back.
    expect(pieceAt(2, 2)).toBe(null)
    expect(pieceAt(0, 0)?.textContent).toBe('車')
    expect(pieceAt(1, 0)?.textContent).toBe('馬')
    expect(pieceAt(1, 7)?.textContent).toBe('炮')
    expect(boardWrap().classList.contains('no-click')).toBe(false)
  })

  const CORRUPTED_SAVES: { name: string; raw: string }[] = [
    { name: 'invalid JSON', raw: 'zzz{' },
    { name: 'v=3', raw: '{"v":3,"moves":["炮二平五","馬8进7"]}' },
    { name: 'odd move count', raw: '{"v":1,"moves":["炮二平五"]}' },
    // 帅五平六: the red king sidestep is occupied by its own advisor, absent from the rebuilt list -> replay fails midway.
    { name: 'move not in rebuilt legal list', raw: '{"v":1,"moves":["帅五平六","馬8进7"]}' },
  ]
  for (const c of CORRUPTED_SAVES) {
    it(`corrupted save (${c.name}) is discarded: fresh board, storage item removed`, () => {
      localStorage.setItem(saveKey(), c.raw)
      evalGamePage()
      expect(document.querySelectorAll('#board g.piece').length).toBe(32)
      expect(pieceAt(1, 0)?.textContent).toBe('馬')
      expect(pieceAt(1, 7)?.textContent).toBe('炮')
      expect(pieceAt(4, 7)).toBe(null)
      expect(localStorage.getItem(saveKey())).toBe(null)
    })
  }

  it('failure: POST 500 keeps board locked with retry; retry re-POSTs identical body; 200 recovers', async () => {
    evalGamePage()
    const f = stubFetch()
    clickCell(1, 7)
    clickCell(4, 7)
    f.next({ ok: false, status: 500 })
    await flushAsync()
    const err = document.querySelector('.bubble.error')
    if (!err) throw new Error('error bubble missing after HTTP 500')
    expect(err.textContent).toContain('HTTP 500')
    if (!err.querySelector('.retry-btn')) throw new Error('retry button missing in error bubble')
    expect(boardWrap().classList.contains('no-click')).toBe(true)
    // Retry: error bubble removed, re-POST with a byte-identical payload (the observation is a pure function of the position).
    ;(err.querySelector('.retry-btn') as HTMLElement).click()
    expect(f.posts.length).toBe(2)
    expect(f.posts[1].body).toBe(f.posts[0].body)
    expect(document.querySelector('.bubble.error')).toBe(null)
    // Recovery on 200: move applied, save written, unlocked; empty note -> no bubble.
    f.next(streamReply({ type: 'final', move: '馬8进7', note: '' }))
    await flushAsync()
    expect(pieceAt(2, 2)?.textContent).toBe('馬')
    expect(localStorage.getItem(saveKey())).toBe('{"v":2,"moves":["炮二平五","馬8进7"],"notes":[""]}')
    expect(boardWrap().classList.contains('no-click')).toBe(false)
    expect(document.querySelectorAll('.bubble').length).toBe(0)
  })
  it('resign: final done=true ends the game with a result banner, no move applied', async () => {
    const f = stubFetch()
    evalGamePage()
    clickCell(1, 7)
    clickCell(4, 7)
    await flushAsync()
    expect(f.posts.length).toBe(1)
    f.next(streamReply(
      { type: 'thinking', text: '大势已去' },
      { type: 'note', text: '技不如人。' },
      { type: 'final', move: '认输', note: '技不如人。', done: true },
    ))
    await flushAsync()
    // No black move landed: the board still awaits a reply that never comes.
    expect(pieceAt(1, 0)?.textContent).toBe('馬')
    expect(document.getElementById('resultBanner').style.display).toBe('flex')
    expect(document.getElementById('resultText').textContent).toBe('对方认输，你赢了')
    // The resign note stays visible in the streaming bubble.
    const bubble = document.querySelector('.bubble.gbot') as HTMLElement
    expect(bubble?.textContent).toContain('技不如人。')
    expect(bubble?.classList.contains('pending')).toBe(false)
  })
})
