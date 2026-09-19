// The package ships no TypeScript types — this declares exactly the surface
// vnc_console.ts calls, nothing more. Lives in novnc.d.ts (not types.d.ts):
// TypeScript drops a .d.ts shadowed by the sibling types.ts as its assumed
// declaration-emit output, so an ambient module there never enters the
// program.
declare module '@novnc/novnc' {
  export interface RFBCredentials {
    password: string
  }
  export interface RFBOptions {
    credentials?: RFBCredentials
    wsProtocols?: string[]
  }
  export default class RFB {
    constructor(target: HTMLElement, urlOrChannel: string, options?: RFBOptions)
    viewOnly: boolean
    scaleViewport: boolean
    addEventListener(type: string, listener: (e: CustomEvent) => void): void
    removeEventListener(type: string, listener: (e: CustomEvent) => void): void
    disconnect(): void
    sendCredentials(credentials: RFBCredentials): void
    sendKey(keysym: number, code: string | null, down: boolean): void
  }
}
