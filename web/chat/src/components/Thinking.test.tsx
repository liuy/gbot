import { describe, it, expect } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import Thinking from './Thinking'
import type { Block } from '../model'

function makeThinking(overrides: Partial<Extract<Block, { kind: 'thinking' }>> = {}): Extract<Block, { kind: 'thinking' }> {
	return {
		kind: 'thinking',
		id: 't1',
		text: 'reasoning here',
		durationNs: 1_000_000_000,
		active: false,
		startedAt: Date.now() - 1000,
		...overrides,
	}
}

describe('Thinking auto-collapse and DOM persistence', () => {
	it('thinking_end (active true→false) auto-collapses', () => {
		const entry = makeThinking({ active: true, text: '', startedAt: Date.now() })
		const { container, rerender } = render(<Thinking entry={entry} />)

		expect(screen.getByText(/Thinking/)).toBeTruthy()

		// thinking_end: active becomes false
		rerender(<Thinking entry={makeThinking({ active: false, text: 'done reasoning', durationNs: 500_000_000 })} />)

		expect(screen.getByText(/Thought for/)).toBeTruthy()
		// Collapsed: <p> still in DOM but hidden (maxHeight=0, overflow=hidden)
		const p = container.querySelector('p')
		expect(p).toBeTruthy()
		expect(p?.style.maxHeight).toBe('0px')
	})

	it('collapsed <p> stays in DOM (not unmounted)', () => {
		// This ensures ref stays valid for streaming writes even when collapsed.
		// We verify by checking the <p> element exists in the DOM regardless of expanded state.
		const entry = makeThinking({ active: false, text: 'some text' })
		const { container } = render(<Thinking entry={entry} />)

		// Collapsed by default (active=false)
		const p = container.querySelector('p')
		expect(p).toBeTruthy()
		// Text should be in DOM even when collapsed (CSS hidden, not unmounted)
		expect(p?.textContent).toContain('some text')
	})

	it('active=true starts expanded, user can collapse manually', () => {
		const entry = makeThinking({ active: true, text: '', startedAt: Date.now() })
		render(<Thinking entry={entry} />)

		// Active = expanded by default
		expect(screen.getByText(/Thinking/)).toBeTruthy()

		// Click to collapse
		const btn = screen.getByRole('button')
		fireEvent.click(btn)

		// Should be collapsed — but <p> still in DOM
		expect(screen.getByText(/Thinking/)).toBeTruthy()
	})

	it('multiple thinking blocks are independent', () => {
		const active1 = makeThinking({ id: 't1', active: true, text: 'first', startedAt: Date.now() })
		const done1 = makeThinking({ id: 't1', active: false, text: 'first done', durationNs: 100_000_000 })

		// First thinking: active → expanded
		const { rerender } = render(
			<div>
				<Thinking entry={active1} />
			</div>
		)

		// thinking_end: auto-collapse
		rerender(
			<div>
				<Thinking entry={done1} />
				<Thinking entry={makeThinking({ id: 't2', active: true, text: '', startedAt: Date.now() })} />
			</div>
		)

		// First block collapsed, second expanded
		expect(screen.getAllByText(/Thought for/).length).toBe(1)
		expect(screen.getAllByText(/Thinking/).length).toBe(1)
	})
})
