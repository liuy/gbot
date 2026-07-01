import { useEffect, useRef, useState } from 'react'
import { useWebSocket } from '../websocket'
import type { ServerMessage, QueryEvent, HistoryChatMsg } from '../types'
import {
  newAssistantMessage,
  type ChatMessage,
  type ToolEntry,
  type TextEntry,
  type ThinkingEntry,
} from '../model'
import MessageComponent from './MessageComponent'
import InputBar from './InputBar'
import Ask from './Ask'

type AskData = {
  id: string
  kind: 'permission' | 'input'
  tool_name: string
  input?: unknown
  message?: string
  rule_detail?: string
  prompt?: string
  masked?: boolean
  agent_type?: string
}

let msgIdCounter = 0
function nextId(prefix: string): string {
  msgIdCounter += 1
  return `${prefix}-${msgIdCounter}`
}

// Module-level so messages survive ChatInterface unmount on tab switch.
const persistedMessages: ChatMessage[] = []

export default function ChatInterface() {
  const { subscribe, send } = useWebSocket()
  const messagesRef = useRef<ChatMessage[]>(persistedMessages)
  const [, setTick] = useState(0)
  const forceRender = () => setTick((t) => (t + 1) & 0x7fffffff)

  const [ask, setAsk] = useState<AskData | null>(null)
  const [queuedText, setQueuedText] = useState<string | null>(null)
  const streamingRef = useRef(false)
  const [streaming, setStreaming] = useState(false)

  const bottomRef = useRef<HTMLDivElement | null>(null)
  const scrollRef = useRef<HTMLDivElement | null>(null)

  const scrollToBottom = () => {
    const el = scrollRef.current
    if (!el) return
    const nearBottom =
      el.scrollHeight - el.scrollTop - el.clientHeight < 120
    if (nearBottom) {
      requestAnimationFrame(() => {
        bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
      })
    }
  }

  useEffect(() => {
    const unsubscribe = subscribe((msg: ServerMessage) => {
      switch (msg.type) {
        case 'connect_status':
          return
        case 'history':
          loadHistory(msg.messages)
          return
        case 'error':
          appendError(msg.message)
          return
        case 'ask':
          setAsk({
            id: msg.id,
            kind: msg.kind,
            tool_name: msg.tool_name,
            input: msg.input,
            message: msg.message,
            rule_detail: msg.rule_detail,
            prompt: msg.prompt,
            masked: msg.masked,
            agent_type: msg.agent_type,
          })
          return
        case 'event':
          handleEvent(msg.event)
          return
      }
    })
    return unsubscribe
  }, [])

  useEffect(() => {
    scrollToBottom()
  })

  const currentAssistant = (): ChatMessage | null => {
    const list = messagesRef.current
    if (list.length === 0) return null
    const last = list[list.length - 1]
    return last.role === 'assistant' && last.status === 'streaming'
      ? last
      : null
  }

  const appendError = (text: string) => {
    const cur = currentAssistant()
    if (cur) {
      cur.error = text
      cur.status = 'done'
    } else {
      const m = newAssistantMessage(nextId('a'))
      m.error = text
      m.status = 'done'
      messagesRef.current.push(m)
    }
    streamingRef.current = false
    setStreaming(false)
    setQueuedText(null)
    forceRender()
  }

  const loadHistory = (histMsgs: HistoryChatMsg[]) => {
    if (messagesRef.current.length > 0) return
    // Merge consecutive assistant messages into one (like TUI engineMessagesToViews).
    const merged: HistoryChatMsg[] = []
    for (const h of histMsgs) {
      // Skip user messages that are pure tool_result carriers (no visible text).
      if (h.role === 'user' && (!h.text || h.text.trim() === '')) continue
      const last = merged[merged.length - 1]
      if (last && last.role === 'assistant' && h.role === 'assistant') {
        // Merge into last assistant
        last.text += h.text ?? ''
        last.thinking = [...(last.thinking ?? []), ...(h.thinking ?? [])]
        last.tools = [...(last.tools ?? []), ...(h.tools ?? [])]
        last.blocks = [...(last.blocks ?? []), ...(h.blocks ?? [])]
        if (h.usage) {
          last.usage = {
            inputTokens: (last.usage?.inputTokens ?? 0) + h.usage.inputTokens,
            outputTokens: (last.usage?.outputTokens ?? 0) + h.usage.outputTokens,
            cacheRead: (last.usage?.cacheRead ?? 0) + h.usage.cacheRead,
            cacheCreation: (last.usage?.cacheCreation ?? 0) + h.usage.cacheCreation,
          }
        }
      } else {
        merged.push({ ...h })
      }
    }
    for (const h of merged) {
      const textChunks: TextEntry[] = []
      const thinking: ThinkingEntry[] = []
      const tools: ToolEntry[] = []
      let nextEventIndex: number
      if (h.blocks && h.blocks.length > 0) {
        // Authoritative ordered path: assign one shared eventIndex counter
        // across text/thinking/tool so interleavedItems() sorts them into the
        // original Content[] order.
        let eventIndex = 0
        for (const b of h.blocks) {
          if (b.kind === 'text') {
            textChunks.push({ eventIndex: eventIndex++, text: b.text })
          } else if (b.kind === 'thinking') {
            const th = b.thinking!
            thinking.push({
              eventIndex: eventIndex++,
              text: th.text,
              durationNs: th.durationNs ?? 0,
              active: false,
              startedAt: 0,
            })
          } else if (b.kind === 'tool') {
            const t = b.tool!
            tools.push({
              id: t.id,
              eventIndex: eventIndex++,
              name: t.name,
              summary: t.summary ?? '',
              isSearch: false,
              isRead: false,
              isList: false,
              isLsp: false,
              state: (t.isError ? 'error' : 'done') as 'error' | 'done',
              timingNs: t.durationNs ?? 0,
              displayOutput: t.displayOutput ?? '',
              startedAt: 0,
            })
          }
        }
        nextEventIndex = eventIndex
      } else {
        // Legacy fallback (older backend without blocks): cannot preserve
        // interleave order, but keep prior behavior so it does not regress.
        textChunks.push(...(h.text ? [{ eventIndex: 0, text: h.text }] : []))
        thinking.push(...(h.thinking ?? []).map((t, i) => ({
          eventIndex: i,
          text: t.text,
          durationNs: t.durationNs,
          active: false,
          startedAt: 0,
        })))
        tools.push(...(h.tools ?? []).map((t, i) => ({
          id: t.id,
          eventIndex: i,
          name: t.name,
          summary: t.summary ?? '',
          isSearch: false,
          isRead: false,
          isList: false,
          isLsp: false,
          state: (t.isError ? 'error' : 'done') as 'error' | 'done',
          timingNs: t.durationNs ?? 0,
          displayOutput: t.displayOutput ?? '',
          startedAt: 0,
        })))
        nextEventIndex = (h.thinking?.length ?? 0) + (h.tools?.length ?? 0) + (h.text ? 1 : 0)
      }
      const m: ChatMessage = {
        id: h.id || nextId(h.role === 'user' ? 'u' : 'a'),
        role: h.role,
        textChunks,
        thinking,
        tools,
        nextEventIndex,
        usage: {
          inputTokens: h.usage?.inputTokens ?? 0,
          outputTokens: h.usage?.outputTokens ?? 0,
          cacheRead: h.usage?.cacheRead ?? 0,
          cacheCreation: h.usage?.cacheCreation ?? 0,
        },
        error: h.error ?? '',
        status: h.status ?? 'done',
        startedAt: h.startedAt ?? Date.now(),
      }
      messagesRef.current.push(m)
    }
    forceRender()
    // Scroll to bottom after history loads.
    requestAnimationFrame(() => {
      bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
    })
  }

  const handleEvent = (e: QueryEvent) => {
    switch (e.type) {
      case 'query_start': {
        messagesRef.current.push(newAssistantMessage(nextId('a')))
        streamingRef.current = true
        setStreaming(true)
        forceRender()
        return
      }
      case 'query_end': {
        const cur = currentAssistant()
        if (cur) cur.status = 'done'
        streamingRef.current = false
        setStreaming(false)
        setQueuedText(null)
        forceRender()
        return
      }
      case 'thinking_start': {
        const cur = ensureAssistant()
        cur.thinking.push({
          eventIndex: cur.nextEventIndex++,
          text: '',
          durationNs: 0,
          active: true,
          startedAt: Date.now(),
        })
        forceRender()
        return
      }
      case 'thinking_delta': {
        const cur = ensureAssistant()
        const t = cur.thinking[cur.thinking.length - 1]
        if (t && e.thinking?.text) {
          t.text += e.thinking.text
        }
        forceRender()
        return
      }
      case 'thinking_end': {
        const cur = ensureAssistant()
        const t = cur.thinking[cur.thinking.length - 1]
        if (t) {
          t.active = false
          if (e.thinking?.duration) {
            t.durationNs = e.thinking.duration
          }
        }
        forceRender()
        return
      }
      case 'text_start': {
        const cur = ensureAssistant()
        cur.textChunks.push({ eventIndex: cur.nextEventIndex++, text: '' })
        forceRender()
        return
      }
      case 'text_delta': {
        const cur = ensureAssistant()
        if (e.text && cur.textChunks.length > 0) {
          cur.textChunks[cur.textChunks.length - 1].text += e.text
        }
        forceRender()
        return
      }
      case 'text_end': {
        forceRender()
        return
      }
      case 'tool_start': {
        if (!e.tool_use) return
        const tu = e.tool_use
        const cur = ensureAssistant()
        cur.tools.push({
          id: tu.id,
          eventIndex: cur.nextEventIndex++,
          name: tu.name,
          summary: '',
          isSearch: !!tu.is_search,
          isRead: !!tu.is_read,
          isList: !!tu.is_list,
          isLsp: !!tu.is_lsp,
          state: 'running',
          timingNs: 0,
          displayOutput: '',
          startedAt: Date.now(),
        })
        forceRender()
        return
      }
      case 'tool_param_delta': {
        if (!e.partial_input) return
        const cur = ensureAssistant()
        const tool = findTool(cur.tools, e.partial_input.id)
        if (tool && e.partial_input.summary) {
          tool.summary = e.partial_input.summary
        }
        forceRender()
        return
      }
      case 'tool_run': {
        forceRender()
        return
      }
      case 'tool_output_delta': {
        return
      }
      case 'tool_end': {
        if (!e.tool_result) return
        const tr = e.tool_result
        const cur = ensureAssistant()
        const tool = findTool(cur.tools, tr.tool_use_id)
        if (tool) {
          tool.state = tr.is_error ? 'error' : 'done'
          if (tr.timing) tool.timingNs = tr.timing
          tool.displayOutput = tr.display_output ?? ''
          if (tr.is_search !== undefined) tool.isSearch = tr.is_search
          if (tr.is_read !== undefined) tool.isRead = tr.is_read
          if (tr.is_list !== undefined) tool.isList = tr.is_list
          if (tr.is_lsp !== undefined) tool.isLsp = tr.is_lsp
        }
        forceRender()
        return
      }
      case 'usage': {
        if (!e.usage_event) return
        const u = e.usage_event
        const cur = ensureAssistant()
        cur.usage.inputTokens = u.input_tokens
        cur.usage.outputTokens = u.output_tokens
        cur.usage.cacheRead = u.cache_read_input_tokens ?? 0
        cur.usage.cacheCreation = u.cache_creation_input_tokens ?? 0
        forceRender()
        return
      }
      case 'retry_attempt':
        return
      default:
        return
    }
  }

  const ensureAssistant = (): ChatMessage => {
    const cur = currentAssistant()
    if (cur) return cur
    const m = newAssistantMessage(nextId('a'))
    messagesRef.current.push(m)
    streamingRef.current = true
    setStreaming(true)
    return m
  }

  const onSend = (text: string) => {
    if (streamingRef.current) {
      setQueuedText(text)
      return
    }
    messagesRef.current.push({
      id: nextId('u'),
      role: 'user',
      textChunks: [{ eventIndex: 0, text }],
      thinking: [],
      tools: [],
      nextEventIndex: 0,
      usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
      error: '',
      status: 'done',
      startedAt: Date.now(),
    })
    send({ type: 'message', text })
    forceRender()
  }

  const onStop = () => {
    send({ type: 'stop' })
  }

  const onCancelQueued = () => {
    setQueuedText(null)
  }

  return (
    <div className="flex h-full flex-col">
      <div ref={scrollRef} className="flex-1 overflow-y-auto">
        <div className="mx-auto max-w-3xl py-4">
          <div className="space-y-4">
          {messagesRef.current.map((m) => (
            <MessageComponent key={m.id} message={m} />
          ))}
          {ask && <Ask ask={ask} />}
          </div>
          <div ref={bottomRef} />
        </div>
      </div>
      <InputBar
        streaming={streaming}
        queuedText={queuedText}
        onSend={onSend}
        onStop={onStop}
        onCancelQueued={onCancelQueued}
      />
    </div>
  )
}

function findTool(tools: ToolEntry[], id: string): ToolEntry | undefined {
  for (let i = tools.length - 1; i >= 0; i--) {
    if (tools[i].id === id) return tools[i]
  }
  return undefined
}
