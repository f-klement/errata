// The oracle of sources.json: what minimatch 10.2.6 builds from each glob,
// as Renovate calls it, new Minimatch(glob, {dot: true, nocase: true}).
// For each glob it writes "throw" where minimatch throws, and otherwise
// [marks, set]: marks holds n for a negated glob, c for a comment and e for
// an empty one, and set each brace expansion's segments, each written as **
// for a globstar, = and the string minimatch compares, or the regex's flags
// between slashes and its source (_src, without ^ and $), as /iu/[^/]*?. TestSegmentsAgreeWithMinimatch
// holds the port to the same. It reads a list of globs on stdin and writes
// the cases; with --check it reads the cases instead, and exits 1 on one
// that is not minimatch's. Run in an empty directory, as oracle.mjs says.
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';

const require = createRequire(process.cwd() + '/');
const { Minimatch, GLOBSTAR } = require('minimatch');

function built(glob) {
  let m;
  try {
    m = new Minimatch(glob, { dot: true, nocase: true });
  } catch {
    return 'throw';
  }
  const seg = (p) => (p === GLOBSTAR ? '**' : typeof p === 'string' ? '=' + p : `/${p.flags}/${p._src}`);
  const marks = (m.negate ? 'n' : '') + (m.comment ? 'c' : '') + (m.empty ? 'e' : '');
  return [marks, m.set.map((s) => s.map(seg))];
}

const input = JSON.parse(readFileSync(0, 'utf8'));
if (process.argv.includes('--check')) {
  let wrong = 0;
  for (const [glob, want] of input) {
    const got = JSON.stringify(built(glob));
    if (got !== JSON.stringify(want)) {
      wrong++;
      console.log(JSON.stringify([glob, JSON.parse(got)]));
    }
  }
  console.log(`${input.length} globs, ${wrong} not minimatch's`);
  process.exit(wrong ? 1 : 0);
}
console.log('[\n' + input.map((g) => JSON.stringify([g, built(g)])).join(',\n') + '\n]');
