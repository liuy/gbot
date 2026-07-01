import type { ChatMessage } from '../model'
import { interleavedItems } from '../model'
import Markdown from './Markdown'
import Thinking from './Thinking'
import ToolRenderer from './ToolRenderer'
import ProgressBar from './ProgressBar'

const avatarCls = "mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded-full text-[8px] font-semibold text-white"
const avatarU = `${avatarCls} bg-gradient-to-br from-teal-500 to-cyan-600`
const avatarG = `${avatarCls} bg-gradient-to-br from-blue to-purple-500`

export default function MessageComponent({
  message,
}: {
  message: ChatMessage
}) {
  const isUser = message.role === 'user'

  return (
    <div className="px-5">
      <div className="grid grid-cols-[auto_1fr_auto] gap-x-2">
        <div className={isUser ? 'invisible' : avatarG}>G</div>

        <div>
          {isUser ? (
            <div className="ml-auto w-fit text-left text-t1 text-[15px]">
              {message.textChunks.map((c) => c.text).join('')}
            </div>
          ) : (
            <div className="space-y-1">
              {interleavedItems(message).map((item, i) => {
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
          )}
        </div>

        <div className={isUser ? avatarU : 'invisible'}>U</div>
      </div>
    </div>
  )
}
