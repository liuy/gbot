import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import { type ReactNode } from 'react'

// jsdom lacks IntersectionObserver
class MockIntersectionObserver {
	observe() {}
	unobserve() {}
	disconnect() {}
	takeRecords() { return [] }
}
vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)

// ── Mock WebSocket ──────────────────────────────────────────────

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

// ── Import AFTER mock ───────────────────────────────────────────

import ChatInterface from './ChatInterface'

function renderChat() {
	sentMessages = []
	listeners = new Set()
	return render(<ChatInterface />)
}

// Helper: simulate typing + pressing Enter
function send(text: string) {
	// Find the textarea
	const textarea = document.querySelector('textarea')
	expect(textarea).toBeTruthy()
	fireEvent.change(textarea!, { target: { value: text } })
	fireEvent.keyDown(textarea!, { key: 'Enter' })
}

// Helper: click STOP button
function clickStop() {
	const stopBtn = screen.queryByText('STOP')
	if (stopBtn) fireEvent.click(stopBtn)
}

// Helper: dispatch WS events from "server"
function serverSends(events: any[]) {
	for (const e of events) {
		dispatchToClient({ type: 'event', event: e })
	}
}

// ── Tests ───────────────────────────────────────────────────────

describe('webchat integration: send + abort', () => {
	beforeEach(() => {
		vi.clearAllMocks()
	})

	it('shows user message after send', async () => {
		renderChat()
		dispatchToClient({ type: 'connect_status', connected: true })

		send('hello world')

		// User message should appear in the DOM
		expect(screen.getByText('hello world')).toBeTruthy()
		// WS should have received a message
		expect(sentMessages.some((m) => m.type === 'message' && m.text === 'hello world')).toBe(true)
	})

	it('abort during thinking removes messages and restores input', async () => {
		renderChat()
		dispatchToClient({ type: 'connect_status', connected: true })

		send('test query')

		// Engine starts processing
		serverSends([{ type: 'query_start' }, { type: 'thinking_start' }])

		// User clicks STOP
		clickStop()

		// Engine ends query
		serverSends([{ type: 'thinking_end' }, { type: 'query_end' }])

		// User message should be gone
		expect(screen.queryByText('test query')).toBeNull()

		// Input should have the text restored
		const textarea = document.querySelector('textarea') as HTMLTextAreaElement
		expect(textarea.value).toBe('test query')
	})

	it('abort with partial response shows interrupted', async () => {
		renderChat()
		dispatchToClient({ type: 'connect_status', connected: true })

		send('another query')

		serverSends([
			{ type: 'query_start' },
			{ type: 'text_start' },
			{ type: 'text_delta', text: 'partial' },
		])

		clickStop()

		serverSends([{ type: 'text_end' }, { type: 'query_end' }])

		// User message should still be visible
		expect(screen.getByText('another query')).toBeTruthy()
		// Should show partial response + interrupt marker
		expect(screen.getByText('partial')).toBeTruthy()
		expect(screen.getByText('[Request interrupted by user]')).toBeTruthy()
	})

	it('repeated send + abort restores correct text', async () => {
		renderChat()
		dispatchToClient({ type: 'connect_status', connected: true })

		// First send + abort
		send('repeat me')
		serverSends([{ type: 'query_start' }, { type: 'thinking_start' }])
		clickStop()
		serverSends([{ type: 'thinking_end' }, { type: 'query_end' }])

		expect(screen.queryByText('repeat me')).toBeNull()

		// Second send (same text) + abort
		send('repeat me')
		serverSends([{ type: 'query_start' }, { type: 'thinking_start' }])
		clickStop()
		serverSends([{ type: 'thinking_end' }, { type: 'query_end' }])

		// Input must have the correct text, not stale content
		const textarea = document.querySelector('textarea') as HTMLTextAreaElement
		expect(textarea.value).toBe('repeat me')
	})
})
