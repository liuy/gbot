import { tv } from 'tailwind-variants'
import { createNode, cx } from './dom'
import { renderIcon, type IconName } from './icons'
import { bindLongPress, createPopupPanel, createPopupHost } from './utils'
import { floatingButton } from './styles/recipes'
import { progressRingCircles, progressRingDashOffset } from './components/progress_ring'

// ── Recipes ──────────────────────────────────────────────────
//
// tv recipes here are the button-factory implementation detail — they are
// not the cross-module atoms that live in styles/recipes.ts. floatingButton
// (still in recipes.ts) is independent: chat scrollBtn / task_panel root
// use it directly, and iconButtonRecipe's `floating` variant is defined
// for symmetry but currently has zero callers.

export const iconButtonRecipe = tv({
  base: 'flex items-center justify-center transition-colors',
  variants: {
    variant: {
      default: 'text-blue hover:text-white',
      ghost: 'text-t2 hover:text-t1',
      solid: 'bg-blue text-white hover:bg-blue/80',
      subtle: 'text-blue hover:text-white hover:bg-blue/10',
      floating: 'bg-blue text-white shadow-lg hover:bg-blue/80',
    },
    size: {
      // auto: shrink to icon size — for header hamburgerWrap where the
      // adjacent wordmark is a sibling, not a child, and a fixed w/h would
      // add visible padding between them.
      auto: '',
      xs: 'w-4 h-4 rounded-full',
      sm: 'w-7 h-7 rounded-full',
      // md stays square (no rounded): the sidebar themeToggle is square in
      // the current visual, and adding rounded-full here would be a
      // visible regression.
      md: 'w-10 h-10',
      lg: 'w-9 h-9 rounded-lg',
    },
  },
})

export const textButtonRecipe = tv({
  // base holds only transition-colors; rounded-lg lives inside each variant
  // that needs it so `link` can stay cornerless and padding-free for
  // breadcrumb-style triggers (header pickers).
  base: 'transition-colors',
  variants: {
    variant: {
      primary: 'rounded-lg bg-blue/20 text-blue hover:bg-blue/30',
      danger: 'rounded-lg bg-red/10 text-red hover:bg-red/20',
      ghost: 'rounded-lg bg-transparent text-t2 hover:bg-ink3/50',
      // link: no padding, no rounded — caller controls spacing via container.
      // Used for header breadcrumb triggers (modelPicker/enginePicker/etc).
      link: 'bg-transparent text-t2 hover:text-t1',
    },
    size: {
      sm: 'px-3 py-1.5 text-sm',
      md: 'px-4 py-2 text-base',
    },
  },
  // link variant composes without size padding: the size class still applies
  // but link overrides padding via the variant. This keeps the type system
  // happy (size is required by callers) without forcing padding on triggers.
  compoundVariants: [
    { variant: 'link', class: 'px-0 py-0' },
  ],
})

export const toggleGroupRecipe = tv({ base: 'inline-flex items-center gap-1' })

export const comboButtonRecipe = tv({ base: 'flex items-center gap-1 transition-colors cursor-pointer' })

// ── createIconButton ─────────────────────────────────────────

export interface IconButtonOptions {
  icon: IconName
  label: string
  size?: 'auto' | 'xs' | 'sm' | 'md' | 'lg'
  variant?: 'default' | 'ghost' | 'solid' | 'subtle' | 'floating'
  // iconSize overrides the size-derived default; needed because real callers
  // span 9..24px (retry=9, plusBtn=24) and binding icon size to button
  // size would force multiple visible regressions.
  iconSize?: number
  strokeWidth?: number
  // setIcon is passed into onClick so 2-state toggle buttons (copy↔copied,
  // star↔unstar, follow↔following) can swap icons without a separate factory.
  // Single-icon buttons ignore the second argument.
  onClick?: (e: MouseEvent, setIcon: (icon: IconName) => void) => void
  onDblClick?: (e: MouseEvent) => void
  onLongPress?: () => void
  className?: string
}

function iconSizeFor(size: 'auto' | 'xs' | 'sm' | 'md' | 'lg'): number {
  switch (size) {
    case 'auto': return 18
    case 'xs': return 9
    case 'sm': return 18
    case 'md': return 22
    case 'lg': return 20
  }
}

