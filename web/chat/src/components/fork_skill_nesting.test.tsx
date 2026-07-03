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

// Simulates a /review skill invocation: Skill tool_start → fork Reviewer agent
// → Reviewer text_delta/tool_start/tool_end → Skill tool_end
// Matches real daemon event sequence captured from logs.
function dispatchEvent(event: any) {
	dispatchToClient({ type: 'event', event })
}

describe('fork skill sub-agent nesting', () => {
	beforeEach(() => {
		sentMessages = []
		listeners = new Set()
	})

	it('Skill tool nests Reviewer text and tools as children', () => {
		renderChat()

		// Main query starts
		dispatchEvent({ type: 'query_start' })

		// LLM decides to call Skill tool
		dispatchEvent({
			type: 'tool_start',
			tool_use: { id: 'skill-1', name: 'Skill', input: { skill: 'review' } },
		})

		// Skill tool runs (fork sub-agent)
		dispatchEvent({ type: 'tool_run', tool_use: { id: 'skill-1', name: 'Skill' } })

		// Sub-agent (Reviewer) emits events with agent metadata
		const agent = { parent_tool_use_id: 'skill-1', agent_type: 'Reviewer', depth: 1 }

		// Reviewer reads a file
		dispatchEvent({
			type: 'tool_start',
			tool_use: { id: 'rev-read-1', name: 'Read', input: { file_path: '/tmp/test.go' } },
			agent,
		})
		dispatchEvent({
			type: 'tool_param_delta',
			partial_input: { id: 'rev-read-1', summary: 'Read /tmp/test.go' },
			agent,
		})
		dispatchEvent({
			type: 'tool_end',
			tool_result: { tool_use_id: 'rev-read-1', is_error: false },
			agent,
		})

		// Reviewer produces text response
		dispatchEvent({ type: 'text_start', agent })
		dispatchEvent({ type: 'text_delta', text: 'Code looks good.', agent })
		dispatchEvent({ type: 'text_end', agent })

		// DEBUG: dump DOM
		screen.debug(undefined, 100000)

		// Skill tool ends
		dispatchEvent({
			type: 'tool_end',
			tool_result: { tool_use_id: 'skill-1', is_error: false, display_output: 'Review complete' },
		})

		// Skill tool should exist
		expect(screen.getByText('Skill')).toBeTruthy()

		// Expand the Skill tool to see children
		const skillBtn = screen.getByText('Skill').closest('[role="button"]')!
		fireEvent.click(skillBtn)

		// Reviewer's Read tool should be nested inside
		expect(screen.getByText('Read')).toBeTruthy()

		// Reviewer's text should be nested inside
		expect(screen.getByText(/Code looks good/)).toBeTruthy()
	})

	it('stop during sub-agent shows interrupt, not double []', () => {
		renderChat()

		dispatchEvent({ type: 'query_start' })
		dispatchEvent({ type: 'text_start' })
		dispatchEvent({ type: 'text_delta', text: 'Starting work' })

		// Skill tool starts
		dispatchEvent({
			type: 'tool_start',
			tool_use: { id: 'skill-2', name: 'Skill', input: { skill: 'review' } },
		})
		dispatchEvent({ type: 'tool_run', tool_use: { id: 'skill-2', name: 'Skill' } })

		// Sub-agent events
		const agent = { parent_tool_use_id: 'skill-2', agent_type: 'Reviewer', depth: 1 }
		dispatchEvent({
			type: 'tool_start',
			tool_use: { id: 'rev-bash-1', name: 'Bash', input: { command: 'ls' } },
			agent,
		})

		// User stops — connector injects interrupt text_delta for main engine only
		dispatchEvent({ type: 'text_delta', text: '[Request interrupted by user]' })
		dispatchEvent({ type: 'query_end', aborted: true })

		// Should NOT see "[]" duplicated
		const bodyText = document.body.textContent || ''
		expect(bodyText).not.toMatch(/\[\]\s*\[\]/)

		// Should see interrupt message
		expect(screen.getByText(/\[Request interrupted/)).toBeTruthy()
	})
})
