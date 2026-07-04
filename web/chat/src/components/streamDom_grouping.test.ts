import { describe, it, expect } from 'vitest'
import {
	appendToolBlock,
	appendTextBlock,
} from './streamDom'

function setup() {
	const container = document.createElement('div')
	document.body.appendChild(container)
	return container
}

function isCollapsible(name: string): boolean {
	return name === 'Grep' || name === 'Glob' || name === 'Read' || name === 'Lsp' || name === 'Web'
}

function appendTool(container: HTMLElement, name: string) {
	return appendToolBlock(container, name, null, isCollapsible(name))
}

describe('collapsible tool grouping during streaming', () => {
	it('single collapsible tool renders standalone (no group)', () => {
		const container = setup()
		appendTool(container, 'Grep')
		expect(container.lastElementChild?.getAttribute('data-tool-root')).toBeTruthy()
		expect(container.querySelector('[data-tool-group]')).toBeNull()
	})

	it('two consecutive collapsible tools form a group', () => {
		const container = setup()
		appendTool(container, 'Grep')
		appendTool(container, 'Glob')

		const group = container.querySelector('[data-tool-group]')
		expect(group).toBeTruthy()
		const tools = group!.querySelectorAll('[data-tool-root]')
		expect(tools.length).toBe(2)
	})

	it('three consecutive collapsible tools append to existing group', () => {
		const container = setup()
		appendTool(container, 'Grep')
		appendTool(container, 'Glob')
		appendTool(container, 'Read')

		const group = container.querySelector('[data-tool-group]')
		expect(group).toBeTruthy()
		expect(group!.querySelectorAll('[data-tool-root]').length).toBe(3)
	})

	it('non-collapsible tool breaks the group', () => {
		const container = setup()
		appendTool(container, 'Grep')
		appendTool(container, 'Glob')
		appendTool(container, 'Bash')

		const groups = container.querySelectorAll('[data-tool-group]')
		expect(groups.length).toBe(1)
		const lastChild = container.lastElementChild
		expect(lastChild?.getAttribute('data-tool-root')).toBeTruthy()
		expect(lastChild?.getAttribute('data-tool-group')).toBeNull()
	})

	it('text block between collapsible tools prevents grouping', () => {
		const container = setup()
		appendTool(container, 'Grep')
		appendTextBlock(container, null)
		appendTool(container, 'Glob')

		expect(container.querySelector('[data-tool-group]')).toBeNull()
	})

	it('non-collapsible then collapsible: no group', () => {
		const container = setup()
		appendTool(container, 'Bash')
		appendTool(container, 'Grep')

		expect(container.querySelector('[data-tool-group]')).toBeNull()
	})

	it('collapsible tools after non-collapsible form new group', () => {
		const container = setup()
		appendTool(container, 'Bash')
		appendTool(container, 'Grep')
		appendTool(container, 'Glob')

		const group = container.querySelector('[data-tool-group]')
		expect(group).toBeTruthy()
		expect(group!.querySelectorAll('[data-tool-root]').length).toBe(2)
	})
})
