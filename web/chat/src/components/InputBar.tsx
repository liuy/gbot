import { useRef, useState } from 'react'
import { useWebSocket } from '../websocket'

export default function InputBar({
	streaming,
	queuedText,
	onSend,
	onStop,
	onCancelQueued,
}: {
	streaming: boolean
	queuedText: string | null
	onSend: (text: string) => void
	onStop: () => void
	onCancelQueued: () => void
}) {
	const { connected } = useWebSocket()
	const [value, setValue] = useState('')
	const taRef = useRef<HTMLTextAreaElement>(null)

	const onSubmit = (e: React.FormEvent) => {
		e.preventDefault()
		const text = value.trim()
		if (!text || !connected) return
		onSend(text)
		setValue('')
	}

	const onKeyDown = (e: React.KeyboardEvent) => {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault()
			onSubmit(e as unknown as React.FormEvent)
		}
	}

	const onInput = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
		setValue(e.target.value)
	}

	const canSend = value.trim().length > 0 && connected

	return (
		<div className="sticky bottom-0 z-10 px-5 pb-3 pt-1">
			{streaming && queuedText !== null && (
				<div
					className="mb-2 mx-auto bg-ink2/80 backdrop-blur border border-hairline rounded-full px-4 py-2 flex items-center gap-2 w-fit modal-enter cursor-pointer"
					onClick={onCancelQueued}
				>
					<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" className="text-t3">
						<circle cx="12" cy="12" r="10" />
						<path d="M12 8v4M12 16h.01" />
					</svg>
					<span className="text-[12px] text-t2 font-light italic truncate max-w-[240px]">{queuedText}</span>
					<span className="text-[10px] text-t3 mono ml-1">Tap to CANCEL</span>
				</div>
			)}

			<form onSubmit={onSubmit}>
				<div className="card-bg rounded-xl border border-hairline glow-blue">
					<div className="flex items-end gap-2 px-4 py-2.5">
						{streaming && (
							<button
								type="button"
								onClick={onStop}
								className="flex-shrink-0 flex items-center justify-center w-7 h-7 rounded-full text-blue transition-colors pulse-blue"
								style={{ background: 'rgba(0,180,255,0.12)' }}
							>
								<span className="text-[8px] mono font-bold tracking-wide">STOP</span>
							</button>
						)}

						<div
							className="flex-1 flex justify-center min-h-[20px] cursor-text"
							onClick={() => taRef.current?.focus()}
						>
							<textarea
								ref={taRef}
								rows={1}
								value={value}
								onChange={onInput}
								onKeyDown={onKeyDown}
								placeholder="Sup?"
								disabled={!connected}
								className="bg-transparent text-[14px] text-t1 placeholder-t3 resize-none outline-none font-light text-center disabled:opacity-40"
							style={{
								fieldSizing: 'content',
								width: 'fit-content',
								maxWidth: '100%',
								maxHeight: '120px',
								overflow: 'hidden',
							} as React.CSSProperties}
							/>
						</div>

						<button
							type="submit"
							disabled={!canSend}
							className="flex-shrink-0 text-blue hover:text-white transition-colors pb-0.5 disabled:opacity-30"
							aria-label="Send"
						>
							<svg className="h-6 w-6" viewBox="0 0 20 20" fill="currentColor">
								<path d="M3 10l14-7-7 14-2-5-5-2z" />
							</svg>
						</button>
					</div>
				</div>
			</form>
		</div>
	)
}
