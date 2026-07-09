export type SessionListItem = {
  id: string
  title: string
  updatedAt: number
}

export type ServerMessage =
  | { type: 'connect_status'; connected: boolean; agent?: string; model?: string; sessionID?: string; usage?: ServerUsage }
  | { type: 'queued'; uuid: string }
  | { type: 'cancel_result'; removed: string[] }
  | { type: 'event'; event: QueryEvent }
  | {
      type: 'ask'
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
  | { type: 'error'; message: string }
  | { type: 'history'; messages: HistoryChatMsg[]; nextCursor: string; hasMore: boolean }
  | { type: 'task_list'; tasks: TaskWireItem[] }
  | { type: 'session_list'; sessions: SessionListItem[] }
  | {
      type: 'config'
      models: { provider: string; model: string }[]
      current: { provider: string; model: string }
    }

export type TaskWireItem = {
  id: string
  subject: string
  status: 'pending' | 'in_progress' | 'completed'
  owner?: string
  blockedBy?: string[]
  activeForm?: string
}

export type HistoryBlockTool = {
  id: string
  name: string
  summary?: string
  displayOutput?: string
  isError?: boolean
  isRunning?: boolean
  durationNs?: number
}

export type HistoryBlockThinking = {
  text: string
  durationNs?: number
}

export type HistoryBlock =
  | { kind: 'text'; text: string }
  | { kind: 'thinking'; thinking: HistoryBlockThinking }
  | { kind: 'tool'; tool: HistoryBlockTool }

export type HistoryChatMsg = {
  id: string
  role: 'user' | 'assistant'
  text: string
  thinking: { text: string; durationNs: number }[]
  tools: { id: string; name: string; summary?: string; displayOutput?: string; isError?: boolean; isRunning?: boolean; durationNs?: number }[]
  blocks?: HistoryBlock[]
  usage: {
    inputTokens: number
    outputTokens: number
    cacheRead: number
    cacheCreation: number
  }
  error: string
  status: 'streaming' | 'done'
  startedAt: number
}

export type QueryEvent = {
  type: string
  text?: string
  session_id?: string
  tool_use?: {
    id: string
    name: string
    input: unknown
    summary?: string
    is_search?: boolean
    is_read?: boolean
    is_list?: boolean
    is_lsp?: boolean
  }
  tool_result?: {
    tool_use_id: string
    output: unknown
    display_output?: string
    is_error?: boolean
    is_search?: boolean
    is_read?: boolean
    is_list?: boolean
    is_lsp?: boolean
  }
  partial_input?: {
    id: string
    name: string
    delta: string
    summary?: string
    is_search?: boolean
    is_read?: boolean
    is_list?: boolean
    is_lsp?: boolean
  }
  usage_event?: {
    input_tokens: number
    output_tokens: number
    cache_read_input_tokens?: number
    cache_creation_input_tokens?: number
  }
  agent?: {
    parent_tool_use_id: string
    agent_type: string
    depth: number
  }
  thinking?: {
    duration?: number // NANOSECONDS
    text?: string
  }
  retry_attempt?: {
    attempt: number
    max_retries: number
    retry_in_ms: number
    error: string
  }
  aborted?: boolean
}

export type AskDecision = 'allow' | 'deny' | 'allow_always'

export type ServerUsage = {
  // Go types.Usage JSON tags (snake_case — what server actually sends)
  input_tokens?: number
  output_tokens?: number
  cache_read_input_tokens?: number
  cache_creation_input_tokens?: number
  // PascalCase fallback (e.g. manually constructed in tests)
  InputTokens?: number
  OutputTokens?: number
  CacheReadInputTokens?: number
  CacheCreationInputTokens?: number
}