export function createIconButton(opts: IconButtonOptions): HTMLButtonElement {
  const size = opts.size ?? 'sm'
  const variant = opts.variant ?? 'default'
  const iconSize = opts.iconSize ?? iconSizeFor(size)
  const btn = createNode('button', {
    className: iconButtonRecipe({ variant, size, class: opts.className }),
    props: { type: 'button' },
    attrs: { 'aria-label': opts.label },
  })
  btn.replaceChildren(renderIcon(opts.icon, {
    size: iconSize,
    strokeWidth: opts.strokeWidth,
  }))

  // Click listener is installed only when onLongPress or onClick is provided.
  // Two reasons: (1) when onLongPress fires, the browser synthesizes a
  // follow-up `click` that must be swallowed by consumeTrigger so onClick
  // does not double-fire; (2) callers that wire their own addEventListener
  // (plusBtn / sendBtn) pass no onClick here, so the factory stays out of
  // their click path entirely.
  if (opts.onLongPress || opts.onClick) {
    let lp: { consumeTrigger: () => boolean } | null = null
    if (opts.onLongPress) {
      // useMouse mirrors sidebar themeToggle: desktop mousedown also starts
      // the long-press timer so the same handler serves touch + mouse.
      lp = bindLongPress(btn, opts.onLongPress, { useMouse: true })
    }
    // setIcon lets onClick swap the rendered icon — used by 2-state toggle
    // buttons (copy↔copied, star↔unstar). Reuses iconSize / strokeWidth
    // from the initial render so the swapped icon visually matches.
    const setIcon = (icon: IconName) => {
      btn.replaceChildren(renderIcon(icon, { size: iconSize, strokeWidth: opts.strokeWidth }))
    }
    btn.addEventListener('click', (e) => {
      if (lp?.consumeTrigger()) return
      opts.onClick?.(e, setIcon)
    })
  }

  if (opts.onDblClick) {
    btn.addEventListener('dblclick', opts.onDblClick)
  }

  return btn
}

// ── createTextButton ─────────────────────────────────────────

export interface TextButtonOptions {
  text: string
  variant: 'primary' | 'danger' | 'ghost' | 'link'
  size?: 'sm' | 'md'
  icon?: IconName
  iconSize?: number
  onClick?: (e: MouseEvent) => void
  onDblClick?: (e: MouseEvent) => void
  onLongPress?: () => void
  className?: string
}

export function createTextButton(opts: TextButtonOptions): HTMLButtonElement {
  const size = opts.size ?? 'sm'
  const btn = createNode('button', {
    className: textButtonRecipe({ variant: opts.variant, size, class: opts.className }),
    props: { type: 'button' },
    attrs: { 'aria-label': opts.text },
  })
  if (opts.icon) {
    const icon = renderIcon(opts.icon, { size: opts.iconSize ?? 14 })
    const labelSpan = createNode('span', { text: opts.text })
    btn.replaceChildren(icon, labelSpan)
  } else {
    btn.textContent = opts.text
  }

  if (opts.onLongPress || opts.onClick) {
    let lp: { consumeTrigger: () => boolean } | null = null
    if (opts.onLongPress) {
      lp = bindLongPress(btn, opts.onLongPress, { useMouse: true })
    }
    btn.addEventListener('click', (e) => {
      if (lp?.consumeTrigger()) return
      opts.onClick?.(e)
    })
  }

  if (opts.onDblClick) {
    btn.addEventListener('dblclick', opts.onDblClick)
  }

  return btn
}

// ── createToggleGroup ────────────────────────────────────────
//
// API-completeness port from persona. Zero callers today; the icon branch
// goes through createIconButton (variant ghost) so visual styling stays
// consistent with the rest of the button system.

export type ToggleGroupItem = {
  id: string
  icon?: IconName
  label: string
  className?: string
}

export interface ToggleGroupHandle {
  element: HTMLElement
  setSelected: (id: string) => void
}

export function createToggleGroup(opts: {
  items: ToggleGroupItem[]
  selectedId: string
  onSelect: (id: string) => void
  className?: string
}): ToggleGroupHandle {
  const wrap = createNode('div', {
    className: cx(toggleGroupRecipe(), opts.className),
    attrs: { role: 'group' },
  })
  let currentId = opts.selectedId
  const buttons: { id: string; btn: HTMLButtonElement }[] = []

  const updatePressed = () => {
    for (const entry of buttons) {
      entry.btn.setAttribute('aria-pressed', entry.id === currentId ? 'true' : 'false')
    }
  }

  for (const item of opts.items) {
    const onSelectItem = () => {
      currentId = item.id
      updatePressed()
      opts.onSelect(item.id)
    }
    const btn = item.icon
      ? createIconButton({
          icon: item.icon,
          label: item.label,
          variant: 'ghost',
          className: item.className,
          onClick: onSelectItem,
        })
      : createTextButton({
          text: item.label,
          variant: 'link',
          className: item.className,
          onClick: onSelectItem,
        })
    btn.setAttribute('aria-pressed', item.id === currentId ? 'true' : 'false')
    buttons.push({ id: item.id, btn })
    wrap.appendChild(btn)
  }

  return {
    element: wrap,
    setSelected: (id: string) => { currentId = id; updatePressed() },
  }
}

// ── createComboButton ────────────────────────────────────────
//
// Wraps createPopupHost so header pickers (modelPicker / enginePicker) share
// a single open/close + outside-click machinery. className is applied to the
// trigger <button>, not the wrap div: header.test.ts:384/442 select via
// `button.mono.text-[14px]:not(.hidden)` and would miss a wrap-level class.

export interface ComboButtonHandle {
  wrap: HTMLElement
  setLabel: (text: string) => void
  open: () => void
  close: () => void
  toggle: () => void
}

