import { describe, it, expect } from 'vitest'
import { readFileSync } from 'fs'
import { resolve } from 'path'

// TDD: queued message must reach the server during streaming.
// Bug: InputBar.onSubmit returns early when streaming===true (line 24),
// and ChatInterface.onSend only does setQueuedText without calling send().
// The server's enqueueFn path is correct but unreachable.
describe('queued message send during streaming (TDD)', () => {
	const inputSrc = readFileSync(resolve(__dirname, './InputBar.tsx'), 'utf-8')
	const chatSrc = readFileSync(resolve(__dirname, './ChatInterface.tsx'), 'utf-8')

	it('InputBar does not block submit when streaming', () => {
		// The onSubmit guard must NOT include `streaming` as a block condition.
		// streaming only controls whether the STOP button shows, not whether
		// the user can type and send a queued message.
		const onSubmitBlock = inputSrc.match(/const onSubmit[\s\S]*?setValue\(''\)/)
		expect(onSubmitBlock).toBeTruthy()
		expect(onSubmitBlock![0]).not.toMatch(/streaming.*return/)
	})

	it('ChatInterface.onSend shows user message immediately during streaming', () => {
		// During streaming, the user message must be pushed to messagesRef
		// immediately (not deferred to query_end), matching TUI behavior
		// where the message is visible as soon as sent.
		const onSendBlock = chatSrc.match(/const onSend[\s\S]*?\n\t}/)
		expect(onSendBlock).toBeTruthy()
		// User message push must happen BEFORE the streaming branch check
		const pushIdx = onSendBlock![0].indexOf('messagesRef.current.push')
		const streamingIdx = onSendBlock![0].indexOf('streamingRef.current')
		expect(pushIdx).toBeGreaterThan(-1)
		expect(streamingIdx).toBeGreaterThan(-1)
		expect(pushIdx).toBeLessThan(streamingIdx)
	})
})
