import { useState, useRef, useEffect } from 'react'
import { useWebSocket } from '../websocket'

function SettingsIcon() {
	return (
		<button className="w-6 h-6 rounded-md flex items-center justify-center hover:ring-2 hover:ring-blue/40 transition-all text-t2 hover:text-t1">
			<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5">
				<circle cx="12" cy="12" r="3" />
				<path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 01-2.83 2.83l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-4 0v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 010-4h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 012.83-2.83l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 014 0v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 010 4h-.09a1.65 1.65 0 00-1.51 1z" />
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
		<header className="sticky top-0 z-30 card-bg">
			<div className="flex items-center gap-2.5 px-4 h-11 max-w-2xl mx-auto">
				<span
					className={`font-semibold tracking-tight text-[14px] transition-colors ${
						connected
							? 'text-blue pulse'
							: 'text-t3'
					}`}
				>GBot</span>

				<Dropdown
					trigger={<span className="mono text-[11px] text-t2 group-hover:text-t1 transition-colors">glm-5.2</span>}
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
				<SettingsIcon />
			</div>
		</header>
	)
}
