// Diff-mode mutation: build Stryker --mutate expressions from git changes.
// Usage: node scripts/stryker-diff.mjs [base-ref=HEAD] [FORCE=1]
// Mirrors `make mutate` on the Go side: one diff against <base> covering
// committed+staged+unstaged changes, line-ranged per hunk, capped by
// total mutated lines unless FORCE=1.
import { execFileSync } from 'node:child_process';

const base = process.argv[2] || 'HEAD'; // `||` not `??`: make expands unset $(MUTATE_BASE) to ""
const root = new URL('../../..', import.meta.url).pathname; // scripts/ -> web/ui -> web -> repo root
const prefix = 'web/ui/';
const maxLines = Number(process.env.WEB_MUTATE_MAX_LINES ?? 500);

const git = (...args) => {
  try {
    return execFileSync('git', ['-C', root, ...args], { encoding: 'utf8' });
  } catch {
    return '';
  }
};

if (!git('rev-parse', '--verify', '-q', base).trim()) {
  console.log(`NOTE: ${base} not found, skipping (make web-mutate MUTATE_BASE=<ref>)`);
  process.exit(0);
}

const out = git('diff', '-U0', '--diff-filter=ACMR', base, '--', `${prefix}src`);

const hunks = new Map(); // file -> [start-end, ...]
let file = null;
for (const line of out.split('\n')) {
  if (line.startsWith('+++ b/')) {
    const p = line.slice(6);
    file =
      p.startsWith(prefix) &&
      p.endsWith('.ts') &&
      !p.endsWith('.test.ts') &&
      !p.endsWith('vitest-setup.ts')
        ? p.slice(prefix.length)
        : null;
    continue;
  }
  const m = line.match(/^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@/);
  if (!m || !file) continue;
  const start = Number(m[1]);
  const len = m[2] === undefined ? 1 : Number(m[2]);
  if (len === 0) continue; // pure deletion hunk
  hunks.set(file, [...(hunks.get(file) ?? []), `${start}-${start + len - 1}`]);
}

if (hunks.size === 0) {
  console.log(`no changed non-test .ts files vs ${base}`);
  process.exit(0);
}

const mutate = [...hunks].flatMap(([f, rs]) => rs.map((r) => `${f}:${r}`));
let lineCount = 0;
for (const rs of hunks.values()) {
  for (const r of rs) {
    const [s, e] = r.split('-').map(Number);
    lineCount += e - s + 1;
  }
}
if (lineCount > maxLines && process.env.FORCE !== '1') {
  console.log(
    `NOTE: ${lineCount} changed lines to mutate (> ${maxLines}), skipping\n      make web-mutate FORCE=1 to run them anyway (or bump WEB_MUTATE_MAX_LINES)`
  );
  process.exit(0);
}

console.log(`mutating ${mutate.length} range(s) in ${hunks.size} file(s):\n  ${mutate.join('\n  ')}`);
execFileSync('npx', ['stryker', 'run', '--mutate', mutate.join(',')], {
  cwd: new URL('..', import.meta.url).pathname,
  stdio: 'inherit',
});
