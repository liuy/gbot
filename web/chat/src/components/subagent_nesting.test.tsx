import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, act, within } from '@testing-library/react'
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
	const r = render(<ChatInterface />)
	// Reset module-level persistedMessages: connect_status flips
	// expectingInitialRef, then an empty initial history replaces all.
	dispatchToClient({ type: 'connect_status', connected: true })
	dispatchToClient({ type: 'history', messages: [], nextCursor: '', hasMore: false })
	return r
}

const AGENT_META = { parent_tool_use_id: 'agent-1', agent_type: 'Explore', depth: 1 }

// Click the Agent tool header (the <span role="button"> containing the tool
// name) to expand it. Returns the header element.
function expandToolByName(name: RegExp): HTMLElement {
	// ToolRenderer renders a <span role="button"> with the tool name text.
	const headers = screen.getAllByRole('button').filter((el) => name.test(el.textContent ?? ''))
	expect(headers.length).toBeGreaterThanOrEqual(1)
	fireEvent.click(headers[0])
	return headers[0]
}

describe('sub-agent nesting: live streaming', () => {
	beforeEach(() => {
		sentMessages = []
		listeners = new Set()
	})

	it('sub-agent tool + text nest under parent Agent tool, not top-level', () => {
		renderChat()
		dispatchToClient({ type: 'event', event: { type: 'query_start' } })
		// Top-level Agent tool starts
		dispatchToClient({ type: 'event', event: { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent', input: {} } } })
		// Sub-agent Grep tool — nests under agent-1
		dispatchToClient({ type: 'event', event: { type: 'tool_start', tool_use: { id: 'grep-1', name: 'Grep' }, agent: AGENT_META } })
		// Sub-agent text
		dispatchToClient({ type: 'event', event: { type: 'text_delta', text: 'Found it.', agent: AGENT_META } })
		// Sub-agent Grep ends
		dispatchToClient({ type: 'event', event: { type: 'tool_end', tool_result: { tool_use_id: 'grep-1', display_output: '3 matches' }, agent: AGENT_META } })
		// Agent ends
		dispatchToClient({ type: 'event', event: { type: 'tool_end', tool_result: { tool_use_id: 'agent-1', display_output: 'agent done' } } })
		dispatchToClient({ type: 'event', event: { type: 'query_end' } })

		// Exactly ONE top-level Agent tool indicator (no flat Grep).
		expect(screen.getAllByText(/Agent/).length).toBe(1)
		// Grep is NOT top-level: collapsed-by-default Agent hides children.
		expect(screen.queryByText(/Grep/)).toBeNull()

		// Expand Agent — Grep + Found it. appear inside.
		expandToolByName(/Agent/)
		expect(screen.getByText(/Grep/)).toBeTruthy()
		expect(screen.getByText(/Found it\./)).toBeTruthy()
	})

	it('top-level events stay flat when no agent metadata', () => {
		renderChat()
		dispatchToClient({ type: 'event', event: { type: 'query_start' } })
		dispatchToClient({ type: 'event', event: { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent', input: {} } } })
		// No agent metadata — these should be flat siblings.
		dispatchToClient({ type: 'event', event: { type: 'tool_start', tool_use: { id: 'grep-1', name: 'Grep', is_search: true } } })
		dispatchToClient({ type: 'event', event: { type: 'text_delta', text: 'flat text' } })
		dispatchToClient({ type: 'event', event: { type: 'query_end' } })

		// All three visible as siblings WITHOUT expanding anything — flat layout.
		expect(screen.getByText(/Agent/)).toBeTruthy()
		expect(screen.getByText(/Grep/)).toBeTruthy()
		expect(screen.getByText(/flat text/)).toBeTruthy()
	})

	it('turn_start and query_end with agent metadata do not finish the stream', () => {
		renderChat()
		dispatchToClient({ type: 'event', event: { type: 'query_start' } })
		dispatchToClient({ type: 'event', event: { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent', input: {} } } })

		// Sub-agent turn_start + query_end arrive with agent metadata.
		dispatchToClient({ type: 'event', event: { type: 'turn_start', agent: AGENT_META } })
		dispatchToClient({ type: 'event', event: { type: 'query_end', agent: AGENT_META } })

		// Top-level stream must still be active: STOP button present.
		expect(screen.queryByText('STOP')).not.toBeNull()

		// Top-level query_end (no agent) finishes the stream.
		dispatchToClient({ type: 'event', event: { type: 'query_end' } })
		expect(screen.queryByText('STOP')).toBeNull()
	})

	it('text_delta with unknown parent_tool_use_id is silently dropped', () => {
		renderChat()
		dispatchToClient({ type: 'event', event: { type: 'query_start' } })
		// Bogus parent — event dropped (no crash, no extra text).
		dispatchToClient({ type: 'event', event: {
			type: 'text_delta',
			text: 'orphan',
			agent: { parent_tool_use_id: 'nope', agent_type: 'Explore', depth: 1 },
		}})
		dispatchToClient({ type: 'event', event: { type: 'query_end' } })

		expect(screen.queryByText(/orphan/)).toBeNull()
	})

	it('tool_end preserves existing children on the parent Agent tool', () => {
		renderChat()
		dispatchToClient({ type: 'event', event: { type: 'query_start' } })
		dispatchToClient({ type: 'event', event: { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent', input: {} } } })
		// Sub-agent text accumulates into children.
		dispatchToClient({ type: 'event', event: { type: 'text_delta', text: 'sub text', agent: AGENT_META } })
		// Agent tool_end — children must survive.
		dispatchToClient({ type: 'event', event: { type: 'tool_end', tool_result: { tool_use_id: 'agent-1', display_output: 'done' } } })
		dispatchToClient({ type: 'event', event: { type: 'query_end' } })

		// Expand Agent — 'sub text' must still be present.
		expandToolByName(/Agent/)
		expect(screen.getByText(/sub text/)).toBeTruthy()
	})
})

describe('sub-agent nesting: depth >= 2 (nested Agent)', () => {
	beforeEach(() => {
		sentMessages = []
		listeners = new Set()
	})

	it('Read nests under agent-2 which nests under agent-1', () => {
		renderChat()
		dispatchToClient({ type: 'event', event: { type: 'query_start' } })
		dispatchToClient({ type: 'event', event: { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent', input: {} } } })
		dispatchToClient({ type: 'event', event: {
			type: 'tool_start',
			tool_use: { id: 'agent-2', name: 'Agent', input: {} },
			agent: { parent_tool_use_id: 'agent-1', agent_type: 'Explore', depth: 1 },
		}})
		dispatchToClient({ type: 'event', event: {
			type: 'tool_start',
			tool_use: { id: 'read-1', name: 'Read', is_read: true },
			agent: { parent_tool_use_id: 'agent-2', agent_type: 'Explore', depth: 2 },
		}})
		// agent-1 and agent-2 both running with children → auto-expanded.
		// Read is nested under agent-2, visible due to auto-expand cascade.
		expect(screen.getByText(/Read/)).toBeTruthy()

		dispatchToClient({ type: 'event', event: { type: 'query_end' } })
	})
})

describe('sub-agent nesting: empty children render no container', () => {
	it('Agent with no sub-events renders no children container when expanded', () => {
		renderChat()
		dispatchToClient({ type: 'event', event: { type: 'query_start' } })
		dispatchToClient({ type: 'event', event: { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent', input: {} } } })
		dispatchToClient({ type: 'event', event: { type: 'query_end' } })

		// Expand the Agent — no children container should appear.
		const header = expandToolByName(/Agent/)
		// The border-l/pl-2 children container class only renders when
		// children.length > 0. Verify no element with that signature exists.
		const containers = document.querySelectorAll('.border-l.border-t3\\/30.pl-2')
		expect(containers.length).toBe(0)
	})
})

describe('sub-agent nesting: auto-expand on running, auto-collapse on done', () => {
	it('Agent tool auto-expands while running, collapses when done', () => {
		renderChat()
		dispatchToClient({ type: 'event', event: { type: 'query_start' } })

		// Agent tool_start (running)
		dispatchToClient({ type: 'event', event: { type: 'tool_start', tool_use: { id: 'agent-1', name: 'Agent', input: {} } } })

		// Sub-agent text (creates children)
		const agent = { parent_tool_use_id: 'agent-1', agent_type: 'Explore', depth: 1 }
		dispatchToClient({ type: 'event', event: { type: 'text_delta', text: 'exploring...', agent } })

		// Running + children → auto-expanded → text visible
		expect(screen.getByText('exploring...')).toBeTruthy()

		// Agent tool_end → done → auto-collapse
		dispatchToClient({ type: 'event', event: { type: 'tool_end', tool_result: { tool_use_id: 'agent-1', is_error: false } } })

		// After done: children collapsed, text not visible
		expect(screen.queryByText('exploring...')).toBeNull()
	})
})
