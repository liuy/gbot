// Prebundles @novnc/novnc into a single TLA-safe ESM file for the daemon to
// serve at /assets/novnc.esm.js (the WUI loads it via native import()).
//
// Four source patches (see PATCHES):
//  * two decoder configs get hardwareAcceleration: 'prefer-software' —
//    measured on the target device (Honor A16, Chrome 144 WebView): the
//    hardware H264 path accepts configure()/decode() but never produces a
//    frame and flush() hangs forever (Chromium 455794276 class, won't fix);
//    software decodes the same sample in ~8 ms.
//  * the remote cursor is hidden while view-only — noVNC renders it as a CSS
//    cursor, so it follows the local pointer even though no input is sent,
//    which reads as "control is still active"; and a live viewOnly toggle
//    must repaint the cursor immediately (the setter only handles keyboard).
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
// second matching config would otherwise patch the wrong site.
const PATCHES = [
  // The capability probe — must succeed for noVNC to enable the H264 path.
  {
    file: join(novnc, 'core', 'util', 'browser.js'),
    from: '        optimizeForLatency: true,\n    };',
    to: "        optimizeForLatency: true,\n        hardwareAcceleration: 'prefer-software',\n    };",
    sentinel: 'prefer-software',
  },
  // The live streaming decoder — same stall applies to the real decode path.
  {
    file: join(novnc, 'core', 'decoders', 'h264.js'),
    from: '            optimizeForLatency: true,',
    to: "            optimizeForLatency: true,\n            hardwareAcceleration: 'prefer-software',",
    sentinel: 'prefer-software',
  },
  // Remote cursor follows the LOCAL pointer because noVNC renders it as a CSS
  // cursor. In view-only that reads as "the mouse still works" — hide it so
  // the mode is visually unambiguous.
  {
    file: join(novnc, 'core', 'rfb.js'),
    from: '        const image = this._shouldShowDotCursor() ? RFB.cursors.dot : this._cursorImage;',
    to: '        const image = this._viewOnly ? RFB.cursors.none\n'
      + '            : (this._shouldShowDotCursor() ? RFB.cursors.dot : this._cursorImage);',
    sentinel: 'this._viewOnly ? RFB.cursors.none',
  },
  // ...and make a live toggle take effect at once (the setter otherwise only
  // ungrabs the keyboard).
  {
    file: join(novnc, 'core', 'rfb.js'),
    from: '            if (viewOnly) {\n'
      + '                this._keyboard.ungrab();\n'
      + '            } else {\n'
      + '                this._keyboard.grab();\n'
      + '            }\n'
      + '        }\n'
      + '    }\n',
    to: '            if (viewOnly) {\n'
      + '                this._keyboard.ungrab();\n'
      + '            } else {\n'
      + '                this._keyboard.grab();\n'
      + '            }\n'
      + '        }\n'
      + '        this._refreshCursor();\n'
      + '    }\n',
    sentinel: 'this._keyboard.grab();\n            }\n        }\n        this._refreshCursor();',
  },
]

function patch({ file, from, to, sentinel }) {
  const src = readFileSync(file, 'utf8')
  if (src.includes(sentinel)) return 'already patched'
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
