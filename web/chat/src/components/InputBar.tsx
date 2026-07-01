import { useState } from 'react'
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

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    const text = value.trim()
    if (!text || streaming || !connected) return
    onSend(text)
    setValue('')
  }

  const canSend = value.trim().length > 0 && !streaming && connected

  return (
    <div className="px-5 pb-3 pt-1">
      {streaming && queuedText !== null && (
        <button
          type="button"
          onClick={onCancelQueued}
          className="mb-2 flex w-full items-center justify-between rounded-lg bg-card px-3 py-2 text-left"
        >
          <span className="truncate text-t2 text-sm">{queuedText}</span>
          <span className="ml-2 shrink-0 text-xs text-amber">Tap to CANCEL</span>
        </button>
      )}
      <form onSubmit={onSubmit} className="relative">
        <div className="glass flex items-center gap-2 rounded-2xl border border-hairline px-3 py-2 shadow-[0_0_20px_rgba(0,180,255,0.08)]">
          {streaming ? (
            <button
              type="button"
              onClick={onStop}
              className="shrink-0 rounded-lg bg-blue/10 px-3 py-1.5 text-sm text-blue pulse-blue"
            >
              STOP
            </button>
          ) : null}
          <input
            type="text"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder="Message GBot"
            disabled={!connected}
            className="flex-1 bg-transparent text-t1 text-[15px] placeholder:text-t3 focus:outline-none disabled:opacity-40"
          />
          <button
            type="submit"
            disabled={!canSend}
            className="shrink-0 rounded-lg p-1.5 text-blue transition-opacity disabled:opacity-30"
            aria-label="Send"
          >
            <svg
              className="h-5 w-5"
              viewBox="0 0 20 20"
              fill="currentColor"
            >
              <path d="M3 10l14-7-7 14-2-5-5-2z" />
            </svg>
          </button>
        </div>
      </form>
    </div>
  )
}
