import { defineConfig } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import { viteSingleFile } from 'vite-plugin-singlefile'
import path from 'path'

export default defineConfig({
  plugins: [tailwindcss(), viteSingleFile()],
  base: './',
  build: {
    // @novnc/novnc's core/util/browser.js contains a top-level await, which
    // this single-file pipeline cannot inline correctly (it emits a TDZ
    // namespace read). noVNC is therefore NOT part of this bundle — it is
    // served as a separate prebundle and loaded at runtime (see
    // src/rfb_loader.ts). es2022 is kept for the rest of the app.
    target: 'es2022',
    outDir: path.resolve(__dirname, '../../pkg/connector/wui/assets'),
    // false: assets/ also holds the committed novnc.esm.js (regenerated via
    // npm run build:novnc). Tradeoff: any OTHER stale file in assets/ also
    // survives a build, so assets/ must hold tracked artifacts only.
    emptyOutDir: false,
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test-setup.ts'],
  },
  server: {
    proxy: {
      '/ws/chat': {
        target: 'ws://localhost:8765',
        ws: true,
      },
      '/wui/vnc': {
        target: 'ws://localhost:8765',
        ws: true,
      },
    },
  },
})
