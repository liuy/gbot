import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

export default defineConfig({
  plugins: [tailwindcss(), react()],
  base: './',
  build: {
    outDir: path.resolve(__dirname, '../../pkg/connector/webchat/assets'),
    emptyOutDir: true,
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
