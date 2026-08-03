declare module 'idiomorph' {
  export interface MorphCallbacks {
    beforeNodeAdded?: (node: Node) => boolean | void
    afterNodeAdded?: (node: Node) => void
    beforeNodeMorphed?: (oldNode: Node, newNode: Node) => boolean | void
    afterNodeMorphed?: (oldNode: Node, newNode: Node) => void
    beforeNodeRemoved?: (node: Node) => boolean | void
    afterNodeRemoved?: (node: Node) => void
  }

  export interface MorphOptions {
    morphStyle?: 'outerHTML' | 'innerHTML'
    ignoreActive?: boolean
    ignoreActiveValue?: boolean
    restoreFocus?: boolean
    callbacks?: MorphCallbacks
    head?: { style?: 'merge' | 'append' | 'morph' | 'none'; shouldPreserve?: (el: Element) => boolean; shouldReAppend?: (el: Element) => boolean; shouldRemove?: (el: Element) => boolean }
  }

  export const Idiomorph: {
    morph(fromNode: Node, toNode: Node | string, options?: MorphOptions): Node[]
    defaults: MorphOptions
  }
}
