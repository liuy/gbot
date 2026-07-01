import { type ReactNode, useState, type ReactElement } from 'react'

function fallbackCopy(text: string) {
	const ta = document.createElement('textarea')
	ta.value = text
	ta.style.position = 'fixed'
	ta.style.opacity = '0'
	document.body.appendChild(ta)
	ta.select()
	try { document.execCommand('copy') } catch {}
	document.body.removeChild(ta)
}

export function PreWithCopy({ children }: { children?: ReactNode }) {
	const [copied, setCopied] = useState(false)

	const codeEl = children as ReactElement<{ children?: ReactNode }>
	const text = codeEl?.props?.children
	const code = typeof text === 'string' ? text.replace(/\n$/, '') : String(text ?? '')

	const copy = () => {
		if (navigator.clipboard?.writeText) {
			navigator.clipboard.writeText(code).catch(() => fallbackCopy(code))
		} else {
			fallbackCopy(code)
		}
		setCopied(true)
		setTimeout(() => setCopied(false), 1500)
	}

	return (
		<pre className="group relative overflow-x-auto">
			<button
				onClick={copy}
				className="absolute right-2 top-2 z-10 text-t2"
			>
				{copied ? (
					<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
						<polyline points="20 6 9 17 4 12" />
					</svg>
				) : (
					<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
						<rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
						<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
					</svg>
				)}
			</button>
			{children}
		</pre>
	)
}
