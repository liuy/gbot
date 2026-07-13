const MAX_LOGS = 1000
const logBuffer: string[] = []
const logListeners: Set<() => void> = new Set()

export function getDebugLogs(): string[] {
  return logBuffer.slice()
}

export function onDebugLog(fn: () => void): () => void {
  logListeners.add(fn)
  return () => { logListeners.delete(fn) }
}

function formatArgs(args: unknown[]): string {
  return args.map(a => {
    if (typeof a === 'object' && a !== null) {
      try { return JSON.stringify(a) } catch { return String(a) }
    }
    return String(a)
  }).join(' ')
}

function capture(method: 'log' | 'debug' | 'info' | 'warn' | 'error', args: unknown[]) {
  logBuffer.push(formatArgs(args))
  if (logBuffer.length > MAX_LOGS) logBuffer.shift()
  logListeners.forEach(fn => fn())
}

for (const method of ['log', 'debug', 'info', 'warn', 'error'] as const) {
  const original = console[method].bind(console)
  console[method] = function (...args: unknown[]) {
    capture(method, args)
    original(...args)
  }
}
