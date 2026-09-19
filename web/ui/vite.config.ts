import { defineConfig } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import { viteSingleFile } from 'vite-plugin-singlefile'
import path from 'path'

export default defineConfig({
  plugins: [tailwindcss(), viteSingleFile()],
  base: './',
  build: {
    // @novnc/novnc's core/util/browser.js contains a top-level await; Vite
    // 6.4.3's default target (chrome87/es2020-era baselines) makes esbuild
    // hard-fail on TLA. There is no per-dependency target override.
    target: 'es2022',
    outDir: path.resolve(__dirname, '../../pkg/connector/wui/assets'),
    emptyOutDir: true,
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
