import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import { Profiler, type ReactNode } from 'react'

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
		subscribe: (fn: Listener) => { listeners.add(fn); return () => { listeners.delete(fn) } },
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

function serverSends(events: any[]) {
	for (const e of events) {
		dispatchToClient({ type: 'event', event: e })
	}
}

// Count commits to the InputBar subtree using React Profiler. The Profiler
// fires onRender for every commit that includes the wrapped subtree. We wrap
// the ENTIRE ChatInterface in a Profiler; the count increments on every commit
// that reaches InputBar. With React.memo on InputBar, commits triggered by
// text_delta (which don't change any InputBar props) skip the subtree entirely
// and the Profiler does NOT fire.
let profilerCount = 0
function renderChatWithProfiler() {
	sentMessages = []
	listeners = new Set()
	profilerCount = 0
	return render(
		<Profiler id="chat" onRender={() => { profilerCount++ }}>
			<ChatInterface />
		</Profiler>
	)
}

describe('streaming DOM isolation', () => {
	beforeEach(() => {
		sentMessages = []
		listeners = new Set()
		profilerCount = 0
	})

	it('text_delta content reaches DOM via sink', () => {
		renderChat()
		dispatchToClient({ type: 'connect_status', connected: true })

		serverSends([
			{ type: 'query_start' },
			{ type: 'text_start' },
			{ type: 'text_delta', text: 'Hel' },
			{ type: 'text_delta', text: 'lo ' },
			{ type: 'text_delta', text: 'world' },
		])

		expect(screen.getByText('Hello world')).toBeTruthy()
	})

	it('text_delta does not re-render InputBar', () => {
		renderChatWithProfiler()
		dispatchToClient({ type: 'connect_status', connected: true })

		serverSends([
			{ type: 'query_start' },
			{ type: 'text_start' },
		])

		// Capture profiler count after structural mount (text_start + query_start).
		const mountCount = profilerCount

		for (let i = 0; i < 5; i++) {
			dispatchToClient({ type: 'event', event: { type: 'text_delta', text: `chunk${i} ` } })
		}

		expect(profilerCount).toBe(mountCount)
	})

	it('query_end commits markdown-formatted text', () => {
		renderChat()
		dispatchToClient({ type: 'connect_status', connected: true })

		serverSends([
			{ type: 'query_start' },
			{ type: 'text_start' },
			{ type: 'text_delta', text: '**bold**' },
			{ type: 'text_end' },
			{ type: 'query_end' },
		])

		// After query_end, markdown renders: **bold** becomes <strong>bold</strong>
		expect(document.querySelector('strong')).toBeTruthy()
		expect(screen.getByText('bold')).toBeTruthy()
	})

	it('thinking_delta writes to thinking sink', () => {
		renderChat()
		dispatchToClient({ type: 'connect_status', connected: true })

		serverSends([
			{ type: 'query_start' },
			{ type: 'thinking_start' },
			{ type: 'thinking_delta', thinking: { text: 'reasoning ' } },
			{ type: 'thinking_delta', thinking: { text: 'about stuff' } },
		])

		// forceExpanded keeps the body visible during streaming.
		expect(screen.getByText('reasoning about stuff')).toBeTruthy()
	})

	it('structural events still render: text_start mounts sink', () => {
		renderChat()
		dispatchToClient({ type: 'connect_status', connected: true })

		serverSends([
			{ type: 'query_start' },
			{ type: 'text_start' },
		])

		// STOP button visible (streaming state active)
		expect(screen.queryByText('STOP')).toBeTruthy()

		// Now dispatch a delta and verify the sink populates.
		dispatchToClient({ type: 'event', event: { type: 'text_delta', text: 'arrived' } })
		expect(screen.getByText('arrived')).toBeTruthy()
	})

	it('abort mid-stream shows interrupt marker and keeps partial text', () => {
		renderChat()
		dispatchToClient({ type: 'connect_status', connected: true })

		serverSends([
			{ type: 'query_start' },
			{ type: 'text_start' },
			{ type: 'text_delta', text: 'partial' },
		])

		// Click STOP
		const stopBtn = screen.queryByText('STOP')
		if (stopBtn) fireEvent.click(stopBtn)

		serverSends([
			{ type: 'text_end' },
			{ type: 'text_delta', text: '[Request interrupted by user]' },
			{ type: 'query_end', aborted: true },
		])

		expect(screen.getByText(/partial/)).toBeTruthy()
		expect(screen.getByText(/\[Request interrupted by user\]/)).toBeTruthy()
	})

	it('empty text_delta is a no-op', () => {
		renderChat()
		dispatchToClient({ type: 'connect_status', connected: true })

		serverSends([
			{ type: 'query_start' },
			{ type: 'text_start' },
		])

		// Count md-body sinks before the empty delta.
		const sinksBefore = document.querySelectorAll('.md-body').length

		dispatchToClient({ type: 'event', event: { type: 'text_delta', text: '' } })

		// No crash, no new sink mounted, accumulator unchanged.
		const sinksAfter = document.querySelectorAll('.md-body').length
		expect(sinksAfter).toBe(sinksBefore)
		// The streaming sink (last md-body) must be empty — empty delta added nothing.
		const sinks = document.querySelectorAll('.md-body')
		expect(sinks[sinks.length - 1]?.textContent).toBe('')
	})

	it('multiple text blocks in one turn each get fresh sinks', () => {
		renderChat()
		dispatchToClient({ type: 'connect_status', connected: true })

		serverSends([
			{ type: 'query_start' },
			{ type: 'text_start' },
			{ type: 'text_delta', text: 'first' },
			{ type: 'text_end' },
			{ type: 'text_start' },
			{ type: 'text_delta', text: 'second' },
			{ type: 'text_end' },
			{ type: 'query_end' },
		])

		expect(screen.getByText('first')).toBeTruthy()
		expect(screen.getByText('second')).toBeTruthy()
	})

	it('interleaved text and thinking both writable', () => {
		renderChat()
		dispatchToClient({ type: 'connect_status', connected: true })

		serverSends([
			{ type: 'query_start' },
			{ type: 'thinking_start' },
			{ type: 'thinking_delta', thinking: { text: 'deep thought' } },
			{ type: 'thinking_end', thinking: { duration: 1000000000 } },
			{ type: 'text_start' },
			{ type: 'text_delta', text: 'answer' },
		])

		// During streaming, Thinking is forceExpanded so its body is visible.
		expect(screen.getByText('deep thought')).toBeTruthy()
		expect(screen.getByText('answer')).toBeTruthy()
	})
})
