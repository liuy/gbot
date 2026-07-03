import { memo, useMemo, type ReactNode } from 'react'
import type { ChatMessage, Block } from '../model'
import Markdown from './Markdown'
import Thinking from './Thinking'
import ToolRenderer from './ToolRenderer'
import ToolGroup from './ToolGroup'
import ProgressBar from './ProgressBar'

export const avatarG = "flex h-5 w-5 shrink-0 items-center justify-center rounded-md bg-gradient-to-br from-blue to-violet text-[11px] font-bold text-white"
const avatarU = "flex h-5 w-5 shrink-0 items-center justify-center rounded-md bg-gradient-to-br from-t2 to-t3"

export function isCollapsibleTool(b: Block): boolean {
	return b.kind === 'tool' && (b.isSearch || b.isRead || b.isList || b.isLsp || b.isWeb)
}

// Renders blocks in order, grouping consecutive collapsible tool blocks into a
// <ToolGroup>. Non-empty text and non-collapsible tools flush the buffer;
// thinking does not break a group (matches TUI detectToolGroups).
function renderGrouped(blocks: Block[]) {
	const out: ReactNode[] = []
	let buffer: Extract<Block, { kind: 'tool' }>[] = []
	let key = 0

	const flush = () => {
		if (buffer.length === 0) return
		if (buffer.length >= 2) {
			out.push(<ToolGroup key={`grp-${key++}`} tools={buffer} />)
		} else {
			out.push(<ToolRenderer key={buffer[0].id} tool={buffer[0]} />)
		}
		buffer = []
	}

	for (const b of blocks) {
		switch (b.kind) {
			case 'thinking':
				out.push(<Thinking key={b.id} entry={b} />)
				break
			case 'tool':
				if (isCollapsibleTool(b)) {
					buffer.push(b)
				} else {
					flush()
					out.push(<ToolRenderer key={b.id} tool={b} />)
				}
				break
			case 'text':
				if (!b.text) break
				flush()
				out.push(<Markdown key={`txt-${key++}`}>{b.text}</Markdown>)
				break
			case 'user':
				if (!b.text) break
				flush()
				out.push(<div key={`usr-${key++}`} className="text-[13px] text-t2 italic ml-2 my-1">{b.text}</div>)
				break
		}
	}
	flush()
	return out
}

function MessageComponentBase({
  message,
}: {
  message: ChatMessage
}) {
  const isUser = message.role === 'user'
  const groups = useMemo(() => renderGrouped(message.blocks), [message.blocks])

  return (
    <div className="px-1.5">
      <div className="grid grid-cols-[1.25rem_1fr_1.25rem] items-start gap-x-1.5">

        <div className={isUser ? '' : avatarG}>{!isUser && 'G'}</div>

        <div className="min-w-0">
          {isUser ? (
            <div className="ml-auto w-fit text-left text-t1 text-[15px]">
              {message.blocks.flatMap(b => (b.kind === 'text' || b.kind === 'user') ? [b.text] : []).join('')}
            </div>
          ) : (
            <div className="space-y-3">
              {groups}
              {message.error && (
                <div className="rounded-lg border border-red/40 bg-red/5 px-3 py-2 text-sm text-red">
                  {message.error}
                </div>
              )}
              {message.status === 'streaming' && <ProgressBar message={message} />}
            </div>
          )}
        </div>

        <div className={isUser ? avatarU : ''}>
          {isUser && (
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="white" strokeWidth="2">
              <circle cx="12" cy="8" r="4" />
              <path d="M4 21v-1a8 8 0 0116 0v1" />
            </svg>
          )}
        </div>
      </div>
    </div>
  )
}

export default memo(MessageComponentBase)
