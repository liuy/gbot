export type ServerMessage =
  | { type: 'connect_status'; connected: boolean }
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
  | { type: 'history'; messages: HistoryChatMsg[] }

export type HistoryBlockTool = {
  id: string
  name: string
  summary?: string
  displayOutput?: string
  isError?: boolean
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
  tools: { id: string; name: string; summary?: string; displayOutput?: string; isError?: boolean; durationNs?: number }[]
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
}

export type AskDecision = 'allow' | 'deny' | 'allow_always'
