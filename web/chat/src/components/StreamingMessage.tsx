import type { ReactNode, RefObject } from 'react'
import type { ChatMessage, Block } from '../model'
import { isCollapsibleTool } from './MessageComponent'
import StreamingText from './StreamingText'
import Thinking from './Thinking'
import ToolRenderer from './ToolRenderer'
import ToolGroup from './ToolGroup'
import ProgressBar from './ProgressBar'

const avatarG = 'flex h-5 w-5 shrink-0 items-center justify-center rounded-md bg-gradient-to-br from-blue to-violet text-[11px] font-bold text-white'

// Renders the streaming assistant message. text/thinking blocks write to DOM
// sinks (refs); tool blocks render normally via React. Mirrors MessageComponent's
// renderGrouped grouping and grid layout for visual parity.
export default function StreamingMessage({
	message,
	textRef,
	thinkingRef,
	flushTextRef,
}: {
	message: ChatMessage
	textRef: RefObject<HTMLDivElement | null>
	thinkingRef: RefObject<HTMLParagraphElement | null>
	flushTextRef?: () => void
}) {
	const out: ReactNode[] = []
	let buffer: Extract<Block, { kind: 'tool' }>[] = []
	let key = 0

	const flush = () => {
		if (buffer.length === 0) return
		if (buffer.length >= 2) {
			out.push(<ToolGroup key={`grp-${key++}`} tools={buffer} />)
		} else {
			out.push(<ToolRenderer key={buffer[0].id} tool={buffer[0]} />)
		}
		buffer = []
	}

	for (const b of message.blocks) {
		switch (b.kind) {
			case 'thinking':
				out.push(<Thinking key={b.id} entry={b} textRef={thinkingRef} forceExpanded />)
				break
			case 'tool':
				if (isCollapsibleTool(b)) {
					buffer.push(b)
				} else {
					flush()
					out.push(<ToolRenderer key={b.id} tool={b} />)
				}
				break
			case 'text':
				// Always mount the sink during streaming for a text block — even
				// when b.text is empty. text_start pushes an empty text block then
				// schedules a forceRender; this branch mounts the sink so
				// subsequent text_delta events have a DOM target. Skipping here
				// would leave streamTextRef null until the next structural render,
				// dropping early deltas.
				flush()
				out.push(
					<StreamingText
						key={`txt-${key++}`}
						ref={textRef}
						className="md-body text-t1 text-[15px] leading-relaxed whitespace-pre-wrap"
						flushRef={flushTextRef}
					/>
				)
				break
			case 'user':
				if (!b.text) break
				flush()
				out.push(<div key={`usr-${key++}`} className="text-[13px] text-t2 italic ml-2 my-1">{b.text}</div>)
				break
		}
	}
	flush()

	return (
		<div className="px-1.5">
			<div className="grid grid-cols-[1.25rem_1fr_1.25rem] items-start gap-x-1.5">
				<div className={avatarG}>G</div>
				<div className="min-w-0">
					<div className="space-y-3">
						{out}
						{message.error && (
							<div className="rounded-lg border border-red/40 bg-red/5 px-3 py-2 text-sm text-red">
								{message.error}
							</div>
						)}
						<ProgressBar message={message} />
					</div>
				</div>
				<div className="" />
			</div>
		</div>
	)
}
