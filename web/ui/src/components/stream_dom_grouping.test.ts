import { describe, it, expect } from 'vitest'
import {
	appendToolBlock,
	appendTextBlock,
	appendThinkingBlock,
	markToolCollapsible,
	finishTool,
	writeThinkingText,
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

	it('absorbed pre-group thinking keeps document order', () => {
		const container = setup()
		const t1 = appendThinkingBlock(container, Date.now())
		writeThinkingText(t1.p, 'first thought')
		const t2 = appendThinkingBlock(container, Date.now())
		writeThinkingText(t2.p, 'second thought')
		appendTool(container, 'Grep')
		appendTool(container, 'Glob')

		const group = container.querySelector('[data-tool-group]') as HTMLElement
		const tc = toolsContainer(group)
		// The leading-thinking walk collects newest-first and must reverse to
		// document order before re-parenting, or the transcript reads backwards.
		// Direct child: the markdown render nests its own <p> inside.
		const ps = tc.querySelectorAll('[data-thinking] > p')
		expect(ps[0]?.textContent).toContain('first thought')
		expect(ps[1]?.textContent).toContain('second thought')
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

describe('group header interactions', () => {
  it('group chevron rotates to match expansion state on header clicks', () => {
    const container = setup()
    appendTool(container, 'Grep')
    appendTool(container, 'Glob')

    const group = container.querySelector('[data-tool-group]') as HTMLElement
    const tc = group.querySelector('[data-group-tools]') as HTMLElement
    const svg = group.querySelector('[data-group-chevron] svg') as SVGElement
    expect(tc.classList.contains('hidden')).toBe(true)
    expect(svg.getAttribute('class')).not.toContain('rotate-90')

    const header = group.querySelector('[data-group-header]') as HTMLElement
    header.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(tc.classList.contains('hidden')).toBe(false)
    expect(svg.getAttribute('class')).toContain('rotate-90')

    header.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(tc.classList.contains('hidden')).toBe(true)
    expect(svg.getAttribute('class')).not.toContain('rotate-90')
  })

  it('a tool joining an existing group keeps its own expand/collapse listeners', () => {
    const container = setup()
    appendTool(container, 'Grep')
    appendTool(container, 'Glob')
    const h3 = appendTool(container, 'Read') // group-extension path

    expect(h3.body.classList.contains('hidden')).toBe(true)
    h3.header.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(h3.body.classList.contains('hidden')).toBe(false)
    expect(h3.childrenContainer.classList.contains('hidden')).toBe(false)
    h3.body.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(h3.body.classList.contains('hidden')).toBe(true)
    expect(h3.childrenContainer.classList.contains('hidden')).toBe(true)
  })
  it('a tool present at group creation keeps its own expand/collapse listeners', () => {
    const container = setup()
    appendTool(container, 'Grep')
    const h2 = appendTool(container, 'Glob') // group-creation path

    expect(h2.body.classList.contains('hidden')).toBe(true)
    h2.header.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(h2.body.classList.contains('hidden')).toBe(false)
    expect(h2.childrenContainer.classList.contains('hidden')).toBe(false)
    h2.body.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(h2.body.classList.contains('hidden')).toBe(true)
    expect(h2.childrenContainer.classList.contains('hidden')).toBe(true)
  })
})

describe('group summary and duration', () => {
  function groupEl(container: HTMLElement): HTMLElement {
    return container.querySelector('[data-tool-group]') as HTMLElement
  }

  it('summary lists tool nouns; duration stays blank while any tool runs', () => {
    const container = setup()
    const h1 = appendTool(container, 'Grep')
    const h2 = appendTool(container, 'Glob')
    const group = groupEl(container)
    expect(group.querySelector('[data-group-summary]')!.textContent).toBe('2 Searches')

    finishTool(h1, { isError: false, durationNs: 1_000_000_000, output: '' })
    const dur = group.querySelector('[data-group-duration]') as HTMLElement
    // h2 still running → no duration yet
    expect(dur.textContent).toBe('')

    finishTool(h2, { isError: false, durationNs: 2_000_000_000, output: '' })
    expect(dur.textContent).toBe(' 3s')
  })

  it('duration stays blank when the summed timing is zero', () => {
    const container = setup()
    const h1 = appendTool(container, 'Grep')
    const h2 = appendTool(container, 'Glob')
    finishTool(h1, { isError: false, durationNs: 0, output: '' })
    finishTool(h2, { isError: false, durationNs: 0, output: '' })
    const dur = groupEl(container).querySelector('[data-group-duration]') as HTMLElement
    expect(dur.textContent).toBe('')
  })

  it('duration clears when a new running tool joins the done group', () => {
    const container = setup()
    const h1 = appendTool(container, 'Grep')
    const h2 = appendTool(container, 'Glob')
    finishTool(h1, { isError: false, durationNs: 1_000_000_000, output: '' })
    finishTool(h2, { isError: false, durationNs: 2_000_000_000, output: '' })
    const dur = groupEl(container).querySelector('[data-group-duration]') as HTMLElement
    expect(dur.textContent).toBe(' 3s')

    appendTool(container, 'Read') // running tool joins → duration blanks again
    expect(dur.textContent).toBe('')
  })

  it('group dot goes green when all done, back to white+heartbeat when a tool joins', () => {
    const container = setup()
    const h1 = appendTool(container, 'Grep')
    const h2 = appendTool(container, 'Glob')
    finishTool(h1, { isError: false, durationNs: 0, output: '' })
    finishTool(h2, { isError: false, durationNs: 0, output: '' })
    const dot = groupEl(container).querySelector('[data-group-dot]') as HTMLElement
    expect(dot.classList.contains('text-green')).toBe(true)
    expect(dot.classList.contains('text-white')).toBe(false)
    expect(dot.classList.contains('heartbeat')).toBe(false)

    appendTool(container, 'Read') // running again
    expect(dot.classList.contains('text-white')).toBe(true)
    expect(dot.classList.contains('heartbeat')).toBe(true)
    expect(dot.classList.contains('text-green')).toBe(false)
  })

  it('updateGroupSummary tolerates a tool root without a name', () => {
    const container = setup()
    const h1 = appendTool(container, 'Grep')
    appendTool(container, 'Glob')
    const group = groupEl(container)
    const tc = group.querySelector('[data-group-tools]') as HTMLElement
    const bare = document.createElement('div')
    bare.dataset.toolRoot = '1'
    tc.appendChild(bare)

    finishTool(h1, { isError: false, durationNs: 0, output: '' })
    // Empty name contributes an empty noun: "1 " — not a placeholder string.
    expect(group.querySelector('[data-group-summary]')!.textContent).toBe('2 Searches, 1 ')
  })

  it('finishTool does not throw when the surrounding group lacks a duration span', () => {
    const container = setup()
    const h = appendTool(container, 'Bash')
    // Hand-built group missing [data-group-duration]: the duration guard in
    // updateGroupSummary must skip instead of dereferencing null.
    const fake = document.createElement('div')
    fake.dataset.toolGroup = '1'
    const summary = document.createElement('span')
    summary.dataset.groupSummary = '1'
    const tc = document.createElement('div')
    tc.dataset.groupTools = '1'
    fake.appendChild(summary)
    fake.appendChild(tc)
    container.replaceChild(fake, h.root)
    tc.appendChild(h.root)

    expect(() => finishTool(h, { isError: false, durationNs: 0, output: '' })).not.toThrow()
    // updateGroupSummary still ran (summary refreshed) — the no-throw is not
    // vacuous.
    expect(summary.textContent).toBe('1 Bash')
  })
})

describe('defensive group-shape fallbacks', () => {
  it('appendToolBlock appends directly into a group element lacking a tools container', () => {
    const container = setup()
    const fake = document.createElement('div')
    fake.dataset.toolGroup = '1'
    container.appendChild(fake)

    const h = appendToolBlock(container, 'Grep', null, true)
    expect(h.root.parentElement).toBe(fake)
    expect(fake.querySelectorAll('[data-tool-root]').length).toBe(1)
    expect(container.children.length).toBe(1)
  })

  it('markToolCollapsible leaves the tool in place when the group lacks a tools container', () => {
    const container = setup()
    const tool = appendToolBlock(container, 'Read', null, false)
    const fake = document.createElement('div')
    fake.dataset.toolGroup = '1'
    container.insertBefore(fake, tool.root)

    expect(() => markToolCollapsible(tool.root)).not.toThrow()
    expect(tool.root.parentElement).toBe(container)
    expect(container.children.length).toBe(2)
  })
})

describe('markToolCollapsible edge cases', () => {
  it('is idempotent: re-marking a grouped tool changes nothing', () => {
    const container = setup()
    const t1 = appendToolBlock(container, 'Grep', null, false)
    const t2 = appendToolBlock(container, 'Glob', null, false)
    markToolCollapsible(t1.root)
    markToolCollapsible(t2.root)
    markToolCollapsible(t2.root) // already inside a group

    expect(container.querySelectorAll('[data-tool-group]').length).toBe(1)
    const tc = container.querySelector('[data-group-tools]') as HTMLElement
    expect(tc.querySelectorAll('[data-tool-group]').length).toBe(0)
    expect(tc.children.length).toBe(2)
  })

  it('creates no group when the previous sibling is non-collapsible', () => {
    const container = setup()
    appendToolBlock(container, 'Bash', null, false)
    const grep = appendToolBlock(container, 'Grep', null, false)
    markToolCollapsible(grep.root)

    expect(container.querySelector('[data-tool-group]')).toBeNull()
    expect(container.children.length).toBe(2)
  })

  it('absorbs pre-group thinking above the first tool on group creation', () => {
    const container = setup()
    appendThinkingBlock(container, 0)
    const t1 = appendToolBlock(container, 'Grep', null, false)
    const t2 = appendToolBlock(container, 'Glob', null, false)
    markToolCollapsible(t1.root) // no sibling before the thinking → no-op
    markToolCollapsible(t2.root) // creates the group

    const group = container.querySelector('[data-tool-group]') as HTMLElement
    const tc = group.querySelector('[data-group-tools]') as HTMLElement
    expect(container.children.length).toBe(1)
    expect(tc.children.length).toBe(3)
    expect((tc.children[0] as HTMLElement).dataset.thinking).toBe('1')
    expect((tc.children[1] as HTMLElement).dataset.toolName).toBe('Grep')
    expect((tc.children[2] as HTMLElement).dataset.toolName).toBe('Glob')
    // Group-creation path also refreshes the summary.
    expect(group.querySelector('[data-group-summary]')!.textContent).toBe('2 Searches')
  })
})

describe('markToolCollapsible into an existing stream group', () => {
  it('moves a retroactively-marked tool into the group, absorbing inter-tool thinking', () => {
    const container = setup()
    appendToolBlock(container, 'Grep', null, true)
    appendToolBlock(container, 'Glob', null, true)
    appendThinkingBlock(container, 0)
    const read = appendToolBlock(container, 'Read', null, false)

    markToolCollapsible(read.root)

    const group = container.querySelector('[data-tool-group]') as HTMLElement
    const tc = group.querySelector('[data-group-tools]') as HTMLElement
    expect(container.children.length).toBe(1)
    expect(tc.querySelectorAll('[data-tool-root]').length).toBe(3)
    expect(read.root.parentElement).toBe(tc)
    expect((tc.children[2] as HTMLElement).dataset.thinking).toBe('1')
    expect((tc.children[3] as HTMLElement).dataset.toolName).toBe('Read')
    expect(group.querySelector('[data-group-summary]')!.textContent).toBe('2 Searches, 1 Read')
  })
})
