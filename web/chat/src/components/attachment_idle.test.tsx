import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, act } from '@testing-library/react'
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
	act(() => { listeners.forEach((fn) => fn(msg)) })
}

vi.mock('../websocket', () => ({
	useWebSocket: () => ({
		subscribe: (fn: Listener) => { listeners.add(fn); return () => listeners.delete(fn) },
		send: (payload: object) => { sentMessages.push(payload) },
		connected: true,
	}),
	WebSocketProvider: ({ children }: { children: ReactNode }) => children,
}))

import ChatInterface from './ChatInterface'

function renderChat() {
	sentMessages = []
	listeners = new Set()
	return render(<ChatInterface />)
}

describe('attachment drained after query_end (processAttachments path)', () => {
	beforeEach(() => {
		sentMessages = []
		listeners = new Set()
	})

	it('text_delta after attachment+turn_start renders in assistant', () => {
		renderChat()

		// Previous query ends
		dispatchToClient({ type: 'event', event: { type: 'query_end' } })

		// Attachment drained by processAttachments — emits turn_start, NOT query_start
		dispatchToClient({
			type: 'event',
			event: {
				type: 'attachment',
				message: { attachment: { prompt: '11', source_uuid: 'u-1' } },
			},
		})

		// processAttachments calls runTurns → turn_start (no query_start!)
		dispatchToClient({ type: 'event', event: { type: 'turn_start' } })

		// LLM text response
		dispatchToClient({ type: 'event', event: { type: 'text_delta', text: '收到' } })

		expect(screen.getByText('收到')).toBeTruthy()
		expect(screen.getByText('11')).toBeTruthy()
	})
})
