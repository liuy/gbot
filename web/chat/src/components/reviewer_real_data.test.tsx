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
	const r = render(<ChatInterface />)
	// Reset module-level persistedMessages: connect_status flips
	// expectingInitialRef, then an empty initial history replaces all.
	dispatchToClient({ type: 'connect_status', connected: true })
	dispatchToClient({ type: 'history', messages: [], nextCursor: '', hasMore: false })
	return r
}

function dispatchEvent(event: any) {
	dispatchToClient({ type: 'event', event })
}

// Real wire events captured from daemon log (~/.gbot/projects/daemon/gbot.log)
// during a /review skill run. These are NOT synthetic — every field value
// comes from the actual tagged:dispatch + engine:* log lines.
//
// Key real-data facts that differ from synthetic mocks:
//  - Sub-agent turn_start carries agent meta and arrives BEFORE thinking/text.
//  - Sub-agent emits thinking_start/delta/end THEN text_start/delta/end THEN tool_start.
//  - thinking_delta puts text in event.thinking.text (NOT event.text), per flushThinkBuf.
//  - text_delta puts text in event.text, per flushTextBuf.
//  - Skill tool_param_delta arrives with summary="Execute skill" on the MAIN engine
//    (no agent meta), updating the top-level Skill block before sub-agent events.
const PARENT_ID = 'call_4d2f23cbdc934b0b823b2df3'
const reviewerAgent = () => ({
	parent_tool_use_id: PARENT_ID,
	agent_type: 'Reviewer',
	depth: 1,
})

