export const SVG_NS = 'http://www.w3.org/2000/svg'

export type IconName =
  | 'keyboard'
  | 'eye'
  | 'pointer'
  | 'maximize'
  | 'one-to-one'
  | 'plus'
  | 'send'
  | 'camera'
  | 'image'
  | 'file'
  | 'film'
  | 'globe'
  | 'box'
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
  | 'trash'

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
  eye: {
    path: '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  pointer: {
    path: '<path d="M14 4.1 12 6"/><path d="m5.1 8-2.9-.8"/><path d="m6 12-1.9 2"/><path d="M7.2 2.2 8 5.1"/><path d="M9.037 9.69a.498.498 0 0 1 .653-.653l11 4.5a.5.5 0 0 1-.074.949l-4.349 1.041a1 1 0 0 0-.74.739l-1.04 4.35a.5.5 0 0 1-.95.074z"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  'one-to-one': {
    // Ratio badge for the VNC view-mode chip. The text is deliberately large
    // relative to the frame: at the 15px chip size anything smaller turns to
    // mush. fill/stroke are set on the text itself, so the outline variant's
    // stroked defaults do not turn the glyphs into outlines.
    path: '<rect x="2" y="5" width="20" height="14" rx="3"/><text x="12" y="16" text-anchor="middle" font-size="9.5" font-weight="700" fill="currentColor" stroke="none">1:1</text>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  maximize: {
    path: '<path d="M8 3H5a2 2 0 0 0-2 2v3"/><path d="M21 8V5a2 2 0 0 0-2-2h-3"/><path d="M3 16v3a2 2 0 0 0 2 2h3"/><path d="M16 21h3a2 2 0 0 0 2-2v-3"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  keyboard: {
    path: '<rect x="2" y="4" width="20" height="16" rx="2"/><path d="M6 8h.01M10 8h.01M14 8h.01M18 8h.01M6 12h.01M10 12h.01M14 12h.01M18 12h.01M8 16h8"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
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
  film: {
    path: '<rect x="2" y="2" width="20" height="20" rx="2.18"/><path d="M7 2v20M17 2v20M2 12h20M2 7h5M2 17h5M17 17h5M17 7h5"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  globe: {
    path: '<circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20"/><path d="M2 12h20"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  box: {
    path: '<path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/>',
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
    // Full-short-full bars in the menu icon's filled language, echoing
    // the 2-line menu's inset second bar — family consistency with rank.
    path: '<rect x="2" y="4.5" width="20" height="3" rx="1.5" fill="currentColor" stroke="none"/><rect x="6" y="10.5" width="14" height="3" rx="1.5" fill="currentColor" stroke="none"/><rect x="2" y="16.5" width="20" height="3" rx="1.5" fill="currentColor" stroke="none"/>',
    variant: 'mixed',
  },
  search: {
    path: '<circle cx="11" cy="11" r="7"/><path d="M21 21l-4.35-4.35"/>',
    variant: 'outline',
    defaultStrokeWidth: 2,
  },
  trash: {
    path: '<path d="M3 6h18"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/><path d="M10 11v6"/><path d="M14 11v6"/>',
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
  // Decorative: the parent chip carries a title, so the svg is hidden from
  // assistive tech to avoid duplicate announcements.
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
