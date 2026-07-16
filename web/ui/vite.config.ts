import { defineConfig } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import { viteSingleFile } from 'vite-plugin-singlefile'
import path from 'path'

export default defineConfig({
  plugins: [tailwindcss(), viteSingleFile()],
  base: './',
  build: {
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
    },
  },
})
