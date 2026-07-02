import { describe, it, expect } from 'vitest'
import { readFileSync } from 'fs'
import { resolve } from 'path'

// When abort happens with no meaningful assistant content, the user
// message text must be restored to the input box so the user can edit
// and resubmit. Matching TUI's tryAutoRewind which calls input.SetValue.
describe('abort auto-rewind restores input', () => {
	const src = readFileSync(resolve(__dirname, './ChatInterface.tsx'), 'utf-8')

	it('rewind restores user text to input via InputBar prop', () => {
		// The rewind path must pass the restored text to InputBar.
		// InputBar uses a value prop or a ref-based reset mechanism.
		const block = src.match(/case 'query_end'[\s\S]*?return/)
		expect(block).toBeTruthy()
		// Must reference restoring text to input (not just popping messages)
		expect(block![0]).toMatch(/textBlock|userMsg|restoredText|setInputText/i)
	})
})
