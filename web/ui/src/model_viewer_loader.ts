// Seam module so jsdom tests stub the bundle import instead of loading the
// real 1 MB component (which needs WebGL). Mirrors the rfb_loader pattern.
export const MODEL_VIEWER_URL: string = '/assets/model-viewer.esm.js'

export function loadModelViewer(): Promise<unknown> {
  return import(/* @vite-ignore */ MODEL_VIEWER_URL)
}
