// Speech renderer: the deterministic TTS feed projection (D5). Storage
// invariant — outputs NEVER touch messages, history, or cache keys; they are
// POST /api/tts bodies only.
//
// Two-blade rule (10-27 final ruling):
//   1. fenced code blocks and tables: swallowed whole, zero replacement text;
//   2. everything else (lists included): symbols stripped, text kept — lists
//      are NEVER swallowed, every item survives as spoken text.
//
// Used per contiguous text run (10-31 ruling): qwen3-tts has no
// cross-request context, so the whole run is projected once and POSTed as
// one request — per-sentence requests made prosody drift between sentences.

export function plainForSpeech(text: string): string {
  for (const md of ['**', '__', '~~', '*', '_', '`', '#']) {
    text = text.split(md).join('')
  }
  return text
}

const fenceRe = /^ {0,3}(`{3,}|~{3,})/
const pipeLineRe = /^\s*\|/
const listItemRe = /^(?:- |\* |\d+\. )/

function isBlank(s: string): boolean {
  return s.trim() === ''
}

function stripLine(line: string): string {
  // List markers first: '*' items must lose their marker before the global
  // '*' strip can eat it and leave a stray leading space.
  return plainForSpeech(line.replace(listItemRe, '')).trimStart()
}

// renderForSpeech projects a full markdown text onto spoken prose. Blank-run
// collapsing keeps one blank line around block boundaries so spoken runs
// retain paragraph separation.
export function renderForSpeech(text: string): string {
  const lines = text.split('\n')
  const out: string[] = []
  let i = 0
  while (i < lines.length) {
    const line = lines[i]
    const fence = fenceRe.exec(line)
    if (fence) {
      // Swallow opener..closer (or EOF when unclosed) including both fences.
      const marker = fence[1][0]
      let j = i + 1
      while (j < lines.length) {
        const closer = new RegExp(`^ {0,3}\\${marker}{3,}`).exec(lines[j])
        if (closer) {
          j++
          break
        }
        j++
      }
      i = j
      continue
    }
    if (pipeLineRe.test(line)) {
      // Maximal pipe-line run; >=2 lines is a table (swallowed), a lone
      // pipe line is prose (kept).
      let j = i
      while (j < lines.length && pipeLineRe.test(lines[j])) j++
      if (j - i >= 2) {
        i = j
        continue
      }
    }
    out.push(stripLine(line))
    i++
  }
  // Collapse runs of >=2 blank lines to one (interior runs — a trailing run
  // is left verbatim so later pushes cannot rewrite the projection).
  const collapsed: string[] = []
  let blankRun = 0
  for (const l of out) {
    if (isBlank(l)) {
      blankRun++
      continue
    }
    if (blankRun >= 1) collapsed.push('')
    blankRun = 0
    collapsed.push(l)
  }
  for (let k = 0; k < blankRun; k++) collapsed.push('')
  return collapsed.join('\n')
}