export function createComboButton(opts: {
	label: string
	className?: string
	onOpen: (panel: HTMLElement) => void
	onClose?: () => void
	onClick?: (e: MouseEvent) => void
	onDblClick?: (e: MouseEvent) => void
	onLongPress?: () => void
}): ComboButtonHandle {
	const wrap = createNode('div', { className: 'relative' })
	const panel = createPopupPanel({})
	const host = createPopupHost({
		trigger: wrap,
		panel,
		onOpen: () => opts.onOpen(panel),
		onClose: opts.onClose,
	})
	// Built after host so the click closure can reference host.toggle without
	// a forward-declaration dance. onClick (when provided) fully replaces the
	// default toggle behavior — callers that want to drive open/close through
	// their own state machine (none today, but the spec requires the option)
	// pass onClick and manage host.open / host.close themselves via the handle.
	const trigger = createTextButton({
		text: opts.label,
		variant: 'link',
		className: opts.className,
		onClick: (e) => {
			if (opts.onClick) opts.onClick(e)
			else host.toggle()
		},
		onDblClick: opts.onDblClick,
		onLongPress: opts.onLongPress,
	})
	wrap.appendChild(trigger)
	return {
		wrap,
		setLabel: (text) => {
			trigger.textContent = text
		},
		open: host.open,
		close: host.close,
		toggle: host.toggle,
	}
}

// ── createFloatButton ────────────────────────────────────────
//
// Floating button with optional progress ring + inner icon/label. Powers
// chat scrollBtn (progress ring + scroll-to-bottom arrow) and task_panel
// root (progress ring + counter text). The position variant comes from
// the existing floatingButton recipe in styles/recipes.ts so visuals stay
// byte-stable with the pre-factory implementation.

export interface FloatButtonHandle {
  root: HTMLButtonElement
  setProgress: (pct: number) => void
  setLabel: (text: string) => void
}

export function createFloatButton(opts: {
  position: 'center' | 'right'
  progressRing?: {
    progressClassName: string
    backgroundClassName?: string
    backgroundOpacity?: number
    transitionMs?: number
    transitionEasing?: string
  }
  // labelClassName lets callers identify the <text> element via their own
  // selector (e.g. task_panel.test.ts asserts `.task-label`). Defaults to
  // 'float-label' which is also what setLabel queries internally.
  labelClassName?: string
  innerIcon?: IconName
  innerLabel?: string
  onClick?: () => void
}): FloatButtonHandle {
  const root = createNode('button', {
    className: floatingButton({ position: opts.position }),
    props: { type: 'button' },
  })

  const labelCls = opts.labelClassName ?? 'float-label'
  const parts: string[] = ['<svg width="44" height="44" viewBox="0 0 44 44">']
  if (opts.progressRing) {
    parts.push(progressRingCircles(opts.progressRing))
  }
  if (opts.innerIcon) {
    // renderIcon returns an SVGElement with its own 24×24 viewBox; we embed
    // it as a child via outerHTML and translate it to the 44×44 center. This
    // keeps icon paths in a single source of truth (icons.ts) instead of
    // duplicating path strings per rendering context.
    const innerSvg = renderIcon(opts.innerIcon, { size: 28, strokeWidth: 1.5 })
    parts.push('<g transform="translate(8, 8)">')
    parts.push(innerSvg.outerHTML)
    parts.push('</g>')
  }
  if (opts.innerLabel !== undefined) {
    parts.push(
      `<text class="${labelCls}" x="22" y="22" text-anchor="middle" dominant-baseline="central" ` +
      'fill="currentColor" style="font-size:11px;font-weight:600;font-family:ui-monospace,monospace">' +
      escapeXml(opts.innerLabel) + '</text>'
    )
  } else {
    // Empty <text> so setLabel has a target even without initial content.
    parts.push(
      `<text class="${labelCls}" x="22" y="22" text-anchor="middle" dominant-baseline="central" ` +
      'fill="currentColor" style="font-size:11px;font-weight:600;font-family:ui-monospace,monospace"></text>'
    )
  }
  parts.push('</svg>')
  root.innerHTML = parts.join('')

  if (opts.onClick) {
    root.addEventListener('click', opts.onClick)
  }

  const progressCircle = opts.progressRing
    ? root.querySelector('.' + opts.progressRing.progressClassName) as SVGCircleElement | null
    : null
  const labelEl = root.querySelector('.' + labelCls) as SVGTextElement | null

  return {
    root,
    setProgress: (pct: number) => {
      if (progressCircle) {
        progressCircle.setAttribute('stroke-dashoffset', String(progressRingDashOffset(pct)))
      }
    },
    setLabel: (text: string) => {
      if (labelEl) labelEl.textContent = text
    },
  }
}

// getInnerIconPath removed — createFloatButton now uses renderIcon from
// icons.ts to embed inner icons, keeping all icon paths in a single source.

function escapeXml(s: string): string {
  return s.replace(/[<>&'"]/g, (c) => {
    switch (c) {
      case '<': return '&lt;'
      case '>': return '&gt;'
      case '&': return '&amp;'
      case '\'': return '&apos;'
      case '"': return '&quot;'
      default: return c
    }
  })
}
