import { describe, it, expect } from 'vitest'
import { readFileSync } from 'fs'
import { resolve } from 'path'

// When user cancels the queued bubble, frontend must notify server to
// remove the attachment from engine's queue. Server returns the UUID
// after enqueue so frontend can reference it on cancel.
describe('cancel queued message', () => {
	const chatSrc = readFileSync(resolve(__dirname, './ChatInterface.tsx'), 'utf-8')

	it('onCancelQueued sends cancel_queued with uuid to server', () => {
		const block = chatSrc.match(/const onCancelQueued[\s\S]*?\n\t}/)
		expect(block).toBeTruthy()
		expect(block![0]).toContain("type: 'cancel_queued'")
		expect(block![0]).toMatch(/uuid|queuedUuid|attachmentUuid/i)
	})

	it('frontend stores queued uuid from server response', () => {
		// handleMessageInbound or the queued path must save the UUID
		// returned by the server for later cancel reference
		expect(chatSrc).toMatch(/queuedUuid|attachmentUuid|queuedUUID/i)
	})
})
