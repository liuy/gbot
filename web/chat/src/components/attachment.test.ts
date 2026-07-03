import { describe, it, expect } from 'vitest'
import { readFileSync } from 'fs'
import { resolve } from 'path'

// Engine emits EventAttachment when a queued message is drained.
// Two drain points: turn boundary (PriorityNext) and query end (DrainAll).
// Both emit EventAttachment with Message containing the user's text and the
// SourceUUID of the drained queue item. The frontend reads text + source_uuid
// from the event payload (not local state) so each drain renders correctly
// even when multiple are queued.
describe('attachment event handling', () => {
	const chatSrc = readFileSync(resolve(__dirname, './ChatInterface.tsx'), 'utf-8')

	it('handleEvent processes attachment event as user message', () => {
		expect(chatSrc).toMatch(/case 'attachment'/)
	})

	it('attachment handler pushes user message with text from event payload', () => {
		const block = chatSrc.match(/case 'attachment': \{[\s\S]*?\n\t\t\t\}/)
		expect(block).toBeTruthy()
		expect(block![0]).toContain("'user'")
		expect(block![0]).toMatch(/att\.prompt|message\?\.attachment\?\.prompt/)
		expect(block![0]).not.toMatch(/queuedText/)
	})
})
