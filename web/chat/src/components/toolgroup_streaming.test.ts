import { describe, it, expect } from 'vitest'
import {
	appendToolBlock,
} from './streamDom'

function setup() {
	const container = document.createElement('div')
	document.body.appendChild(container)
	return container
}

// streaming 期间 ToolGroup 必须有摘要行和折叠——和 query_end 后的
// MessageComponent.ToolGroup 视觉一致。
describe('ToolGroup streaming rendering', () => {
	it('group has summary header with tool count', () => {
		const container = setup()
		appendToolBlock(container, 'Grep', null, true)
		appendToolBlock(container, 'Glob', null, true)

		const group = container.querySelector('[data-tool-group]') as HTMLElement
		expect(group).toBeTruthy()

		// Summary: "2 Searches" (matches ToolGroup.tsx nounFor() + plural)
		const summary = group.querySelector('[data-group-summary]')
		expect(summary).toBeTruthy()
		expect(summary!.textContent).toContain('2')
		expect(summary!.textContent).toContain('Search')
	})

	it('group summary updates when third tool added', () => {
		const container = setup()
		appendToolBlock(container, 'Grep', null, true)
		appendToolBlock(container, 'Glob', null, true)
		appendToolBlock(container, 'Read', null, true)

		const summary = container.querySelector('[data-group-summary]')
		expect(summary!.textContent).toContain('2')
		expect(summary!.textContent).toContain('Search')
		expect(summary!.textContent).toContain('1')
		expect(summary!.textContent).toContain('Read')
	})

	it('thinking does NOT break a group of collapsible tools', () => {
		const container = setup()
		// web, web, thinking, web — thinking should not break the group
		appendToolBlock(container, 'Web', null, true)
		appendToolBlock(container, 'Web', null, true)
		// Simulate thinking block with data-thinking
		const thinking = document.createElement('div')
		thinking.dataset.thinking = '1'
		container.appendChild(thinking)
		appendToolBlock(container, 'Web', null, true)

		// All 3 Web tools should be in one group
		const groups = container.querySelectorAll('[data-tool-group]')
		expect(groups.length).toBe(1)
		expect(groups[0].querySelectorAll('[data-tool-root]').length).toBe(3)
		// Summary: "3 Webs"
		const summary = groups[0].querySelector('[data-group-summary]')
		expect(summary!.textContent).toContain('3')
		expect(summary!.textContent).toContain('Web')
	})

	it('group header has fold/unfold toggle', () => {
		const container = setup()
		appendToolBlock(container, 'Grep', null, true)
		appendToolBlock(container, 'Glob', null, true)

		const group = container.querySelector('[data-tool-group]') as HTMLElement
		const header = group.querySelector('[data-group-header]')
		expect(header).toBeTruthy()

		// Click header should toggle visibility of tools inside
		const toolsContainer = group.querySelector('[data-group-tools]') as HTMLElement
		expect(toolsContainer.style.display).toBe('none') // default collapsed

		header!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
		expect(toolsContainer.style.display).toBe('') // expanded

		header!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
		expect(toolsContainer.style.display).toBe('none') // collapsed again
	})

	it('running tool in group shows running state in dot', () => {
		const container = setup()
		appendToolBlock(container, 'Grep', null, true)
		appendToolBlock(container, 'Glob', null, true)

		const group = container.querySelector('[data-tool-group]') as HTMLElement
		const dot = group.querySelector('[data-group-dot]')
		expect(dot!.classList.contains('heartbeat')).toBe(true)
		expect(dot!.textContent).toBe('●')
	})
})
