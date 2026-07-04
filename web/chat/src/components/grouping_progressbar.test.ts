import { describe, it, expect } from 'vitest'
import {
	appendToolBlock,
	appendProgressBar,
} from './streamDom'

function setup() {
	const container = document.createElement('div')
	document.body.appendChild(container)
	return container
}

// Progress bar can land between two collapsible tools during streaming.
// appendToolBlock uses `before.previousElementSibling` to find the previous
// tool, so the second tool still groups with the first even with a progress
// bar in between.
describe('grouping with progress bar between tools', () => {
	it('two collapsible tools group even when progress bar sits between them', () => {
		const container = setup()

		// First Grep
		appendToolBlock(container, 'Grep', null, true)

		// Progress bar created between tools (simulates useEffect firing)
		appendProgressBar(container)

		// Second Grep — before=progressBar
		const bar = container.lastElementChild as HTMLElement
		appendToolBlock(container, 'Grep', bar, true)

		const groups = container.querySelectorAll('[data-tool-group]')
		expect(groups.length).toBe(1)
		expect(groups[0].querySelectorAll('[data-tool-root]').length).toBe(2)
	})
})
