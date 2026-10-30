// The oracle of parts.json: what minimatch 10.2.6's own exports answer for
// the two steps the port takes from it whole, braceExpand(s) and
// unescape(s), each with minimatch's defaults as Renovate leaves them.
// TestPartsAgreeWithMinimatch holds braceExpand and unescape in the port to
// the same. It reads {"braceExpand": [s, ...], "unescape": [s, ...]} on
// stdin and writes each string with minimatch's answer; with --check it
// reads that and exits 1 on an answer that is not minimatch's. Run in an
// empty directory, as oracle.mjs says.
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';

const require = createRequire(process.cwd() + '/');
const mm = require('minimatch');
const steps = { braceExpand: (s) => mm.braceExpand(s), unescape: (s) => mm.unescape(s) };

const input = JSON.parse(readFileSync(0, 'utf8'));
if (process.argv.includes('--check')) {
  let wrong = 0;
  let n = 0;
  for (const [name, cases] of Object.entries(input)) {
    for (const [s, want] of cases) {
      n++;
      const got = steps[name](s);
      if (JSON.stringify(got) !== JSON.stringify(want)) {
        wrong++;
        console.log(JSON.stringify([name, s, got]));
      }
    }
  }
  console.log(`${n} cases, ${wrong} not minimatch's answer`);
  process.exit(wrong ? 1 : 0);
}
const out = Object.entries(input).map(
  ([name, list]) => JSON.stringify(name) + ': [\n' + list.map((s) => JSON.stringify([s, steps[name](s)])).join(',\n') + '\n]',
);
console.log('{\n' + out.join(',\n') + '\n}');
