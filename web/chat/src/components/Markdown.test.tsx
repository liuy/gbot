import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import Markdown from './Markdown'
import { readFileSync } from 'fs'
import { resolve } from 'path'

const css = readFileSync(resolve(__dirname, '../index.css'), 'utf-8')

describe('Markdown', () => {
	it('wraps content in md-body scope for CSS targeting', () => {
		const { container } = render(<Markdown>{'hello'}</Markdown>)
		expect(container.querySelector('.md-body')).toBeTruthy()
	})

	it('renders inline code as <code> not inside <pre>', () => {
		const { container } = render(
			<Markdown>{'text with `inline` code'}</Markdown>
		)
		const inlineCodes = container.querySelectorAll(':not(pre) > code')
		expect(inlineCodes.length).toBe(1)
		expect(inlineCodes[0].textContent).toBe('inline')
	})

	it('renders unordered list', () => {
		const { container } = render(
			<Markdown>{'- item 1\n- item 2'}</Markdown>
		)
		const ul = container.querySelector('ul')
		expect(ul).toBeTruthy()
		expect(ul!.children.length).toBe(2)
	})

	it('renders ordered list', () => {
		const { container } = render(
			<Markdown>{'1. first\n2. second'}</Markdown>
		)
		const ol = container.querySelector('ol')
		expect(ol).toBeTruthy()
		expect(ol!.children.length).toBe(2)
	})

	it('renders table with th and td', () => {
		const { container } = render(
			<Markdown>{'| H1 | H2 |\n|---|---|\n| a | b |'}</Markdown>
		)
		const table = container.querySelector('table')
		expect(table).toBeTruthy()
		expect(table!.querySelectorAll('th').length).toBe(2)
		expect(table!.querySelectorAll('td').length).toBe(2)
	})

	it('renders code block with rehype-highlight hljs class', () => {
		const { container } = render(
			<Markdown>{'```python\nprint("hi")\n```'}</Markdown>
		)
		const pre = container.querySelector('pre')
		expect(pre).toBeTruthy()
		const code = pre!.querySelector('code')
		expect(code?.className).toContain('hljs')
	})

	it('renders blockquote', () => {
		const { container } = render(
			<Markdown>{'> quoted text'}</Markdown>
		)
		const bq = container.querySelector('blockquote')
		expect(bq).toBeTruthy()
		expect(bq!.textContent).toContain('quoted text')
	})
})

describe('Markdown CSS rules', () => {
	it('has .md-body table border-collapse rule', () => {
		expect(css).toContain('.md-body table')
		expect(css).toContain('border-collapse')
	})

	it('has .md-body ul list-style-type disc', () => {
		expect(css).toContain('.md-body ul')
		expect(css).toContain('list-style-type: disc')
	})

	it('has .md-body ol list-style-type decimal', () => {
		expect(css).toContain('.md-body ol')
		expect(css).toContain('list-style-type: decimal')
	})

	it('has .md-body inline code style (:not(pre) > code)', () => {
		expect(css).toContain(':not(pre) > code')
	})

	it('has .md-body blockquote border-left', () => {
		expect(css).toContain('.md-body blockquote')
		expect(css).toContain('border-left')
	})

	it('has .md-body pre overflow-x auto', () => {
		expect(css).toContain('.md-body pre')
		expect(css).toContain('overflow-x: auto')
	})
})
