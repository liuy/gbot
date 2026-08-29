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
    // 红方记谱从红方视角数列（colFrom=x+1）：x=7 是第 8 线，马八进七/马八进九
    { name: 'red horse to 7', pen: START_PEN, side: 'RED', from: [7, 9], to: [6, 7], want: '马八进七' },
    { name: 'red horse to 9', pen: START_PEN, side: 'RED', from: [7, 9], to: [8, 7], want: '马八进九' },
    { name: 'red advisor advance', pen: START_PEN, side: 'RED', from: [3, 9], to: [4, 8], want: '士四进五' },
    { name: 'red elephant advance', pen: START_PEN, side: 'RED', from: [2, 9], to: [4, 7], want: '相三进五' },
    { name: 'black cannon to 5', pen: START_PEN, side: 'BLACK', from: [1, 2], to: [4, 2], want: '砲8平5' },
    { name: 'black chariot advance 2', pen: START_PEN, side: 'BLACK', from: [8, 0], to: [8, 2], want: '車1进2' },
    { name: 'black advisor advance', pen: START_PEN, side: 'BLACK', from: [3, 0], to: [4, 1], want: '仕6进5' },
    // 标准记谱：叠子前缀（前/中/后）取代列号——「前兵进一」而非「前兵五进一」。
    // 两王分属不同列（3k 与 5K），避免照面规则把兵的着法全部过滤掉。
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
    const riskyLine = lines[evalIdx + 3]
    if (!riskyLine.includes('砲8进7(吃马·会被车吃)')) throw new Error('cannon exchange not evaluated: ' + riskyLine)
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
    // 红方行棋：左炮（x=1,y=7）全谱 12 个落点、左车（x=0,y=9）被自家兵封仅 2 个
    clickCell(1, 7)
    expect(dotCount()).toBe(12)
    clickCell(0, 9)
    expect(dotCount()).toBe(2)
    // 点黑子不产生落点（未轮到黑方手动行棋）
    clickCell(4, 0)
    expect(dotCount()).toBe(0)
  })

  it('hot path: red cannon move POSTs observation, black reply lands, save written', async () => {
    const ChessGame = evalGamePage()
    const f = stubFetch()
    clickCell(1, 7)
    clickCell(4, 7)
    // 红步同步生效：炮已从 (1,7) 移到 (4,7)
    expect(pieceAt(1, 7)).toBe(null)
    expect(pieceAt(4, 7)?.textContent).toBe('炮')
    // POST 载荷：prompt 原样、state 含上一步与黑方 legal 清单（馬8进7 必在其列）
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
    // 清单与页面在同一局面下生成的黑方记谱逐一相等（馬8进7 必在其列）
    const list = stateLines.slice(markerIdx + 1).filter((l) => l !== '')
    expect(list).toEqual(ChessGame.legalNotations('BLACK'))
    expect(list).toContain('馬8进7')
    // 挂起态：禁点 + pending 气泡 + 点击无效
    expect(boardWrap().classList.contains('no-click')).toBe(true)
    expect(document.querySelector('.bubble.pending')?.textContent).toContain('思考中')
    clickCell(0, 9)
    expect(f.posts.length).toBe(1)
    expect(dotCount()).toBe(0)
    // 应答 {move,note}（Go handler 真实形状）：黑马 (1,0)→(2,2)，存档精确写入
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
    // 当前轮红：红马 (7,9) 两个落点；点黑马无落点
    clickCell(7, 9)
    expect(dotCount()).toBe(2)
    clickCell(2, 2)
    expect(dotCount()).toBe(0)
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
    // 帅五平六：红帅旁移被自家仕占据，重建清单里不存在 → 重放中途失败
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
    // 重试：错误气泡移除、再次 POST 且载荷逐字符一致（观察是位置的纯函数）
    ;(err.querySelector('.retry-btn') as HTMLElement).click()
    expect(f.posts.length).toBe(2)
    expect(f.posts[1].body).toBe(f.posts[0].body)
    expect(document.querySelector('.bubble.error')).toBe(null)
    // 200 后恢复：落子、写档、解锁；note 为空串 → 不出气泡
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
