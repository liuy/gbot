import { describe, it, expect } from 'vitest'
import { render, fireEvent } from '@testing-library/react'
import { readFileSync } from 'fs'
import { resolve } from 'path'
import MessageComponent from './MessageComponent'
import type { ChatMessage, ToolEntry } from '../model'

const baseMsg: ChatMessage = {
	id: 'a1',
	role: 'assistant',
	textChunks: [{ eventIndex: 0, text: 'hello' }],
	thinking: [],
	tools: [],
	nextEventIndex: 1,
	usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
	error: '',
	status: 'done',
	startedAt: 0,
}

describe('MessageComponent layout', () => {
	it('assistant content div has min-w-0 to allow inner overflow scroll', () => {
		const { container } = render(<MessageComponent message={baseMsg} />)
		const grid = container.querySelector('[class*="grid"]')
		expect(grid).toBeTruthy()
		const contentDiv = grid!.children[1] as HTMLElement
		expect(contentDiv.className).toContain('min-w-0')
	})

	it('user content div has min-w-0 to allow inner overflow scroll', () => {
		const userMsg = { ...baseMsg, id: 'u1', role: 'user' as const }
		const { container } = render(<MessageComponent message={userMsg} />)
		const grid = container.querySelector('[class*="grid"]')
		expect(grid).toBeTruthy()
		const contentDiv = grid!.children[1] as HTMLElement
		expect(contentDiv.className).toContain('min-w-0')
	})
})

describe('ChatInterface tool duration', () => {
	// TUI uses perceived wall-clock time (time.Since(startedAt)) for streaming
	// tool duration, NOT [Tool spent Xs] prefix. Web must match — tool_end
	// handler should set timingNs from Date.now() - startedAt.
	it('tool_end uses wall-clock time, not prefix parsing', () => {
		const src = readFileSync(resolve(__dirname, './ChatInterface.tsx'), 'utf-8')
		expect(src).toContain('Date.now() - tool.startedAt')
		expect(src).not.toContain('parseDurationFromOutput')
	})
})

function mkTool(over: Partial<ToolEntry> & Pick<ToolEntry, 'id' | 'eventIndex' | 'name'>): ToolEntry {
	return {
		summary: '',
		isSearch: false,
		isRead: false,
		isList: false,
		isLsp: false,
		state: 'done',
		timingNs: 0,
		displayOutput: '',
		startedAt: 0,
		...over,
	}
}

function assistantMsg(over: Partial<ChatMessage>): ChatMessage {
	return {
		...baseMsg,
		id: 'a2',
		textChunks: [],
		...over,
	}
}

