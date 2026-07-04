import { forwardRef } from 'react'
import { avatarG } from './MessageComponent'

// Renders the streaming assistant message as a single empty container.
// ChatInterface appends all blocks (text/thinking/tool/user/progress)
// imperatively via streamDom.ts during streaming. React NEVER touches this
// container's children until query_end swaps this component out for
// MessageComponent. The single ref forwards the inner div to ChatInterface
// so it can call streamDom helpers against it directly.
const StreamingMessage = forwardRef<HTMLDivElement>(function StreamingMessage(_props, ref) {
	return (
		<div className="px-1.5">
			<div className="grid grid-cols-[1.25rem_1fr_1.25rem] items-start gap-x-1.5">
				<div className={avatarG}>G</div>
				<div className="min-w-0">
					<div ref={ref} className="space-y-3" />
				</div>
				<div className="" />
			</div>
		</div>
	)
})

export default StreamingMessage
