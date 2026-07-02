import { describe, it, expect } from 'vitest'
import { render, fireEvent } from '@testing-library/react'
import { readFileSync } from 'fs'
import { resolve } from 'path'
import MessageComponent from './MessageComponent'
import type { ChatMessage, Block } from '../model'

const baseMsg: ChatMessage = {
	id: 'a1',
	role: 'assistant',
	blocks: [{ kind: 'text', id: 'txt-0', text: 'hello' }],
	usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
	error: '',
	status: 'done',
	startedAt: 0,
}

describe('MessageComponent layout', () => {
	it('uses grid 3-col layout: [avatar | content | avatar]', () => {
		const { container } = render(<MessageComponent message={baseMsg} />)
		const grid = container.querySelector('[class*="grid"]')
		expect(grid).toBeTruthy()
		expect(grid!.className).toContain('grid-cols-[1.25rem_1fr_1.25rem]')
		expect(grid!.className).toContain('gap-x-1.5')
		// 3 children: left avatar, content, right avatar
		expect(grid!.children.length).toBe(3)
	})

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

	it('G avatar: rounded-md + blue→violet gradient + font-bold', () => {
		const { container } = render(<MessageComponent message={baseMsg} />)
		const leftAvatar = container.querySelector('[class*="grid"]')!.children[0] as HTMLElement
		expect(leftAvatar.className).toContain('rounded-md')
		expect(leftAvatar.className).toContain('from-blue')
		expect(leftAvatar.className).toContain('to-violet')
		expect(leftAvatar.className).toContain('font-bold')
		expect(leftAvatar.textContent).toBe('G')
	})

	it('U avatar: rounded-md + t2→t3 gradient + SVG person icon (not letter)', () => {
		const userMsg = { ...baseMsg, id: 'u1', role: 'user' as const }
		const { container } = render(<MessageComponent message={userMsg} />)
		const rightAvatar = container.querySelector('[class*="grid"]')!.children[2] as HTMLElement
		expect(rightAvatar.className).toContain('rounded-md')
		expect(rightAvatar.className).toContain('from-t2')
		expect(rightAvatar.className).toContain('to-t3')
		// SVG person icon, not letter "U"
		expect(rightAvatar.querySelector('svg')).toBeTruthy()
		expect(rightAvatar.textContent).not.toContain('U')
	})

	it('assistant right column is empty (no leaked avatar)', () => {
		const { container } = render(<MessageComponent message={baseMsg} />)
		const rightCol = container.querySelector('[class*="grid"]')!.children[2] as HTMLElement
		expect(rightCol.className).toBe('')
		expect(rightCol.children.length).toBe(0)
	})

	it('user left column is empty (no leaked avatar)', () => {
		const userMsg = { ...baseMsg, id: 'u1', role: 'user' as const }
		const { container } = render(<MessageComponent message={userMsg} />)
		const leftCol = container.querySelector('[class*="grid"]')!.children[0] as HTMLElement
		expect(leftCol.className).toBe('')
		expect(leftCol.children.length).toBe(0)
	})

	it('G avatar font size is 11px (not 9px)', () => {
		const { container } = render(<MessageComponent message={baseMsg} />)
		const avatar = container.querySelector('[class*="grid"]')!.children[0] as HTMLElement
		expect(avatar.className).toContain('text-[11px]')
		expect(avatar.className).not.toContain('text-[9px]')
	})
})

describe('ChatInterface tool duration', () => {
	// TUI uses perceived wall-clock time (time.Since(startedAt)) for streaming
	// tool duration, NOT [Tool spent Xs] prefix. Web must match — tool_end
	// handler should set timingNs from Date.now() - startedAt.
	it('tool_end uses wall-clock time, not prefix parsing', () => {
		const src = readFileSync(resolve(__dirname, './ChatInterface.tsx'), 'utf-8')
		expect(src).toContain('Date.now() - b.startedAt')
		expect(src).not.toContain('parseDurationFromOutput')
	})
})

describe('CSS design tokens (frozen)', () => {
	const css = readFileSync(resolve(__dirname, '../index.css'), 'utf-8')

	it('glass alpha is 0.15 (not 0.3)', () => {
		expect(css).toContain('rgba(6, 8, 15, 0.15)')
		expect(css).not.toContain('rgba(6, 8, 15, 0.3)')
	})

	it('glass-header alpha is 0.1', () => {
		expect(css).toContain('rgba(6, 8, 15, 0.1)')
	})

	it('card-bg alpha is 0.3', () => {
		expect(css).toContain('rgba(12, 16, 24, 0.3)')
	})

	it('violet color is #9D5CFF (not Tailwind purple-500)', () => {
		expect(css).toContain('--color-violet: #9D5CFF')
	})

	it('ink/ink2/ink3 colors defined', () => {
		expect(css).toContain('--color-ink: #06080F')
		expect(css).toContain('--color-ink2: #0C1018')
		expect(css).toContain('--color-ink3: #121826')
	})

	it('glass has -webkit-backdrop-filter for Android WebView', () => {
		expect(css).toContain('-webkit-backdrop-filter: blur(16px) saturate(1.2)')
	})

	it('glass-solid has blur(20px) saturate(1.4)', () => {
		expect(css).toContain('blur(20px) saturate(1.4)')
	})

	it('glow-blue has alpha 0.5', () => {
		expect(css).toContain('rgba(0,180,255,0.5)')
	})

	it('vertical scrollbar is hidden (width: 0)', () => {
		expect(css).toContain('width: 0')
	})

	it('body uses gradient background', () => {
		expect(css).toContain('linear-gradient(180deg, rgba(4,6,12,0.98)')
	})
})

