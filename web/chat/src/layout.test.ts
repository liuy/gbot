import { describe, it, expect } from 'vitest'
import fs from 'fs'
import path from 'path'

// html background prevents white flash when Chrome address bar
// collapses/expands on Android. Must be a solid dark color (not gradient)
// because gradient doesn't cover the full viewport during resize.
// Uses a deep blue (#060a14) instead of pure black so body's semi-transparent
// gradient reveals a subtle blue tint through it.
const css = fs.readFileSync(path.resolve(__dirname, 'index.css'), 'utf-8')

describe('Chrome address bar flash prevention', () => {
	it('html element has dark solid background', () => {
		const match = css.match(/html\s*\{[^}]*background:\s*#0a1628/)
		expect(match).not.toBeNull()
		expect(match![0]).toContain('background:')
		expect(match![0]).toContain('#0a1628')
	})
})
