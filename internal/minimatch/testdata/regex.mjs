// The oracle of regex.json: what JavaScript's RegExp answers for each case,
// new RegExp(source, flags).test(input), true or false, or "throw" where
// new RegExp throws. TestRegexAgreesWithJavaScript holds regexp.go to the
// same, past the sources minimatch builds: every escape, class, quantifier
// and lookahead the matcher reads, under each of the flags i and u. It reads
// a list of [source, flags, input] on stdin and writes each with its answer;
// with --check it reads [source, flags, input, answer] and exits 1 on an
// answer that is not JavaScript's. Run under the Node whose tables
// unicode_node.go holds.
import { readFileSync } from 'node:fs';

function answer(source, flags, input) {
  let re;
  try {
    re = new RegExp(source, flags);
  } catch {
    return 'throw';
  }
  return re.test(input);
}

const input = JSON.parse(readFileSync(0, 'utf8'));
if (process.argv.includes('--check')) {
  let wrong = 0;
  for (const [s, f, i, want] of input) {
    const got = answer(s, f, i);
    if (got !== want) {
      wrong++;
      console.log(JSON.stringify([s, f, i, got]));
    }
  }
  console.log(`${input.length} cases, ${wrong} not JavaScript's answer`);
  process.exit(wrong ? 1 : 0);
}
console.log('[\n' + input.map(([s, f, i]) => JSON.stringify([s, f, i, answer(s, f, i)])).join(',\n') + '\n]');
