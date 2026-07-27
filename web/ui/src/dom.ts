export function createElement<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className?: string,
): HTMLElementTagNameMap[K] {
  const element = document.createElement(tag)
  if (className) {
    element.className = className
  }
  return element
}

export function createElementInDocument<K extends keyof HTMLElementTagNameMap>(
  doc: Document,
  tag: K,
  className?: string,
): HTMLElementTagNameMap[K] {
  const element = doc.createElement(tag)
  if (className) {
    element.className = className
  }
  return element
}

export function createFragment(): DocumentFragment {
  return document.createDocumentFragment()
}

export interface CreateNodeOptions {
  className?: string
  /** Sets `element.textContent` before any `children` are appended. */
  text?: string
  /** Attribute name → value pairs applied via `setAttribute`. */
  attrs?: Record<string, string>
  props?: Record<string, unknown>
  /**
   * Inline styles. Nullish (`undefined`/`null`) values are skipped so callers
   * can inline conditionals (e.g. `borderColor: cfg.borderColor`) without an
   * `if` per property. Note this only *sets* values, it never clears them, so
   * prefer it for constructing fresh elements rather than re-styling live ones.
   */
  style?: Partial<CSSStyleDeclaration>
}

export function createNode<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  opts: CreateNodeOptions = {},
  ...children: Array<Node | string | null | false | undefined>
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag)
  if (opts.className) el.className = opts.className
  if (opts.text !== undefined) el.textContent = opts.text
  if (opts.attrs) {
    for (const [k, v] of Object.entries(opts.attrs)) el.setAttribute(k, v)
  }
  if (opts.props) {
    const target = el as unknown as Record<string, unknown>
    for (const [k, v] of Object.entries(opts.props)) target[k] = v
  }
  if (opts.style) {
    // Per-property loop with `value != null` guard, matching persona contract.
    // Object.assign would pass null/undefined straight into CSSStyleDeclaration
    // setters (null clears the property, undefined is browser-dependent).
    const style = el.style as unknown as Record<string, string>
    const source = opts.style as Record<string, string | null | undefined>
    for (const property of Object.keys(source)) {
      const value = source[property]
      if (value != null) style[property] = value
    }
  }
  const appendable = children.filter(
    (c): c is Node | string => c != null && c !== false,
  )
  if (appendable.length > 0) el.append(...appendable)
  return el
}

/**
 * Join truthy class-name fragments into a single space-separated string.
 * Falsy fragments (`false` / `null` / `undefined` / `""`) are dropped, so
 * conditional classes read inline as `cond && "x"` instead of imperative
 * `classList.add(...)` branches.
 */
export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(' ')
}
