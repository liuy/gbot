// Shared QueryEvent→Block reducer: the single place wire events become
// render blocks. Extracted verbatim from jobs_store's applyStreamEvent so
// every consumer (task panel job feeds, voice face) reduces identically and
// renders through the same chat-standard block pipeline (user ruling: one
// pipeline, no parallel rendering code per surface).
import type { Block } from '../model'
import type { QueryEvent } from '../types'

type ToolBlock = Extract<Block, { kind: 'tool' }>

// findBlock mirrors the connector's findBlock: depth-first search of the
// block tree for a tool block by id. Sub-agent events address their parent
// tool by id, wherever in the tree it lives.
export function findBlock(blocks: Block[], id: string): ToolBlock | null {
  for (const b of blocks) {
    if (b.kind === 'tool') {
      if (b.id === id) return b
      const nested = findBlock(b.children, id)
      if (nested) return nested
    }
  }
  return null
}

// applyStreamEvent mirrors the connector's updateStreamState for the event
// types that build blocks. blocks is the feed root (tool events address
// blocks anywhere in the tree); target is the list to append to (null = the
// parent block vanished; the event is dropped, same as Go's nil targetList).
// Returns whether the feed content changed (drives change notification).
export function applyStreamEvent(blocks: Block[], target: Block[] | null, e: QueryEvent): boolean {
  switch (e.type) {
    case 'text_delta': {
      if (!target) return false
      const last = target[target.length - 1]
      if (last && last.kind === 'text') {
        last.text += e.text ?? ''
      } else {
        target.push({ kind: 'text', id: '', text: e.text ?? '' })
      }
      return true
    }
    case 'tool_start': {
      if (!target || !e.tool_use) return false
      const tu = e.tool_use
      target.push({
        kind: 'tool',
        id: tu.id,
        name: tu.name,
        summary: tu.summary ?? '',
        isSearch: !!tu.is_search,
        isRead: !!tu.is_read,
        isList: !!tu.is_list,
        isLsp: !!tu.is_lsp,
        isTask: !!tu.is_task,
        isWeb: tu.name === 'Web',
        state: 'running',
        timingNs: 0,
        displayOutput: '',
        startedAt: Date.now(),
        children: [],
      })
      return true
    }
    case 'tool_param_delta': {
      if (!e.partial_input) return false
      const b = findBlock(blocks, e.partial_input.id)
      if (!b) return false
      if (e.partial_input.summary) b.summary = e.partial_input.summary
      if (e.partial_input.is_search) b.isSearch = true
      if (e.partial_input.is_read) b.isRead = true
      if (e.partial_input.is_list) b.isList = true
      if (e.partial_input.is_lsp) b.isLsp = true
      if (e.partial_input.is_task) b.isTask = true
      return true
    }
    case 'tool_output_delta': {
      if (!e.tool_result) return false
      const b = findBlock(blocks, e.tool_result.tool_use_id)
      if (!b) return false
      b.displayOutput = e.tool_result.display_output ?? ''
      return true
    }
    case 'tool_end': {
      if (!e.tool_result) return false
      const b = findBlock(blocks, e.tool_result.tool_use_id)
      if (!b) return false
      b.state = e.tool_result.is_error ? 'error' : 'done'
      b.displayOutput = e.tool_result.display_output ?? ''
      if (e.tool_result.is_search) b.isSearch = true
      if (e.tool_result.is_read) b.isRead = true
      if (e.tool_result.is_list) b.isList = true
      if (e.tool_result.is_lsp) b.isLsp = true
      if (e.tool_result.is_task) b.isTask = true
      b.timingNs = (Date.now() - b.startedAt) * 1e6
      // Agent tool: streamed children are superseded by the final result —
      // the server cache clears them on tool_end, so the live path must
      // too or a reconnect hydration would render differently.
      b.children = []
      return true
    }
    case 'thinking_start': {
      if (!target) return false
      target.push({ kind: 'thinking', id: '', text: '', durationNs: 0, active: true, startedAt: Date.now() })
      return true
    }
    case 'thinking_delta': {
      if (!target || !e.thinking) return false
      const last = target[target.length - 1]
      if (!last || last.kind !== 'thinking') return false
      last.text += e.thinking.text ?? ''
      return true
    }
    case 'thinking_end': {
      if (!target || !e.thinking) return false
      const last = target[target.length - 1]
      if (!last || last.kind !== 'thinking') return false
      last.active = false
      last.durationNs = e.thinking.duration ?? 0
      return true
    }
    case 'attachment': {
      if (!target || !e.message) return false
      // Mirrors Go updateStreamState: prompt-mode attachments carry the
      // original user text in prompt; queued-message events carry only the
      // reminder envelope in content — prefer the prompt to keep the echo
      // unwrapped.
      let text: string = ''
      const att = e.message.attachment
      if (att && att.mode === 'prompt' && att.prompt) {
        text = att.prompt
      } else {
        for (const cb of e.message.content ?? []) {
          if (cb.type === 'text' && cb.text) text += cb.text
        }
      }
      if (!text) return false
      target.push({ kind: 'user', id: '', text })
      return true
    }
  }
  return false
}

export type EventBlockChange =
  | { kind: 'event'; e: QueryEvent }
  | { kind: 'reset' }

export interface EventBlockStore {
  // Reduces one event into the block list; true when content changed.
  apply(e: QueryEvent): boolean
  blocks(): Block[]
  // Monotonic change counter — bumps on every content change and on reset.
  version(): number
  reset(): void
  // Fired synchronously per apply/reset so a consumer can drive its DOM
  // sink with the same granularity the jobs panel gets from FeedChange.
  onChange(cb: (c: EventBlockChange) => void): () => void
}

export function createEventBlockStore(): EventBlockStore {
  let blocks: Block[] = []
  let version = 0
  const cbs = new Set<(c: EventBlockChange) => void>()

  const emit = (c: EventBlockChange) => {
    for (const cb of cbs) cb(c)
  }

  return {
    apply: (e) => {
      // The store is a single feed root: every consumer that routes nested
      // events (task panel) goes through jobs_store instead, so root and
      // target are the same list here.
      const changed = applyStreamEvent(blocks, blocks, e)
      if (changed) {
        version++
        emit({ kind: 'event', e })
      }
      return changed
    },
    blocks: () => blocks,
    version: () => version,
    reset: () => {
      blocks = []
      version++
      emit({ kind: 'reset' })
    },
    onChange: (cb) => {
      cbs.add(cb)
      return () => cbs.delete(cb)
    },
  }
}
