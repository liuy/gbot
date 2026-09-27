// Bundles the first-party model-viewer fork (src/model-viewer/, based on
// @google/model-viewer@4.3.1) into the single
// ESM file the daemon serves at /assets/model-viewer.esm.js (the WUI loads it
// via native import()). Replaces the former committed copy of upstream's
// dist/model-viewer.min.js: same URL slot, our own source build, so walk mode
// can be implemented inside the fork. No source patches — fork changes are
// committed directly in the fork tree, unlike build-novnc.mjs.
//
// Flags mirror upstream's v4.3.1 tsconfig semantics via
// src/model-viewer/tsconfig.json:
// experimentalDecorators is REQUIRED (their @style decorator is legacy
// (proto, key) style), es2017 target implies useDefineForClassFields=false.
// --legal-comments=inline keeps the per-file Apache-2.0 headers in the bundle.
// Everything is bundled (three, lit, gainmap-js included) — the app never
// imports three itself, so exactly one three instance exists per page.
//
// Usage:
//   node scripts/build-model-viewer.mjs           regenerate the committed bundle
//   node scripts/build-model-viewer.mjs --check   verify the committed bundle is
//                                                 current (used by `make web-check`)
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { readFileSync, mkdirSync, readdirSync, rmSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { tmpdir } from 'node:os'

const here = dirname(fileURLToPath(import.meta.url))
const ui = join(here, '..')
const entry = join(ui, 'src', 'model-viewer', 'model-viewer.ts')
const out = join(ui, '..', '..', 'pkg', 'connector', 'wui', 'assets', 'model-viewer.esm.js')
const check = process.argv.includes('--check')

// Deterministic output: same sources → same bytes, which is what --check
// relies on to prove the committed artifact is current. Bundle bytes will
// forever differ from the old upstream dist copy (esbuild vs rollup/terser);
// currency is measured against OUR build only.
//
// __WALK_BUILD__ is a content hash of the fork sources — deterministic (so
// --check stays valid) yet guaranteed to change whenever any source changes,
// which is what lets the walk debug overlay prove which code a client is
// actually running.
function sourceHash() {
  const hash = createHash('sha1')
  const walk = (dir) => {
    for (const name of readdirSync(dir).sort()) {
      const p = join(dir, name)
      if (statSync(p).isDirectory()) {
        walk(p)
      } else {
        hash.update(name)
        hash.update(readFileSync(p))
      }
    }
  }
  walk(join(ui, 'src', 'model-viewer'))
  return hash.digest('hex').slice(0, 8)
}

function bundle(target) {
  mkdirSync(dirname(target), { recursive: true })
  execFileSync(
    join(ui, 'node_modules', '.bin', 'esbuild'),
    [
      entry,
      '--bundle',
      '--format=esm',
      '--platform=browser',
      '--target=es2017',
      '--tsconfig=src/model-viewer/tsconfig.json',
      '--minify',
      '--legal-comments=inline',
      `--define:__WALK_BUILD__=${JSON.stringify(sourceHash())}`,
      `--outfile=${target}`,
    ],
    { cwd: ui, stdio: ['ignore', 'ignore', check ? 'ignore' : 'inherit'] },
  )
  return readFileSync(target)
}

if (check) {
  const tmp = join(tmpdir(), `model-viewer.check.${process.pid}.esm.js`)
  try {
    const fresh = bundle(tmp)
    const committed = readFileSync(out)
    if (!fresh.equals(committed)) {
      console.error(
        'model-viewer.esm.js is stale — run `npm run build:mv` in web/ui and commit the result',
      )
      process.exit(1)
    }
    console.log(`model-viewer.esm.js up to date (${committed.length} bytes)`)
  } finally {
    rmSync(tmp, { force: true })
  }
} else {
  bundle(out)
  console.log('bundled →', out)
}
