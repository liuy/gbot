export const SVG_NS = 'http://www.w3.org/2000/svg'

export type IconName =
  | 'plus'
  | 'send'
  | 'camera'
  | 'image'
  | 'file'
  | 'x'
  | 'refresh'
  | 'upload'
  | 'chevron-right'
  | 'chevron-left'
  | 'menu'
  | 'moon'
  | 'sun'
  | 'tai-chi'
  | 'user'
  | 'scroll-to-bottom'
  | 'dot'
  | 'copy'
  | 'check'
  | 'search'
  | 'settings'

export type IconVariant = 'outline' | 'solid' | 'mixed'

export interface IconOptions {
  size?: number
  strokeWidth?: number
  className?: string
}

export interface IconDef {
  path: string
  variant: IconVariant
  defaultStrokeWidth?: number
}

const ICONS: Record<IconName, IconDef> = {
  plus: {
    path: '<path d="M12 5v14M5 12h14"/>',
    variant: 'outline',
    defaultStrokeWidth: 2.5,
  },
  send: {
    path: '<path d="M4 12l16-8-8 16-2-6-6-2z"/>',
    variant: 'solid',
  },
  camera: {
    path: '<path d="M14.5 4h-5L7 7H4a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V9a2 2 0 0 0-2-2h-3l-2.5-3z"/><circle cx="12" cy="13" r="3"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  image: {
    path: '<rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8.5" cy="8.5" r="1.5"/><path d="M21 15l-5-5L5 21"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  file: {
    path: '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  x: {
    path: '<path d="M6 6L18 18M6 18L18 6"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  refresh: {
    path: '<path d="M21 12a9 9 0 11-6.219-8.56"/>',
    variant: 'outline',
    defaultStrokeWidth: 2.5,
  },
  upload: {
    path: '<path d="M12 16V4M5 11l7-7 7 7M5 20h14"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  'chevron-right': {
    path: '<path d="M9 6L15 12L9 18"/>',
    variant: 'outline',
    defaultStrokeWidth: 1.5,
  },
  'chevron-left': {
    path: '<path d="M15 6L9 12L15 18"/>',
    variant: 'outline',
    defaultStrokeWidth: 1.5,
  },
  menu: {
    path: '<rect x="2" y="6" width="20" height="3" rx="1.5" fill="currentColor" stroke="none"/><rect x="6" y="15" width="14" height="3" rx="1.5" fill="currentColor" stroke="none"/>',
    variant: 'mixed',
  },
  moon: {
    path: '<path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  sun: {
    path: '<circle cx="12" cy="12" r="5"/><path d="M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  'tai-chi': {
    path: '<path d="M17 3.34a10 10 0 1 1 -14.995 8.984l-.005 -.324l.005 -.324a10 10 0 0 1 14.995 -8.336zm-9 1.732a8 8 0 0 0 4 14.928l.2 -.005a4 4 0 0 0 0 -7.99l-.2 -.005a4 4 0 0 1 -.2 -7.995l.2 -.005a7.995 7.995 0 0 0 -4 1.072zm4 1.428a1.5 1.5 0 1 0 0 3a1.5 1.5 0 0 0 0 -3" fill="currentColor"/><circle cx="12" cy="15.5" r="1.5" fill="currentColor"/><circle cx="12" cy="8.5" r="1.5" fill="var(--color-ink2, white)"/>',
    variant: 'mixed',
  },
  user: {
    path: '<circle cx="12" cy="8" r="4"/><path d="M4 21v-1a8 8 0 0116 0v1"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  'scroll-to-bottom': {
    path: '<path d="M12 5v13M7 13l5 5 5-5"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  dot: {
    path: '<circle cx="12" cy="12" r="10"/><path d="M12 8v4M12 16h.01"/>',
    variant: 'outline',
    defaultStrokeWidth: 2.5,
  },
  copy: {
    path: '<rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  check: {
    path: '<polyline points="20 6 9 17 4 12"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  settings: {
    path: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  search: {
    path: '<circle cx="11" cy="11" r="7"/><path d="M21 21l-4.35-4.35"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
}

export function renderIcon(name: IconName, opts: IconOptions = {}): SVGElement {
  const def = ICONS[name]
  const size = opts.size ?? 24
  const svg = document.createElementNS(SVG_NS, 'svg')
  // xmlns attribute is serialized in outerHTML so tests can assert namespace
  // and the SVG stays well-formed when extracted across documents.
  svg.setAttribute('xmlns', SVG_NS)
  svg.setAttribute('width', String(size))
  svg.setAttribute('height', String(size))
  svg.setAttribute('viewBox', '0 0 24 24')
  // Decorative: button parents carry aria-label, so the svg must be hidden
  // from assistive tech to avoid duplicate announcements.
  svg.setAttribute('aria-hidden', 'true')
  if (opts.className) svg.setAttribute('class', opts.className)

  if (def.variant === 'outline') {
    svg.setAttribute('fill', 'none')
    svg.setAttribute('stroke', 'currentColor')
    svg.setAttribute('stroke-width', String(opts.strokeWidth ?? def.defaultStrokeWidth ?? 2))
    svg.setAttribute('stroke-linecap', 'round')
    svg.setAttribute('stroke-linejoin', 'round')
  } else if (def.variant === 'solid') {
    svg.setAttribute('fill', 'currentColor')
  }
  // mixed: leave svg-level defaults unset; each path carries its own fill/stroke.

  // DOMParser gives correct SVG namespace to children. The wrapper svg must
  // carry xmlns so jsdom assigns children to the SVG namespace; without it
  // child namespace is implementation-defined.
  const parsed = new DOMParser().parseFromString(
    `<svg xmlns="${SVG_NS}">${def.path}</svg>`,
    'image/svg+xml',
  )
  // Array.from snapshots the live childNodes before append mutates it.
  svg.append(...Array.from(parsed.documentElement.childNodes))
  return svg
}
