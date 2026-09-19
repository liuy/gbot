// Test-only replacement for @novnc/novnc (see __mocks__ README note in
// vnc_console.test.ts): records every construction for assertions.
export interface MockRFBInstance {
  target: HTMLElement
  url: string
  options: { credentials?: { password: string }; wsProtocols?: string[] } | undefined
  viewOnly: boolean
  scaleViewport: boolean
  disconnectCalls: number
  sentCredentials: Array<{ password: string }>
  sentKeys: { keysym: number; down: boolean }[]
  fire: (type: string, detail?: Record<string, unknown>) => void
}

type Listener = (e: { detail: Record<string, unknown> }) => void

export class MockRFB {
  static instances: MockRFB[] = []
  canvas: HTMLCanvasElement
  viewOnly = false
  scaleViewport = false
  disconnectCalls = 0
  sentCredentials: Array<{ password: string }> = []
  sentKeys: { keysym: number; down: boolean }[] = []
  private listeners = new Map<string, Listener[]>()
  constructor(
    public target: HTMLElement,
    public url: string,
    public options?: { credentials?: { password: string }; wsProtocols?: string[] },
  ) {
    MockRFB.instances.push(this)
    // Real RFB appends its canvas into the target; the sheet dispatches a
    // synthetic mouseup there to release held buttons.
    this.canvas = document.createElement('canvas')
    target.appendChild(this.canvas)
  }
  addEventListener(type: string, listener: Listener): void {
    const arr = this.listeners.get(type) ?? []
    arr.push(listener)
    this.listeners.set(type, arr)
  }
  removeEventListener(): void {}
  disconnect(): void {
    this.disconnectCalls++
  }
  sendCredentials(credentials: { password: string }): void {
    this.sentCredentials.push(credentials)
  }
  sendKey(keysym: number, _code: string | null, down: boolean): void {
    this.sentKeys.push({ keysym, down })
  }
  fire(type: string, detail: Record<string, unknown> = {}): void {
    for (const l of this.listeners.get(type) ?? []) l({ detail })
  }
}

export default MockRFB

export const instances = (): MockRFB[] => MockRFB.instances
