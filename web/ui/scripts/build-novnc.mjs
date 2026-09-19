// Prebundles @novnc/novnc into a single TLA-safe ESM file for the daemon to
// serve at /assets/novnc.esm.js (the WUI loads it via native import()).
//
// Both patches below add hardwareAcceleration: 'prefer-software' to a
// decoder config. Driven by device measurements (Honor A16, Chrome 144
// WebView): the hardware H264 decode path accepts configure() and decode()
// but never produces a frame and flush() hangs forever (Chromium issue
// 455794276 class — platform hardware-decoder stalls, "won't fix"). With
// prefer-software the same decode completes in ~8ms with a real frame.
//
// Usage:
//   node scripts/build-novnc.mjs           regenerate the committed bundle
//   node scripts/build-novnc.mjs --check   verify the committed bundle is
//                                          current (used by `make web-check`)
import { execFileSync } from 'node:child_process'
import { readFileSync, writeFileSync, mkdirSync, rmSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { tmpdir } from 'node:os'

const here = dirname(fileURLToPath(import.meta.url))
const ui = join(here, '..')
const novnc = join(ui, 'node_modules', '@novnc', 'novnc')
const esbuild = join(ui, 'node_modules', '.bin', 'esbuild')
const out = join(ui, '..', '..', 'pkg', 'connector', 'wui', 'assets', 'novnc.esm.js')
const check = process.argv.includes('--check')

// anchor must be unique within the file — a dependency bump that adds a
// second matching config would otherwise patch the wrong decoder.
const PATCHES = [
  // The capability probe — must succeed for noVNC to enable the H264 path.
  {
    file: join(novnc, 'core', 'util', 'browser.js'),
    from: '        optimizeForLatency: true,\n    };',
    to: "        optimizeForLatency: true,\n        hardwareAcceleration: 'prefer-software',\n    };",
  },
  // The live streaming decoder — same stall applies to the real decode path.
  {
    file: join(novnc, 'core', 'decoders', 'h264.js'),
    from: '            optimizeForLatency: true,',
    to: "            optimizeForLatency: true,\n            hardwareAcceleration: 'prefer-software',",
  },
]

function patch({ file, from, to }) {
  const src = readFileSync(file, 'utf8')
  if (src.includes('prefer-software')) return 'already patched'
  const hits = src.split(from).length - 1
  if (hits !== 1) throw new Error(`anchor found ${hits} times (want 1) in ${file}`)
  writeFileSync(file, src.replace(from, to))
  return 'patched'
}

for (const p of PATCHES) console.log(`${p.file.split('/node_modules/')[1]}:`, patch(p))

// Deterministic output: same sources → same bytes, which is what --check
// relies on to prove the committed artifact is current.
function bundle(target) {
  mkdirSync(dirname(target), { recursive: true })
  execFileSync(
    esbuild,
    [
      join(novnc, 'core', 'rfb.js'),
      '--bundle',
      '--format=esm',
      '--platform=browser',
      '--minify',
      `--outfile=${target}`,
    ],
    { cwd: ui, stdio: ['ignore', 'ignore', check ? 'ignore' : 'inherit'] },
  )
  return readFileSync(target)
}

if (check) {
  const tmp = join(tmpdir(), `novnc.check.${process.pid}.esm.js`)
  try {
    const fresh = bundle(tmp)
    const committed = readFileSync(out)
    if (!fresh.equals(committed)) {
      console.error(
        'novnc.esm.js is stale — run `npm run build:novnc` in web/ui and commit the result',
      )
      process.exit(1)
    }
    console.log(`novnc.esm.js up to date (${committed.length} bytes)`)
  } finally {
    rmSync(tmp, { force: true })
  }
} else {
  bundle(out)
  console.log('bundled →', out)
}
