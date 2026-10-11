import js from '@eslint/js'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  {
    // Global ignore block (must be its own object in flat config).
    // src/model-viewer is the self-bundled model-viewer fork (own esbuild +
    // tsconfig pipeline, upstream-derived code style) — the app's tsc and
    // eslint universes must not absorb it.
    // src/idiomorph is a maintained fork of upstream idiomorph — linting it
    // would force style drift from upstream and break re-vendoring.
    ignores: ['dist/', 'node_modules/', 'src/model-viewer/', 'src/idiomorph/'],
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
