import type { Block } from './model'

export type EngineListItem = {
  id: string
  name: string
  model: string
}

export type SessionListItem = {
  id: string
  title: string
  updatedAt: number
}

export type ServerMessage =
  | { type: 'connect_status'; connected: boolean; agent?: string; model?: string; sessionID?: string; inputHistory?: string[]; engineID?: string; engineName?: string }
  | { type: 'stats'; usage?: ServerUsage; queryStartMs?: number; toolCount?: number; thinkingMs?: number; contextUsed?: number; contextTotal?: number }
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
      deadline_unix?: number
    }
  | { type: 'error'; message: string }
  | { type: 'history'; messages: HistoryChatMsg[]; nextCursor: string; hasMore: boolean; compactBoundary?: boolean }
  | { type: 'task_list'; tasks: TaskWireItem[] }
  | { type: 'engine_list'; engines: EngineListItem[]; activeID: string }
  | { type: 'session_list'; sessions: SessionListItem[] }
  | {
      type: 'config'
      models: { provider: string; model: string }[]
      current: { provider: string; model: string }
    }
  | {
      type: 'metadata'
      connect: { connected: boolean; agent?: string; model?: string; sessionID?: string; inputHistory?: string[]; engineID?: string; engineName?: string }
      config: { models: { provider: string; model: string }[]; current: { provider: string; model: string } }
      engines: { engines: EngineListItem[]; activeID: string }
      tasks?: { tasks: TaskWireItem[] }
      history: { messages: HistoryChatMsg[]; nextCursor: string; hasMore: boolean }
      snapshot?: { blocks: Block[] }
      stats: { usage?: ServerUsage; queryStartMs?: number; toolCount?: number; thinkingMs?: number; contextUsed?: number; contextTotal?: number }
    }
  | { type: 'streamState'; blocks: Block[] }
  | { type: 'context_breakdown' } & ContextBreakdownData
  | { type: 'model_switched'; contextUsed: number; contextTotal: number }
  | { type: 'quota_result'; entries: { provider: string; quota: string }[] }
  | { type: 'file_start'; name: string; mime: string; size: number }
  | { type: 'file_end'; name: string }

export type ContextCategoryData = {
  name: string; tokens: number; percentage: number
  color: string; isFree: boolean; isReserved: boolean
}
export type MCPToolDetailData = { name: string; serverName: string; tokens: number; isLoaded: boolean }
export type SystemToolDetailData = { name: string; tokens: number }
export type SystemPromptSectionData = { name: string; tokens: number }
export type MemoryFileDetailData = { path: string; tokens: number }
export type AgentDetailData = { agentType: string; source: string; tokens: number }
export type SkillDetailData = { name: string; source: string; tokens: number }
export type MessageBreakdownData = {
  toolCallTokens: number; toolResultTokens: number; attachmentTokens: number
  assistantTextTokens: number; userTextTokens: number
  toolCallsByType: { name: string; callTokens: number; resultTokens: number }[]
  attachmentsByType: { name: string; tokens: number }[]
}
export type APIUsageData = {
  inputTokens: number; outputTokens: number
  cacheCreationInputTokens: number; cacheReadInputTokens: number
}
export type ContextBreakdownData = {
  model: string; contextWindow: number; totalTokens: number; percentage: number
  isAutoCompact: boolean; categories: ContextCategoryData[]
  mcpToolsLoaded: MCPToolDetailData[]; mcpToolsDeferred: MCPToolDetailData[]
  deferredBuiltinTools: SystemToolDetailData[]; systemTools: SystemToolDetailData[]
  systemPromptSections: SystemPromptSectionData[]; memoryFiles: MemoryFileDetailData[]
  agents: AgentDetailData[]; skills: SkillDetailData[]
  messageBreakdown: MessageBreakdownData | null; apiUsage: APIUsageData | null
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
  is_search?: boolean
  is_read?: boolean
  is_list?: boolean
  is_lsp?: boolean
}

export type HistoryBlockThinking = {
  text: string
  durationNs?: number
}

export type HistoryBlock =
  | { kind: 'text'; text: string }
  | { kind: 'thinking'; thinking: HistoryBlockThinking }
  | { kind: 'tool'; tool: HistoryBlockTool }
  | { kind: 'image'; src: string }  // data URL — backend base64-inlines the resized thumbnail

export type HistoryChatMsg = {
  id: string
  role: 'user' | 'assistant' | 'system'
  compactBoundary?: boolean
  text: string
  thinking: { text: string; durationNs: number }[]
  tools: { id: string; name: string; summary?: string; displayOutput?: string; isError?: boolean; isRunning?: boolean; durationNs?: number; is_search?: boolean; is_read?: boolean; is_list?: boolean; is_lsp?: boolean }[]
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

// Full response for ask_response wire message. Permission asks use `decision`;
// input asks use `text` + `aborted` (+ `timeout` to distinguish countdown
// expiry from user-initiated cancel). Mirrors Go's types.AskResponse.
export interface AskResponsePayload {
  decision?: AskDecision
  text?: string
  aborted?: boolean
  timeout?: boolean
}

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
