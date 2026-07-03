import { describe, it, expect } from 'vitest'
import { readFileSync } from 'fs'
import { resolve } from 'path'

// When user cancels the queued bubble, frontend must notify server to remove
// the attachment from engine's queue. Webchat sends the full queued UUID list
// for batch cancellation (TUI popAllQueuedToInput parity). After cancel, the
// queued text is restored to the input box joined by newlines.
describe('cancel queued message', () => {
	const chatSrc = readFileSync(resolve(__dirname, './ChatInterface.tsx'), 'utf-8')

	it('onCancelQueued sends cancel_queued with uuids array to server', () => {
		const block = chatSrc.match(/const onCancelQueued[\s\S]*?\n\t}/)
		expect(block).toBeTruthy()
		expect(block![0]).toContain("type: 'cancel_queued'")
		expect(block![0]).toMatch(/uuids/)
		expect(block![0]).toMatch(/\.map\(\(m\) => m\.uuid\)/)
	})

	it('onCancelQueued restores queued text to input via appendQueuedText', () => {
		const block = chatSrc.match(/const onCancelQueued[\s\S]*?\n\t}/)
		expect(block).toBeTruthy()
		expect(block![0]).toMatch(/appendQueuedText/)
	})

	it('frontend tracks queued array (not single uuid)', () => {
		expect(chatSrc).toMatch(/queuedMsgs/)
		expect(chatSrc).not.toMatch(/queuedUuidRef/)
		expect(chatSrc).not.toMatch(/setQueuedText\(/)
	})
})
