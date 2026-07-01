export type ToolEntry = {
  id: string
  eventIndex: number
  name: string
  summary: string
  isSearch: boolean
  isRead: boolean
  isList: boolean
  isLsp: boolean
  state: 'running' | 'done' | 'error'
  timingNs: number
  displayOutput: string
  startedAt: number // ms epoch, for live timer while running
}

export type ThinkingEntry = {
  eventIndex: number
  text: string
  durationNs: number
  active: boolean
  startedAt: number // ms epoch, for live timer
}

export type TextEntry = {
  eventIndex: number
  text: string
}

// Sorted view of thinking + tools + text for chronological rendering.
export type InterleavedItem =
  | { kind: 'thinking'; entry: ThinkingEntry }
  | { kind: 'tool'; entry: ToolEntry }
  | { kind: 'text'; entry: TextEntry }

export type ChatMessage = {
  id: string
  role: 'user' | 'assistant'
  textChunks: TextEntry[]
  thinking: ThinkingEntry[]
  tools: ToolEntry[]
  nextEventIndex: number
  usage: {
    inputTokens: number
    outputTokens: number
    cacheRead: number
    cacheCreation: number
  }
  error: string
  status: 'streaming' | 'done'
  startedAt: number // ms epoch
}

export function newAssistantMessage(id: string): ChatMessage {
  return {
    id,
    role: 'assistant',
    textChunks: [],
    thinking: [],
    tools: [],
    nextEventIndex: 0,
    usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
    error: '',
    status: 'streaming',
    startedAt: Date.now(),
  }
}

export function interleavedItems(msg: ChatMessage): InterleavedItem[] {
  const items: InterleavedItem[] = [
    ...msg.thinking.map((e) => ({ kind: 'thinking' as const, entry: e })),
    ...msg.tools.map((e) => ({ kind: 'tool' as const, entry: e })),
    ...msg.textChunks.map((e) => ({ kind: 'text' as const, entry: e })),
  ]
  items.sort((a, b) => a.entry.eventIndex - b.entry.eventIndex)
  return items
}
