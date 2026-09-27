import js from '@eslint/js'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  {
    // Global ignore block (must be its own object in flat config).
    // src/model-viewer is the self-bundled model-viewer fork (own esbuild +
    // tsconfig pipeline, upstream-derived code style) — the app's tsc and
    // eslint universes must not absorb it.
    ignores: ['dist/', 'node_modules/', 'src/model-viewer/'],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    rules: {
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_' },
      ],
    },
  },
)
