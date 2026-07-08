import { describe, it, expect } from 'vitest'
import {
	appendToolBlock,
	appendTextBlock,
	appendThinkingBlock,
} from './stream_dom'

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

describe('thinking absorption into tool groups', () => {
	function appendThinking(container: HTMLElement): HTMLElement {
		const { p } = appendThinkingBlock(container, Date.now())
		return p.parentElement!
	}

	function toolsContainer(group: Element): HTMLElement {
		return group.querySelector('[data-group-tools]') as HTMLElement
	}

	it('pre-group thinking absorbed on group creation', () => {
		const container = setup()
		appendThinking(container)
		appendTool(container, 'Grep')
		appendTool(container, 'Glob')

		expect(container.children.length).toBe(1)
		const group = container.children[0] as HTMLElement
		expect(group.dataset.toolGroup).toBe('1')

		const tc = toolsContainer(group)
		expect(tc.querySelectorAll('[data-tool-root]').length).toBe(2)
		expect(tc.querySelectorAll('[data-thinking]').length).toBe(1)
		expect((tc.children[0] as HTMLElement).dataset.thinking).toBe('1')
		expect((tc.children[1] as HTMLElement).dataset.toolRoot).toBe('1')
		expect((tc.children[2] as HTMLElement).dataset.toolRoot).toBe('1')
	})

	it('two pre-group thinking blocks absorbed', () => {
		const container = setup()
		appendThinking(container)
		appendThinking(container)
		appendTool(container, 'Grep')
		appendTool(container, 'Glob')

		const group = container.querySelector('[data-tool-group]') as HTMLElement
		const tc = toolsContainer(group)
		expect(tc.querySelectorAll('[data-thinking]').length).toBe(2)
		expect((tc.children[0] as HTMLElement).dataset.thinking).toBe('1')
		expect((tc.children[1] as HTMLElement).dataset.thinking).toBe('1')
		expect((tc.children[2] as HTMLElement).dataset.toolRoot).toBe('1')
		expect((tc.children[3] as HTMLElement).dataset.toolRoot).toBe('1')
	})

	it('inter-tool thinking absorbed on group extension', () => {
		const container = setup()
		appendTool(container, 'Grep')
		appendTool(container, 'Glob')
		appendThinking(container)
		appendTool(container, 'Read')

		const group = container.querySelector('[data-tool-group]') as HTMLElement
		const tc = toolsContainer(group)
		expect(tc.querySelectorAll('[data-tool-root]').length).toBe(3)
		expect(tc.querySelectorAll('[data-thinking]').length).toBe(1)
		expect((tc.children[0] as HTMLElement).dataset.toolName).toBe('Grep')
		expect((tc.children[1] as HTMLElement).dataset.toolName).toBe('Glob')
		expect((tc.children[2] as HTMLElement).dataset.thinking).toBe('1')
		expect((tc.children[3] as HTMLElement).dataset.toolName).toBe('Read')
	})

	it('trailing thinking NOT absorbed', () => {
		const container = setup()
		appendTool(container, 'Grep')
		appendTool(container, 'Glob')
		appendThinking(container)

		expect(container.children.length).toBe(2)
		expect((container.children[0] as HTMLElement).dataset.toolGroup).toBe('1')
		expect((container.children[1] as HTMLElement).dataset.thinking).toBe('1')

		const group = container.children[0] as HTMLElement
		const tc = toolsContainer(group)
		expect(tc.querySelectorAll('[data-tool-root]').length).toBe(2)
		expect(tc.querySelectorAll('[data-thinking]').length).toBe(0)
	})

	it('pre-group thinking absorbed even when text precedes thinking', () => {
		const container = setup()
		appendTextBlock(container, null)
		appendThinking(container)
		appendTool(container, 'Grep')
		appendTool(container, 'Glob')

		expect(container.children.length).toBe(2)
		expect((container.children[0] as HTMLElement).classList.contains('md-body')).toBe(true)
		const group = container.children[1] as HTMLElement
		expect(group.dataset.toolGroup).toBe('1')
		const tc = toolsContainer(group)
		expect(tc.querySelectorAll('[data-tool-root]').length).toBe(2)
		expect(tc.querySelectorAll('[data-thinking]').length).toBe(1)
	})

	it('non-thinking block between thinking and group prevents absorption', () => {
		const container = setup()
		appendThinking(container)
		appendTextBlock(container, null)
		appendTool(container, 'Grep')
		appendTool(container, 'Glob')

		const group = container.querySelector('[data-tool-group]') as HTMLElement
		const tc = toolsContainer(group)
		expect(tc.querySelectorAll('[data-tool-root]').length).toBe(2)
		expect(tc.querySelectorAll('[data-thinking]').length).toBe(0)
		expect(container.querySelectorAll(':scope > [data-thinking]').length).toBe(1)
	})

	it('collapsed group hides absorbed thinking, expanded shows it', () => {
		const container = setup()
		appendThinking(container)
		appendTool(container, 'Grep')
		appendTool(container, 'Glob')

		const group = container.querySelector('[data-tool-group]') as HTMLElement
		const tc = toolsContainer(group)
		expect(tc.style.display).toBe('none')
		expect(tc.querySelectorAll('[data-thinking]').length).toBe(1)

		const header = group.querySelector('[data-group-header]') as HTMLElement
		header.dispatchEvent(new MouseEvent('click', { bubbles: true }))
		expect(tc.style.display).toBe('')
		expect(tc.querySelectorAll('[data-thinking]').length).toBe(1)
	})
})
