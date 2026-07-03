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

// TUI parity: repl.go:1364 — mid-turn drain appends BlockUser inside
// the current assistant message's blocks. The streaming assistant MUST
// remain the last message in the list so text_delta/thinking_delta
// handlers can find it via list[list.length - 1].
describe('attachment during streaming must not break text_delta', () => {
	beforeEach(() => {
		sentMessages = []
		listeners = new Set()
	})

	it('text_delta after attachment renders in assistant, not lost', () => {
		renderChat()

		dispatchToClient({ type: 'event', event: { type: 'query_start' } })

		dispatchToClient({ type: 'event', event: { type: 'text_delta', text: 'Hello ' } })

		dispatchToClient({
			type: 'event',
			event: {
				type: 'attachment',
				message: { attachment: { prompt: 'queued msg', source_uuid: 'u-1' } },
			},
		})

		dispatchToClient({ type: 'event', event: { type: 'text_delta', text: 'world!' } })

		expect(screen.getByText(/Hello world!/)).toBeTruthy()
		expect(screen.getByText('queued msg')).toBeTruthy()
	})

	it('text_delta after attachment in idle (!streaming) renders in assistant', () => {
		renderChat()

		// First query ends (streamingRef set to false)
		dispatchToClient({ type: 'event', event: { type: 'query_end' } })

		// Attachment drained after query_end — !streaming path
		dispatchToClient({
			type: 'event',
			event: {
				type: 'attachment',
				message: { attachment: { prompt: '11', source_uuid: 'u-1' } },
			},
		})

		// New query starts
		dispatchToClient({ type: 'event', event: { type: 'query_start' } })

		// LLM response
		dispatchToClient({ type: 'event', event: { type: 'text_delta', text: '收到' } })

		expect(screen.getByText('收到')).toBeTruthy()
		expect(screen.getByText('11')).toBeTruthy()
	})
})
