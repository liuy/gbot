import { describe, it, expect } from 'vitest'
import {
	appendToolBlock,
	appendTextBlock,
	appendThinkingBlock,
	markToolCollapsible,
} from './stream_dom'
import { isCollapsibleToolName } from '../utils'

function setup() {
	const container = document.createElement('div')
	document.body.appendChild(container)
	return container
}

function appendTool(container: HTMLElement, name: string) {
	return appendToolBlock(container, name, null, isCollapsibleToolName(name))
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

  it('consecutive Bash tools with isSearch=true form a group', () => {
    const container = setup()
    // In real streaming, chat.ts now computes:
    // collapsible = isCollapsibleToolName('Bash') || !!tu.is_search = true
    appendToolBlock(container, 'Bash', null, true)
    appendToolBlock(container, 'Bash', null, true)

    const group = container.querySelector('[data-tool-group]')
    expect(group).toBeTruthy()
    expect(group!.querySelectorAll('[data-tool-root]').length).toBe(2)
  })

  it('thinking blocks between collapsible tools do not break grouping', () => {
    const container = setup()
    appendToolBlock(container, 'Bash', null, true)
    // Insert thinking block between tools (like streaming does)
    appendThinkingBlock(container, 0)
    appendToolBlock(container, 'Bash', null, true)

    const group = container.querySelector('[data-tool-group]')
    expect(group).toBeTruthy()
    expect(group!.querySelectorAll('[data-tool-root]').length).toBe(2)
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
    expect(tc.classList.contains('hidden')).toBe(true)
    expect(tc.querySelectorAll('[data-thinking]').length).toBe(1)

    const header = group.querySelector('[data-group-header]') as HTMLElement
    header.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(tc.classList.contains('hidden')).toBe(false)
    expect(tc.querySelectorAll('[data-thinking]').length).toBe(1)
  })

  it('inter-tool thinking absorbed on group creation (two tools with thinking between)', () => {
    const container = setup()
    appendTool(container, 'Grep')
    appendThinking(container)
    appendTool(container, 'Glob')

    const group = container.querySelector('[data-tool-group]') as HTMLElement
    expect(group).toBeTruthy()
    const tc = toolsContainer(group)
    expect(tc.querySelectorAll('[data-tool-root]').length).toBe(2)
    expect(tc.querySelectorAll('[data-thinking]').length).toBe(1)
    expect((tc.children[0] as HTMLElement).dataset.toolName).toBe('Grep')
    expect((tc.children[1] as HTMLElement).dataset.thinking).toBe('1')
    expect((tc.children[2] as HTMLElement).dataset.toolName).toBe('Glob')
  })

  it('markToolCollapsible absorbs inter-tool thinking', () => {
    // Simulate streaming: tools are not collapsible at creation (is_read
    // hasn't arrived yet), and markToolCollapsible is called retroactively.
    const container = setup()
    const t1 = appendToolBlock(container, 'Grep', null, false)
    appendThinkingBlock(container, 0)
    const t2 = appendToolBlock(container, 'Grep', null, false)

    // is_read arrives for both tools — mark retroactively.
    markToolCollapsible(t1.root)
    markToolCollapsible(t2.root)

    const group = container.querySelector('[data-tool-group]') as HTMLElement
    expect(group).toBeTruthy()
    const tc = group.querySelector('[data-group-tools]') as HTMLElement
    expect(tc.querySelectorAll('[data-thinking]').length).toBe(1)
    expect(tc.querySelectorAll('[data-tool-root]').length).toBe(2)
  })
})
