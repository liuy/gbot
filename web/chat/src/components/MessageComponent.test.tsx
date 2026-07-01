import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import { readFileSync } from 'fs'
import { resolve } from 'path'
import MessageComponent from './MessageComponent'
import type { ChatMessage } from '../model'

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

describe('ChatInterface scroll container', () => {
	// Read the source file to verify the scroll container has overflow-x-hidden
	// preventing horizontal scrollbar on the main chat area.
	it('scroll container has overflow-x-hidden', () => {
		const src = readFileSync(resolve(__dirname, './ChatInterface.tsx'), 'utf-8')
		expect(src).toContain('overflow-x-hidden')
	})
})
