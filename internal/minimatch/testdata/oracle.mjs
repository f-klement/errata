// The oracle of cases.json: minimatch 10.2.6 itself, as Renovate calls it,
// new Minimatch(glob, {dot: true, nocase: true}).match(file). It reads a
// list of [file, glob, answer] on stdin, the answer true, false or "throw",
// writes each case whose answer is not minimatch's, and exits 1 if there is
// one. TestMatchAgreesWithMinimatch holds the port to the same answers. Run
// in an empty directory, under the Node whose tables unicode_node.go holds:
//
//   npm install minimatch@10.2.6
//   node <this file> < cases.json
//
// With --write it writes the cases instead, each with minimatch's answer.
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';

const require = createRequire(process.cwd() + '/');
const { Minimatch } = require('minimatch');

function answer(file, glob) {
  try {
    return new Minimatch(glob, { dot: true, nocase: true }).match(file);
  } catch {
    return 'throw';
  }
}

const cases = JSON.parse(readFileSync(0, 'utf8'));
if (process.argv.includes('--write')) {
  console.log('[\n' + cases.map(([f, g]) => JSON.stringify([f, g, answer(f, g)])).join(',\n') + '\n]');
  process.exit(0);
}
let wrong = 0;
for (const [f, g, want] of cases) {
  const got = answer(f, g);
  if (got !== want) {
    wrong++;
    console.log(JSON.stringify([f, g, got]));
  }
}
console.log(`${cases.length} cases, ${wrong} not minimatch's answer`);
process.exit(wrong ? 1 : 0);
