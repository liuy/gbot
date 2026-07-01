import { useEffect, useState } from 'react'
import type { ChatMessage } from '../model'
import { formatTokenCount, formatDurationNs } from '../utils'

export default function ProgressBar({
  message,
}: {
  message: ChatMessage
}) {
  // Tick while streaming so the elapsed duration updates live.
  const [, setTick] = useState(0)
  useEffect(() => {
    if (message.status !== 'streaming') return
    const id = setInterval(() => setTick((t) => t + 1), 200)
    return () => clearInterval(id)
  }, [message.status])

  const elapsedSec = (Date.now() - message.startedAt) / 1000
  const { inputTokens, outputTokens, cacheRead, cacheCreation } = message.usage
  const totalIn = inputTokens + cacheRead + cacheCreation
  const rate = elapsedSec > 0 ? outputTokens / elapsedSec : 0
  const toolCount = message.tools.length

  const streaming = message.status === 'streaming'

  let cachePct = 0
  if (totalIn > 0) {
    cachePct = Math.round((cacheRead / totalIn) * 100)
  }

  return (
    <div className="mt-2 flex items-center gap-2 overflow-x-auto overflow-y-hidden whitespace-nowrap text-xs text-t3">
      {streaming && (
        <span className="inline-block overflow-hidden text-[12px] text-blue heartbeat">●</span>
      )}
      <span>{formatDurationNs(elapsedSec * 1e9)}</span>
      <span>· ↑{formatTokenCount(inputTokens)}</span>
      <span>↓{formatTokenCount(outputTokens)}</span>
      <span>· {rate.toFixed(1)} t/s</span>
      <span>· {toolCount} tools</span>
      {!streaming && cachePct > 0 && <span>· {cachePct}% cached</span>}
    </div>
  )
}
