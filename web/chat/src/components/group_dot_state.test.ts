import { describe, it, expect } from 'vitest'
import {
	appendToolBlock,
	finishTool,
} from './stream_dom'

function setup() {
	const container = document.createElement('div')
	document.body.appendChild(container)
	return container
}

describe('group dot state after tools finish', () => {
	it('group dot turns green when all tools finish', () => {
		const container = setup()
		const h1 = appendToolBlock(container, 'Web', null, true)
		const h2 = appendToolBlock(container, 'Web', null, true)

		const group = container.querySelector('[data-tool-group]') as HTMLElement
		expect(group).toBeTruthy()

		// While running: dot has heartbeat + text-white
		const dot = group.querySelector('[data-group-dot]') as HTMLElement
		expect(dot.classList.contains('heartbeat')).toBe(true)
		expect(dot.classList.contains('text-white')).toBe(true)

		// Finish both tools
		finishTool(h1, { isError: false, durationNs: 1000000000, output: 'done1' })
		finishTool(h2, { isError: false, durationNs: 2000000000, output: 'done2' })

		// After all done: dot should be green, no heartbeat
		expect(dot.classList.contains('heartbeat')).toBe(false)
		expect(dot.classList.contains('text-white')).toBe(false)
		expect(dot.classList.contains('text-green')).toBe(true)
	})

	it('group dot matches individual tool dot style', () => {
		const container = setup()
		const h1 = appendToolBlock(container, 'Web', null, true)
		appendToolBlock(container, 'Web', null, true)

		const group = container.querySelector('[data-tool-group]') as HTMLElement
		const groupDot = group.querySelector('[data-group-dot]') as HTMLElement

		// Both should have text-white + heartbeat while running
		expect(groupDot.classList.contains('text-white')).toBe(true)
		expect(groupDot.classList.contains('heartbeat')).toBe(true)
		expect(h1.dot.classList.contains('text-white')).toBe(true)
		expect(h1.dot.classList.contains('heartbeat')).toBe(true)
		expect(groupDot.className).toBe(h1.dot.className)
	})
})
