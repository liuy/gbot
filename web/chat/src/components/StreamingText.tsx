import { forwardRef, useEffect } from 'react'

// Plain DOM sink. ChatInterface writes .textContent directly via ref; React
// never re-renders this component during streaming.
//
// flushRef: invoked once on mount so ChatInterface can drain any text_delta
// that arrived between text_start (which scheduled a forceRender to mount
// this sink) and React actually committing it. Without the flush, deltas
// arriving in that window update streamTextAccum but find streamTextRef null
// and are silently dropped.
const StreamingText = forwardRef<HTMLDivElement, { className?: string; flushRef?: () => void }>(
	function StreamingText({ className, flushRef }, ref) {
		useEffect(() => {
			// Mount fires AFTER the ref callback assigns streamTextRef.current,
			// so the sink is already live. Drain deferred accumulator content so
			// nothing is lost regardless of event arrival timing.
			flushRef?.()
		}, [])
		return <div ref={ref} className={className} />
	}
)

export default StreamingText
