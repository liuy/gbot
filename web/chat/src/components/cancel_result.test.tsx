import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import { type ReactNode } from 'react'

class MockIntersectionObserver {
	observe() {}
	unobserve() {}
	disconnect() {}
	takeRecords() { return [] }
}
vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)

type Listener = (msg: any) => void

let listeners: Set<Listener> = new Set()
let sentMessages: any[] = []

function dispatchToClient(msg: any) {
	act(() => {
		listeners.forEach((fn) => fn(msg))
	})
}

vi.mock('../websocket', () => {
	return {
		useWebSocket: () => ({
			subscribe: (fn: Listener) => {
				listeners.add(fn)
				return () => listeners.delete(fn)
			},
			send: (payload: object) => {
				sentMessages.push(payload)
			},
			connected: true,
		}),
		WebSocketProvider: ({ children }: { children: ReactNode }) => children,
	}
})

import ChatInterface from './ChatInterface'

function renderChat() {
	sentMessages = []
	listeners = new Set()
	return render(<ChatInterface />)
}

function typeAndEnter(text: string) {
	const textarea = document.querySelector('textarea')!
	fireEvent.change(textarea, { target: { value: text } })
	fireEvent.keyDown(textarea, { key: 'Enter' })
}

describe('cancel_queued with partial drain (TUI parity)', () => {
	beforeEach(() => {
		sentMessages = []
		listeners = new Set()
	})

	it('queues 3 messages, msg1 drained by engine, cancel restores only msg2+3', () => {
		renderChat()

		// Server is streaming
		dispatchToClient({ type: 'event', event: { type: 'query_start' } })

		// Queue msg1
		typeAndEnter('test message 1')
		dispatchToClient({ type: 'queued', uuid: 'u-1' })

		// Queue msg2
		typeAndEnter('test message 2')
		dispatchToClient({ type: 'queued', uuid: 'u-2' })

		// Queue msg3
		typeAndEnter('test message 3')
		dispatchToClient({ type: 'queued', uuid: 'u-3' })

		// 3 queued bubbles visible
		expect(screen.getAllByText(/Tap to CANCEL/).length).toBeGreaterThanOrEqual(1)

		// Engine drains msg1 (attachment event) — removes from queuedMsgs
		dispatchToClient({
			type: 'event',
			event: {
				type: 'attachment',
				message: {
					attachment: {
						prompt: 'test message 1',
						source_uuid: 'u-1',
					},
				},
			},
		})

		// User taps cancel on remaining bubbles (msg2+3)
		act(() => {
			const cancelBtn = screen.getAllByText(/Tap to CANCEL/)[0]
			fireEvent.click(cancelBtn)
		})

		// cancel_queued sent with only u-2, u-3 (u-1 already drained)
		const cancelMsg = sentMessages.find((m) => m.type === 'cancel_queued')
		expect(cancelMsg).toBeTruthy()
		expect(cancelMsg.uuids).toEqual(['u-2', 'u-3'])

		// Server responds: both successfully removed
		dispatchToClient({
			type: 'cancel_result',
			removed: ['u-2', 'u-3'],
		})

		// All queued bubbles gone
		expect(screen.queryByText(/Tap to CANCEL/)).toBeNull()

		// Only msg 2 + 3 restored to input (msg1 was drained, not in cancel)
		const textarea = document.querySelector('textarea') as HTMLTextAreaElement
		expect(textarea.value).toContain('test message 2')
		expect(textarea.value).toContain('test message 3')
		expect(textarea.value).not.toContain('test message 1')
	})

	it('cancel_result partial: some UUIDs not removed (engine drained mid-cancel)', () => {
		renderChat()

		dispatchToClient({ type: 'event', event: { type: 'query_start' } })

		// Queue msg a
		typeAndEnter('test message a')
		dispatchToClient({ type: 'queued', uuid: 'u-a' })

		// Queue msg b
		typeAndEnter('test message b')
		dispatchToClient({ type: 'queued', uuid: 'u-b' })

		// User taps cancel — sends both UUIDs
		act(() => {
			const cancelBtn = screen.getAllByText(/Tap to CANCEL/)[0]
			fireEvent.click(cancelBtn)
		})

		const cancelMsg = sentMessages.find((m) => m.type === 'cancel_queued')
		expect(cancelMsg.uuids).toEqual(['u-a', 'u-b'])

		// Server responds: u-a was already drained, only u-b removed
		dispatchToClient({
			type: 'cancel_result',
			removed: ['u-b'],
		})

		// Only u-b's text restored (u-a was drained, not restored)
		const textarea = document.querySelector('textarea') as HTMLTextAreaElement
		expect(textarea.value).toContain('test message b')
		expect(textarea.value).not.toContain('test message a')
	})
})
