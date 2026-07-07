import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'fs'
import { resolve, join, relative } from 'path'

interface Violation {
  file: string
  line: number
  level: string
  pattern: string
  snippet: string
}

interface Pattern {
  name: string
  level: 'P1' | 'P2' | 'P3'
  match: (line: string) => string | null
  exempt?: (lines: string[], idx: number, match: string) => boolean
}

function isComment(line: string): boolean {
  return line.trim().startsWith('//')
}

function isBlank(line: string): boolean {
  return line.trim() === ''
}

const patterns: Pattern[] = [
  {
    name: 'toBeTruthy() without further checks on same variable',
    level: 'P1',
    match: (line) => {
      const m = line.match(/expect\(([^)]+)\)\.toBeTruthy\(\)/)
      return m ? m[0] : null
    },
    exempt: (lines, idx, match) => {
      if (isComment(lines[idx])) return true
      const varMatch = match.match(/expect\(([^)]+)\)\.toBeTruthy\(\)/)
      if (!varMatch) return false
      const v = varMatch[1].trim()
      const baseVar = v.split('.')[0].replace(/[?!]/g, '')
      for (let i = idx + 1; i < lines.length && i <= idx + 8; i++) {
        if (isBlank(lines[i]) || isComment(lines[i])) continue
        if (lines[i].includes(baseVar)) {
          return true
        }
      }
      return false
    },
  },
  {
    name: 'toBeDefined() without further checks on same variable',
    level: 'P1',
    match: (line) => {
      const m = line.match(/expect\(([^)]+)\)\.toBeDefined\(\)/)
      return m ? m[0] : null
    },
    exempt: (lines, idx, match) => {
      if (isComment(lines[idx])) return true
      const varMatch = match.match(/expect\(([^)]+)\)\.toBeDefined\(\)/)
      if (!varMatch) return false
      const v = varMatch[1].trim()
      const baseVar = v.split('.')[0].replace(/[?!]/g, '')
      for (let i = idx + 1; i < lines.length && i <= idx + 8; i++) {
        if (isBlank(lines[i]) || isComment(lines[i])) continue
        if (lines[i].includes(baseVar)) {
          return true
        }
      }
      return false
    },
  },
  {
    name: 'setTimeout-based promise wait (flaky timing)',
    level: 'P2',
    match: (line) => {
      if (isComment(line)) return null
      const m = line.match(/new Promise\(\(?[^)]*\)?\s*=>\s*setTimeout\(/)
      return m ? m[0] : null
    },
  },
  {
    name: 'toContain with literal where toBe would be exact',
    level: 'P3',
    match: (line) => {
      if (isComment(line)) return null
      const m = line.match(/expect\((\w+\.\w+)\)\.toContain\(['"]([^'"]+)['"]\)/)
      if (!m) return null
      const literal = m[2]
      if (literal.includes(' ') || literal.includes('\n') || literal.length > 20) return null
      return m[0]
    },
    exempt: (lines, idx, match) => {
      const complexProps = ['.textContent', '.className', '.innerHTML', '.outerHTML', '.cssText', '.value', '.id']
      for (const p of complexProps) {
        if (match.includes(p)) return true
      }
      return false
    },
  },
  {
    name: 'expect(x.length).toBeGreaterThan(0) without exact count',
    level: 'P3',
    match: (line) => {
      if (isComment(line)) return null
      const m = line.match(/expect\([^)]*\.length\)\.toBeGreaterThan\(0\)/)
      return m ? m[0] : null
    },
  },
  {
    name: 'constant-subject assertion (always true)',
    level: 'P1',
    match: (line) => {
      if (isComment(line)) return null
      const m = line.match(/expect\((true|false|null|undefined|0|1)\)\.(toBe|toEqual)\((true|false|null|undefined|0|1)\)/)
      if (!m) return null
      return m[0]
    },
  },
  {
    name: 'self-comparison (x === x)',
    level: 'P1',
    match: (line) => {
      if (isComment(line)) return null
      const m = line.match(/expect\((\w+)\)\.(toBe|toEqual)\(\1\)/)
      if (!m) return null
      return m[0]
    },
  },
  {
    name: 'vacuous matcher expect.anything() always passes',
    level: 'P2',
    match: (line) => {
      if (isComment(line)) return null
      const m = line.match(/expect\.anything\(\)/)
      return m ? m[0] : null
    },
  },
  {
    name: 'unawaited promise assertion (.resolves/.rejects without await)',
    level: 'P1',
    match: (line) => {
      if (isComment(line)) return null
      const m = line.match(/expect\([^)]+\)\.(resolves|rejects)\./)
      if (!m) return null
      const trimmed = line.trim()
      if (trimmed.startsWith('await ')) return null
      return m[0]
    },
  },
  {
    name: 'expect.hasAssertions() guard only (no real assertions follow)',
    level: 'P2',
    match: (line) => {
      if (isComment(line)) return null
      const m = line.match(/expect\.hasAssertions\(\)/)
      return m ? m[0] : null
    },
  },
]

function findEmptyAsyncTests(lines: string[]): Violation[] {
  const viols: Violation[] = []
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    const m = line.match(/(it|test)\(['"`].*['"`],\s*async\s*\(\)\s*=>\s*\{\s*\}\)/)
    if (m) {
      viols.push({
        file: '',
        line: i + 1,
        level: 'P1',
        pattern: 'empty async test body',
        snippet: line.trim(),
      })
    }
  }
  return viols
}

function findTestFiles(dir: string): string[] {
  const results: string[] = []
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, e.name)
    if (e.isDirectory()) {
      results.push(...findTestFiles(full))
    } else if (e.name.endsWith('.test.ts')) {
      results.push(full)
    }
  }
  return results.sort()
}

function scanFile(filePath: string, baseDir: string): Violation[] {
  const content = readFileSync(filePath, 'utf-8')
  const lines = content.split('\n')
  const relPath = relative(baseDir, filePath).replace(/\\/g, '/')
  const violations: Violation[] = []

  for (const pat of patterns) {
    for (let i = 0; i < lines.length; i++) {
      const match = pat.match(lines[i])
      if (!match) continue
      if (pat.exempt && pat.exempt(lines, i, match)) continue
      violations.push({
        file: relPath,
        line: i + 1,
        level: pat.level,
        pattern: pat.name,
        snippet: lines[i].trim(),
      })
    }
  }

  const empties = findEmptyAsyncTests(lines).map((v) => ({ ...v, file: relPath }))
  violations.push(...empties)

  return violations
}

describe('weak assertion scanner', () => {
  it('no weak assertions in src test files', () => {
    const projectRoot = resolve(__dirname, '..')
    const srcDir = resolve(projectRoot, 'src')
    const files = findTestFiles(srcDir)

    if (files.length === 0) {
      expect.fail('no test files found in src/')
    }

    const allViolations: Violation[] = []
    for (const f of files) {
      allViolations.push(...scanFile(f, projectRoot))
    }

    if (allViolations.length > 0) {
      const grouped = new Map<string, Violation[]>()
      for (const v of allViolations) {
        if (!grouped.has(v.file)) grouped.set(v.file, [])
        grouped.get(v.file)!.push(v)
      }
      let msg = `\n=== Found ${allViolations.length} weak assertion issues ===\n`
      for (const [file, viols] of grouped) {
        msg += `\n${file}:\n`
        for (const v of viols) {
          msg += `  ${v.line}:${v.level} ${v.pattern}\n`
          msg += `    ${v.snippet}\n`
        }
      }
      expect.fail(msg)
    }
  })
})
