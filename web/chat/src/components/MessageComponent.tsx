import type { ChatMessage } from '../model'
import { interleavedItems } from '../model'
import Markdown from './Markdown'
import Thinking from './Thinking'
import ToolRenderer from './ToolRenderer'
import ProgressBar from './ProgressBar'

export default function MessageComponent({
  message,
}: {
  message: ChatMessage
}) {
  if (message.role === 'user') {
    const userText = message.textChunks.map((c) => c.text).join('')
    return (
      <div className="flex justify-end px-5">
        <div className="flex max-w-[80%] gap-2">
          <div className="text-t1 text-[15px]">
            {userText}
          </div>
          <div className="mt-1 flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-gradient-to-br from-teal-500 to-cyan-600 text-[8px] font-semibold text-white">U</div>
        </div>
      </div>
    )
  }

  const items = interleavedItems(message)

  return (
    <div className="px-5">
      <div className="flex gap-2">
        <div className="mt-1 flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-gradient-to-br from-blue to-purple-500 text-[8px] font-semibold text-white">G</div>
        <div className="min-w-0 flex-1 space-y-1">
          {items.map((item, i) => {
            switch (item.kind) {
              case 'thinking':
                return <Thinking key={`t-${i}`} entry={item.entry} />
              case 'tool':
                return <ToolRenderer key={item.entry.id} tool={item.entry} />
              case 'text':
                return item.entry.text ? (
                  <Markdown key={`txt-${i}`}>{item.entry.text}</Markdown>
                ) : null
            }
          })}
          {message.error && (
            <div className="rounded-lg border border-red/40 bg-red/5 px-3 py-2 text-sm text-red">
              {message.error}
            </div>
          )}
          {message.status === 'streaming' && <ProgressBar message={message} />}
        </div>
      </div>
    </div>
  )
}
