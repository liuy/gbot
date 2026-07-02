import { describe, it, expect } from 'vitest'
import fs from 'fs'
import path from 'path'

// html background color prevents white flash when Chrome address bar
// collapses/expands on Android. Chrome uses html element's background
// during viewport resize transitions — without a dark color, the gap
// is white.
const css = fs.readFileSync(path.resolve(__dirname, 'index.css'), 'utf-8')

describe('Chrome address bar flash prevention', () => {
	it('html element has dark background (#04060c)', () => {
		// Match "html {" followed by "background: #04060c"
		const match = css.match(/html\s*\{[^}]*background:\s*#04060c/)
		expect(match).toBeTruthy()
	})
})
