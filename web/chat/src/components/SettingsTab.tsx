import { useState } from 'react'

export default function SettingsTab() {
  const [host, setHost] = useState('localhost')
  const [port, setPort] = useState('8765')

  return (
    <div className="mx-auto max-w-2xl space-y-7 px-5 py-4">
      <div className="flex items-center gap-3">
        <label className="w-16 shrink-0 text-sm text-t2">Host</label>
        <input
          type="text"
          value={host}
          onChange={(e) => setHost(e.target.value)}
          className="flex-1 border-b border-hairline bg-transparent pb-1 text-t1 focus:outline-none focus:border-blue"
        />
      </div>
      <div className="flex items-center gap-3">
        <label className="w-16 shrink-0 text-sm text-t2">Port</label>
        <input
          type="text"
          value={port}
          onChange={(e) => setPort(e.target.value)}
          className="flex-1 border-b border-hairline bg-transparent pb-1 text-t1 focus:outline-none focus:border-blue"
        />
      </div>
      <div>
        <button
          type="button"
          className="rounded-lg bg-blue/15 px-4 py-2 text-sm text-blue"
        >
          Connect
        </button>
      </div>
    </div>
  )
}
