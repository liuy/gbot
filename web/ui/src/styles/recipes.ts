import { tv } from 'tailwind-variants'

export const errorBox = tv({
  base: 'rounded-lg border border-red/40 bg-red/5 px-3 py-2 text-sm text-red break-all',
})

export const compactDividerContainer = tv({ base: 'flex items-center gap-2 my-4 px-4' })
export const dividerHairline = tv({ base: 'flex-1 border-t border-hairline' })
export const dividerLabel = tv({ base: 'text-blue text-[10px] shrink-0' })
export const timeDividerContainer = tv({ base: 'flex justify-center items-center my-4 px-4' })

export const floatingButton = tv({
  base: 'flex h-11 w-11 items-center justify-center rounded-full bg-transparent opacity-0 pointer-events-none transition-all duration-200 text-blue z-50 absolute bottom-24',
  variants: { position: { center: 'left-1/2 -translate-x-1/2', right: 'right-5' } },
})

export const popupPanel = tv({
  base: 'bg-ink2/75 backdrop-blur-[20px] backdrop-saturate-[1.5] border border-hairline rounded-xl shadow-2xl modal-enter z-40 hidden w-[90vw] max-w-sm fixed',
  variants: { position: { top: 'left-1/2 -translate-x-1/2 top-12', bottom: 'left-1/2 -translate-x-1/2 bottom-20' } },
})

export const anchoredPopup = tv({
  base: 'fixed hidden z-40 bg-ink2/75 backdrop-blur-[20px] backdrop-saturate-[1.5] border border-hairline rounded-xl shadow-2xl',
})

// Shared between tool header and thinking header
export const toolHeaderBtn = tv({ base: 'flex items-baseline cursor-pointer bg-transparent border-0 p-0 text-left' })
export const toolPrefix = tv({ base: 'shrink-0 w-6' })
export const toolHeaderContent = tv({ base: 'flex-1 min-w-0' })
export const runningDot = tv({
  base: 'text-[10px] leading-none align-middle inline-block w-3 text-center',
  variants: { color: { white: 'text-white heartbeat', blue: 'text-blue heartbeat' } },
})
export const chevron = tv({
  base: 'inline-block align-middle text-t3 transition-transform',
  variants: { expanded: { true: 'rotate-90', false: '' } },
})
export const thinkingGlyph = tv({ base: 'text-amber text-sm inline-block w-3 text-center heartbeat' })
export const thinkingLabel = tv({ base: 'text-amber text-sm' })

export const textBlock = tv({ base: 'md-body md-text text-t1 text-[15px] break-words' })
export const userEchoBlock = tv({ base: 'text-[13px] text-t2 italic ml-2 my-1' })
export const userTextSpan = tv({ base: 'whitespace-pre-wrap break-words' })
export const thinkingText = tv({ base: 'md-body md-text ml-6 text-t2 text-sm break-words' })

export const toolName = tv({ base: 'font-mono text-sm text-blue' })
export const toolSummary = tv({ base: 'text-sm text-t2 font-light break-all' })
export const toolDuration = tv({
  base: 'font-mono text-xs',
  variants: { state: { running: 'text-blue', done: 'text-t3', error: 'text-red' } },
})
export const toolBody = tv({ base: 'md-body ml-6 font-mono text-sm leading-relaxed text-t2 overflow-x-auto break-words hidden' })
export const toolChildren = tv({ base: 'ml-6 mt-1 space-y-1 border-l border-t3/30 pl-2 hidden' })

export const groupSummary = tv({ base: 'font-mono text-sm text-blue' })
export const groupDuration = tv({ base: 'font-mono text-xs text-t3' })
export const groupToolsContainer = tv({ base: 'ml-6 hidden' })

// contentArea has no base — role variant is the whole output.
export const contentArea = tv({
  variants: {
    role: {
      user: 'ml-auto max-w-fit text-left text-t1 text-[15px] whitespace-pre-wrap break-words',
      assistant: 'space-y-3',
    },
  },
})

export const progressBar = tv({ base: 'mt-2 flex items-center gap-1 overflow-x-auto overflow-y-hidden whitespace-nowrap text-xs text-t3' })

export const shellOuter = tv({ base: 'px-1.5' })
export const shellGrid = tv({ base: 'grid grid-cols-[1.25rem_1fr_1.25rem] items-start gap-x-1.5' })
export const shellCenter = tv({ base: 'min-w-0' })
export const avatarBase = tv({ base: 'flex h-5 w-5 shrink-0 items-center justify-center rounded-md' })
export const avatarG = tv({ base: 'text-[11px] font-bold avatar-g-bg' })
export const avatarU = tv({ base: 'bg-gradient-to-br from-t2 to-t3' })
export const disconnectBannerClass = tv({
  base: 'absolute top-11 inset-x-0 z-50 card-bg border-b border-hairline px-4 py-1.5 flex items-center justify-center transition-all duration-300 overflow-hidden max-h-0 opacity-0',
})
export const disconnectText = tv({ base: 'text-[12px] text-red' })