describe('real-data sub-agent rendering', () => {
	beforeEach(() => {
		sentMessages = []
		listeners = new Set()
	})

	it('replays real /review sequence: Reviewer thinking/text/tools nest under Skill', () => {
		renderChat()

		// query_start — main engine creates streaming assistant
		dispatchEvent({ type: 'query_start' })

		// Main engine produces text before the Skill call (real flushes)
		dispatchEvent({ type: 'text_start' })
		dispatchEvent({ type: 'text_delta', text: '嵌套渲染对齐 TUI。启动 reviewer' })
		dispatchEvent({ type: 'text_delta', text: '：' })
		dispatchEvent({ type: 'text_end' })

		// LLM calls the Skill tool on the MAIN engine (no agent meta).
		// tool_start then a stream of tool_param_delta building the JSON input.
		dispatchEvent({
			type: 'tool_start',
			tool_use: { id: PARENT_ID, name: 'Skill', input: { skill: 'review' } },
		})
		// Real Skill tool_param_delta sequence: each carries summary="Execute skill"
		dispatchEvent({
			type: 'tool_param_delta',
			partial_input: { id: PARENT_ID, name: 'Skill', delta: '{"', summary: 'Execute skill' },
		})
		dispatchEvent({
			type: 'tool_param_delta',
			partial_input: { id: PARENT_ID, name: 'Skill', delta: 'skill', summary: 'Execute skill' },
		})
		dispatchEvent({
			type: 'tool_param_delta',
			partial_input: { id: PARENT_ID, name: 'Skill', delta: '}:"review"}', summary: 'review' },
		})
		dispatchEvent({ type: 'tool_run', tool_use: { id: PARENT_ID, name: 'Skill' } })

		// === Sub-agent (Reviewer) events — all carry agent meta ===
		const agent = reviewerAgent()

		// turn_start (sub-engine runTurns) — arrives FIRST, with agent meta
		dispatchEvent({ type: 'turn_start', agent })

		// thinking_start/delta/end (flushThinkBuf builds new event with Thinking.Text)
		dispatchEvent({ type: 'thinking_start', agent })
		dispatchEvent({
			type: 'thinking_delta',
			thinking: { text: 'The user wants me to review the changes made in the current commit.' },
			agent,
		})
		dispatchEvent({
			type: 'thinking_delta',
			thinking: { text: ' Let me look at the git diff to see what changed.' },
			agent,
		})
		dispatchEvent({ type: 'thinking_end', agent })

		// text_start is a no-op for sub-agents; text_delta lazily creates the block
		dispatchEvent({ type: 'text_start', agent })
		dispatchEvent({
			type: 'text_delta',
			text: "I'll start by checking for plan files and reviewing the git diff.",
			agent,
		})
		dispatchEvent({ type: 'text_end', agent })

		// Sub-agent's own tool (Bash) — tool_start with agent meta
		const subToolID = 'call_ce56c38983d340acb452221c'
		dispatchEvent({
			type: 'tool_start',
			tool_use: { id: subToolID, name: 'Bash', input: { command: 'git log -1' } },
			agent,
		})
		// Real tool_param_delta stream for the sub-agent tool (summary="Execute a bash command")
		dispatchEvent({
			type: 'tool_param_delta',
			partial_input: { id: subToolID, name: 'Bash', delta: '{"', summary: 'Execute a bash command' },
			agent,
		})
		dispatchEvent({
			type: 'tool_param_delta',
			partial_input: { id: subToolID, name: 'Bash', delta: 'command', summary: 'Execute a bash command' },
			agent,
		})
		// tool_output_delta — sub-agent Bash streams output (821 of these in real log)
		dispatchEvent({
			type: 'tool_output_delta',
			tool_result: { tool_use_id: subToolID, display_output: 'commit 29eb157' },
			agent,
		})
		// tool_end
		dispatchEvent({
			type: 'tool_end',
			tool_result: { tool_use_id: subToolID, display_output: 'commit 29eb157', is_error: false },
			agent,
		})

		// Assertions: the top-level Skill block exists
		expect(screen.getByText('Skill')).toBeTruthy()

		// Expand Skill to reveal nested children
		const skillBtn = screen.getByText('Skill').closest('[role="button"]')!
		fireEvent.click(skillBtn)

		// Reviewer's Bash tool should be nested inside the Skill block
		expect(screen.getByText('Bash')).toBeTruthy()

		// Reviewer's text should be nested inside
		expect(screen.getByText(/I'll start by checking for plan files/)).toBeTruthy()
	})

	it('stop during sub-agent does not produce double interrupt messages', () => {
		renderChat()

		// query_start + main text
		dispatchEvent({ type: 'query_start' })
		dispatchEvent({ type: 'text_start' })
		dispatchEvent({ type: 'text_delta', text: 'Starting review' })

		// Skill tool on main engine
		dispatchEvent({
			type: 'tool_start',
			tool_use: { id: PARENT_ID, name: 'Skill', input: { skill: 'review' } },
		})
		dispatchEvent({
			type: 'tool_param_delta',
			partial_input: { id: PARENT_ID, name: 'Skill', delta: '{"skill":"review"}', summary: 'review' },
		})
		dispatchEvent({ type: 'tool_run', tool_use: { id: PARENT_ID, name: 'Skill' } })

		// Sub-agent running
		const agent = reviewerAgent()
		dispatchEvent({ type: 'turn_start', agent })
		dispatchEvent({
			type: 'tool_start',
			tool_use: { id: 'call_subtool', name: 'Bash', input: { command: 'ls' } },
			agent,
		})
		dispatchEvent({
			type: 'tool_param_delta',
			partial_input: { id: 'call_subtool', name: 'Bash', delta: 'ls', summary: 'Execute a bash command' },
			agent,
		})

		// === Real abort sequence after connector fix ===
		// Connector only injects interrupt for main engine query_end (event.Agent == nil).
		// Sub-agent query_end is passed through with agent meta; frontend skips it.

		// 1. Sub-agent query_end itself (aborted: true, agent meta) — frontend skips
		dispatchEvent({ type: 'query_end', aborted: true, agent })

		// 2. Skill tool_end (main engine, no agent)
		dispatchEvent({
			type: 'tool_end',
			tool_result: { tool_use_id: PARENT_ID, display_output: 'null', is_error: false },
		})

		// 3. Main engine injects interrupt text_delta (no agent) — connector only
		//    does this for main engine query_end, not sub-agent
		dispatchEvent({ type: 'text_delta', text: '[Request interrupted by user]' })

		// 4. Main query_end (aborted: true, no agent)
		dispatchEvent({ type: 'query_end', aborted: true })

		const bodyText = document.body.textContent || ''

		// The interrupt message must appear exactly once, not twice.
		const interruptCount = (bodyText.match(/\[Request interrupted by user\]/g) || []).length
		expect(interruptCount).toBe(1)
	})

	it('sub-agent content remains visible under Skill after abort (symptom 1)', () => {
		renderChat()

		dispatchEvent({ type: 'query_start' })
		dispatchEvent({ type: 'text_start' })
		dispatchEvent({ type: 'text_delta', text: 'Starting review' })

		// Skill tool on main engine
		dispatchEvent({
			type: 'tool_start',
			tool_use: { id: PARENT_ID, name: 'Skill', input: { skill: 'review' } },
		})
		dispatchEvent({
			type: 'tool_param_delta',
			partial_input: { id: PARENT_ID, name: 'Skill', delta: '{"skill":"review"}', summary: 'review' },
		})
		dispatchEvent({ type: 'tool_run', tool_use: { id: PARENT_ID, name: 'Skill' } })

		// Sub-agent produces text and a tool BEFORE the user stops
		const agent = reviewerAgent()
		dispatchEvent({ type: 'turn_start', agent })
		dispatchEvent({ type: 'text_start', agent })
		dispatchEvent({
			type: 'text_delta',
			text: 'I will review the latest commit now.',
			agent,
		})
		dispatchEvent({ type: 'text_end', agent })
		dispatchEvent({
			type: 'tool_start',
			tool_use: { id: 'call_subtool_visible', name: 'Bash', input: { command: 'git log -1' } },
			agent,
		})
		dispatchEvent({
			type: 'tool_param_delta',
			partial_input: { id: 'call_subtool_visible', name: 'Bash', delta: 'git log', summary: 'Execute a bash command' },
			agent,
		})

		// Abort sequence (same as the double-interrupt test)
		dispatchEvent({ type: 'text_delta', text: '[Request interrupted by user]' })
		dispatchEvent({ type: 'query_end', aborted: true, agent })
		dispatchEvent({
			type: 'tool_end',
			tool_result: { tool_use_id: PARENT_ID, display_output: 'null', is_error: false },
		})
		dispatchEvent({ type: 'text_delta', text: '[Request interrupted by user]' })
		dispatchEvent({ type: 'query_end', aborted: true })

		// The Skill block must still exist and be expandable
		expect(screen.getByText('Skill')).toBeTruthy()
		const skillBtn = screen.getByText('Skill').closest('[role="button"]')!
		fireEvent.click(skillBtn)

		// Reviewer's text and Bash tool must remain nested and visible
		expect(screen.getByText(/I will review the latest commit/)).toBeTruthy()
		expect(screen.getByText('Bash')).toBeTruthy()
	})
})
