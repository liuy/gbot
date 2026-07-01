// Mirrors pkg/utils/duration.go:10. Input is SECONDS (caller converts ns).
// <1s: "0.3s", 1-59s: "Xs", 60s-59m: "Xm Ys", >=1h: "Xh Ym Zs".
export function formatDuration(seconds: number): string {
  const s = Math.floor(seconds)
  if (s < 1) {
    return seconds.toFixed(1) + 's'
  }
  if (s < 60) {
    return s + 's'
  }
  if (s < 3600) {
    const m = Math.floor(s / 60)
    const sec = s % 60
    return m + 'm ' + sec + 's'
  }
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  return h + 'h ' + m + 'm ' + sec + 's'
}

// Mirrors pkg/types/text.go:84. Base 1024.
// <1000: raw, >=1k: "1.2k", >=1M: "2.3M", >=1G: "4.5G".
export function formatTokenCount(n: number): string {
  if (n < 1000) {
    return String(n)
  }
  if (n < 1024 * 1024) {
    return (n / 1024).toFixed(1) + 'k'
  }
  if (n < 1024 * 1024 * 1024) {
    return (n / (1024 * 1024)).toFixed(1) + 'M'
  }
  return (n / (1024 * 1024 * 1024)).toFixed(1) + 'G'
}

// Formats a duration given a nanosecond value (toolOutput prefix-parse, thinking.duration).
export function formatDurationNs(nanos: number): string {
  if (!nanos || nanos <= 0) return '0s'
  return formatDuration(nanos / 1e9)
}

// Mirrors pkg/tool/render.go StripANSI: removes ANSI escape sequences so the
// web frontend can re-style displayOutput independently of terminal colors.
export function stripAnsi(s: string): string {
  // eslint-disable-next-line no-control-regex
  return s.replace(/\x1b\[[0-9;]*[a-zA-Z]/g, '')
}

// Extracts the [Tool spent Xs] prefix that pkg/engine/runTools.go:prependDuration
// adds to tool_result Output JSON. Returns the duration in nanoseconds, or 0 if
// no prefix is present. Mirrors renderToolOutput in pkg/connector/webchat/connector.go.
export function parseDurationFromOutput(output: unknown): number {
	if (typeof output !== 'string') return 0
	let s = output
	try {
		const decoded = JSON.parse(s)
		if (typeof decoded === 'string') s = decoded
	} catch {
		// Not JSON — try the raw string (defensive; should not normally happen).
	}
	const prefix = '[Tool spent '
	if (!s.startsWith(prefix)) return 0
	const closeBracket = s.indexOf(']', prefix.length)
	if (closeBracket < 0) return 0
	const inner = s.slice(prefix.length, closeBracket)
	const numPart = inner.replace(/s$/, '')
	const sec = parseFloat(numPart)
	if (isNaN(sec)) return 0
	return sec * 1e9
}
