import { describe, it, expect } from 'vitest'
import { readFileSync } from 'fs'
import { resolve } from 'path'

// Queued message must reach the server during streaming.
// InputBar must not block submit when streaming; ChatInterface.onSend
// must call send() even during streaming so the server can enqueue.
describe('queued message send during streaming', () => {
	const inputSrc = readFileSync(resolve(__dirname, './InputBar.tsx'), 'utf-8')
	const chatSrc = readFileSync(resolve(__dirname, './ChatInterface.tsx'), 'utf-8')

	it('InputBar does not block submit when streaming', () => {
		const onSubmitBlock = inputSrc.match(/const onSubmit[\s\S]*?Ref\.current\.value = ''/)
		expect(onSubmitBlock).toBeTruthy()
		expect(onSubmitBlock![0]).not.toMatch(/streaming.*return/)
	})

	it('ChatInterface.onSend calls send() during streaming', () => {
		const onSendBlock = chatSrc.match(/const onSend[\s\S]*?\n\t}/)
		expect(onSendBlock).toBeTruthy()
		expect(onSendBlock![0]).toContain("send({ type: 'message'")
	})

	it('ChatInterface.onSend during streaming pushes to queuedMsgs array', () => {
		const onSendBlock = chatSrc.match(/const onSend[\s\S]*?\n\t}/)
		expect(onSendBlock).toBeTruthy()
		expect(onSendBlock![0]).toMatch(/setQueuedMsgs\(\(prev\) => \[\.\.\.prev,/)
		expect(onSendBlock![0]).toContain("send({ type: 'message'")
	})
})
