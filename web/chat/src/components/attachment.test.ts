import { describe, it, expect } from 'vitest'
import { readFileSync } from 'fs'
import { resolve } from 'path'

// Engine emits EventAttachment when a queued message is drained.
// Two drain points: turn boundary (PriorityNext) and query end (DrainAll).
// Both emit EventAttachment with Message containing the user's text.
// WebChat frontend must handle this event to show the user message.
describe('attachment event handling', () => {
	const chatSrc = readFileSync(resolve(__dirname, './ChatInterface.tsx'), 'utf-8')

	it('handleEvent processes attachment event as user message', () => {
		expect(chatSrc).toMatch(/case 'attachment'/)
	})

	it('attachment handler pushes user message with text from queuedText', () => {
		const block = chatSrc.match(/case 'attachment'[\s\S]*?return/)
		expect(block).toBeTruthy()
		expect(block![0]).toContain("'user'")
		expect(block![0]).toContain('queuedText')
	})
})
