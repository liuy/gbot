export type HistCursor = 'none' | 'home' | 'end'

export interface HistResult {
  text: string
  cursor: HistCursor
}

// History stores command history for Up/Down navigation.
//
// Port of pkg/tui/history.go. State model:
//   - historyIndex: 0 = at draft (initial), 1+ = navigating history
//     (1 = newest entry, N = oldest entry)
//   - savedDraft: user's current input, saved on first Up press
//     (only saved when input is non-empty)
export class History {
  private items: string[] = []
  private historyIndex = 0
  private savedDraft = ''

  load(arr: string[]): void {
    this.items = arr.slice()
    this.historyIndex = 0
    this.savedDraft = ''
  }

  add(cmd: string): void {
    if (cmd === '') return
    if (this.items.length > 0 && this.items[this.items.length - 1] === cmd) return
    this.items.push(cmd)
    this.historyIndex = 0
    this.savedDraft = ''
  }

  up(current: string): HistResult {
    if (this.items.length === 0) {
      return { text: current, cursor: 'none' }
    }
    const targetIndex = this.historyIndex
    this.historyIndex++
    if (targetIndex === 0) {
      if (current.trim() !== '') {
        this.savedDraft = current
      } else {
        this.savedDraft = ''
      }
    }
    if (targetIndex >= this.items.length) {
      this.historyIndex--
      return { text: '', cursor: 'none' }
    }
    const item = this.items[this.items.length - 1 - targetIndex]
    return { text: item, cursor: 'home' }
  }

  down(): HistResult {
    const currentIndex = this.historyIndex
    if (currentIndex > 1) {
      this.historyIndex--
      const item = this.items[this.items.length - currentIndex + 1]
      return { text: item, cursor: 'end' }
    }
    if (currentIndex === 1) {
      this.historyIndex = 0
      if (this.savedDraft !== '') {
        return { text: this.savedDraft, cursor: 'end' }
      }
      return { text: '', cursor: 'end' }
    }
    return { text: '', cursor: 'none' }
  }

  resetNav(): void {
    this.historyIndex = 0
    this.savedDraft = ''
  }
}
