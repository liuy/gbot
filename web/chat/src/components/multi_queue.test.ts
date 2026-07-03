import { describe, it, expect } from 'vitest'
import { readFileSync } from 'fs'
import { resolve } from 'path'

// Multi-queue support: webchat tracks an array of queued messages instead of
// a single one (TUI pendingQueue parity). Multiple bubbles render; tapping
// any cancels all (batch); Up key pops them all back to input. The 'queued'
// server reply stamps the UUID onto the first unstamped array entry — FIFO
// ordering because msgCh is a single ordered channel.
describe('multi-queue support', () => {
	const chatSrc = readFileSync(resolve(__dirname, './ChatInterface.tsx'), 'utf-8')
	const inputSrc = readFileSync(resolve(__dirname, './InputBar.tsx'), 'utf-8')

	it('ChatInterface uses queuedMsgs array state', () => {
		expect(chatSrc).toMatch(/useState<\{ uuid: string; text: string \}\[\]>/)
	})

	it('queued case stamps UUID onto the first unstamped entry (FIFO)', () => {
		const block = chatSrc.match(/case 'queued'[\s\S]*?return/)
		expect(block).toBeTruthy()
		// FIFO: scan from index 0; the first entry whose uuid is still empty
		// gets stamped. msgCh ordering makes the Nth reply map to the Nth
		// unstamped entry.
		expect(block![0]).toMatch(/\.uuid === ''/)
	})

	it('attachment case reads source_uuid from event payload', () => {
		const block = chatSrc.match(/case 'attachment': \{[\s\S]*?\n\t\t\t\}/)
		expect(block).toBeTruthy()
		expect(block![0]).toMatch(/source_uuid/)
		expect(block![0]).toMatch(/setQueuedMsgs\(\(prev\) => prev\.filter/)
	})

	it('InputBar renders one bubble per queued message', () => {
		expect(inputSrc).toMatch(/queuedMsgs\.map\(\(m, i\) =>/)
	})

	it('InputBar Up key triggers onCancelQueued when streaming and queue non-empty', () => {
		const block = inputSrc.match(/const onKeyDown[\s\S]*?\n\t}/)
		expect(block).toBeTruthy()
		expect(block![0]).toMatch(/ArrowUp/)
		expect(block![0]).toMatch(/streaming/)
		expect(block![0]).toMatch(/queuedMsgs\.length > 0/)
	})

	it('InputBarHandle has appendQueuedText method', () => {
		expect(inputSrc).toMatch(/appendQueuedText: \(text: string\) =>/)
	})
})
