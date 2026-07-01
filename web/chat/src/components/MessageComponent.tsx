import type { ReactNode } from 'react'
import type { ChatMessage, InterleavedItem, ToolEntry } from '../model'
import { interleavedItems } from '../model'
import Markdown from './Markdown'
import Thinking from './Thinking'
import ToolRenderer from './ToolRenderer'
import ToolGroup from './ToolGroup'
import ProgressBar from './ProgressBar'

const avatarCls = "flex h-5 w-5 shrink-0 items-center justify-center rounded-full text-[9px] font-semibold text-white"
const avatarU = `${avatarCls} bg-gradient-to-br from-teal-500 to-cyan-600`
const avatarG = `${avatarCls} bg-gradient-to-br from-blue to-purple-500`

function isCollapsible(t: ToolEntry): boolean {
	return t.isSearch || t.isRead || t.isList || t.isLsp || t.isWeb
}

// Renders interleaved items, grouping consecutive collapsible tools into a
// <ToolGroup>. Non-empty text and non-collapsible tools flush the buffer;
// thinking does not break a group (matches TUI detectToolGroups).
function renderGrouped(items: InterleavedItem[]) {
	const out: ReactNode[] = []
	let buffer: ToolEntry[] = []
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

	for (const item of items) {
		switch (item.kind) {
			case 'thinking':
				out.push(<Thinking key={`t-${key++}`} entry={item.entry} />)
				break
			case 'tool':
				if (isCollapsible(item.entry)) {
					buffer.push(item.entry)
				} else {
					flush()
					out.push(<ToolRenderer key={item.entry.id} tool={item.entry} />)
				}
				break
			case 'text':
				if (!item.entry.text) break
				flush()
				out.push(<Markdown key={`txt-${key++}`}>{item.entry.text}</Markdown>)
				break
		}
	}
	flush()
	return out
}

export default function MessageComponent({
  message,
}: {
  message: ChatMessage
}) {
  const isUser = message.role === 'user'

  return (
    <div className="px-1.5">
      <div className="grid grid-cols-[1.25rem_1fr_1.25rem] items-start gap-x-1.5">

        <div className={isUser ? '' : avatarG}>{!isUser && 'G'}</div>

        <div className="min-w-0">
          {isUser ? (
            <div className="ml-auto w-fit text-left text-t1 text-[15px]">
              {message.textChunks.map((c) => c.text).join('')}
            </div>
          ) : (
            <div className="space-y-3">
              {renderGrouped(interleavedItems(message))}
              {message.error && (
                <div className="rounded-lg border border-red/40 bg-red/5 px-3 py-2 text-sm text-red">
                  {message.error}
                </div>
              )}
              {message.status === 'streaming' && <ProgressBar message={message} />}
            </div>
          )}
        </div>

        <div className={isUser ? avatarU : ''}>{isUser && 'U'}</div>
      </div>
    </div>
  )
}
