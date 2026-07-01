import { type ReactNode, useState, type ReactElement } from 'react'

export function PreWithCopy({ children }: { children?: ReactNode }) {
	const [copied, setCopied] = useState(false)

	const codeEl = children as ReactElement<{ children?: ReactNode }>
	const text = codeEl?.props?.children
	const code = typeof text === 'string' ? text.replace(/\n$/, '') : String(text ?? '')

	const copy = () => {
		navigator.clipboard?.writeText(code)
		setCopied(true)
		setTimeout(() => setCopied(false), 1500)
	}

	return (
		<pre className="group relative overflow-x-auto">
			<button
				onClick={copy}
				className="absolute right-2 top-2 z-10 text-t3 transition-opacity hover:text-t1 opacity-40"
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