// Collapsed ToolGroup children aren't rendered, so a group is detected by its
// summary <button> ("2 Searches, 1 Read"). A bare collapsible tool renders its
// name via ToolRenderer. Non-collapsible tools always render bare.
describe('MessageComponent tool grouping', () => {
	it('groups two consecutive collapsible tools into a ToolGroup', () => {
		const msg = assistantMsg({
			tools: [
				mkTool({ id: 't1', eventIndex: 0, name: 'Grep', isSearch: true }),
				mkTool({ id: 't2', eventIndex: 1, name: 'Read', isRead: true }),
			],
		})
		const { container } = render(<MessageComponent message={msg} />)
		const buttons = container.querySelectorAll('button[type="button"]')
		expect(buttons.length).toBe(1)
		expect(buttons[0].textContent).toContain('1 Search')
		expect(buttons[0].textContent).toContain('1 Read')
	})

	it('renders a single collapsible tool bare (no group header)', () => {
		const msg = assistantMsg({
			tools: [mkTool({ id: 't1', eventIndex: 0, name: 'Grep', isSearch: true })],
		})
		const { container } = render(<MessageComponent message={msg} />)
		expect(container.querySelectorAll('button[type="button"]').length).toBe(0)
		expect(container.textContent).toContain('Grep')
	})

	it('non-collapsible tool breaks the group and renders bare', () => {
		const msg = assistantMsg({
			tools: [
				mkTool({ id: 't1', eventIndex: 0, name: 'Grep', isSearch: true }),
				mkTool({ id: 't2', eventIndex: 1, name: 'Bash' }),
				mkTool({ id: 't3', eventIndex: 2, name: 'Read', isRead: true }),
			],
		})
		const { container } = render(<MessageComponent message={msg} />)
		// t1 alone is a single-tool buffer -> bare; t3 alone -> bare. No group.
		expect(container.querySelectorAll('button[type="button"]').length).toBe(0)
		expect(container.textContent).toContain('Grep')
		expect(container.textContent).toContain('Bash')
		expect(container.textContent).toContain('Read')
	})

	it('non-empty text breaks the group', () => {
		const msg = assistantMsg({
			tools: [
				mkTool({ id: 't1', eventIndex: 0, name: 'Grep', isSearch: true }),
				mkTool({ id: 't2', eventIndex: 1, name: 'Read', isRead: true }),
				mkTool({ id: 't3', eventIndex: 3, name: 'Glob', isList: true }),
				mkTool({ id: 't4', eventIndex: 4, name: 'Read', isRead: true }),
			],
			textChunks: [{ eventIndex: 2, text: 'thinking about results' }],
		})
		const { container } = render(<MessageComponent message={msg} />)
		// Two groups of 2 each -> two summary buttons.
		const buttons = container.querySelectorAll('button[type="button"]')
		expect(buttons.length).toBe(2)
		expect(container.textContent).toContain('thinking about results')
	})

	it('thinking does not break the group', () => {
		const msg = assistantMsg({
			tools: [
				mkTool({ id: 't1', eventIndex: 0, name: 'Grep', isSearch: true }),
				mkTool({ id: 't2', eventIndex: 2, name: 'Read', isRead: true }),
			],
			thinking: [{ eventIndex: 1, text: 'reasoning', durationNs: 0, active: false, startedAt: 0 }],
		})
		const { container } = render(<MessageComponent message={msg} />)
		// Still one group of 2 despite the thinking between them.
		const buttons = container.querySelectorAll('button[type="button"]')
		expect(buttons.length).toBe(1)
		expect(buttons[0].textContent).toContain('1 Search')
		expect(buttons[0].textContent).toContain('1 Read')
		// Thinking renders (collapsed shows only its label, not the text body).
		expect(container.textContent).toContain('Thought for')
	})

	it('empty text does not break the group', () => {
		const msg = assistantMsg({
			tools: [
				mkTool({ id: 't1', eventIndex: 0, name: 'Grep', isSearch: true }),
				mkTool({ id: 't2', eventIndex: 2, name: 'Read', isRead: true }),
			],
			textChunks: [{ eventIndex: 1, text: '' }],
		})
		const { container } = render(<MessageComponent message={msg} />)
		const buttons = container.querySelectorAll('button[type="button"]')
		expect(buttons.length).toBe(1)
		expect(buttons[0].textContent).toContain('1 Search')
		expect(buttons[0].textContent).toContain('1 Read')
	})

	it('expanding a group reveals its child tools', () => {
		const msg = assistantMsg({
			tools: [
				mkTool({ id: 't1', eventIndex: 0, name: 'Grep', isSearch: true, summary: 'pattern foo' }),
				mkTool({ id: 't2', eventIndex: 1, name: 'Read', isRead: true, summary: 'main.go' }),
			],
		})
		const { container } = render(<MessageComponent message={msg} />)
		// Collapsed: child tool summaries not visible.
		expect(container.textContent).not.toContain('pattern foo')
		const btn = container.querySelector('button[type="button"]')!
		fireEvent.click(btn)
		// Expanded: both child tools render.
		expect(container.textContent).toContain('pattern foo')
		expect(container.textContent).toContain('main.go')
	})
})
