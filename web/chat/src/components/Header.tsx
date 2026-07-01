import { useWebSocket } from '../websocket'

export default function Header() {
  const { connected } = useWebSocket()

  return (
    <header className="glass-header flex items-center justify-between px-5 py-3">
      <div className="flex items-center gap-3">
        <span
          className={
            'inline-block h-2 w-2 rounded-full ' +
            (connected ? 'bg-green blink-green' : 'bg-red')
          }
          aria-label={connected ? 'connected' : 'disconnected'}
        />
        <span className="text-t1 font-semibold">GBot</span>
        <span className="text-t3">│</span>
        <span className="text-t2 text-sm">glm-5.2</span>
        <span className="text-t3">│</span>
        <span className="text-t2 text-sm">main</span>
        <span className="text-t3">│</span>
        <span className="text-t2 text-sm">main</span>
      </div>
      <div className="flex h-7 w-7 items-center justify-center rounded-full bg-gradient-to-br from-t2/40 to-t3/30 text-xs text-t1">
        U
      </div>
    </header>
  )
}