describe('App layout (frozen)', () => {
	it('ChatInterface has overflow-x-hidden to prevent horizontal scrollbar', () => {
		const src = readFileSync(resolve(__dirname, '../components/ChatInterface.tsx'), 'utf-8')
		expect(src).toContain('overflow-x-hidden')
		expect(src).toContain('overflow-y-auto')
	})
})

function mkTool(over: Partial<Extract<Block, { kind: 'tool' }>> & Pick<Extract<Block, { kind: 'tool' }>, 'id' | 'name'>): Extract<Block, { kind: 'tool' }> {
	return {
		kind: 'tool',
		summary: '',
		isSearch: false,
		isRead: false,
		isList: false,
		isLsp: false,
		isWeb: false,
		state: 'done',
		timingNs: 0,
		displayOutput: '',
		startedAt: 0,
		...over,
	}
}

function assistantMsg(over: Pick<ChatMessage, 'blocks'>): ChatMessage {
	return {
		...baseMsg,
		id: 'a2',
		blocks: over.blocks,
	}
}

// Collapsed ToolGroup children aren't rendered, so a group is detected by its
// summary <button> ("2 Searches, 1 Read"). A bare collapsible tool renders its
// name via ToolRenderer. Non-collapsible tools always render bare.
describe('MessageComponent tool grouping', () => {
	it('groups two consecutive collapsible tools into a ToolGroup', () => {
		const msg = assistantMsg({
			blocks: [
				mkTool({ id: 't1', name: 'Grep', isSearch: true }),
				mkTool({ id: 't2', name: 'Read', isRead: true }),
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
			blocks: [mkTool({ id: 't1', name: 'Grep', isSearch: true })],
		})
		const { container } = render(<MessageComponent message={msg} />)
		expect(container.querySelectorAll('button[type="button"]').length).toBe(0)
		expect(container.textContent).toContain('Grep')
	})

	it('non-collapsible tool breaks the group and renders bare', () => {
		const msg = assistantMsg({
			blocks: [
				mkTool({ id: 't1', name: 'Grep', isSearch: true }),
				mkTool({ id: 't2', name: 'Bash' }),
				mkTool({ id: 't3', name: 'Read', isRead: true }),
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
			blocks: [
				mkTool({ id: 't1', name: 'Grep', isSearch: true }),
				mkTool({ id: 't2', name: 'Read', isRead: true }),
				{ kind: 'text', id: 'txt-1', text: 'thinking about results' },
				mkTool({ id: 't3', name: 'Glob', isList: true }),
				mkTool({ id: 't4', name: 'Read', isRead: true }),
			],
		})
		const { container } = render(<MessageComponent message={msg} />)
		// Two groups of 2 each -> two summary buttons.
		const buttons = container.querySelectorAll('button[type="button"]')
		expect(buttons.length).toBe(2)
		expect(container.textContent).toContain('thinking about results')
	})

	it('thinking does not break the group', () => {
		const msg = assistantMsg({
			blocks: [
				mkTool({ id: 't1', name: 'Grep', isSearch: true }),
				{ kind: 'thinking', id: 'th-1', text: 'reasoning', durationNs: 0, active: false, startedAt: 0 },
				mkTool({ id: 't2', name: 'Read', isRead: true }),
			],
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
			blocks: [
				mkTool({ id: 't1', name: 'Grep', isSearch: true }),
				{ kind: 'text', id: 'txt-1', text: '' },
				mkTool({ id: 't2', name: 'Read', isRead: true }),
			],
		})
		const { container } = render(<MessageComponent message={msg} />)
		const buttons = container.querySelectorAll('button[type="button"]')
		expect(buttons.length).toBe(1)
		expect(buttons[0].textContent).toContain('1 Search')
		expect(buttons[0].textContent).toContain('1 Read')
	})

	it('expanding a group reveals its child tools', () => {
		const msg = assistantMsg({
			blocks: [
				mkTool({ id: 't1', name: 'Grep', isSearch: true, summary: 'pattern foo' }),
				mkTool({ id: 't2', name: 'Read', isRead: true, summary: 'main.go' }),
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
