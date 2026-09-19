// noVNC's module graph contains top-level await (WebCodecs H264 capability
// detection in core/util/browser.js), which the vite single-file build cannot
// inline correctly — it emits a namespace reference whose module body never
// runs, so the first import throws a TDZ ReferenceError. Instead the library
// is prebundled by scripts/build-novnc.mjs into ONE file served by the daemon
// and loaded here through the browser's native module loader.
//
// The daemon serves this exact path (pkg/connector/wui/assets.go); the
// variable specifier also keeps TypeScript and the bundler from resolving it
// at build time. Typed `string` deliberately — a literal type would make tsc
// try to resolve the module.
export const NOVNC_MODULE_URL: string = '/assets/novnc.esm.js'

export async function loadRFB(): Promise<typeof import('@novnc/novnc')['default']> {
  const mod = (await import(/* @vite-ignore */ NOVNC_MODULE_URL)) as {
    default: typeof import('@novnc/novnc')['default']
  }
  return mod.default
}
