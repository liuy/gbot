import { useState, useRef, useEffect } from 'react'
import { useWebSocket } from '../websocket'

function UserAvatar() {
	return (
		<button className="w-6 h-6 rounded-full flex items-center justify-center hover:ring-2 hover:ring-blue/40 transition-all bg-gradient-to-br from-t2 to-t3">
			<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="white" strokeWidth="2">
				<circle cx="12" cy="8" r="4" />
				<path d="M4 21v-1a8 8 0 0116 0v1" />
			</svg>
		</button>
	)
}

function DDChevron() {
	return (
		<svg width="8" height="8" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" className="text-t3 group-hover:text-t1">
			<path d="M6 9l6 6 6-6" />
		</svg>
	)
}

type Option = {
	label: string
	active?: boolean
}

function Dropdown({
	trigger,
	options,
	width,
}: {
	trigger: React.ReactNode
	options: Option[]
	width: string
}) {
	const [open, setOpen] = useState(false)
	const ref = useRef<HTMLDivElement>(null)

	useEffect(() => {
		if (!open) return
		const onClick = (e: MouseEvent) => {
			if (ref.current && !ref.current.contains(e.target as Node)) {
				setOpen(false)
			}
		}
		document.addEventListener('mousedown', onClick)
		return () => document.removeEventListener('mousedown', onClick)
	}, [open])

	return (
		<div className="relative" ref={ref}>
			<button
				onClick={() => setOpen(!open)}
				className="flex items-center gap-1 group"
			>
				{trigger}
				<DDChevron />
			</button>
			{open && (
				<div className={`absolute top-full left-0 mt-1 glass-solid border border-hairline rounded-lg shadow-xl modal-enter z-40 ${width}`}>
					<div className="p-1 max-h-60 overflow-y-auto">
						{options.map((opt) => (
							<button
								key={opt.label}
								className="w-full flex items-center justify-between px-2.5 py-2 rounded-md hover:bg-ink3 text-left"
							>
								<span className={`text-[13px] ${opt.active ? 'text-t1' : 'text-t2'}`}>{opt.label}</span>
								{opt.active && (
									<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="#00B4FF" strokeWidth="2.5">
										<path d="M20 6L9 17l-5-5" />
									</svg>
								)}
							</button>
						))}
					</div>
				</div>
			)}
		</div>
	)
}

export default function Header() {
	const { connected } = useWebSocket()

	return (
		<header className="sticky top-0 z-30 glass">
			<div className="flex items-center gap-2.5 px-4 h-11 max-w-2xl mx-auto">
				<span
					className={`w-1.5 h-1.5 rounded-full ${connected ? 'bg-green pulse' : 'bg-red'}`}
				/>
				<span className="text-t1 font-semibold tracking-tight text-[14px]">GBot</span>

				<Dropdown
					trigger={<span className="mono text-[11px] text-blue group-hover:text-t1 transition-colors">glm-5.2</span>}
					options={[
						{ label: 'glm-5.2', active: true },
						{ label: 'glm-4.6' },
						{ label: 'gpt-5' },
						{ label: 'claude-sonnet-4.5' },
					]}
					width="w-56"
				/>

				<Dropdown
					trigger={<span className="text-[12px] text-t2 group-hover:text-t1 transition-colors truncate-sm">modality-fix</span>}
					options={[
						{ label: 'modality-fix', active: true },
						{ label: 'ws-reversal' },
					]}
					width="w-52"
				/>

				<Dropdown
					trigger={<span className="text-[12px] text-t2 group-hover:text-t1 transition-colors truncate-sm">main</span>}
					options={[
						{ label: 'main', active: true },
						{ label: 'wechat-bot' },
					]}
					width="w-52"
				/>

				<div className="flex-1" />
				<UserAvatar />
			</div>
		</header>
	)
}
