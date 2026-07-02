import { useEffect, useRef, useState } from 'react'
import { useWebSocket } from '../websocket'
import type { ServerMessage, QueryEvent, HistoryChatMsg } from '../types'
import { newAssistantMessage, type ChatMessage, type Block } from '../model'
import MessageComponent from './MessageComponent'
import InputBar from './InputBar'
import Ask from './Ask'
import Header from './Header'

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

// Mirrors TUI classifyToolName (pkg/tui/app.go:783). History messages don't
// carry is_search/is_read streaming flags, so we infer from tool name.
function classifyToolName(name: string): { isSearch: boolean; isRead: boolean; isList: boolean; isLsp: boolean; isWeb: boolean } {
  switch (name) {
    case 'Read': return { isRead: true, isSearch: false, isList: false, isLsp: false, isWeb: false }
    case 'Grep': case 'Glob': return { isSearch: true, isRead: false, isList: false, isLsp: false, isWeb: false }
    case 'Lsp': return { isLsp: true, isSearch: false, isRead: false, isList: false, isWeb: false }
    case 'Web': return { isWeb: true, isSearch: false, isRead: false, isList: false, isLsp: false }
    default: return { isSearch: false, isRead: false, isList: false, isLsp: false, isWeb: false }
  }
}

function findBlockById(msg: ChatMessage, id: string): Block | undefined {
  for (let i = msg.blocks.length - 1; i >= 0; i--) {
    if (msg.blocks[i].id === id) return msg.blocks[i]
  }
  return undefined
}

function findLastBlock(msg: ChatMessage, kind: Block['kind']): Block | undefined {
  for (let i = msg.blocks.length - 1; i >= 0; i--) {
    if (msg.blocks[i].kind === kind) return msg.blocks[i]
  }
  return undefined
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
        bottomRef.current?.scrollIntoView({ behavior: 'auto' })
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
      const m: ChatMessage = {
        id: h.id || nextId(h.role === 'user' ? 'u' : 'a'),
        role: h.role,
        blocks: [],
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
      if (h.blocks && h.blocks.length > 0) {
        for (const b of h.blocks) {
          if (b.kind === 'text') {
            m.blocks.push({ kind: 'text', id: nextId('txt'), text: b.text })
          } else if (b.kind === 'thinking') {
            const th = b.thinking!
            m.blocks.push({
              kind: 'thinking',
              id: nextId('th'),
              text: th.text,
              durationNs: th.durationNs ?? 0,
              active: false,
              startedAt: 0,
            })
          } else if (b.kind === 'tool') {
            const t = b.tool!
            const srk = classifyToolName(t.name)
            m.blocks.push({
              kind: 'tool',
              id: t.id,
              name: t.name,
              summary: t.summary ?? '',
              isSearch: srk.isSearch,
              isRead: srk.isRead,
              isList: srk.isList,
              isLsp: srk.isLsp,
              isWeb: srk.isWeb,
              state: (t.isError ? 'error' : 'done') as 'error' | 'done',
              timingNs: t.durationNs ?? 0,
              displayOutput: t.displayOutput ?? '',
              startedAt: 0,
            })
          }
        }
      } else {
        // User messages carry text; assistant legacy messages may have text
        // without blocks. Push a single text block so they render.
        if (h.text) {
          m.blocks.push({ kind: 'text', id: nextId('txt'), text: h.text })
        }
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
        cur.blocks.push({
          kind: 'thinking',
          id: nextId('th'),
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
        const last = findLastBlock(cur, 'thinking')
        if (last && last.kind === 'thinking' && e.thinking?.text) {
          last.text += e.thinking.text
        }
        forceRender()
        return
      }
      case 'thinking_end': {
        const cur = ensureAssistant()
        const last = findLastBlock(cur, 'thinking')
        if (last && last.kind === 'thinking') {
          last.active = false
          if (e.thinking?.duration) {
            last.durationNs = e.thinking.duration
          }
        }
        forceRender()
        return
      }
      case 'text_start': {
        const cur = ensureAssistant()
        cur.blocks.push({ kind: 'text', id: nextId('txt'), text: '' })
        forceRender()
        return
      }
      case 'text_delta': {
        const cur = ensureAssistant()
        const last = findLastBlock(cur, 'text')
        if (last && last.kind === 'text' && e.text) {
          last.text += e.text
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
        cur.blocks.push({
          kind: 'tool',
          id: tu.id,
          name: tu.name,
          summary: '',
          isSearch: !!tu.is_search,
          isRead: !!tu.is_read,
          isList: !!tu.is_list,
          isLsp: !!tu.is_lsp,
          isWeb: tu.name === 'Web',
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
        const block = findBlockById(cur, e.partial_input.id)
        if (block && block.kind === 'tool' && e.partial_input.summary) {
          block.summary = e.partial_input.summary
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
        const block = findBlockById(cur, tr.tool_use_id)
        if (block && block.kind === 'tool') {
          block.state = tr.is_error ? 'error' : 'done'
          block.timingNs = (Date.now() - block.startedAt) * 1e6
          block.displayOutput = tr.display_output ?? ''
          if (tr.is_search !== undefined) block.isSearch = tr.is_search
          if (tr.is_read !== undefined) block.isRead = tr.is_read
          if (tr.is_list !== undefined) block.isList = tr.is_list
          if (tr.is_lsp !== undefined) block.isLsp = tr.is_lsp
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
      blocks: [{ kind: 'text', id: nextId('txt'), text }],
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
    <>
      <Header />
      <div className="mx-auto max-w-2xl py-4">
        <div className="space-y-7">
          {messagesRef.current.map((m) => (
            <MessageComponent key={m.id} message={m} />
          ))}
          {ask && <Ask ask={ask} />}
        </div>
        <div ref={bottomRef} />
      </div>
      <InputBar
        streaming={streaming}
        queuedText={queuedText}
        onSend={onSend}
        onStop={onStop}
        onCancelQueued={onCancelQueued}
      />
    </>
  )
}
