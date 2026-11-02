import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/vitest-setup.ts'],
    // Stryker's sandbox holds instrumented copies of these very tests; a
    // leftover sandbox after an interrupted run would double-collect them.
    exclude: ['**/node_modules/**', '**/dist/**', '**/.stryker-tmp/**'],
  },
  // Stryker sandboxes symlink node_modules back to the real checkout; the
  // realpath falls outside the sandbox root and vite's fs guard rejects
  // `?raw` imports from it. Test runs have no HTTP attack surface.
  server: { fs: { strict: false } },
})
